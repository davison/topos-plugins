package main

import (
	"os"
	"path/filepath"
	"testing"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func searchReq(q string, folders []string, required ...string) *toposv1.SearchRequest {
	req := &toposv1.SearchRequest{Query: q, RequiredTerms: required}
	if folders != nil {
		req.MatchFields = map[string]*toposv1.StringList{"folders": {Values: folders}}
	}
	return req
}

// TestSearch_WithinMembershipOverFileText (M2-R2): only member files are
// searched, the query reaches a text file's contents, a binary is matched
// on its name alone, and an empty membership map is refused.
func TestSearch_WithinMembershipOverFileText(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"house", "work"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("house/boiler-service.md", "# Boiler\n\nThe annual boiler service invoice from Acme.")
	write("house/garden.txt", "Nothing about heating here.")
	write("work/boiler-room.md", "The boiler at work needs a service too.")
	p := NewSourcePlugin(root, nil, true)

	if _, err := p.Search(t.Context(), searchReq("boiler", nil)); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty membership must be refused, got %v", err)
	}

	// A map populated only under a field this plugin never declared is no
	// membership for it either — refused, never widened to the whole
	// source (davison/topos#50; tp#26 review round 1).
	if _, err := p.Search(t.Context(), &toposv1.SearchRequest{Query: "boiler", MatchFields: map[string]*toposv1.StringList{"tags": {Values: []string{"house"}}}}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("foreign-only match_fields: got %v, want InvalidArgument", err)
	}
	resp, err := p.Search(t.Context(), searchReq("boiler", []string{"house"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetHits()) != 1 || resp.GetHits()[0].GetItem().GetSourceId() != "house/boiler-service.md" {
		t.Fatalf("expected only the house member that mentions the boiler, got %+v", resp.GetHits())
	}
	hit := resp.GetHits()[0]
	if hit.GetMatchedIn() != toposv1.MatchedIn_MATCHED_IN_TITLE {
		t.Errorf("'boiler' is in the file name (title): got %v", hit.GetMatchedIn())
	}
	if len(hit.GetSnippet()) == 0 || len(hit.GetSnippet()) > 200 {
		t.Errorf("bounded snippet from the text: %q", hit.GetSnippet())
	}
	resp, _ = p.Search(t.Context(), searchReq("acme", []string{"house"}))
	if len(resp.GetHits()) != 1 || resp.GetHits()[0].GetMatchedIn() != toposv1.MatchedIn_MATCHED_IN_BODY {
		t.Errorf("'acme' is only in the body: %+v", resp.GetHits())
	}
	resp, _ = p.Search(t.Context(), searchReq("boiler", []string{"house"}, "acme"))
	if len(resp.GetHits()) != 1 {
		t.Errorf("a required term present keeps the hit: %+v", resp.GetHits())
	}
	resp, _ = p.Search(t.Context(), searchReq("boiler", []string{"house"}, "zebra"))
	if len(resp.GetHits()) != 0 {
		t.Errorf("a required term absent drops the hit: %+v", resp.GetHits())
	}
}
