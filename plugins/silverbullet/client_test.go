package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fixtureListing mixes a markdown page, a leading-underscore library path,
// and a non-markdown file — the exact three-way split isPage must
// distinguish, mirroring the real instance's mixed listing (Task 1 Step 0
// observed all three shapes: e.g. "Decking.md", "Library/Std/Plugs/....js",
// "_resources/....jpg").
var fixtureListing = []FileMeta{
	{Name: "Decking.md", Created: 1000, LastModified: 2000, ContentType: "text/markdown", Size: 42, Perm: "ro"},
	{Name: "_plug/foo.md", Created: 1000, LastModified: 2000, ContentType: "text/markdown", Size: 10, Perm: "ro"},
	{Name: "_resources/photo.jpg", Created: 1000, LastModified: 2000, ContentType: "image/jpeg", Size: 999, Perm: "ro"},
}

func newClientFixtureServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestListFiles_DecodesBareArrayEnvelope(t *testing.T) {
	srv := newClientFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.fs" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fixtureListing)
	})

	c := NewClient(srv.URL, "test-token", "")
	files, err := c.ListFiles(context.Background())
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != len(fixtureListing) {
		t.Fatalf("expected %d files, got %d", len(fixtureListing), len(files))
	}

	var pages []FileMeta
	for _, f := range files {
		if isPage(f) {
			pages = append(pages, f)
		}
	}
	if len(pages) != 1 || pages[0].Name != "Decking.md" {
		t.Fatalf("expected isPage to keep only Decking.md, got: %+v", pages)
	}
}

func TestReadFile_ReturnsRawBytesOn200(t *testing.T) {
	const body = "# Decking\n\nsome content"
	srv := newClientFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte(body))
	})

	c := NewClient(srv.URL, "test-token", "")
	got, err := c.ReadFile(context.Background(), "Decking.md")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != body {
		t.Fatalf("unexpected body: %q", got)
	}
}

func TestReadFile_404MapsToErrNotFound(t *testing.T) {
	srv := newClientFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	c := NewClient(srv.URL, "test-token", "")
	_, err := c.ReadFile(context.Background(), "DoesNotExist.md")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected errors.Is(err, ErrNotFound), got %v", err)
	}
}

func TestReadFile_NonNotFoundStatus_ReturnsDistinctError(t *testing.T) {
	srv := newClientFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	c := NewClient(srv.URL, "test-token", "")
	_, err := c.ReadFile(context.Background(), "Decking.md")
	if err == nil {
		t.Fatal("expected a non-nil error for a 500 status")
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("expected a non-ErrNotFound error for a 500 status, got %v", err)
	}
}

// TestRequests_CarryBearerAuthHeader_NeverLogTokenValue asserts both
// halves of T-02-03: the outbound request actually carries the configured
// bearer token, and nothing this client does — including its own error
// paths — ever surfaces the token's literal value in a test failure
// message or log line.
func TestRequests_CarryBearerAuthHeader_NeverLogTokenValue(t *testing.T) {
	const token = "s3cr3t-token-value"
	var gotAuth, gotSyncMode string

	srv := newClientFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotSyncMode = r.Header.Get("X-Sync-Mode")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]FileMeta{})
	})

	c := NewClient(srv.URL, token, "")
	if _, err := c.ListFiles(context.Background()); err != nil {
		t.Fatalf("ListFiles: %v", err)
	}

	if gotAuth != "Bearer "+token {
		t.Errorf("expected Authorization 'Bearer %s', got %q", token, gotAuth)
	}
	if gotSyncMode != "true" {
		t.Errorf("expected X-Sync-Mode 'true', got %q", gotSyncMode)
	}

	// Exercise an error path too (unreachable server) and confirm the
	// resulting error string never contains the token — a client that
	// logged its own request object (forbidden per client.go's doc
	// comments) would leak it here.
	srv2 := newClientFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {})
	srv2.Close()
	c2 := NewClient(srv2.URL, token, "")
	_, err := c2.ListFiles(context.Background())
	if err == nil {
		t.Fatal("expected an error from an unreachable server")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("error message must never contain the token value, got: %v", err)
	}
}

func TestClient_NormalizesTrailingSlashInBaseURL(t *testing.T) {
	var gotPath string
	srv := newClientFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]FileMeta{})
	})

	// A trailing slash in base_url must never produce a "//.fs" request —
	// confirmed live (Task 1 Step 0) that this SilverBullet instance
	// answers a doubled-slash request with its SPA HTML shell instead of
	// the JSON listing.
	c := NewClient(srv.URL+"/", "test-token", "")
	if _, err := c.ListFiles(context.Background()); err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if gotPath != "/.fs" {
		t.Fatalf("expected request path \"/.fs\", got %q (trailing slash not normalized)", gotPath)
	}
}
