package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// newFetchTestPlugin builds a SourcePlugin against a fresh fixture
// database (byte_identical_test.go's buildFixtureDatabase — the shared
// fixture-at-test-time convention this package already established, no
// committed binary fixture) and returns it along with the two digest
// source_ids the fixture data actually produces: the group's day1 digest
// (2 messages) and the private conversation's day1 digest (1 message).
func newFetchTestPlugin(t *testing.T) (plugin *SourcePlugin, groupDay1SourceID, privateDay1SourceID string) {
	t.Helper()

	configDir := t.TempDir()
	sqlDir := filepath.Join(configDir, "sql")
	if err := os.MkdirAll(sqlDir, 0o755); err != nil {
		t.Fatalf("create fixture sql dir: %v", err)
	}
	dbPath := filepath.Join(sqlDir, "db.sqlite")
	buildFixtureDatabase(t, dbPath, highestSupportedSchemaVersion)

	configJSON := `{"key":"` + fixtureKeyHex + `"}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configJSON), 0o600); err != nil {
		t.Fatalf("write fixture config.json: %v", err)
	}

	plugin = NewSourcePlugin(configDir)
	return plugin, sourceIDForDigest("conv-group", "2026-01-05"), sourceIDForDigest("conv-private", "2026-01-05")
}

// TestFetch_FullReturnsUnwrappedTranscriptFragment proves D-11's cutover:
// Fetch returns the RAW, unsanitized, unwrapped transcript fragment plus
// the declared chat content shape — sanitization, wrapping and theming
// now happen at the kernel's rendition boundary
// (kernel/httpapi/rendition.go), not in this plugin.
func TestFetch_FullReturnsUnwrappedTranscriptFragment(t *testing.T) {
	plugin, groupSourceID, _ := newFetchTestPlugin(t)
	resp, err := plugin.Fetch(context.Background(), &toposv1.FetchRequest{
		SourceId: groupSourceID,
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err != nil {
		t.Fatalf("Fetch(FULL): %v", err)
	}
	if !resp.GetAvailable() {
		t.Fatalf("expected Available=true, got response: %+v", resp)
	}
	if resp.GetMimeType() != "text/html" {
		t.Errorf("expected mime_type text/html, got %q", resp.GetMimeType())
	}
	if resp.GetContentShape() != toposv1.ContentShape_CONTENT_SHAPE_CHAT_TRANSCRIPT {
		t.Errorf("expected ContentShape CONTENT_SHAPE_CHAT_TRANSCRIPT, got %v", resp.GetContentShape())
	}
	if resp.GetSizeBytes() == 0 || len(resp.GetData()) == 0 {
		t.Errorf("expected non-empty size_bytes and data, got size=%d data_len=%d", resp.GetSizeBytes(), len(resp.GetData()))
	}
	if strings.HasPrefix(string(resp.GetData()), "<!doctype html>") {
		t.Errorf("expected an UNWRAPPED fragment (no doctype) — wrapping is now the kernel's job, got: %s", resp.GetData()[:min(40, len(resp.GetData()))])
	}
	if !strings.Contains(string(resp.GetData()), `class="run`) {
		t.Errorf("expected the transcript's own run/bubble markup to be present, got: %s", resp.GetData())
	}
}

func TestFetch_PreviewReturnsIdenticalUnwrappedFragment(t *testing.T) {
	plugin, groupSourceID, _ := newFetchTestPlugin(t)
	resp, err := plugin.Fetch(context.Background(), &toposv1.FetchRequest{
		SourceId: groupSourceID,
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_PREVIEW,
	})
	if err != nil {
		t.Fatalf("Fetch(PREVIEW): %v", err)
	}
	if !resp.GetAvailable() || resp.GetMimeType() != "text/html" || len(resp.GetData()) == 0 {
		t.Fatalf("expected an available text/html rendition with data, got: %+v", resp)
	}
	if resp.GetContentShape() != toposv1.ContentShape_CONTENT_SHAPE_CHAT_TRANSCRIPT {
		t.Errorf("expected ContentShape CONTENT_SHAPE_CHAT_TRANSCRIPT, got %v", resp.GetContentShape())
	}
}

func TestFetch_ThumbnailAlwaysUnavailable(t *testing.T) {
	plugin, groupSourceID, _ := newFetchTestPlugin(t)
	resp, err := plugin.Fetch(context.Background(), &toposv1.FetchRequest{
		SourceId: groupSourceID,
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_THUMBNAIL,
	})
	if err != nil {
		t.Fatalf("Fetch(THUMBNAIL): %v", err)
	}
	if resp.GetAvailable() {
		t.Error("expected THUMBNAIL to always be unavailable")
	}
	if resp.GetUnavailableReason() != noThumbnailReason {
		t.Errorf("expected the fixed unavailable reason %q, got %q", noThumbnailReason, resp.GetUnavailableReason())
	}
	if len(resp.GetData()) != 0 {
		t.Errorf("expected no data for an unavailable rendition, got %d bytes", len(resp.GetData()))
	}
}

func TestFetch_UnspecifiedVariantIsInvalidArgument(t *testing.T) {
	plugin, groupSourceID, _ := newFetchTestPlugin(t)
	_, err := plugin.Fetch(context.Background(), &toposv1.FetchRequest{
		SourceId: groupSourceID,
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_UNSPECIFIED,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected codes.InvalidArgument, got %v", err)
	}
}

func TestFetch_UnknownSourceIDIsNotFound(t *testing.T) {
	plugin, _, _ := newFetchTestPlugin(t)
	_, err := plugin.Fetch(context.Background(), &toposv1.FetchRequest{
		SourceId: sourceIDForDigest("does-not-exist", "2026-01-05"),
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if status.Code(err) != codes.NotFound {
		t.Errorf("expected codes.NotFound, got %v", err)
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("expected the error to name the unknown source_id, got %v", err)
	}
}

func TestFetch_MalformedSourceIDIsNotFound(t *testing.T) {
	plugin, _, _ := newFetchTestPlugin(t)
	_, err := plugin.Fetch(context.Background(), &toposv1.FetchRequest{
		SourceId: "not-a-valid-source-id",
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if status.Code(err) != codes.NotFound {
		t.Errorf("expected codes.NotFound for a malformed source_id, got %v", err)
	}
}

func TestFetch_SingleMessageDayRendersExactlyOneBubble(t *testing.T) {
	plugin, _, privateSourceID := newFetchTestPlugin(t)
	resp, err := plugin.Fetch(context.Background(), &toposv1.FetchRequest{
		SourceId: privateSourceID,
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err != nil {
		t.Fatalf("Fetch(FULL): %v", err)
	}
	doc := string(resp.GetData())
	if got := strings.Count(doc, `class="bubble`); got != 1 {
		t.Errorf("expected exactly one bubble for a single-message day, got %d in document: %s", got, doc)
	}
}
