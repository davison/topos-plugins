package main

import (
	"context"

	"github.com/davison/topos-plugins/searchkit"
	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Search (M2-R2, davison/topos#50) — sdk.ContentSearcher for paperless-ngx:
// membership by exactly Match's rule (the `tags` keywords resolved to tag
// ids), the query sent to paperless's own full-text `query` parameter
// within those tags, then the required terms and the query re-checked
// against title, tags and the OCR content paperless returns, so the
// answer is the same AND every other plugin gives.
func (p *SourcePlugin) Search(ctx context.Context, req *toposv1.SearchRequest) (*toposv1.SearchResponse, error) {
	if err := searchkit.RequireMembership(req); err != nil {
		return nil, err
	}
	terms := searchkit.Terms(req.GetQuery())
	if len(terms) == 0 {
		return &toposv1.SearchResponse{Hits: []*toposv1.SearchHit{}}, nil
	}
	required := searchkit.Required(req)
	tagIDs, err := p.client.ResolveTagIDs(ctx, req.GetMatchFields()["tags"].GetValues())
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "paperless: resolve tag ids: %v", err)
	}
	if len(tagIDs) == 0 {
		return &toposv1.SearchResponse{Hits: []*toposv1.SearchHit{}}, nil
	}
	docs, err := p.client.SearchDocuments(ctx, tagIDs, req.GetQuery())
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "paperless: search documents: %v", err)
	}
	allTags, err := p.client.AllTags(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "paperless: list tags: %v", err)
	}
	var hits []*toposv1.SearchHit
	for _, d := range docs {
		it := p.toItem(d, allTags)
		if !searchkit.Matches(it.GetTitle(), it.GetPreview(), d.Content, it.GetLabels(), terms, required) {
			continue
		}
		hits = append(hits, &toposv1.SearchHit{Item: it, Snippet: searchkit.Snippet(d.Content, terms), MatchedIn: searchkit.MatchedIn(it.GetTitle(), d.Content, it.GetLabels(), terms)})
	}
	hits, truncated := searchkit.Limit(hits, req)
	return &toposv1.SearchResponse{Hits: hits, Truncated: truncated, Note: "paperless-ngx full-text search within the member tags"}, nil
}
