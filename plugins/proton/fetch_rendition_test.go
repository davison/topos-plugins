// These tests exist because the plugin — not the shared detail pane —
// owns the choice of representation a Proton email is rendered as.
// web/src/lib/components/DetailPane.svelte still branches only on the
// SHAPE of the content it is handed and names no source: a UI-side
// "prefer text over rendition" rule was ruled out by
// plugins/silverbullet/plugin.go's fetchFull, which legitimately returns
// BOTH a text/html rendition AND Text together, so a source-agnostic pane
// cannot be where this decision lives. It has to be made here, once, in
// the producer.
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// TestFetch_PrefersPlainTextOverHTMLRendition is 03-09-PLAN.md Task 1's
// first proof: a multipart/alternative message carrying both a
// text/plain and a text/html part must arrive at the browser as the
// plain text ALONE — no rendition at all — because the rendition is
// served under a CSP that blocks every subresource, so an image-bearing
// HTML design fetched instead could only ever render as broken images.
func TestFetch_PrefersPlainTextOverHTMLRendition(t *testing.T) {
	serverAddr := newTestIMAPServer(t)
	plugin := newTestPluginDialingServer(t, serverAddr)

	ctx := context.Background()
	matchResp, err := plugin.Match(ctx, foldersMatchReq([]string{"DeltaTeam"}))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(matchResp.GetItems()) != 1 {
		t.Fatalf("Match: got %d items, want exactly 1", len(matchResp.GetItems()))
	}
	item := matchResp.GetItems()[0]

	fetchResp, err := plugin.Fetch(ctx, &toposv1.FetchRequest{
		SourceId: item.GetSourceId(),
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !fetchResp.GetAvailable() {
		t.Fatalf("Fetch: Available = %v, want true", fetchResp.GetAvailable())
	}
	if !strings.Contains(fetchResp.GetText(), "distinctive plain text marker sentence") {
		t.Errorf("Fetch: Text = %q, want it to contain the delta fixture's plain-text marker sentence", fetchResp.GetText())
	}
	// Assert the absence of the rendition on all three fields: a
	// partially-populated rendition would confuse the kernel's
	// MimeType != "" predicate (kernel/httpapi/item.go) and must fail
	// loudly here.
	if fetchResp.GetMimeType() != "" {
		t.Errorf("Fetch: MimeType = %q, want empty string (no rendition when the plain text is renderable)", fetchResp.GetMimeType())
	}
	if len(fetchResp.GetData()) != 0 {
		t.Errorf("Fetch: len(Data) = %d, want 0 (no rendition when the plain text is renderable)", len(fetchResp.GetData()))
	}
	if fetchResp.GetSizeBytes() != 0 {
		t.Errorf("Fetch: SizeBytes = %d, want 0 (no rendition when the plain text is renderable)", fetchResp.GetSizeBytes())
	}
}

// TestFetch_HTMLOnlyMessageKeepsTheRendition proves the fallback is kept,
// not deleted: a text/html-only message (no plain-text alternative) must
// still arrive as a text/html rendition. D-11 moved sanitization, wrapping
// and theming to the kernel's rendition boundary
// (kernel/httpapi/rendition.go) — this plugin now returns the RAW,
// unsanitized HTML part plus the declared email content shape, never a
// wrapped document.
func TestFetch_HTMLOnlyMessageKeepsTheRendition(t *testing.T) {
	serverAddr := newTestIMAPServer(t)
	plugin := newTestPluginDialingServer(t, serverAddr)

	ctx := context.Background()
	matchResp, err := plugin.Match(ctx, foldersMatchReq([]string{"EpsilonTeam"}))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(matchResp.GetItems()) != 1 {
		t.Fatalf("Match: got %d items, want exactly 1", len(matchResp.GetItems()))
	}
	item := matchResp.GetItems()[0]

	fetchResp, err := plugin.Fetch(ctx, &toposv1.FetchRequest{
		SourceId: item.GetSourceId(),
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !fetchResp.GetAvailable() {
		t.Fatalf("Fetch: Available = %v, want true", fetchResp.GetAvailable())
	}
	if fetchResp.GetMimeType() != "text/html" {
		t.Errorf("Fetch: MimeType = %q, want %q (the fallback is kept for an HTML-only message)", fetchResp.GetMimeType(), "text/html")
	}
	if fetchResp.GetContentShape() != toposv1.ContentShape_CONTENT_SHAPE_EMAIL_HTML {
		t.Errorf("Fetch: ContentShape = %v, want CONTENT_SHAPE_EMAIL_HTML", fetchResp.GetContentShape())
	}
	data := fetchResp.GetData()
	if bytes.HasPrefix(data, []byte("<!doctype html>")) {
		t.Errorf("Fetch: Data is a wrapped document; expected the RAW, unwrapped HTML part (D-11 moved wrapping kernel-side), got %q", string(data))
	}
	if !bytes.Contains(data, []byte("Epsilon heading")) {
		t.Errorf("Fetch: Data does not contain the message's own visible text; got %q", string(data))
	}
	if fetchResp.GetSizeBytes() != int64(len(data)) {
		t.Errorf("Fetch: SizeBytes = %d, want %d (len(Data))", fetchResp.GetSizeBytes(), len(data))
	}
	if fetchResp.GetText() != "" {
		t.Errorf("Fetch: Text = %q, want empty string (the message has no renderable plain-text alternative)", fetchResp.GetText())
	}
}

// TestFetch_MessageWithNoRenderablePartIsAvailableAndEmpty proves the
// empty-input edge resolves as available-and-empty, never as an error and
// never as a fabricated rendition: a message whose only part is
// whitespace, with no HTML part either, must still Fetch cleanly.
func TestFetch_MessageWithNoRenderablePartIsAvailableAndEmpty(t *testing.T) {
	serverAddr := newTestIMAPServer(t)
	plugin := newTestPluginDialingServer(t, serverAddr)

	ctx := context.Background()
	matchResp, err := plugin.Match(ctx, foldersMatchReq([]string{"ZetaTeam"}))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(matchResp.GetItems()) != 1 {
		t.Fatalf("Match: got %d items, want exactly 1", len(matchResp.GetItems()))
	}
	item := matchResp.GetItems()[0]

	fetchResp, err := plugin.Fetch(ctx, &toposv1.FetchRequest{
		SourceId: item.GetSourceId(),
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err != nil {
		t.Fatalf("Fetch: unexpected error %v, want a normal available-and-empty response, never a gRPC error", err)
	}
	if !fetchResp.GetAvailable() {
		t.Fatalf("Fetch: Available = %v, want true", fetchResp.GetAvailable())
	}
	if fetchResp.GetMimeType() != "" {
		t.Errorf("Fetch: MimeType = %q, want empty string (no rendition fabricated for a message with nothing renderable)", fetchResp.GetMimeType())
	}
	if len(fetchResp.GetData()) != 0 {
		t.Errorf("Fetch: len(Data) = %d, want 0", len(fetchResp.GetData()))
	}
	if fetchResp.GetSizeBytes() != 0 {
		t.Errorf("Fetch: SizeBytes = %d, want 0", fetchResp.GetSizeBytes())
	}
}

// TestHasRenderableText_Boundaries pins HasRenderableText's definition of
// "whitespace" to Go's strings.TrimSpace / unicode.IsSpace semantics
// rather than to ASCII spaces only — false for every whitespace-only or
// empty input, true only for a genuine sentence.
func TestHasRenderableText_Boundaries(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"empty string", "", false},
		{"spaces and tabs", "   \t\t  ", false},
		{"CR/LF only", "\r\n\r\n", false},
		{"unicode no-break space only", "   ", false},
		{"ordinary sentence", "This is a real sentence a reader can see.", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasRenderableText(tt.in); got != tt.want {
				t.Errorf("HasRenderableText(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
