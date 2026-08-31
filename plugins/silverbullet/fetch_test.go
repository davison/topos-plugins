package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

const fetchTestPage = "---\ntags: [house]\n---\n# Decking\n\nsome *plan* text"

func TestFetch_FullVariant_AvailableTextHTMLWithText(t *testing.T) {
	srv := newFetchTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.fs/Decking.md" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte(fetchTestPage))
	})

	p := NewSourcePlugin(srv.URL, "test-token", "")
	resp, err := p.Fetch(context.Background(), &toposv1.FetchRequest{
		SourceId: "Decking", Variant: toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !resp.GetAvailable() {
		t.Fatal("expected available=true")
	}
	if resp.GetMimeType() != "text/html" {
		t.Errorf("mime_type = %q, want text/html", resp.GetMimeType())
	}
	if len(resp.GetData()) == 0 {
		t.Error("expected non-empty rendered HTML data")
	}
	if !strings.Contains(string(resp.GetData()), "<h1") {
		t.Errorf("expected rendered HTML to contain an h1, got: %s", resp.GetData())
	}
	if !strings.Contains(resp.GetText(), "# Decking") {
		t.Errorf("expected Text to be the frontmatter-stripped raw markdown, got: %q", resp.GetText())
	}
	if strings.Contains(resp.GetText(), "tags:") {
		t.Errorf("expected Text to have the frontmatter stripped, got: %q", resp.GetText())
	}
}

func TestFetch_PreviewVariant_SameSanitizedHTMLAsFull(t *testing.T) {
	srv := newFetchTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte(fetchTestPage))
	})

	p := NewSourcePlugin(srv.URL, "test-token", "")
	full, err := p.Fetch(context.Background(), &toposv1.FetchRequest{
		SourceId: "Decking", Variant: toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err != nil {
		t.Fatalf("Fetch(FULL): %v", err)
	}
	preview, err := p.Fetch(context.Background(), &toposv1.FetchRequest{
		SourceId: "Decking", Variant: toposv1.ContentVariant_CONTENT_VARIANT_PREVIEW,
	})
	if err != nil {
		t.Fatalf("Fetch(PREVIEW): %v", err)
	}
	if full.GetMimeType() != preview.GetMimeType() || string(full.GetData()) != string(preview.GetData()) {
		t.Errorf("expected FULL and PREVIEW to return identical mime_type/data, got FULL=%q/%q PREVIEW=%q/%q",
			full.GetMimeType(), full.GetData(), preview.GetMimeType(), preview.GetData())
	}
}

func TestFetch_ThumbnailVariant_UnavailableNoError(t *testing.T) {
	srv := newFetchTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request for thumbnail variant: %s", r.URL.Path)
	})

	p := NewSourcePlugin(srv.URL, "test-token", "")
	resp, err := p.Fetch(context.Background(), &toposv1.FetchRequest{
		SourceId: "Decking", Variant: toposv1.ContentVariant_CONTENT_VARIANT_THUMBNAIL,
	})
	if err != nil {
		t.Fatalf("expected no error for THUMBNAIL, got %v", err)
	}
	if resp.GetAvailable() {
		t.Error("expected available=false for THUMBNAIL")
	}
	if resp.GetUnavailableReason() == "" {
		t.Error("expected a non-empty unavailable_reason")
	}
}

func TestFetch_UnknownPage_ReturnsNotFound(t *testing.T) {
	srv := newFetchTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	p := NewSourcePlugin(srv.URL, "test-token", "")
	_, err := p.Fetch(context.Background(), &toposv1.FetchRequest{
		SourceId: "DoesNotExist", Variant: toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err == nil {
		t.Fatal("expected an error for an unknown page")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.NotFound {
		t.Fatalf("expected codes.NotFound, got %v", err)
	}
}

func TestFetch_UnreachableSource_ReturnsUnavailable(t *testing.T) {
	srv := newFetchTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	srv.Close() // closed before use: every request fails to connect

	p := NewSourcePlugin(srv.URL, "test-token", "")
	_, err := p.Fetch(context.Background(), &toposv1.FetchRequest{
		SourceId: "Decking", Variant: toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err == nil {
		t.Fatal("expected an error for an unreachable source")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unavailable {
		t.Fatalf("expected codes.Unavailable, got %v", err)
	}
}
