package main

import (
	"context"
	"errors"
	"strings"

	"github.com/davison/topos-plugins/searchkit"
	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Search (M2-R2, davison/topos#50) — sdk.ContentSearcher for SilverBullet:
// the same space listing and bounded page reads Match performs (there is
// no server-side search), membership by exactly Match's rule (tags or
// page names), then the query and required terms against each member
// page's name, tags and body.
func (p *SourcePlugin) Search(ctx context.Context, req *toposv1.SearchRequest) (*toposv1.SearchResponse, error) {
	if err := searchkit.RequireMembership(req); err != nil {
		return nil, err
	}
	terms := searchkit.Terms(req.GetQuery())
	if len(terms) == 0 {
		return &toposv1.SearchResponse{Hits: []*toposv1.SearchHit{}}, nil
	}
	required := searchkit.Required(req)
	tagKeywords := req.GetMatchFields()["tags"].GetValues()
	pageKeywords := req.GetMatchFields()["pages"].GetValues()
	files, err := p.client.ListFiles(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "silverbullet: list files: %v", err)
	}
	var candidates []FileMeta
	for _, f := range files {
		if isPage(f) {
			candidates = append(candidates, f)
		}
	}
	hitsByIdx := make([]*toposv1.SearchHit, len(candidates))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(matchConcurrency)
	for i, f := range candidates {
		i, f := i, f
		g.Go(func() error {
			raw, err := p.client.ReadFile(gctx, f.Name)
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					return nil
				}
				return err
			}
			body, tags := ExtractTagsAndBody(raw)
			pagePath := strings.TrimSuffix(f.Name, ".md")
			if !(tagsMatchAnyKeyword(tags, tagKeywords) || pageNameMatchesAnyKeyword(pagePath, pageKeywords)) {
				return nil // not a member
			}
			it := p.toItem(f, tags, body)
			text := string(body)
			if !searchkit.Matches(it.GetTitle(), it.GetPreview(), text, tags, terms, required) {
				return nil
			}
			hitsByIdx[i] = &toposv1.SearchHit{Item: it, Snippet: searchkit.Snippet(text, terms), MatchedIn: searchkit.MatchedIn(it.GetTitle(), text, tags, terms)}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, status.Errorf(codes.Unavailable, "silverbullet: read pages: %v", err)
	}
	var hits []*toposv1.SearchHit
	for _, h := range hitsByIdx {
		if h != nil {
			hits = append(hits, h)
		}
	}
	hits, truncated := searchkit.Limit(hits, req)
	return &toposv1.SearchResponse{Hits: hits, Truncated: truncated, Note: "searched page names, tags and bodies of the member pages"}, nil
}
