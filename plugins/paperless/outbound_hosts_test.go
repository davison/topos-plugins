package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestAllowHost_PredicateTable proves the outbound host allowlist directly:
// a client built against a given base URL permits that hostname (any
// letter case, any port), loopback addresses, and the literal "localhost";
// and refuses a foreign hostname, a foreign non-loopback IP literal, and an
// empty host. Every refusal must satisfy errors.Is(err, ErrForeignHost) —
// the test is on the sentinel specifically, not "any error".
func TestAllowHost_PredicateTable(t *testing.T) {
	c := NewClient("http://paperless.lan:8000", "test-token", "10")

	permit := []string{
		"paperless.lan",
		"PAPERLESS.LAN",
		"paperless.lan:9000",
		"127.0.0.1",
		"127.0.0.1:8080",
		"::1",
		"[::1]:8080",
		"localhost",
		"LOCALHOST",
	}
	for _, host := range permit {
		if err := c.allowHost(host); err != nil {
			t.Errorf("allowHost(%q): expected nil, got %v", host, err)
		}
	}

	refuse := []string{
		"exfil.example.invalid",
		"203.0.113.5",
		"",
	}
	for _, host := range refuse {
		err := c.allowHost(host)
		if err == nil {
			t.Errorf("allowHost(%q): expected an error, got nil", host)
			continue
		}
		if !errors.Is(err, ErrForeignHost) {
			t.Errorf("allowHost(%q): expected errors.Is(err, ErrForeignHost), got %v", host, err)
		}
	}
}

// TestDocument_CrossHostRedirect_Refused proves the guard fires before any
// connection to the foreign host is opened: paperless-ngx (or anything
// impersonating it) answers the document-detail endpoint with a 302 to a
// different host, and Document must refuse it via the sentinel — not merely
// return "some error", which an unresolvable DNS name would also produce.
func TestDocument_CrossHostRedirect_Refused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/documents/1/" {
			w.Header().Set("Location", "http://exfil.example.invalid/api/documents/1/")
			w.WriteHeader(http.StatusFound)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token", "10")
	_, err := c.Document(context.Background(), 1)
	if err == nil {
		t.Fatal("expected an error for a cross-host redirect")
	}
	if !errors.Is(err, ErrForeignHost) {
		t.Fatalf("expected errors.Is(err, ErrForeignHost), got %v", err)
	}
}

// TestDocument_SameHostRedirect_StillFollowed proves the guard does not
// break legitimate same-host redirects, in the spirit of paperless-ngx's
// own Django APPEND_SLASH behavior (a same-host 302 to a different,
// trailing-slash-terminated path). It redirects to a distinct trailing-
// slash path rather than the same path with one extra slash appended,
// because Go's own URL reference resolution (net/url's resolvePath, used
// when following a Location header) collapses repeated slashes as part of
// the standard dot-segment-removal algorithm — a literal "same path plus
// one more slash" redirect target is normalized away by Go before this
// client's guard ever sees it, which would make that specific literal
// untestable here. What's under test is unaffected either way: a
// same-host redirect to a different path is followed and returns the real
// document.
func TestDocument_SameHostRedirect_StillFollowed(t *testing.T) {
	realDoc := map[string]any{
		"id": 5, "title": "Redirected doc", "content": "text",
		"created": "2026-01-01", "added": "2026-01-01T00:00:00Z", "tags": []int{},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/documents/5/":
			http.Redirect(w, r, "/api/documents/5-canonical/", http.StatusFound)
		case "/api/documents/5-canonical/":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(realDoc)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token", "10")
	doc, err := c.Document(context.Background(), 5)
	if err != nil {
		t.Fatalf("Document: %v", err)
	}
	if doc.ID != 5 || doc.Title != "Redirected doc" {
		t.Fatalf("unexpected doc: %+v", doc)
	}
}

// TestDocument_RedirectCap_StopsLooping proves installing a custom
// CheckRedirect does not silently drop Go's own redirect-loop protection:
// a same-host endpoint that redirects to itself forever must not hang
// Document forever, and the handler must not be hit more than 11 times
// (the original request plus 10 redirects).
func TestDocument_RedirectCap_StopsLooping(t *testing.T) {
	var hits int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Redirect(w, r, r.URL.Path, http.StatusFound)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token", "10")
	_, err := c.Document(context.Background(), 6)
	if err == nil {
		t.Fatal("expected an error from an infinite same-host redirect loop")
	}
	if got := atomic.LoadInt32(&hits); got > 11 {
		t.Fatalf("handler hit %d times, want at most 11", got)
	}
}

// TestListDocuments_ForeignNextURL_RepinnedToBaseHost proves
// splitNextURL's existing re-pinning is a committed guarantee: a page-1
// response whose "next" field is an absolute URL on a foreign host results
// in the page-2 request arriving at the configured base host (with the
// page query intact), never at the foreign host named in the payload.
func TestListDocuments_ForeignNextURL_RepinnedToBaseHost(t *testing.T) {
	var gotPage2 bool

	mux := http.NewServeMux()
	mux.HandleFunc("/api/documents/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			gotPage2 = true
			_ = json.NewEncoder(w).Encode(documentsPage{
				Results: []documentResult{
					{ID: 2, Title: "Doc 2", Created: "2026-01-02", Added: "2026-01-02T00:00:00Z", Tags: []int{1}},
				},
			})
			return
		}
		next := "http://exfil.example.invalid/api/documents/?tags__id__in=1&page=2"
		_ = json.NewEncoder(w).Encode(documentsPage{
			Next: &next,
			Results: []documentResult{
				{ID: 1, Title: "Doc 1", Created: "2026-01-01", Added: "2026-01-01T00:00:00Z", Tags: []int{1}},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL, "test-token", "10")
	docs, err := c.ListDocuments(context.Background(), []int{1})
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if !gotPage2 {
		t.Fatal("expected page 2 to be requested against the configured base host, not the foreign host named in next")
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 documents across both pages, got %d: %+v", len(docs), docs)
	}
}
