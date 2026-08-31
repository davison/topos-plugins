package main

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// describeOnlyPlugin implements sdk.SourcePlugin for describeOnlyEnvVar's
// launch mode (main.go, CR-01 / 08-REVIEW.md). Describe returns the
// identical static response SourcePlugin.Describe always returns —
// source_type, display_name, contract_version, and match_vocabulary are
// fixed package-level constants (plugin.go) that never depend on a live
// connection, this plugin's local store, or storelock.go's exclusive
// per-data-directory flock, so this type never opens or acquires any of
// them.
//
// Match, Fetch, and Health DO need a live connection and/or the local
// store — a describe-only launch never opens either, so those three
// explicitly refuse (codes.Unimplemented) rather than silently no-op or
// (worse) panic on unopened state. In practice neither is ever called: the
// only caller of a describe-only launch, kernel/pluginhost.DescribePluginType,
// calls Describe alone and kills the subprocess immediately after —
// pinned by kernel/httpapi/config_test.go's
// TestDescribePluginTypeGuard_ReachesNoRPCBeyondDescribe AST guard. These
// three methods exist only so this type satisfies sdk.SourcePlugin at all,
// and so a defect that DID somehow route Match/Fetch/Health to a
// describe-only instance would fail loudly and specifically instead of
// silently or with a nil-pointer panic.
type describeOnlyPlugin struct{}

func (describeOnlyPlugin) Describe(_ context.Context, _ *toposv1.DescribeRequest) (*toposv1.DescribeResponse, error) {
	return &toposv1.DescribeResponse{
		SourceType:      sourceType,
		DisplayName:     displayName,
		ContractVersion: contractVersion,
		MatchVocabulary: matchVocabulary,
		Icon:            iconSVG,
		IconMime:        iconMIME,
	}, nil
}

func (describeOnlyPlugin) Match(_ context.Context, _ *toposv1.MatchRequest) (*toposv1.MatchResponse, error) {
	return nil, status.Error(codes.Unimplemented, "whatsapp: this subprocess was launched describe-only ("+describeOnlyEnvVar+"=1) and holds no live connection or local store — Match is unavailable")
}

func (describeOnlyPlugin) Fetch(_ context.Context, _ *toposv1.FetchRequest) (*toposv1.FetchResponse, error) {
	return nil, status.Error(codes.Unimplemented, "whatsapp: this subprocess was launched describe-only ("+describeOnlyEnvVar+"=1) and holds no live connection or local store — Fetch is unavailable")
}

func (describeOnlyPlugin) Health(_ context.Context, _ *toposv1.HealthRequest) (*toposv1.HealthResponse, error) {
	return nil, status.Error(codes.Unimplemented, "whatsapp: this subprocess was launched describe-only ("+describeOnlyEnvVar+"=1) and holds no live connection or local store — Health is unavailable")
}
