package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// fakeTags is the full set of tags this fixture server knows about,
// including "Household" — a superstring of the keyword "house" that must
// NOT be matched by an exact, case-insensitive lookup (D-03).
var fakeTags = []tagResult{
	{ID: 1, Name: "House"},
	{ID: 2, Name: "Household"},
	{ID: 3, Name: "house-move"},
}

var fakeDocs = map[int]documentResult{
	42: {ID: 42, Title: "Completion statement", Content: "some   ocr\ttext", Created: "2026-07-20", Added: "2026-07-20T10:15:00Z", Tags: []int{1, 3}},
	43: {ID: 43, Title: "Household inventory", Content: "unrelated", Created: "2026-07-19", Added: "2026-07-19T09:00:00Z", Tags: []int{2}},
}

func newFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		nameFilter := r.URL.Query().Get("name__iexact")

		var results []tagResult
		if nameFilter != "" {
			for _, tg := range fakeTags {
				if strings.EqualFold(tg.Name, nameFilter) {
					results = append(results, tg)
				}
			}
		} else {
			results = fakeTags
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tagsPage{Results: results})
	})

	mux.HandleFunc("/api/documents/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		idsParam := r.URL.Query().Get("tags__id__in")
		if idsParam == "" {
			t.Fatalf("expected tags__id__in query param")
		}

		wantIDs := map[string]bool{}
		for _, s := range strings.Split(idsParam, ",") {
			wantIDs[s] = true
		}

		var results []documentResult
		for _, doc := range fakeDocs {
			for _, tagID := range doc.Tags {
				if wantIDs[strconv.Itoa(tagID)] {
					results = append(results, doc)
					break
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(documentsPage{Results: results})
	})

	return httptest.NewServer(mux)
}

func TestResolveTagIDs_ExactCaseInsensitive_NoSubstringMatch(t *testing.T) {
	srv := newFixtureServer(t)
	defer srv.Close()

	c := NewClient(srv.URL, "test-token", "10")
	ids, err := c.ResolveTagIDs(context.Background(), []string{"house"})
	if err != nil {
		t.Fatalf("ResolveTagIDs: %v", err)
	}

	// "house" must resolve to the "House" tag (case-insensitive exact) and
	// must NOT resolve to "Household" (superstring) — D-03.
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("expected exactly tag ID 1 (House), got %v", ids)
	}
}

func TestResolveTagIDs_DecomposedFormDoesNotMatchPrecomposed(t *testing.T) {
	srv := newFixtureServer(t)
	defer srv.Close()

	c := NewClient(srv.URL, "test-token", "10")

	// strings.EqualFold performs Unicode simple case folding only, with no
	// NFC/NFD normalization (KERN-01/encoding). "house" (precomposed ASCII)
	// must not match a decomposed-form variant that a normalizing compare
	// would conflate — here exercised as an exact-lookup miss, since the
	// fixture holds no decomposed-form tag.
	ids, err := c.ResolveTagIDs(context.Background(), []string{"housé"}) // "house" + combining acute
	if err != nil {
		t.Fatalf("ResolveTagIDs: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("expected zero matches for a decomposed-form keyword with no exact tag counterpart, got %v", ids)
	}
}

func TestListDocuments_FetchesOnlyMatchingTagDocuments(t *testing.T) {
	srv := newFixtureServer(t)
	defer srv.Close()

	c := NewClient(srv.URL, "test-token", "10")
	docs, err := c.ListDocuments(context.Background(), []int{1, 3}) // House, house-move
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(docs) != 1 || docs[0].ID != 42 {
		t.Fatalf("expected exactly document 42, got %+v", docs)
	}
}

func TestListDocuments_HouseholdTagExcludedWhenNotRequested(t *testing.T) {
	srv := newFixtureServer(t)
	defer srv.Close()

	c := NewClient(srv.URL, "test-token", "10")
	docs, err := c.ListDocuments(context.Background(), []int{1}) // House only
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	for _, d := range docs {
		if d.ID == 43 {
			t.Errorf("document 43 (tagged Household only) must not appear when only House (id 1) was requested")
		}
	}
}

func TestListDocuments_EmptyTagIDsReturnsNoDocsNoRequest(t *testing.T) {
	called := false
	mux := http.NewServeMux()
	mux.HandleFunc("/api/documents/", func(w http.ResponseWriter, r *http.Request) {
		called = true
		_ = json.NewEncoder(w).Encode(documentsPage{})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL, "test-token", "10")
	docs, err := c.ListDocuments(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if docs != nil {
		t.Errorf("expected nil docs for empty tag ID list, got %+v", docs)
	}
	if called {
		t.Errorf("expected no HTTP request for an empty tag ID list")
	}
}

// TestMatch_ReadsTypedTagsFieldAndIgnoresUndeclaredKey proves the plugin's
// Match RPC resolves items from match_fields["tags"] and ignores any other
// key in the request map (D-05) — mirrors the mock plugin's reference
// undeclared-key-ignored case.
func TestMatch_ReadsTypedTagsFieldAndIgnoresUndeclaredKey(t *testing.T) {
	srv := newFixtureServer(t)
	defer srv.Close()

	p := NewSourcePlugin(srv.URL, "test-token", "10")

	desc, err := p.Describe(context.Background(), &toposv1.DescribeRequest{})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if len(desc.GetMatchVocabulary()) != 1 || desc.GetMatchVocabulary()[0] != "tags" {
		t.Fatalf("expected match_vocabulary [\"tags\"], got %v", desc.GetMatchVocabulary())
	}

	req := &toposv1.MatchRequest{MatchFields: map[string]*toposv1.StringList{
		"tags":         {Values: []string{"house"}},
		"conversations": {Values: []string{"should-be-ignored"}},
	}}
	resp, err := p.Match(context.Background(), req)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(resp.GetItems()) != 1 || resp.GetItems()[0].GetSourceId() != "42" {
		t.Fatalf("expected exactly document 42 matched via the typed tags field, got %+v", resp.GetItems())
	}
}

// TestMatch_EmptyTagsValueListMatchesNothing proves an empty "tags" value
// list resolves to zero tag IDs and therefore zero items, never everything.
func TestMatch_EmptyTagsValueListMatchesNothing(t *testing.T) {
	srv := newFixtureServer(t)
	defer srv.Close()

	p := NewSourcePlugin(srv.URL, "test-token", "10")
	resp, err := p.Match(context.Background(), &toposv1.MatchRequest{MatchFields: map[string]*toposv1.StringList{
		"tags": {Values: nil},
	}})
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(resp.GetItems()) != 0 {
		t.Errorf("expected zero items for an empty tags value list, got %d", len(resp.GetItems()))
	}
}

func TestGetJSON_SendsAuthAndAcceptHeaders(t *testing.T) {
	var gotAuth, gotAccept string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		_ = json.NewEncoder(w).Encode(tagsPage{})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL, "s3cr3t", "10")
	if _, err := c.ResolveTagIDs(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("ResolveTagIDs: %v", err)
	}

	if gotAuth != "Token s3cr3t" {
		t.Errorf("expected Authorization 'Token s3cr3t', got %q", gotAuth)
	}
	if gotAccept != "application/json; version=10" {
		t.Errorf("expected versioned Accept header, got %q", gotAccept)
	}
}
