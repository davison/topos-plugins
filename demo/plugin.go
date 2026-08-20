// Package main implements topos-plugin-demo: a small, fixed source plugin
// built entirely from topos's published contract (docs/plugin-contract.md),
// its wire contract (proto/topos/v1/plugin.proto), and the sdk module —
// this repository's own seed plugin (16-04-PLAN.md, D-04), not a template
// for a real third-party source.
//
// It returns a small fixed item set — no network dependency, no real
// source system behind it — so this repository's release pipeline has
// something genuine to build and sign.
package main

import (
	"context"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

const (
	sourceType      = "demo"
	displayName     = "topos-plugins Demo"
	contractVersion = "topos.v2"

	// sourceSystem stands in for a real base URL / connection string — the
	// demo plugin has no real remote instance, but Provenance's
	// "source_system" key is documented as required on every item.
	sourceSystem = "demo://topos-plugins"

	// pluginBinaryName is this module's own build-target name, used only
	// in Provenance's "plugin" key.
	pluginBinaryName = "topos-plugin-demo"
)

// matchVocabulary is the field-name vocabulary this plugin declares in its
// Describe response and reads from MatchRequest.match_fields — mirrors the
// reference "mock" plugin's single-field "labels" shape.
var matchVocabulary = []string{"labels"}

// demoItems is the plugin's fixed, in-memory item set.
var demoItems = []*toposv1.Item{
	{
		SourceId:      "1",
		SourceType:    sourceType,
		Title:         "topos-plugins demo item",
		Preview:       "This item exists purely to prove the topos-plugins signing pipeline end to end (TRUST-02).",
		TimestampUnix: 1704067200, // 2024-01-01T00:00:00Z
		Fidelity:      toposv1.LinkFidelity_LINK_FIDELITY_EXACT,
		DeepLink:      "http://localhost/demo/items/1",
		Labels:        []string{"demo"},
		Provenance:    provenanceFor("1"),
	},
}

// provenanceFor builds the five plugin-populated provenance keys the
// contract documents (docs/plugin-contract.md "Provenance") — the sixth,
// synced_at_unix, is filled in by the kernel's index layer at read time
// and must never be set here.
func provenanceFor(sourceID string) map[string]string {
	return map[string]string{
		"source_type":      sourceType,
		"source_system":    sourceSystem,
		"source_id":        sourceID,
		"plugin":           pluginBinaryName,
		"contract_version": contractVersion,
	}
}

// SourcePlugin implements sdk.SourcePlugin with a fixed, in-memory item
// set — no per-instance connection state at all, every launched copy
// behaves identically.
type SourcePlugin struct{}

// NewSourcePlugin builds a SourcePlugin. It takes no arguments — the demo
// plugin has no connection details to configure.
func NewSourcePlugin() *SourcePlugin {
	return &SourcePlugin{}
}

// Describe is called once, immediately after the handshake, before any
// other RPC (contract: "RPC semantics: Describe").
func (p *SourcePlugin) Describe(_ context.Context, _ *toposv1.DescribeRequest) (*toposv1.DescribeResponse, error) {
	return &toposv1.DescribeResponse{
		SourceType:      sourceType,
		DisplayName:     displayName,
		ContractVersion: contractVersion,
		MatchVocabulary: matchVocabulary,
	}, nil
}

// Match is called only at sync time, never at request time (contract: "RPC
// semantics: Match"). It reads only this plugin's one declared field,
// "labels", exactly like the reference "mock" plugin's Match.
func (p *SourcePlugin) Match(_ context.Context, req *toposv1.MatchRequest) (*toposv1.MatchResponse, error) {
	keywords := req.GetMatchFields()["labels"].GetValues()
	var items []*toposv1.Item
	for _, it := range demoItems {
		if labelsMatchAnyKeyword(it.GetLabels(), keywords) {
			items = append(items, it)
		}
	}
	return &toposv1.MatchResponse{Items: items}, nil
}

// labelsMatchAnyKeyword mirrors the reference "mock" plugin's identical
// helper: exact, case-insensitive comparison only — never substring or
// prefix (contract: "Match" rule 2).
func labelsMatchAnyKeyword(labels, keywords []string) bool {
	for _, label := range labels {
		for _, kw := range keywords {
			if strings.EqualFold(label, kw) {
				return true
			}
		}
	}
	return false
}

// Fetch is called only at request time, never from the sync/Match path
// (contract: "RPC semantics: Fetch").
func (p *SourcePlugin) Fetch(_ context.Context, req *toposv1.FetchRequest) (*toposv1.FetchResponse, error) {
	sourceID := req.GetSourceId()
	if sourceID != "1" {
		return nil, status.Errorf(codes.NotFound, "demo: item %q not found", sourceID)
	}
	return &toposv1.FetchResponse{
		Available: true,
		Text:      "topos-plugins demo item — full text.",
	}, nil
}

// Health is a lightweight reachability probe (contract: "RPC semantics:
// Health"). The demo plugin has nothing external to be unreachable from,
// so it always reports reachable with no error.
func (p *SourcePlugin) Health(_ context.Context, _ *toposv1.HealthRequest) (*toposv1.HealthResponse, error) {
	return &toposv1.HealthResponse{Reachable: true}, nil
}
