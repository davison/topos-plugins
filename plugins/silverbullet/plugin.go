package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

const (
	sourceType      = "silverbullet"
	displayName     = "SilverBullet"
	contractVersion = "topos.v2"
	previewRuneCap  = 500
	// matchConcurrency bounds how many page bodies Match reads at once
	// (T-02-05): SilverBullet has no server-side tag filter (RESEARCH.md
	// Pitfall 2), so every sync reads every candidate page's body — this
	// cap, together with the shared transport's MaxConnsPerHost, keeps a
	// large space from opening hundreds of simultaneous connections
	// against the user's own home server.
	matchConcurrency = 4

	// iconMIME is the declared mime for iconSVG below, returned verbatim
	// from Describe (09-02-PLAN.md Task 1, 09-UI-SPEC.md Fix 10).
	iconMIME = "image/svg+xml"
)

// matchVocabulary is the field-name vocabulary this plugin declares and
// reads from MatchRequest.match_fields: "tags" matches a page's frontmatter
// or inline tags, "pages" matches its page name/path — two independent
// fields, unlike frontmatter.go's combined MatchesKeyword (see tag/page
// helpers below).
var matchVocabulary = []string{"tags", "pages"}

// iconSVG is SilverBullet's own real logo mark. Upstream ships this mark
// only as a raster PNG (client/images/logo-dock-96x96.png) — no vector
// source exists — so this asset wraps that exact PNG's bytes as a base64
// data URI inside a plain square <svg><image> document, the identical
// technique upstream's own client/images/favicon.svg already uses for
// its own PNG-backed icon. The embedded bytes are the unmodified upstream
// PNG; only the enclosing SVG wrapper (needed to satisfy this repo's
// uniform icon.svg/image-svg+xml contract) was added.
//
// Source-Project: SilverBullet (silverbulletmd/silverbullet)
// Source-File:    client/images/logo-dock-96x96.png
// Source-Version: a99e5d98bec3317da75e192f1f4b6e3c07dccbec
// Source-License: MIT
//
//go:embed assets/icon.svg
var iconSVG []byte

// SourcePlugin implements sdk.SourcePlugin against a SilverBullet instance
// via Client.
type SourcePlugin struct {
	client  *Client
	baseURL string
}

// NewSourcePlugin builds a SourcePlugin. baseURL and token must be
// non-empty — callers (main.go) fail startup loudly if either is empty
// after config expansion. caCertPath is optional (see NewClient's doc
// comment for why this plugin needs it, beyond the plan's originally
// sketched two-argument constructor).
func NewSourcePlugin(baseURL, token, caCertPath string) *SourcePlugin {
	return &SourcePlugin{
		client:  NewClient(baseURL, token, caCertPath),
		baseURL: strings.TrimRight(baseURL, "/"),
	}
}

func (p *SourcePlugin) Describe(_ context.Context, _ *toposv1.DescribeRequest) (*toposv1.DescribeResponse, error) {
	return &toposv1.DescribeResponse{
		SourceType:      sourceType,
		DisplayName:     displayName,
		ContractVersion: contractVersion,
		MatchVocabulary: matchVocabulary,
		Icon:            iconSVG,
		IconMime:        iconMIME,
	}, nil
}

// tagsMatchAnyKeyword reports whether any of tags case-insensitively
// equals any of keywords — the "tags" field's exact-match check, split out
// of frontmatter.go's MatchesKeyword (which combines tag-or-path matching
// into one check) now that "tags" and "pages" are independent match_fields.
// D-04's comparison itself (strings.EqualFold, no substring/prefix) is
// unchanged.
func tagsMatchAnyKeyword(tags, keywords []string) bool {
	for _, t := range tags {
		for _, kw := range keywords {
			if strings.EqualFold(t, kw) {
				return true
			}
		}
	}
	return false
}

// pageNameMatchesAnyKeyword reports whether pagePath (or its final
// segment) case-insensitively equals any of keywords — the "pages" field's
// exact-match check. Reuses MatchesKeyword with a nil tags argument so the
// path-comparison logic (full path or final segment, exact only) stays the
// single source of truth in frontmatter.go, just scoped to path-only.
func pageNameMatchesAnyKeyword(pagePath string, keywords []string) bool {
	for _, kw := range keywords {
		if MatchesKeyword(pagePath, nil, kw) {
			return true
		}
	}
	return false
}

// pageMatch holds one page's Match-time state: its listing metadata, the
// tags resolved by ExtractTagsAndBody, and its frontmatter-stripped body
// (kept only long enough to build the item's Snippet — never persisted
// beyond that).
type pageMatch struct {
	file FileMeta
	tags []string
	body []byte
}

// Match lists the space once, filters to markdown pages (isPage), then
// reads each candidate page's body through a bounded worker pool
// (matchConcurrency) to extract its tags and test D-03's keyword match —
// SilverBullet has no server-side tag/name filter (RESEARCH.md Pitfall 2),
// so this cost is unavoidable and scales with total space size, not
// matched-item count (accepted for this phase's MVP scope, A-SRC-05).
func (p *SourcePlugin) Match(ctx context.Context, req *toposv1.MatchRequest) (*toposv1.MatchResponse, error) {
	tagKeywords := req.GetMatchFields()["tags"].GetValues()
	pageKeywords := req.GetMatchFields()["pages"].GetValues()
	if len(tagKeywords) == 0 && len(pageKeywords) == 0 {
		return &toposv1.MatchResponse{}, nil
	}

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

	matches := make([]*pageMatch, len(candidates))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(matchConcurrency)
	for i, f := range candidates {
		i, f := i, f
		g.Go(func() error {
			raw, err := p.client.ReadFile(gctx, f.Name)
			if err != nil {
				// Discriminate the one read failure that is a safe skip
				// from every other one, which must fail the whole Match
				// (docs/plugin-contract.md's Match section, 02-REVIEW.md
				// CR-01, 02-VERIFICATION.md truth #6):
				//
				//   - errors.Is(err, ErrNotFound): the page was deleted or
				//     renamed between ListFiles and this read. It simply
				//     never matches, same as a page with no matching
				//     tag/name would — return nil so the sync proceeds.
				//   - every other error (dropped connection, TLS failure,
				//     revoked/expired bearer token, any 5xx, the SPA-shell
				//     response, a cancelled context): return err unchanged
				//     so errgroup cancels gctx and g.Wait observes it below.
				//     Silently treating these the same as a 404 previously
				//     made Match return a successful, empty MatchResponse
				//     for a genuine outage occurring after ListFiles had
				//     already succeeded — which makes
				//     kernel/correlate.SyncSource call
				//     ReplaceWebspaceSourceItems with zero items and delete
				//     every previously-synced SilverBullet row for every
				//     webspace, while the run is recorded as ok.
				if errors.Is(err, ErrNotFound) {
					return nil
				}
				return err
			}
			body, tags := ExtractTagsAndBody(raw)
			pagePath := strings.TrimSuffix(f.Name, ".md")
			// D-04: a page matches if its tags match any "tags" value OR
			// its page name matches any "pages" value — the two declared
			// fields are independent, unioned, checks.
			if tagsMatchAnyKeyword(tags, tagKeywords) || pageNameMatchesAnyKeyword(pagePath, pageKeywords) {
				matches[i] = &pageMatch{file: f, tags: tags, body: body}
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, status.Errorf(codes.Unavailable, "silverbullet: match: %v", err)
	}

	items := make([]*toposv1.Item, 0, len(candidates))
	for _, m := range matches {
		if m == nil {
			continue
		}
		items = append(items, p.toItem(m.file, m.tags, m.body))
	}
	return &toposv1.MatchResponse{Items: items}, nil
}

// toItem builds a toposv1.Item from one matched page (D-01, D-04):
// source_id is the page path with ".md" stripped, title is that path's
// final segment, preview is the frontmatter-stripped body's snippet, and
// the deep link is an exact-fidelity link to {base_url}/{page-path}.
func (p *SourcePlugin) toItem(f FileMeta, tags []string, body []byte) *toposv1.Item {
	sourceID := strings.TrimSuffix(f.Name, ".md")
	title := sourceID
	if idx := strings.LastIndex(sourceID, "/"); idx >= 0 {
		title = sourceID[idx+1:]
	}

	return &toposv1.Item{
		SourceId:               sourceID,
		SourceType:             sourceType,
		Title:                  title,
		Preview:                Snippet(body),
		TimestampUnix:          f.LastModified / 1000,
		SecondaryTimestampUnix: f.Created / 1000,
		Fidelity:               toposv1.LinkFidelity_LINK_FIDELITY_EXACT,
		DeepLink:               fmt.Sprintf("%s/%s", p.baseURL, sourceID),
		Labels:                 tags,
		Provenance: map[string]string{
			"source_type":      sourceType,
			"source_system":    p.baseURL,
			"source_id":        sourceID,
			"plugin":           "topos-plugin-silverbullet",
			"contract_version": contractVersion,
		},
		HasThumbnail: false,
	}
}

// noThumbnailReason is the fixed unavailable_reason used for the
// THUMBNAIL variant — a wiki page has no image rendition, ever, unlike
// paperless's noRenditionReason which covers an unsupported-file-type
// edge case.
const noThumbnailReason = "SilverBullet pages have no thumbnail rendition"

// Fetch implements live content fetch on item-open (KERN-03) — never
// called from Match/sync. FULL and PREVIEW both render the page's
// sanitized HTML (D-04: rendered markdown is the default detail-pane
// content); THUMBNAIL is always unavailable, with no error, since a wiki
// page never has an image rendition.
func (p *SourcePlugin) Fetch(ctx context.Context, req *toposv1.FetchRequest) (*toposv1.FetchResponse, error) {
	switch req.GetVariant() {
	case toposv1.ContentVariant_CONTENT_VARIANT_FULL, toposv1.ContentVariant_CONTENT_VARIANT_PREVIEW:
		return p.fetchFull(ctx, req.GetSourceId())
	case toposv1.ContentVariant_CONTENT_VARIANT_THUMBNAIL:
		return &toposv1.FetchResponse{Available: false, UnavailableReason: noThumbnailReason}, nil
	default:
		return nil, status.Error(codes.InvalidArgument, "silverbullet: unspecified content variant")
	}
}

// fetchFull reads the page's raw markdown, strips its frontmatter, renders
// the remaining body to sanitized HTML, and returns both: Data is the
// sanitized HTML (mime_type "text/html", the rendition the detail pane's
// iframe fetches — D-04), Text is the frontmatter-stripped raw markdown
// (for a possible future raw ContentVariant; never persisted to the
// index, matching the plan's hybrid-model prohibition).
//
// sourceID is the page path WITHOUT the ".md" extension (D-01 — it's what
// Match stripped when building the item's source_id, and what the kernel
// round-trips back as FetchRequest.source_id unchanged). The actual file
// on the SilverBullet instance always carries ".md", so it must be
// re-appended before calling ReadFile — a bug caught live against the
// real instance (a request for the bare, extension-less path 404s; Task 1
// Step 0's exploratory checks only ever probed a hardcoded ".md" path
// directly, so this asymmetry didn't surface until Task 2's live check).
func (p *SourcePlugin) fetchFull(ctx context.Context, sourceID string) (*toposv1.FetchResponse, error) {
	filePath := sourceID + ".md"

	raw, err := p.client.ReadFile(ctx, filePath)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "silverbullet: page %q not found", sourceID)
		}
		return nil, status.Errorf(codes.Unavailable, "silverbullet: read %q: %v", sourceID, err)
	}

	body, _ := ExtractTagsAndBody(raw)

	// RenderMarkdown converts the page's markdown to an HTML fragment
	// only — sanitization, wrapping and theming moved to the kernel's
	// rendition boundary (kernel/httpapi/rendition.go, D-11). Fetch
	// returns the unwrapped, unsanitized fragment plus the declared
	// content shape; the kernel sanitizes/wraps/themes it before serving.
	fragment, err := RenderMarkdown(body)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "silverbullet: render %q: %v", sourceID, err)
	}

	return &toposv1.FetchResponse{
		Available:    true,
		MimeType:     "text/html",
		ContentShape: toposv1.ContentShape_CONTENT_SHAPE_MARKDOWN_HTML,
		SizeBytes:    int64(len(fragment)),
		Data:         fragment,
		Text:         string(body),
		Provenance: map[string]string{
			"source_type": sourceType,
			"source_id":   sourceID,
		},
	}, nil
}

func (p *SourcePlugin) Health(ctx context.Context, _ *toposv1.HealthRequest) (*toposv1.HealthResponse, error) {
	_, err := p.client.ListFiles(ctx)
	if err != nil {
		return &toposv1.HealthResponse{
			Reachable: false,
			LastError: err.Error(),
		}, nil
	}
	return &toposv1.HealthResponse{
		Reachable:    true,
		LastSyncUnix: time.Now().Unix(),
	}, nil
}
