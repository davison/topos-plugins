// Package main's fetchcontent.go implements the Fetch RPC's real body:
// fetchItem mirrors plugin.go's Match resolution sequence exactly (token
// -> config -> folder -> sync), resolves req's source_id against the
// just-synced tree — the only access-control boundary this plugin has
// (T-04-02) — and dispatches on ContentVariant. A regular (non-Workspace)
// file's CONTENT_VARIANT_FULL request issues a live, unbounded files.get
// download, fetched fresh from Drive on every call; a Workspace-native
// node's CONTENT_VARIANT_FULL request routes to workspaceexport.go's
// fetchWorkspaceDoc (files.export, CONT-02/CONT-03); nothing here caches,
// memoizes, or reads from syncstate.json (CONT-04).
package main

import (
	"context"
	"io"
	"log"

	"google.golang.org/api/drive/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// unavailableNoRendition is the named reason a CONTENT_VARIANT_PREVIEW or
// CONTENT_VARIANT_THUMBNAIL request returns: this plugin produces no
// binary PREVIEW or THUMBNAIL rendition for any Drive file, regular or
// Workspace-native.
const unavailableNoRendition = "this plugin produces no PREVIEW or THUMBNAIL rendition"

// unavailableTooLargeToReturn is the named reason a regular file's
// CONTENT_VARIANT_FULL request returns when its recorded driveNode.Size
// exceeds maxFetchBytes: a pre-flight refusal, issuing no Drive download at
// all. Distinct from unavailableNoRendition and from every Phase 2/Phase 5
// status/health string (TestUnavailableReasons_AreDistinctFromEachOtherAndFromEveryOtherStatusString
// in fetchcontent_test.go pins the separation).
const unavailableTooLargeToReturn = "this file's recorded size exceeds what this plugin will attempt to download in one Fetch call"

// maxFetchBytes is a pre-flight cap this plugin applies to a regular
// file's CONTENT_VARIANT_FULL download, comfortably below the contract's
// own 64 MiB gRPC message-size ceiling (contract/plugin-contract.md's
// Fetch section: "sdk.GRPCServer... and the kernel's own dial options both
// raise this to 64 MiB"). Research assumption A5: the contract itself
// anticipates and accepts a natural codes.ResourceExhausted for an
// oversized rendition ("A rendition materially larger than that is
// expected to fail with a clear gRPC ResourceExhausted error"), so this
// cap is an optimization within contract-anticipated behavior — failing
// fast with a named, honest reason rather than buffering a huge file into
// memory only to have the transport layer reject it after the fact — not
// a correctness requirement, and it gets no CONTRACT-GAPS.md entry.
const maxFetchBytes = 32 * 1024 * 1024 // 32 MiB: half the 64 MiB ceiling, ample headroom

// fetchItem is the real body of the Fetch RPC (plugin.go's Fetch delegates
// here). Mirrors Match's own token -> config -> folder -> sync resolution
// sequence exactly, each stage wrapped codes.Unavailable on failure, then
// resolves req's source_id against the just-synced tree: an id absent
// from THIS instance's own persisted tree returns codes.NotFound,
// identically whether it never existed anywhere in Drive or exists in
// Drive but outside this instance's configured folder scope (T-04-02) —
// the tree is the only access-control boundary this plugin has, and a
// source_id is never handed to a raw files.get call before that check.
func (p *SourcePlugin) fetchItem(ctx context.Context, req *toposv1.FetchRequest) (*toposv1.FetchResponse, error) {
	if err := p.ensureTokenValid(ctx); err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}

	cfg, err := loadSourceConfig(p.getenv)
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	folderID, err := cfg.folderID()
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}

	st, err := p.ensureSynced(ctx, folderID)
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}

	node, ok := st.Tree[req.GetSourceId()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "gdrive: fetch: source_id %q not found", req.GetSourceId())
	}

	switch req.GetVariant() {
	case toposv1.ContentVariant_CONTENT_VARIANT_FULL:
		svc, err := p.driveService(ctx)
		if err != nil {
			return nil, status.Error(codes.Unavailable, err.Error())
		}
		if isWorkspaceNative(node.MimeType) {
			return fetchWorkspaceDoc(ctx, svc, req.GetSourceId(), node.MimeType, provenanceFor(req.GetSourceId(), st.RootID))
		}
		if node.Size > maxFetchBytes {
			// Pre-flight refusal (research assumption A5): named and
			// honest, issued before any Drive call — never a buffered
			// download followed by a transport-level rejection.
			return &toposv1.FetchResponse{Available: false, UnavailableReason: unavailableTooLargeToReturn}, nil
		}
		return fetchRegularFile(ctx, svc, req.GetSourceId(), node.MimeType, st.RootID)
	case toposv1.ContentVariant_CONTENT_VARIANT_PREVIEW, toposv1.ContentVariant_CONTENT_VARIANT_THUMBNAIL:
		// This plugin produces no binary rendition for any variant or MIME
		// type — a normal, expected available:false outcome, never an
		// error.
		return &toposv1.FetchResponse{Available: false, UnavailableReason: unavailableNoRendition}, nil
	default:
		// CONTENT_VARIANT_UNSPECIFIED is the zero value and is never a
		// valid request (contract/plugin-contract.md's ContentVariant
		// section).
		return nil, status.Error(codes.InvalidArgument, "gdrive: fetch: unspecified content variant")
	}
}

// fetchRegularFile issues files.get with alt=media and NO Range header —
// unlike preview.go's rangePreview, Fetch always downloads and returns
// the whole body, fetched fresh from Drive on this call alone; nothing is
// cached or memoized between calls (CONT-04). Text is populated only for
// a text-shaped MIME type (isTextShaped, shared with preview.go so the
// two RPC paths can never disagree); every other regular MIME type gets
// Available: true with an empty Text and the real downloaded SizeBytes —
// this plugin returns no binary rendition (MimeType stays "" and Data
// stays unset) for a regular file, so a caller cannot distinguish "empty
// binary body" from "no rendition returned" by any other observable
// field. Every failure path here is a genuine transport/read failure and
// returns codes.Unavailable — never a partial, truncated Text presented
// as complete.
func fetchRegularFile(ctx context.Context, svc *drive.Service, fileID, mimeType, rootID string) (*toposv1.FetchResponse, error) {
	resp, err := svc.Files.Get(fileID).Context(ctx).Download()
	if err != nil {
		log.Printf("gdrive: fetch: download %s: %s", fileID, err)
		return nil, status.Error(codes.Unavailable, "gdrive: fetch: download failed")
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("gdrive: fetch: read %s: %s", fileID, err)
		return nil, status.Error(codes.Unavailable, "gdrive: fetch: read failed")
	}

	text := ""
	if isTextShaped(mimeType) {
		text = string(data)
	}

	return &toposv1.FetchResponse{
		Available:  true,
		Text:       text,
		SizeBytes:  int64(len(data)),
		Provenance: provenanceFor(fileID, rootID),
	}, nil
}
