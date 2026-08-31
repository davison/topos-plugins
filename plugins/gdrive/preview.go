// Package main's preview.go builds the bounded, live `Item.preview` string
// both Match-time preview building (this file) and, indirectly,
// fetchcontent.go's Fetch dispatch rely on to classify a Drive file's
// MIME type. Every preview fetch failure degrades to an empty string and a
// log line naming the file id and stage only — never the node's own Name,
// never any fetched byte — matching match.go's existing per-node skip
// discipline (match.go:110-149), applied one level deeper here: a preview
// failure degrades a single Item field, it never drops the item or fails
// the whole Match call.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"google.golang.org/api/drive/v3"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

const (
	// previewRangeBytes is the raw byte window rangePreview requests via
	// an HTTP Range header on a regular file's files.get download, before
	// truncateRunes bounds the result to previewRuneLimit runes.
	// CONTRACT-GAPS.md GAP-14: neither the contract nor PRD.md names an
	// exact number — this is this repository's own choice.
	previewRangeBytes = 8192

	// previewRuneLimit is the final bounded preview length, in runes
	// (never bytes, so a multi-byte character is never split — Pitfall 6).
	// CONTRACT-GAPS.md GAP-14.
	previewRuneLimit = 500

	// previewFetchTimeout bounds each item's own preview fetch inside
	// attachPreviews, so one stuck or slow document can never stall the
	// whole Match call (T-04-05). A deadline-exceeded fetch degrades and
	// logs exactly like any other failure.
	previewFetchTimeout = 10 * time.Second
)

// isWorkspaceNative reports whether mimeType is a Google Workspace-native
// document type (Docs/Sheets/Slides/Drawings/Forms/...), which has no
// native bytes files.get can download — folderMimeType (folderwalk.go) is
// explicitly excluded, since a folder is never a document. Workspace-
// native previews route to workspaceexport.go's exportPreview, below.
func isWorkspaceNative(mimeType string) bool {
	return strings.HasPrefix(mimeType, "application/vnd.google-apps.") && mimeType != folderMimeType
}

// isTextShaped reports whether mimeType is in this plugin's own
// text-shaped preview allowlist (CONTRACT-GAPS.md GAP-15): the `text/`
// prefix, or exactly `application/json`. Every other regular-file MIME
// type gets an empty preview and zero Drive traffic — this plugin bundles
// no PDF/office-binary/OCR text-extraction library (PRD.md's "no third
// dependency recommended"). mimeType is first normalized by cutting at the
// first `;` and trimming surrounding space, so a parameter-qualified value
// (e.g. `text/plain; charset=utf-8`, the shape a real Drive response can
// carry) still matches the same allowlist a bare `text/plain` does.
func isTextShaped(mimeType string) bool {
	if i := strings.IndexByte(mimeType, ';'); i >= 0 {
		mimeType = mimeType[:i]
	}
	mimeType = strings.TrimSpace(mimeType)
	return strings.HasPrefix(mimeType, "text/") || mimeType == "application/json"
}

// truncateRunes cuts s to at most limit runes, always on a rune boundary
// so the result is valid UTF-8 even when s's own byte at that boundary
// falls mid-rune (Pitfall 6). A limit of 0 or less, or an s already within
// the limit, returns s unchanged (via the natural loop-exhaustion path
// below for the latter).
func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == limit {
			return s[:i]
		}
		count++
	}
	return s
}

// buildPreview is the single MIME dispatcher both preview building
// (Match, this file) and Fetch (fetchcontent.go) share, so the two RPC
// paths can never disagree about which file gets which treatment. A
// Workspace-native MIME type routes to workspaceexport.go's
// exportPreview (files.export, CONT-02); everything else routes to
// rangePreview, which itself further narrows to the text-shaped allowlist
// (GAP-15) before issuing any Drive call.
//
// buildPreview and rangePreview (and exportPreview) all return string
// only, never an error — deliberately and permanently: this is the
// structural guarantee that a preview failure can never propagate into
// matchItems' per-node skip path or into Match's own error return. Every
// failure this file (or workspaceexport.go's exportPreview) can produce
// (a non-text-shaped or declined-format MIME, a transport error, a read
// error, a context deadline, an export-ceiling failure) degrades to ""
// and a log line; nothing here has a code path back to a Go error value a
// caller could choose to treat as fatal.
func buildPreview(ctx context.Context, svc *drive.Service, fileID, mimeType string) string {
	if isWorkspaceNative(mimeType) {
		return exportPreview(ctx, svc, fileID, mimeType)
	}
	return rangePreview(ctx, svc, fileID, mimeType)
}

// rangePreview issues a Range-bounded files.get download for a regular
// (non-Workspace) file and returns a rune-safe, previewRuneLimit-bounded
// preview string — or "" on any failure or for a MIME type outside the
// text-shaped allowlist (GAP-15), which refuses the call entirely before
// any Drive traffic is issued. The Range header bounds the fetch to
// previewRangeBytes; io.LimitReader is a second, defense-in-depth cap on
// the read itself, in case a proxy or a future API change silently
// ignores the header and returns the full body anyway. Every failure
// path logs the file id and the failing stage only — never the fetched
// bytes, never the node's own Name.
func rangePreview(ctx context.Context, svc *drive.Service, fileID, mimeType string) string {
	if !isTextShaped(mimeType) {
		return ""
	}

	call := svc.Files.Get(fileID).Context(ctx)
	call.Header().Set("Range", fmt.Sprintf("bytes=0-%d", previewRangeBytes-1))
	resp, err := call.Download()
	if err != nil {
		log.Printf("gdrive: preview: range-fetch %s: %s", fileID, err)
		return ""
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, previewRangeBytes))
	if err != nil {
		log.Printf("gdrive: preview: read %s: %s", fileID, err)
		return ""
	}
	return truncateRunes(string(data), previewRuneLimit)
}

// attachPreviews populates Item.Preview for every item in items whose
// SourceId resolves against tree, mutating each item in place after
// matchItems has already built, filtered, and sorted the full set —
// preview outcome therefore never participates in item selection or
// ordering (match.go's matchItems owns both, unconditionally, before this
// function ever runs). An item whose node is absent from tree is skipped
// (Preview stays unset, matching every other Item field's zero value).
//
// One item's preview failure degrades that one item's Preview field to ""
// and nothing else: it never drops the item from items, never reorders
// items, and never causes attachPreviews (or the Match call around it) to
// return an error — buildPreview's string-only return type makes this
// structural, not conventional (see buildPreview's own doc comment).
//
// Fetching is deliberately sequential — no goroutine fan-out — mirroring
// folderwalk.go's own stated no-fan-out convention (03-RESEARCH.md Open
// Question 1); this keeps the call simple to reason about today and
// leaves Phase 5's RES-01 backoff wrapper a single call site to wrap.
// Each item's own fetch is bounded by a fresh previewFetchTimeout-limited
// context derived from ctx, so one stuck or slow document can never stall
// the whole Match call (T-04-05); the parent ctx's own cancellation is
// still honored between items, so a cancelled Match stops fetching
// further previews rather than draining the whole remaining item set.
//
// Known, deliberate tradeoff (04-RESEARCH.md Open Question 2): this issues
// one Drive call per allowlisted item on every single Match cycle — CONT-04
// forbids caching the built preview, so there is no cheaper alternative
// available to this phase. Sequential fetching plus this per-item timeout
// are the only mitigations this plan takes on; explicit throttling and
// backoff are Phase 5's RES-01, not this plan's.
func attachPreviews(ctx context.Context, svc *drive.Service, tree map[string]*driveNode, items []*toposv1.Item) {
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return
		}
		node, ok := tree[item.GetSourceId()]
		if !ok {
			continue
		}
		itemCtx, cancel := context.WithTimeout(ctx, previewFetchTimeout)
		item.Preview = buildPreview(itemCtx, svc, item.GetSourceId(), node.MimeType)
		cancel()
	}
}
