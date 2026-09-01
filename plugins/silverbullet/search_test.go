package main

import (
	"encoding/json"
	"net/http"
	"testing"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestSearch_WithinTagMembershipOverPageBodies (M2-R2): the same listing and
// page reads Match does; only member pages (by tag) are searched; the query
// reaches the body; required terms AND; empty membership refused.
func TestSearch_WithinTagMembershipOverPageBodies(t *testing.T) {
	pages := map[string]string{
		"Decking.md": "---\ntags: [house]\n---\n# Decking\n\nThe decking plan mentions the boiler cupboard.",
		"Recipe.md":  "---\ntags: [food]\n---\n# Recipe\n\nboiler? no — a boiled egg.",
		"Heating.md": "---\ntags: [house]\n---\n# Heating\n\nNothing relevant.",
	}
	listing := []FileMeta{}
	for name := range pages {
		listing = append(listing, FileMeta{Name: name, Created: 1000, LastModified: 2000, ContentType: "text/markdown", Size: 42, Perm: "ro"})
	}
	srv := newMatchTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.fs" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(listing)
			return
		}
		name := r.URL.Path[len("/.fs/"):]
		if body, ok := pages[name]; ok {
			_, _ = w.Write([]byte(body))
			return
		}
		http.NotFound(w, r)
	})
	p := NewSourcePlugin(srv.URL, "token", "")
	if _, err := p.Search(t.Context(), &toposv1.SearchRequest{Query: "boiler"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty membership must be refused, got %v", err)
	}
	req := &toposv1.SearchRequest{Query: "boiler", MatchFields: map[string]*toposv1.StringList{"tags": {Values: []string{"house"}}}}
	resp, err := p.Search(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetHits()) != 1 || resp.GetHits()[0].GetItem().GetTitle() != "Decking" {
		t.Fatalf("expected only the house-tagged page mentioning the boiler (Recipe is not a member), got %+v", resp.GetHits())
	}
	if resp.GetHits()[0].GetMatchedIn() != toposv1.MatchedIn_MATCHED_IN_BODY {
		t.Errorf("matched in the body: %v", resp.GetHits()[0].GetMatchedIn())
	}
	req.RequiredTerms = []string{"cupboard"}
	if resp, _ = p.Search(t.Context(), req); len(resp.GetHits()) != 1 {
		t.Errorf("required term present: %+v", resp.GetHits())
	}
	req.RequiredTerms = []string{"zebra"}
	if resp, _ = p.Search(t.Context(), req); len(resp.GetHits()) != 0 {
		t.Errorf("required term absent: %+v", resp.GetHits())
	}
}
