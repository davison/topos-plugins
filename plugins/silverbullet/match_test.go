package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// newMatchTestServer mirrors newClientFixtureServer/newFetchTestServer's
// established shape: one httptest.Server per test, closed via t.Cleanup.
func newMatchTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// matchTestPage mirrors fetchTestPage's shape: YAML frontmatter carrying a
// "house" tag, matched by these tests' keyword.
const matchTestPage = "---\ntags: [house]\n---\n# Decking\n\nsome *plan* text"

// matchTestPageOther carries a non-matching tag, mirroring fetchTestPage's
// use of a distinct keyword to prove selectivity.
const matchTestPageOther = "---\ntags: [food]\n---\n# Recipe\n\nsome content"

// tagsMatchReq builds a MatchRequest carrying only the "tags" field — the
// shape most of this file's pre-existing tests use, since matchTestPage's
// frontmatter tag ("house") is what they match against.
func tagsMatchReq(values []string) *toposv1.MatchRequest {
	return &toposv1.MatchRequest{MatchFields: map[string]*toposv1.StringList{
		"tags": {Values: values},
	}}
}

func TestMatch_HappyPath_ReturnsOnlyKeywordMatchedPages(t *testing.T) {
	matchListing := []FileMeta{
		{Name: "Decking.md", Created: 1000, LastModified: 2000, ContentType: "text/markdown", Size: 42, Perm: "ro"},
		{Name: "Recipe.md", Created: 1000, LastModified: 2000, ContentType: "text/markdown", Size: 42, Perm: "ro"},
		{Name: "_plug/foo.md", Created: 1000, LastModified: 2000, ContentType: "text/markdown", Size: 10, Perm: "ro"},
	}

	var plugRequested int32

	srv := newMatchTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/.fs":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(matchListing)
		case r.URL.Path == "/.fs/Decking.md":
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte(matchTestPage))
		case r.URL.Path == "/.fs/Recipe.md":
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte(matchTestPageOther))
		case r.URL.Path == "/.fs/_plug/foo.md":
			atomic.AddInt32(&plugRequested, 1)
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte(matchTestPage))
		default:
			http.NotFound(w, r)
		}
	})

	p := NewSourcePlugin(srv.URL, "test-token", "")
	resp, err := p.Match(context.Background(), tagsMatchReq([]string{"house"}))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(resp.GetItems()) != 1 {
		t.Fatalf("expected exactly 1 item, got %d: %+v", len(resp.GetItems()), resp.GetItems())
	}
	item := resp.GetItems()[0]
	if item.GetSourceId() != "Decking" {
		t.Errorf("SourceId = %q, want %q", item.GetSourceId(), "Decking")
	}
	if item.GetDeepLink() != srv.URL+"/Decking" {
		t.Errorf("DeepLink = %q, want %q", item.GetDeepLink(), srv.URL+"/Decking")
	}
	if item.GetFidelity() != toposv1.LinkFidelity_LINK_FIDELITY_EXACT {
		t.Errorf("Fidelity = %v, want LINK_FIDELITY_EXACT", item.GetFidelity())
	}
	if atomic.LoadInt32(&plugRequested) != 0 {
		t.Error("expected the leading-underscore _plug/ path to never be read (isPage should have filtered it)")
	}
}

func TestMatch_PageDeletedBetweenListingAndRead_SkippedNotFailed(t *testing.T) {
	matchListing := []FileMeta{
		{Name: "Decking.md", Created: 1000, LastModified: 2000, ContentType: "text/markdown", Size: 42, Perm: "ro"},
		{Name: "Gone.md", Created: 1000, LastModified: 2000, ContentType: "text/markdown", Size: 42, Perm: "ro"},
	}

	srv := newMatchTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/.fs":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(matchListing)
		case r.URL.Path == "/.fs/Decking.md":
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte(matchTestPage))
		case r.URL.Path == "/.fs/Gone.md":
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	})

	p := NewSourcePlugin(srv.URL, "test-token", "")
	resp, err := p.Match(context.Background(), tagsMatchReq([]string{"house"}))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(resp.GetItems()) != 1 {
		t.Fatalf("expected exactly 1 item, got %d: %+v", len(resp.GetItems()), resp.GetItems())
	}
	if resp.GetItems()[0].GetSourceId() != "Decking" {
		t.Errorf("SourceId = %q, want %q", resp.GetItems()[0].GetSourceId(), "Decking")
	}
}

func TestMatch_AllPageReadsFail_ReturnsUnavailable(t *testing.T) {
	matchListing := []FileMeta{
		{Name: "Decking.md", Created: 1000, LastModified: 2000, ContentType: "text/markdown", Size: 42, Perm: "ro"},
		{Name: "Recipe.md", Created: 1000, LastModified: 2000, ContentType: "text/markdown", Size: 42, Perm: "ro"},
	}

	srv := newMatchTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.fs":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(matchListing)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	p := NewSourcePlugin(srv.URL, "test-token", "")
	resp, err := p.Match(context.Background(), tagsMatchReq([]string{"house"}))
	if err == nil {
		t.Fatal("expected a non-nil error when every page read fails")
	}
	if resp != nil {
		t.Fatalf("expected a nil response alongside the error, got %+v", resp)
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unavailable {
		t.Fatalf("expected codes.Unavailable, got %v", err)
	}
}

func TestMatch_OutageMidSync_AuthFailure_ReturnsUnavailable(t *testing.T) {
	var matchListing []FileMeta
	for i := 0; i < 6; i++ {
		matchListing = append(matchListing, FileMeta{
			Name: pageName(i), Created: 1000, LastModified: 2000,
			ContentType: "text/markdown", Size: 42, Perm: "ro",
		})
	}

	var readCount int32

	srv := newMatchTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.fs" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(matchListing)
			return
		}
		n := atomic.AddInt32(&readCount, 1)
		if n == 1 {
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte(matchTestPage))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	})

	p := NewSourcePlugin(srv.URL, "test-token", "")
	resp, err := p.Match(context.Background(), tagsMatchReq([]string{"house"}))
	if err == nil {
		t.Fatal("expected a non-nil error when the token is revoked partway through the sync")
	}
	if resp != nil {
		t.Fatalf("expected a nil response alongside the error, got %+v", resp)
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unavailable {
		t.Fatalf("expected codes.Unavailable, got %v", err)
	}
}

func TestMatch_UnavailableError_NeverContainsBearerToken(t *testing.T) {
	const token = "s3cr3t-match-token-value"
	matchListing := []FileMeta{
		{Name: "Decking.md", Created: 1000, LastModified: 2000, ContentType: "text/markdown", Size: 42, Perm: "ro"},
	}

	srv := newMatchTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.fs":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(matchListing)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	p := NewSourcePlugin(srv.URL, token, "")
	_, err := p.Match(context.Background(), tagsMatchReq([]string{"house"}))
	if err == nil {
		t.Fatal("expected a non-nil error when every page read fails")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("error message must never contain the token value, got: %v", err)
	}
	st, ok := status.FromError(err)
	if ok && strings.Contains(st.Message(), token) {
		t.Errorf("gRPC status message must never contain the token value, got: %v", st.Message())
	}
}

// pageName returns a distinct markdown page path for index i, used by the
// mid-sync auth-failure test to build a listing large enough to guarantee
// at least one read happens before the (simulated) token revocation given
// matchConcurrency's worker pool.
func pageName(i int) string {
	return "Page" + strconv.Itoa(i) + ".md"
}

// TestMatch_UndeclaredKeyIsIgnored proves a match_fields key outside this
// plugin's declared vocabulary ("conversations", which silverbullet never
// declares) is ignored entirely — only "tags" and "pages" are read (D-05).
func TestMatch_UndeclaredKeyIsIgnored(t *testing.T) {
	matchListing := []FileMeta{
		{Name: "Decking.md", Created: 1000, LastModified: 2000, ContentType: "text/markdown", Size: 42, Perm: "ro"},
	}
	srv := newMatchTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.fs":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(matchListing)
		case "/.fs/Decking.md":
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte(matchTestPage))
		default:
			http.NotFound(w, r)
		}
	})

	p := NewSourcePlugin(srv.URL, "test-token", "")
	req := &toposv1.MatchRequest{MatchFields: map[string]*toposv1.StringList{
		"tags":          {Values: []string{"house"}},
		"conversations": {Values: []string{"should-be-ignored"}},
	}}
	resp, err := p.Match(context.Background(), req)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(resp.GetItems()) != 1 || resp.GetItems()[0].GetSourceId() != "Decking" {
		t.Fatalf("expected exactly Decking matched via the typed tags field, got %+v", resp.GetItems())
	}
}

// TestMatch_PagesFieldMatchesOnPageNameIndependentlyOfTags proves "tags"
// and "pages" are independent, unioned fields: a page whose NAME (not its
// tags) matches a "pages" value is returned even when "tags" is absent or
// non-matching, and vice versa.
func TestMatch_PagesFieldMatchesOnPageNameIndependentlyOfTags(t *testing.T) {
	matchListing := []FileMeta{
		{Name: "Decking.md", Created: 1000, LastModified: 2000, ContentType: "text/markdown", Size: 42, Perm: "ro"},
		{Name: "Recipe.md", Created: 1000, LastModified: 2000, ContentType: "text/markdown", Size: 42, Perm: "ro"},
	}
	srv := newMatchTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.fs":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(matchListing)
		case "/.fs/Decking.md":
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte(matchTestPage))
		case "/.fs/Recipe.md":
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte(matchTestPageOther))
		default:
			http.NotFound(w, r)
		}
	})

	p := NewSourcePlugin(srv.URL, "test-token", "")
	// "Recipe" matches only by page name — its tag is "food", not "house" —
	// so supplying it only under "pages" (never "tags") must still return
	// it, proving the fields are independent rather than ANDed together.
	req := &toposv1.MatchRequest{MatchFields: map[string]*toposv1.StringList{
		"pages": {Values: []string{"Recipe"}},
	}}
	resp, err := p.Match(context.Background(), req)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(resp.GetItems()) != 1 || resp.GetItems()[0].GetSourceId() != "Recipe" {
		t.Fatalf("expected exactly Recipe matched via the typed pages field, got %+v", resp.GetItems())
	}
}
