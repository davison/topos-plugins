package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestSearch_UsesPaperlessQueryWithinTags (M2-R2): the query goes to
// paperless-ngx's own `query` parameter together with the member tags;
// the plugin then re-checks the AND over title/tags/content; empty
// membership is refused.
func TestSearch_UsesPaperlessQueryWithinTags(t *testing.T) {
	var gotQuery, gotTags string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"count":1,"next":null,"results":[{"id":7,"name":"house"}]}`))
	})
	mux.HandleFunc("/api/documents/", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		gotTags = r.URL.Query().Get("tags__id__in")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"count": 2, "next": nil, "results": []map[string]any{
			{"id": 1, "title": "Boiler service", "content": "Annual boiler service invoice from Acme.", "created": "2026-01-02", "added": "2026-01-02T10:00:00Z", "tags": []int{7}},
			{"id": 2, "title": "Garden", "content": "The server returned this too, but it has no boiler in it.", "created": "2026-01-03", "added": "2026-01-03T10:00:00Z", "tags": []int{7}},
		}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p := NewSourcePlugin(srv.URL, "token", "")

	if _, err := p.Search(t.Context(), &toposv1.SearchRequest{Query: "boiler"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty membership must be refused, got %v", err)
	}
	req := &toposv1.SearchRequest{Query: "boiler", MatchFields: map[string]*toposv1.StringList{"tags": {Values: []string{"house"}}}, RequiredTerms: []string{"acme"}}
	resp, err := p.Search(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != "boiler" || !strings.Contains(gotTags, "7") {
		t.Errorf("paperless must receive the query within the member tags: query=%q tags=%q", gotQuery, gotTags)
	}
	if len(resp.GetHits()) != 1 || resp.GetHits()[0].GetItem().GetTitle() != "Boiler service" {
		t.Fatalf("the AND over title/content keeps only the real hit: %+v", resp.GetHits())
	}
	if resp.GetHits()[0].GetMatchedIn() != toposv1.MatchedIn_MATCHED_IN_TITLE || !strings.Contains(resp.GetHits()[0].GetSnippet(), "boiler") {
		t.Errorf("matched in the title with a content snippet: %+v", resp.GetHits()[0])
	}
}
