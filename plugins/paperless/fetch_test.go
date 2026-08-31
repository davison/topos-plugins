package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

func newFetchTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestFetch_FullVariant_TextAndRendition(t *testing.T) {
	pdfBytes := []byte("%PDF-1.4 fake rendition bytes")

	srv := newFetchTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/documents/42/":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 42, "title": "Completion statement", "content": "extracted document text",
				"created": "2026-01-01", "added": "2026-01-01T00:00:00Z", "tags": []int{},
			})
		case "/api/documents/42/preview/":
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write(pdfBytes)
		default:
			http.NotFound(w, r)
		}
	})

	p := NewSourcePlugin(srv.URL, "test-token", "10")
	resp, err := p.Fetch(context.Background(), &toposv1.FetchRequest{
		SourceId: "42", Variant: toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !resp.GetAvailable() {
		t.Fatal("expected available=true")
	}
	if resp.GetText() != "extracted document text" {
		t.Errorf("text = %q", resp.GetText())
	}
	if resp.GetMimeType() != "application/pdf" {
		t.Errorf("mime_type = %q", resp.GetMimeType())
	}
	if string(resp.GetData()) != string(pdfBytes) {
		t.Errorf("data mismatch: got %d bytes, want %d", len(resp.GetData()), len(pdfBytes))
	}
	if resp.GetSizeBytes() != int64(len(pdfBytes)) {
		t.Errorf("size_bytes = %d, want %d", resp.GetSizeBytes(), len(pdfBytes))
	}
}

func TestFetch_FullVariant_EmptyExtractedContent(t *testing.T) {
	srv := newFetchTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/documents/7/":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 7, "title": "Blank scan", "content": "",
				"created": "2026-01-01", "added": "2026-01-01T00:00:00Z", "tags": []int{},
			})
		case "/api/documents/7/preview/":
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	})

	p := NewSourcePlugin(srv.URL, "test-token", "10")
	resp, err := p.Fetch(context.Background(), &toposv1.FetchRequest{
		SourceId: "7", Variant: toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.GetText() != "" {
		t.Errorf("expected empty text, got %q", resp.GetText())
	}
	if resp.GetAvailable() {
		t.Error("expected available=false when the rendition 404s")
	}
	if resp.GetUnavailableReason() == "" {
		t.Error("expected a non-empty unavailable_reason")
	}
}

func TestFetch_PreviewVariant_404IsUnavailableNotError(t *testing.T) {
	srv := newFetchTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	p := NewSourcePlugin(srv.URL, "test-token", "10")
	resp, err := p.Fetch(context.Background(), &toposv1.FetchRequest{
		SourceId: "99", Variant: toposv1.ContentVariant_CONTENT_VARIANT_PREVIEW,
	})
	if err != nil {
		t.Fatalf("expected no error for a 404 rendition, got %v", err)
	}
	if resp.GetAvailable() {
		t.Error("expected available=false")
	}
	if len(resp.GetData()) != 0 {
		t.Error("expected no data chunks for an unavailable rendition")
	}
	if resp.GetUnavailableReason() == "" {
		t.Error("expected a non-empty unavailable_reason")
	}
}

func TestFetch_UnreachableSource_ReturnsUnavailable(t *testing.T) {
	srv := newFetchTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	srv.Close() // closed before use: every request fails to connect

	p := NewSourcePlugin(srv.URL, "test-token", "10")
	_, err := p.Fetch(context.Background(), &toposv1.FetchRequest{
		SourceId: "1", Variant: toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err == nil {
		t.Fatal("expected an error for an unreachable source")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unavailable {
		t.Fatalf("expected codes.Unavailable, got %v", err)
	}
}

func TestFetch_UnknownDocument_ReturnsNotFound(t *testing.T) {
	srv := newFetchTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// The document detail endpoint itself 404s — the document does
		// not exist, distinct from "exists but has no rendition".
		http.NotFound(w, r)
	})

	p := NewSourcePlugin(srv.URL, "test-token", "10")
	_, err := p.Fetch(context.Background(), &toposv1.FetchRequest{
		SourceId: "99999999", Variant: toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err == nil {
		t.Fatal("expected an error for an unknown document id")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.NotFound {
		t.Fatalf("expected codes.NotFound, got %v", err)
	}
}
