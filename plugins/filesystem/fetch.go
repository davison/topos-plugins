package main

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

const (
	// maxByteRenditionSize is the fixed cap on a bytes-kind or
	// markdown-kind Fetch read (32 MiB) — comfortably under
	// sdk.MaxMessageSize (64 MiB), per 12-02-PLAN.md Task 3 action text.
	maxByteRenditionSize = 32 * 1024 * 1024

	// maxPlainTextSize bounds a plain-text-kind Fetch read (256 KiB).
	// Beyond this, the returned text is honestly truncated with a final-
	// line notice rather than silently cut off mid-content.
	maxPlainTextSize = 256 * 1024

	// oversizeReason names the byte-rendition cap for a file too large to
	// fetch as bytes or markdown.
	oversizeReason = "file exceeds the 32 MiB preview size limit; open in source"

	// metadataOnlyReason is the fixed unavailable_reason for the
	// metadata-only preview kind (office formats, unrenderable images) —
	// mirrors plugins/paperless/plugin.go's noRenditionReason convention:
	// a normal outcome, never an error.
	metadataOnlyReason = "preview not supported for this file type; open in source"

	// noThumbnailReason is the fixed unavailable_reason THUMBNAIL always
	// answers with, for every preview kind — this plugin never generates
	// a thumbnail rendition.
	noThumbnailReason = "no thumbnail rendition"

	// plainTextTruncationNotice is appended as the plain-text kind's
	// final line when the source file is longer than maxPlainTextSize —
	// an honest truncation notice, never a silent cut.
	plainTextTruncationNotice = "\n\n[... truncated at 256 KiB; open in source for the full file]"
)

// Fetch dispatches on ContentVariant exactly like plugins/paperless/
// plugin.go does. FULL and PREVIEW both re-derive the item's
// classification fresh from this instance's own scope (newScope(p.extras),
// scope.go) using the request's source_id — never cached from Match,
// matching every other plugin's "Fetch re-fetches fresh from source" rule
// — and branch on preview kind. THUMBNAIL always answers unavailable, for
// every kind: this plugin never generates a thumbnail rendition.
func (p *SourcePlugin) Fetch(_ context.Context, req *toposv1.FetchRequest) (*toposv1.FetchResponse, error) {
	switch req.GetVariant() {
	case toposv1.ContentVariant_CONTENT_VARIANT_FULL, toposv1.ContentVariant_CONTENT_VARIANT_PREVIEW:
		return p.fetchByKind(req.GetSourceId())
	case toposv1.ContentVariant_CONTENT_VARIANT_THUMBNAIL:
		return &toposv1.FetchResponse{Available: false, UnavailableReason: noThumbnailReason}, nil
	default:
		return nil, status.Error(codes.InvalidArgument, "filesystem: unspecified content variant")
	}
}

// fetchByKind re-validates sourceID resolves inside the configured root
// BEFORE any file is opened (defense-in-depth, mirroring
// kernel/httpapi/fsopen.go's identical guard) — this rejects a source_id
// escape attempt, including a post-index symlink swap (CR-02,
// 12-06-PLAN.md Task 1), before classification or any I/O runs. resolvePath
// can fail for two materially different reasons: a vanished file
// (errors.Is(err, fs.ErrNotExist)) answers the same codes.NotFound message
// statForFetch already produces for a missing file elsewhere in this file —
// a deliberate honesty improvement, since a metadata-only-kind item whose
// file has since vanished now answers NotFound rather than
// available: false, because the resolution step now runs ahead of the kind
// dispatch for every kind. Every other resolvePath failure (a genuine
// containment escape, or an unresolvable symlink chain) keeps the existing
// codes.InvalidArgument.
//
// Classification is then decided by building ONE *scope from p.extras —
// newScope(p.extras), the identical construction Match performs
// (plugin.go:127) — and asking scope.includes(sourceID) the same question
// Match/walk already ask (D-03 precedence: exclude first, then
// include-if-declared REPLACING the default allowlist, then the default
// allowlist alone). This is the fix for the gap 12-VERIFICATION.md
// recorded: the package-level classify() helper alone has no knowledge of
// include_glob/exclude_glob and cannot reproduce scope.includes' "unknown
// extension admitted by glob -> metadata-only" branch, so any item indexed
// only because include_glob widened past the default allowlist used to
// answer a false NotFound instead of an honest metadata-only preview.
// scope.includes' three outcomes map as follows:
//   - a non-nil error (a malformed operator glob) becomes codes.Unavailable
//     — a deliberate departure from codes.Internal: Match already maps this
//     exact error class to codes.Unavailable (plugin.go:130-132), and the
//     kernel maps every non-NotFound Fetch error to the same
//     502 source_unavailable regardless, so one malformed pattern produces
//     one class of failure at both entry points.
//   - included == false becomes codes.NotFound, byte-for-byte the message
//     this function already produced: the id is on disk but genuinely
//     outside this instance's current scope (excluded by exclude_glob, or
//     not matched by a declared include_glob, or an unrecognized extension
//     with no include_glob at all) — the honesty fix widens the ANSWER for
//     an in-scope item, never the membership test itself.
//   - otherwise dispatch on the returned classification's kind, unchanged.
func (p *SourcePlugin) fetchByKind(sourceID string) (*toposv1.FetchResponse, error) {
	_, resolved, err := resolvePath(p.root, sourceID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, status.Errorf(codes.NotFound, "filesystem: item %q not found", sourceID)
		}
		return nil, status.Errorf(codes.InvalidArgument, "filesystem: %v", err)
	}

	sc := newScope(p.extras)
	c, included, err := sc.includes(sourceID)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "filesystem: %v", err)
	}
	if !included {
		return nil, status.Errorf(codes.NotFound, "filesystem: item %q not found", sourceID)
	}

	// Every rendition helper below reads through resolved (the symlink-free
	// real path resolvePath already validated), never the lexical full path
	// — WR-02, 12-07-PLAN.md Task 2: the path validated and the path used
	// must be one and the same.
	switch c.kind {
	case previewKindBytes:
		return fetchBytesRendition(resolved, c.mime)
	case previewKindMarkdown:
		return fetchMarkdownRendition(resolved)
	case previewKindPlainText:
		return fetchPlainTextRendition(resolved)
	default: // previewKindMetadataOnly
		return &toposv1.FetchResponse{Available: false, UnavailableReason: metadataOnlyReason}, nil
	}
}

// openForFetch opens path once and stats it through the same handle,
// mapping failures onto the existing codes exactly: a missing file to
// codes.NotFound (a distinct outcome from an unavailable-but-known
// response), any other open failure to codes.Unavailable with an "open:"
// prefix, and a stat failure on the open handle to codes.Unavailable with a
// "stat:" prefix (closing the handle before returning that error).
// Superseded statForFetch (12-07-PLAN.md Task 2, WR-02): reading through
// one handle means the size check and the read can no longer observe
// different files — there is no longer a gap between "how big is this"
// and "read these bytes" for a second open call to fall into.
func openForFetch(path string) (*os.File, os.FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, status.Errorf(codes.NotFound, "filesystem: item not found")
		}
		return nil, nil, status.Errorf(codes.Unavailable, "filesystem: open: %v", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, status.Errorf(codes.Unavailable, "filesystem: stat: %v", err)
	}
	return f, info, nil
}

// fetchBytesRendition serves the bytes-kind preview (PDF, inline-
// renderable images): open once, refuse over maxByteRenditionSize with
// Available false BEFORE reading any bytes — the file's bytes are never
// read into memory when it is oversize — otherwise read the full bytes
// from that same handle, bounded by io.LimitReader at maxByteRenditionSize
// so the cap holds even if the file grows after the stat.
func fetchBytesRendition(path, mime string) (*toposv1.FetchResponse, error) {
	f, info, err := openForFetch(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if info.Size() > maxByteRenditionSize {
		return &toposv1.FetchResponse{Available: false, UnavailableReason: oversizeReason}, nil
	}

	data, err := io.ReadAll(io.LimitReader(f, maxByteRenditionSize))
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "filesystem: read: %v", err)
	}

	return &toposv1.FetchResponse{
		Available: true,
		MimeType:  mime,
		SizeBytes: int64(len(data)),
		Data:      data,
	}, nil
}

// fetchMarkdownRendition serves the markdown-kind preview: open once and
// refuse over maxByteRenditionSize exactly like the bytes kind, otherwise
// read from that same handle (bounded by io.LimitReader) and render through
// RenderMarkdown (render.go), returning the UNSANITIZED HTML fragment with
// CONTENT_SHAPE_MARKDOWN_HTML — the kernel's rendition boundary sanitizes
// it before serving.
func fetchMarkdownRendition(path string) (*toposv1.FetchResponse, error) {
	f, info, err := openForFetch(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if info.Size() > maxByteRenditionSize {
		return &toposv1.FetchResponse{Available: false, UnavailableReason: oversizeReason}, nil
	}

	raw, err := io.ReadAll(io.LimitReader(f, maxByteRenditionSize))
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "filesystem: read: %v", err)
	}

	fragment, err := RenderMarkdown(raw)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "filesystem: render markdown: %v", err)
	}

	return &toposv1.FetchResponse{
		Available:    true,
		MimeType:     "text/html",
		ContentShape: toposv1.ContentShape_CONTENT_SHAPE_MARKDOWN_HTML,
		SizeBytes:    int64(len(fragment)),
		Data:         fragment,
		Text:         string(raw),
	}, nil
}

// fetchPlainTextRendition serves the plain-text-kind preview, bounded to
// maxPlainTextSize: the text field carries the (possibly truncated)
// content, and Data carries the identical bytes so
// GET /api/items/{id}/content can serve it too (kernel/httpapi/item.go's
// allowedRenditionTypes gains a text/plain entry alongside this task).
// When the source file is longer than the bound, the returned text's
// final line honestly says so rather than silently cutting off. Reads
// through openForFetch like its two siblings, so all three renditions
// share one open-and-map helper.
func fetchPlainTextRendition(path string) (*toposv1.FetchResponse, error) {
	f, info, err := openForFetch(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxPlainTextSize))
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "filesystem: read: %v", err)
	}

	text := string(data)
	if info.Size() > maxPlainTextSize {
		text += plainTextTruncationNotice
	}

	return &toposv1.FetchResponse{
		Available: true,
		MimeType:  "text/plain",
		SizeBytes: int64(len(text)),
		Data:      []byte(text),
		Text:      text,
	}, nil
}
