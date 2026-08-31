package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

const (
	sourceType      = "paperless"
	displayName     = "paperless-ngx"
	contractVersion = "topos.v2"
	previewRuneCap  = 500

	// iconMIME is the declared mime for iconSVG below, returned verbatim
	// from Describe (09-02-PLAN.md Task 1, 09-UI-SPEC.md Fix 10).
	iconMIME = "image/svg+xml"
)

// matchVocabulary is the field-name vocabulary this plugin declares and
// reads from MatchRequest.match_fields — paperless-ngx's own native
// categorization is its document tags.
var matchVocabulary = []string{"tags"}

// iconSVG is paperless-ngx's own real logo mark (dark-theme-legible,
// no-text variant), wrapped in a square viewBox by this repo so the
// asset renders consistently alongside the Lucide-derived glyphs — the
// upstream path data itself is copied byte-for-byte, only the enclosing
// <svg>/<g> wrapper (width/height/viewBox and a centering translate) was
// added.
//
// Source-Project: paperless-ngx (paperless-ngx/paperless-ngx)
// Source-File:    src-ui/src/assets/logo-white-notext.svg
// Source-Version: 7620cd02f0c9303c57fe512a5922aa17e24b7d60
// Source-License: GPL-3.0-only
//
//go:embed assets/icon.svg
var iconSVG []byte

// SourcePlugin implements sdk.SourcePlugin against a paperless-ngx
// instance via Client.
type SourcePlugin struct {
	client  *Client
	baseURL string
}

// NewSourcePlugin builds a SourcePlugin. baseURL and token must be
// non-empty — callers (main.go) fail startup loudly if either is empty
// after config expansion.
func NewSourcePlugin(baseURL, token, apiVersion string) *SourcePlugin {
	return &SourcePlugin{
		client:  NewClient(baseURL, token, apiVersion),
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

// Match reads only its declared "tags" field from match_fields, ignoring
// any other key present in the request map (D-05). The tag-name comparison
// itself (client.ResolveTagIDs, exact and case-insensitive) is unchanged —
// only the provenance of its input changed.
func (p *SourcePlugin) Match(ctx context.Context, req *toposv1.MatchRequest) (*toposv1.MatchResponse, error) {
	tagIDs, err := p.client.ResolveTagIDs(ctx, req.GetMatchFields()["tags"].GetValues())
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "paperless: resolve tag ids: %v", err)
	}
	if len(tagIDs) == 0 {
		return &toposv1.MatchResponse{}, nil
	}

	docs, err := p.client.ListDocuments(ctx, tagIDs)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "paperless: list documents: %v", err)
	}

	allTags, err := p.client.AllTags(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "paperless: list tags: %v", err)
	}

	items := make([]*toposv1.Item, 0, len(docs))
	for _, d := range docs {
		items = append(items, p.toItem(d, allTags))
	}

	return &toposv1.MatchResponse{Items: items}, nil
}

func (p *SourcePlugin) toItem(d Document, allTags map[int]Tag) *toposv1.Item {
	labels := make([]string, 0, len(d.TagIDs))
	for _, id := range d.TagIDs {
		if t, ok := allTags[id]; ok {
			labels = append(labels, t.Name)
		}
	}

	sourceID := strconv.Itoa(d.ID)

	return &toposv1.Item{
		SourceId:               sourceID,
		SourceType:             sourceType,
		Title:                  d.Title,
		Preview:                truncatePreview(d.Content),
		TimestampUnix:          d.Created.Unix(),
		SecondaryTimestampUnix: d.Added.Unix(),
		Fidelity:               toposv1.LinkFidelity_LINK_FIDELITY_EXACT,
		DeepLink:               fmt.Sprintf("%s/documents/%s", p.baseURL, sourceID),
		Labels:                 labels,
		Provenance: map[string]string{
			"source_type":      sourceType,
			"source_system":    p.baseURL,
			"source_id":        sourceID,
			"plugin":           "topos-plugin-paperless",
			"contract_version": contractVersion,
		},
		HasThumbnail: true,
	}
}

// truncatePreview collapses whitespace runs and truncates to
// previewRuneCap runes on a rune boundary — the preview is a bounded
// snippet, never the full document content (KERN-03).
func truncatePreview(content string) string {
	collapsed := strings.Join(strings.FieldsFunc(content, unicode.IsSpace), " ")
	runes := []rune(collapsed)
	if len(runes) <= previewRuneCap {
		return collapsed
	}
	return string(runes[:previewRuneCap])
}

// noRenditionReason is the fixed unavailable_reason used whenever
// paperless-ngx 404s a rendition endpoint (preview or thumb) for an
// otherwise-known document — a normal outcome (e.g. an unsupported file
// type), not an error.
const noRenditionReason = "no previewable rendition"

// Fetch implements live content fetch on item-open (KERN-03), the request
// path — never called from Match/sync. It is a single unary RPC (locked
// decision D-Task1, 01-01): the full rendition's bytes are returned in one
// FetchResponse message rather than a stream, bounded by the raised
// MaxMessageSize gRPC limit (sdk.GRPCServer / kernel pluginhost dial
// options).
func (p *SourcePlugin) Fetch(ctx context.Context, req *toposv1.FetchRequest) (*toposv1.FetchResponse, error) {
	id, err := strconv.Atoi(req.GetSourceId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "paperless: invalid source id %q", req.GetSourceId())
	}

	switch req.GetVariant() {
	case toposv1.ContentVariant_CONTENT_VARIANT_FULL:
		return p.fetchFull(ctx, id)
	case toposv1.ContentVariant_CONTENT_VARIANT_PREVIEW:
		return p.fetchRendition(ctx, id, "preview", p.client.Preview)
	case toposv1.ContentVariant_CONTENT_VARIANT_THUMBNAIL:
		return p.fetchRendition(ctx, id, "thumb", p.client.Thumbnail)
	default:
		return nil, status.Error(codes.InvalidArgument, "paperless: unspecified content variant")
	}
}

// fetchFull fetches the document's extracted text (which only exists via
// the document detail endpoint, so document-not-found is authoritatively
// detected here) plus its preview rendition, if any.
func (p *SourcePlugin) fetchFull(ctx context.Context, id int) (*toposv1.FetchResponse, error) {
	doc, err := p.client.Document(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "paperless: document %d not found", id)
		}
		return nil, status.Errorf(codes.Unavailable, "paperless: fetch document %d: %v", id, err)
	}

	rendition, err := p.client.Preview(ctx, id)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "paperless: fetch preview for document %d: %v", id, err)
	}

	resp := &toposv1.FetchResponse{
		Text:       doc.Content,
		Provenance: map[string]string{"source_type": sourceType, "source_id": strconv.Itoa(id)},
	}
	if rendition.Available {
		resp.Available = true
		resp.MimeType = rendition.ContentType
		resp.SizeBytes = int64(len(rendition.Data))
		resp.Data = rendition.Data
	} else {
		resp.Available = false
		resp.UnavailableReason = noRenditionReason
	}
	return resp, nil
}

// fetchRendition fetches only a preview or thumbnail rendition, with no
// extracted text. A 404 from paperless-ngx for the rendition itself is a
// normal "unavailable" outcome, not a gRPC error — the pane falls back to
// extracted text via the full-variant fetch instead.
func (p *SourcePlugin) fetchRendition(ctx context.Context, id int, endpointName string, fetch func(context.Context, int) (RenditionResult, error)) (*toposv1.FetchResponse, error) {
	rendition, err := fetch(ctx, id)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "paperless: fetch %s for document %d: %v", endpointName, id, err)
	}
	if !rendition.Available {
		return &toposv1.FetchResponse{Available: false, UnavailableReason: noRenditionReason}, nil
	}
	return &toposv1.FetchResponse{
		Available: true,
		MimeType:  rendition.ContentType,
		SizeBytes: int64(len(rendition.Data)),
		Data:      rendition.Data,
	}, nil
}

func (p *SourcePlugin) Health(ctx context.Context, _ *toposv1.HealthRequest) (*toposv1.HealthResponse, error) {
	_, err := p.client.AllTags(ctx)
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
