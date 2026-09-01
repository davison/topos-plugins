package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/davison/topos-plugins/searchkit"
	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// searchReadCap bounds how much of a file Search reads to test the query
// against its content — the head of a text-like file, never the whole of
// a large one, and never a binary at all.
const searchReadCap = 512 * 1024

// Search (M2-R2, davison/topos#50) — sdk.ContentSearcher for the filesystem
// plugin: within membership exactly as Match decides it (the `folders`
// labels), the query and required terms are matched against each file's
// name, labels and — for a text-like file — the head of its contents.
// Read-only, bounded, snippets never a body.
func (p *SourcePlugin) Search(ctx context.Context, req *toposv1.SearchRequest) (*toposv1.SearchResponse, error) {
	if err := searchkit.RequireMembership(req); err != nil {
		return nil, err
	}
	terms := searchkit.Terms(req.GetQuery())
	if len(terms) == 0 {
		return &toposv1.SearchResponse{Hits: []*toposv1.SearchHit{}}, nil
	}
	required := searchkit.Required(req)
	sc := newScope(p.extras)
	results, _, err := walk(ctx, p.root, p.recursive, sc)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "filesystem: %v", err)
	}
	folders, hasFolders := req.GetMatchFields()["folders"]
	var hits []*toposv1.SearchHit
	for _, r := range results {
		if ctx.Err() != nil {
			return nil, status.Errorf(codes.DeadlineExceeded, "filesystem: search cancelled: %v", ctx.Err())
		}
		it := p.toItem(r.sourceID, r.info)
		if hasFolders && !labelMatchesAny(it.GetLabels(), folders.GetValues()) {
			continue // not a member — never returned, whatever the query
		}
		body := readTextHead(filepath.Join(p.root, filepath.FromSlash(r.sourceID)))
		if !searchkit.Matches(it.GetTitle(), it.GetPreview(), body, it.GetLabels(), terms, required) {
			continue
		}
		hits = append(hits, &toposv1.SearchHit{
			Item:      it,
			Snippet:   searchkit.Snippet(body, terms),
			MatchedIn: searchkit.MatchedIn(it.GetTitle(), body, it.GetLabels(), terms),
		})
	}
	hits, truncated := searchkit.Limit(hits, req)
	return &toposv1.SearchResponse{Hits: hits, Truncated: truncated, Note: "searched file names, folder labels and the head of text files"}, nil
}

// readTextHead returns up to searchReadCap bytes of path when it sniffs
// as text (http.DetectContentType); "" for anything binary or unreadable.
func readTextHead(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, searchReadCap)
	n, _ := f.Read(buf)
	if n == 0 {
		return ""
	}
	head := buf[:n]
	sniff := head
	if len(sniff) > 512 {
		sniff = sniff[:512]
	}
	ct := http.DetectContentType(sniff)
	if !strings.HasPrefix(ct, "text/") && !strings.Contains(ct, "json") && !strings.Contains(ct, "xml") {
		return ""
	}
	return string(head)
}
