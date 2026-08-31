package main

// These two tests exist because kernel/correlate.SyncSource
// (kernel/correlate/correlate.go) calls a source's Match RPC once PER
// CONFIGURED WEBSPACE, sequentially, within a single sync cycle, against
// the one long-lived plugin instance kernel/pluginhost launches per source
// (kernel/pluginhost/host.go's Discover/bySourceType). This is exactly the
// scenario no test in the phase previously exercised: every existing test
// built a fresh plugin and called Match exactly once. Both tests below
// build ONE plugin instance and call Match twice with disjoint keyword
// sets — the in-test simulation of that per-webspace loop — then Fetch a
// source_id only an EARLIER Match call discovered, proving the plugin's
// resolution state accumulates across Match calls instead of being
// replaced by the most recent one (03-06 Task 1, closing 03-REVIEW.md
// CR-01 / 03-VERIFICATION.md's BLOCKER gap).

import (
	"context"
	"testing"

	"google.golang.org/grpc/status"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// TestMatch_MailboxCacheSurvivesASecondWebspaceMatch: against ONE plugin
// instance, a Match for keyword set A ("AlphaTeam") discovers one item,
// then a Match for the disjoint keyword set B ("GammaTeam") discovers a
// DIFFERENT item. A Fetch (FULL) for the source_id only the FIRST Match
// discovered must still succeed — it must NOT return codes.NotFound, which
// is exactly what a whole-map-replace implementation produces because the
// second Match's cache install would have discarded the first Match's
// entry. A Fetch for the second Match's source_id must also still succeed,
// so accumulating does not trade the newest entry for the oldest.
func TestMatch_MailboxCacheSurvivesASecondWebspaceMatch(t *testing.T) {
	serverAddr := newTestIMAPServer(t)
	plugin := newTestPluginDialingServer(t, serverAddr)

	ctx := context.Background()

	firstResp, err := plugin.Match(ctx, foldersMatchReq([]string{"AlphaTeam"}))
	if err != nil {
		t.Fatalf("first Match (AlphaTeam): %v", err)
	}
	if len(firstResp.GetItems()) != 1 {
		t.Fatalf("first Match (AlphaTeam): got %d items, want exactly 1", len(firstResp.GetItems()))
	}
	firstSourceID := firstResp.GetItems()[0].GetSourceId()

	secondResp, err := plugin.Match(ctx, foldersMatchReq([]string{"GammaTeam"}))
	if err != nil {
		t.Fatalf("second Match (GammaTeam): %v", err)
	}
	if len(secondResp.GetItems()) != 1 {
		t.Fatalf("second Match (GammaTeam): got %d items, want exactly 1", len(secondResp.GetItems()))
	}
	secondSourceID := secondResp.GetItems()[0].GetSourceId()

	if firstSourceID == secondSourceID {
		t.Fatalf("first and second Match discovered the same source_id (%q) — the fixture's GammaTeam message must have a distinct Message-Id, or this test is vacuous", firstSourceID)
	}

	// The regression: Fetch the FIRST Match's source_id after a SECOND,
	// disjoint Match has run against the same plugin instance.
	firstFetch, err := plugin.Fetch(ctx, &toposv1.FetchRequest{
		SourceId: firstSourceID,
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err != nil {
		t.Fatalf("Fetch(firstSourceID) after a second webspace's Match: got error %v (code %s), want nil — the plugin's mailbox resolution state must accumulate across Match calls, not be replaced by the most recent one", err, status.Code(err))
	}
	if !firstFetch.GetAvailable() {
		t.Fatalf("Fetch(firstSourceID) after a second webspace's Match: Available = false, want true")
	}

	// Accumulating must not have traded the newest entry for the oldest.
	secondFetch, err := plugin.Fetch(ctx, &toposv1.FetchRequest{
		SourceId: secondSourceID,
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err != nil {
		t.Fatalf("Fetch(secondSourceID): got error %v (code %s), want nil", err, status.Code(err))
	}
	if !secondFetch.GetAvailable() {
		t.Fatalf("Fetch(secondSourceID): Available = false, want true")
	}
}

// TestMatch_ZeroMailboxMatchPreservesMailboxCache: against ONE plugin
// instance, a Match for "AlphaTeam" discovers one item, then a Match for a
// keyword matching no mailbox at all returns zero items and a nil error —
// the successful-empty-sync contract 03-01 established, which must not
// change. A subsequent Fetch for the FIRST Match's source_id must still
// succeed: the contributing-nothing webspace must erase nothing from the
// shared resolution state.
func TestMatch_ZeroMailboxMatchPreservesMailboxCache(t *testing.T) {
	serverAddr := newTestIMAPServer(t)
	plugin := newTestPluginDialingServer(t, serverAddr)

	ctx := context.Background()

	firstResp, err := plugin.Match(ctx, foldersMatchReq([]string{"AlphaTeam"}))
	if err != nil {
		t.Fatalf("first Match (AlphaTeam): %v", err)
	}
	if len(firstResp.GetItems()) != 1 {
		t.Fatalf("first Match (AlphaTeam): got %d items, want exactly 1", len(firstResp.GetItems()))
	}
	firstSourceID := firstResp.GetItems()[0].GetSourceId()

	// A keyword guaranteed not to match any seeded mailbox leaf name.
	zeroResp, err := plugin.Match(ctx, foldersMatchReq([]string{"NoSuchLabelAnywhere"}))
	if err != nil {
		t.Fatalf("second Match (zero-matching keyword): got error %v, want nil (a zero-mailbox-match Match must succeed with an empty response)", err)
	}
	if len(zeroResp.GetItems()) != 0 {
		t.Fatalf("second Match (zero-matching keyword): got %d items, want 0", len(zeroResp.GetItems()))
	}

	// The regression: Fetch the FIRST Match's source_id after a Match that
	// matched zero mailboxes has run against the same plugin instance.
	fetchResp, err := plugin.Fetch(ctx, &toposv1.FetchRequest{
		SourceId: firstSourceID,
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err != nil {
		t.Fatalf("Fetch(firstSourceID) after a zero-mailbox-matched Match: got error %v (code %s), want nil — a webspace contributing nothing must never erase what an earlier webspace's Match contributed", err, status.Code(err))
	}
	if !fetchResp.GetAvailable() {
		t.Fatalf("Fetch(firstSourceID) after a zero-mailbox-matched Match: Available = false, want true")
	}
}
