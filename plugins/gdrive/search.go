package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/davison/topos-plugins/searchkit"
	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
	"google.golang.org/api/drive/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Search (M2-R2, davison/topos#50) — sdk.ContentSearcher for Google Drive:
// exactly Match's membership (the `folders` labels over this plugin's own
// synced folder tree), then Drive's own full-text index — `fullText
// contains` for every query and required term — restricted to those
// member files. Drive returns no snippets, so a hit carries none;
// matched_in is TITLE when the name alone carries every term, else BODY.
func (p *SourcePlugin) Search(ctx context.Context, req *toposv1.SearchRequest) (*toposv1.SearchResponse, error) {
	if err := searchkit.RequireMembership(req, "folders"); err != nil {
		return nil, err
	}
	terms := searchkit.Terms(req.GetQuery())
	if len(terms) == 0 {
		return &toposv1.SearchResponse{Hits: []*toposv1.SearchHit{}}, nil
	}
	required := searchkit.Required(req)
	if state, msg := p.ensureTokenState(ctx); state != stateHealthy {
		return nil, status.Error(codes.Unavailable, msg)
	}
	cfg, err := loadSourceConfig(p.getenv)
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	folderID, err := cfg.folderID()
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	st, err := p.ensureSynced(ctx, folderID)
	if err != nil {
		state, _ := classifyDriveError(err)
		return nil, status.Error(codes.Unavailable, unhealthyMessage(state, err.Error()))
	}
	members := matchItems(st, &toposv1.MatchRequest{MatchFields: req.GetMatchFields()})
	if len(members) == 0 {
		return &toposv1.SearchResponse{Hits: []*toposv1.SearchHit{}}, nil
	}
	svc, err := p.driveService(ctx)
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	// Drive's own index: every term must be contained; trashed files never.
	var q []string
	for _, t := range append(append([]string{}, terms...), required...) {
		q = append(q, fmt.Sprintf("fullText contains '%s'", strings.ReplaceAll(strings.ReplaceAll(t, `\`, `\\`), `'`, `\'`)))
	}
	q = append(q, "trashed = false")
	matchedIDs := map[string]bool{}
	err = svc.Files.List().Q(strings.Join(q, " and ")).Fields("nextPageToken, files(id)").PageSize(1000).Pages(ctx, func(page *drive.FileList) error {
		for _, f := range page.Files {
			matchedIDs[f.Id] = true
		}
		return nil
	})
	if err != nil {
		state, _ := classifyDriveError(err)
		return nil, status.Error(codes.Unavailable, unhealthyMessage(state, "gdrive: search: "+err.Error()))
	}
	var hits []*toposv1.SearchHit
	for _, it := range members {
		if !matchedIDs[it.GetSourceId()] {
			continue
		}
		where := toposv1.MatchedIn_MATCHED_IN_BODY
		if searchkit.ContainsAll(strings.ToLower(it.GetTitle()), terms) {
			where = toposv1.MatchedIn_MATCHED_IN_TITLE
		}
		hits = append(hits, &toposv1.SearchHit{Item: it, MatchedIn: where})
	}
	searchkit.SortHitsByTimestamp(hits)
	hits, truncated := searchkit.Limit(hits, req)
	return &toposv1.SearchResponse{Hits: hits, Truncated: truncated, Note: fmt.Sprintf("Drive fullText index over %d member file(s); Drive returns no snippets", len(members))}, nil
}
