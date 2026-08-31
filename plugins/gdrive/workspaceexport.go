// Package main's workspaceexport.go fills preview.go's buildPreview and
// fetchcontent.go's fetchItem Workspace-native branches: Google Docs,
// Sheets, and Slides have no native bytes files.get can download, so both
// RPC paths route a Workspace-native MIME type here instead, through the
// SAME workspaceExportMIME lookup table (never an independently
// re-derived classification), so the two RPCs can never disagree about
// which export format a given Workspace document gets (CONT-02). Every
// export failure logs the Drive file id and a fixed reason only — never
// the document's own Name, never any exported byte — the same discipline
// preview.go already establishes.
package main

import (
	"context"
	"errors"
	"io"
	"log"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// workspaceExportMIME is the export table both exportPreview (Match-time)
// and fetchWorkspaceDoc (Fetch-time) consult — locked to exactly the
// three PRD-scoped Workspace types (CONTRACT-GAPS.md GAP-16, GAP-17):
// every other application/vnd.google-apps.* MIME type, including drawing
// (technically exportable by Drive but outside PRD scope) and shortcut
// (deliberately never resolved to its target), is a declined format,
// decided by table lookup alone before any Drive call is made. Adding a
// fourth entry requires a recorded scope change — T-04-12's exact-match
// test pins this table to exactly 3 entries.
var workspaceExportMIME = map[string]string{
	"application/vnd.google-apps.document":     "text/plain",
	"application/vnd.google-apps.spreadsheet":  "text/csv",
	"application/vnd.google-apps.presentation": "text/plain",
}

// exportSizeLimitReason is the exact structured-error Reason token Drive's
// files.export endpoint returns when the target document's export would
// exceed the 10 MB export ceiling (04-RESEARCH.md, verified against
// google.golang.org/api@v0.293.0/googleapi/googleapi.go's own Error.Error()
// formatting and a real-world reproduction of the exact string). This is
// the one token classifyExportError matches on — never the human-readable
// Message text, which carries no stability guarantee.
const exportSizeLimitReason = "exportSizeLimitExceeded"

// unavailableExportCeiling and unavailableDeclinedFormat are CONT-03's two
// distinct, stable, plugin-local named reasons (CONTRACT-GAPS.md GAP-18):
// no permitted input specifies verbatim text for either — unlike the four
// Phase 5 health sentences, which ARE contract-mandated verbatim. Each is
// deliberately worded so it can never be confused with a Phase 5 health
// sentence or a Phase 2 status constant; TestUnavailableExportReasons_...
// in workspaceexport_test.go pins that separation, mirroring
// TestUnavailableReasons_AreDistinctFromEachOtherAndFromEveryOtherStatusString
// (fetchcontent_test.go)'s existing guard for the regular-file reasons.
const (
	unavailableExportCeiling  = "this document's export exceeds Google Drive's 10 MB export limit"
	unavailableDeclinedFormat = "this Google Workspace item type has no export format this plugin supports"
)

// classifyExportError distinguishes CONT-03's export-ceiling cause from
// every other files.export failure, using the exact errors.As(err, &gerr)
// idiom changepoll.go's isStalePageToken already establishes for
// *googleapi.Error (changepoll.go:69-72). It iterates the structured
// Errors slice and matches each item's Reason field against
// exportSizeLimitReason — NEVER against Message or err.Error()'s formatted
// output, which carry no API stability guarantee (04-RESEARCH.md Pitfall
// 2/Pattern 3). isCeiling is true, with the ceiling reason, only on a
// genuine structured-reason match; every other error (a different
// structured reason, a non-googleapi error, a transport failure) reports
// isCeiling false, and the caller returns a generic codes.Unavailable.
//
// Deliberately reactive, never proactive: driveNode.Size reflects Drive's
// own internal representation size for a Workspace-editor file, not any
// export format's byte count, so it cannot predict an export-ceiling
// failure (04-RESEARCH.md Pitfall 4) — a future "optimization" pre-checking
// Size before calling files.export would have to argue with this comment
// and with the recorded CONTRACT-GAPS.md reasoning first.
func classifyExportError(err error) (reason string, isCeiling bool) {
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		for _, item := range gerr.Errors {
			if item.Reason == exportSizeLimitReason {
				return unavailableExportCeiling, true
			}
		}
	}
	return "", false
}

// exportPreview issues a files.export call for a Workspace-native document
// and returns a rune-safe, previewRuneLimit-bounded preview string, or ""
// on a table-lookup miss (no Drive call issued for a declined format) or
// any failure — including a 10 MB export-ceiling failure, which degrades
// to an empty preview here exactly like every other failure (CONT-03's
// named-unavailable reasons apply only to Fetch's fetchWorkspaceDoc,
// never to Item.preview). UNLIKE rangePreview, no Range header is ever
// set here — files.export does not honor partial downloads
// (04-RESEARCH.md Pitfall 1); the whole Google-capped (<=10 MB) export
// body is read, then truncated client-side in Go. Every failure path logs
// the file id and the failing stage only.
func exportPreview(ctx context.Context, svc *drive.Service, fileID, mimeType string) string {
	exportMIME, ok := workspaceExportMIME[mimeType]
	if !ok {
		return ""
	}

	resp, err := svc.Files.Export(fileID, exportMIME).Context(ctx).Download()
	if err != nil {
		log.Printf("gdrive: preview: export %s: %s", fileID, err)
		return ""
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("gdrive: preview: read export %s: %s", fileID, err)
		return ""
	}
	return truncateRunes(string(data), previewRuneLimit)
}

// fetchWorkspaceDoc issues a files.export call for a Workspace-native
// document's Fetch CONTENT_VARIANT_FULL request, returning the whole
// exported text fetched fresh from Drive on this call alone — nothing is
// cached or memoized (CONT-04). MimeType stays "" and Data stays unset: a
// text export is not a binary rendition, so no ContentShape is ever
// needed.
//
// CONT-03's two distinct causes are both the contract's normal, expected
// available:false outcome (contract/plugin-contract.md:783-798), never a
// gRPC error: a table-lookup miss returns unavailableDeclinedFormat
// WITHOUT issuing any Drive call; a files.export failure classified as the
// size ceiling (classifyExportError) returns unavailableExportCeiling.
// Every OTHER export failure — a different structured reason, a transport
// error, or a body-read failure after partial bytes — is a genuine
// failure and returns codes.Unavailable with a fixed message naming
// neither the document nor its content; a partially-read body is
// discarded, never returned as a short Text with Available: true.
func fetchWorkspaceDoc(ctx context.Context, svc *drive.Service, fileID, mimeType string, provenance map[string]string) (*toposv1.FetchResponse, error) {
	exportMIME, ok := workspaceExportMIME[mimeType]
	if !ok {
		return &toposv1.FetchResponse{Available: false, UnavailableReason: unavailableDeclinedFormat}, nil
	}

	resp, err := svc.Files.Export(fileID, exportMIME).Context(ctx).Download()
	if err != nil {
		if reason, isCeiling := classifyExportError(err); isCeiling {
			log.Printf("gdrive: fetch: export %s: ceiling exceeded", fileID)
			return &toposv1.FetchResponse{Available: false, UnavailableReason: reason}, nil
		}
		log.Printf("gdrive: fetch: export %s: %s", fileID, err)
		return nil, status.Error(codes.Unavailable, "gdrive: fetch: export failed")
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("gdrive: fetch: read export %s: %s", fileID, err)
		return nil, status.Error(codes.Unavailable, "gdrive: fetch: read export failed")
	}

	return &toposv1.FetchResponse{
		Available:  true,
		Text:       string(data),
		SizeBytes:  int64(len(data)),
		Provenance: provenance,
	}, nil
}
