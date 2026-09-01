package main

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
	"google.golang.org/api/drive/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fullTextFake answers Drive's fullText queries with a fixed id set and
// records every such query, delegating everything else (the folder walk)
// to the nested fixture handler.
type fullTextFake struct {
	mu      sync.Mutex
	queries []string
	answer  []string
}

func (f *fullTextFake) handler(t *testing.T, inner http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if r.URL.Path == "/files" && strings.Contains(q, "fullText") {
			f.mu.Lock()
			f.queries = append(f.queries, q)
			f.mu.Unlock()
			var files []*drive.File
			for _, id := range f.answer {
				files = append(files, &drive.File{Id: id})
			}
			writeDriveJSON(t, w, &drive.FileList{Files: files})
			return
		}
		inner(w, r)
	}
}

func foldersSearchReq(folders []string, query string, required ...string) *toposv1.SearchRequest {
	return &toposv1.SearchRequest{
		Query:         query,
		MatchFields:   map[string]*toposv1.StringList{"folders": {Values: folders}},
		RequiredTerms: required,
	}
}

// Search asks Drive's own index for every term and keeps only the files
// Match's membership already admits: q1.pdf (under Reports) is a hit,
// old.pdf (under Archive) is not even when Drive returns it.
func TestSearch_DriveIndexIntersectedWithMembership(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, dataFilePath(isolatedDir, tokenFileName))
	fx := newNestedDriveFixture("root-search", "Team Docs")
	fake := &fullTextFake{answer: []string{"q1-1", "old-1"}}
	svc := newFakeDriveService(t, fake.handler(t, newNestedFixtureHandler(t, fx)))
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, fx.rootID), svc)

	resp, err := p.Search(context.Background(), foldersSearchReq([]string{"Reports"}, "quarterly numbers", "final"))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.GetHits()) != 1 || resp.GetHits()[0].GetItem().GetSourceId() != "q1-1" {
		t.Fatalf("hits = %v, want exactly q1-1", resp.GetHits())
	}
	if resp.GetHits()[0].GetMatchedIn() != toposv1.MatchedIn_MATCHED_IN_BODY {
		t.Fatalf("matched_in = %v, want BODY (name carries no term)", resp.GetHits()[0].GetMatchedIn())
	}
	if len(fake.queries) != 1 {
		t.Fatalf("Drive fullText queried %d time(s), want 1", len(fake.queries))
	}
	for _, want := range []string{"fullText contains 'quarterly'", "fullText contains 'numbers'", "fullText contains 'final'", "trashed = false"} {
		if !strings.Contains(fake.queries[0], want) {
			t.Fatalf("Drive query %q lacks %q", fake.queries[0], want)
		}
	}
}

func TestSearch_RefusesEmptyMembership(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, dataFilePath(isolatedDir, tokenFileName))
	fx := newNestedDriveFixture("root-search-2", "Team Docs")
	svc := newFakeDriveService(t, newNestedFixtureHandler(t, fx))
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, fx.rootID), svc)
	_, err := p.Search(context.Background(), &toposv1.SearchRequest{Query: "quarterly"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty match_fields: got %v, want InvalidArgument", err)
	}

	// A map populated only under a field this plugin never declared is no
	// membership for it either — refused, never widened to the whole
	// source (davison/topos#50; tp#26 review round 1).
	if _, err := p.Search(t.Context(), &toposv1.SearchRequest{Query: "quarterly", MatchFields: map[string]*toposv1.StringList{"tags": {Values: []string{"house"}}}}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("foreign-only match_fields: got %v, want InvalidArgument", err)
	}
}
