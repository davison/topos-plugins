package main

import (
	"context"
	"testing"

	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// nonEmptyGroupsRequest is a MatchRequest carrying a non-empty "groups"
// keyword list — used throughout this file so the zero-keywords early
// return (plugin.go's Match, BELOW the health guard) can never be the
// reason a case passes.
func nonEmptyGroupsRequest() *toposv1.MatchRequest {
	return &toposv1.MatchRequest{
		MatchFields: map[string]*toposv1.StringList{
			"groups": {Values: []string{"Some Group"}},
		},
	}
}

// TestDelink_MatchReturnsUnavailableForEveryNonHealthyState is criterion
// 4's own regression: for EVERY non-healthy state, Match called with a
// non-empty "groups" keyword list returns (nil, err) with
// status.Code(err) == codes.Unavailable — never an empty SUCCESS, which is
// exactly what would make kernel/correlate/correlate.go wipe this
// source's previously-synced rows.
func TestDelink_MatchReturnsUnavailableForEveryNonHealthyState(t *testing.T) {
	for _, s := range nonHealthyStates {
		t.Run("", func(t *testing.T) {
			p := newTestPlugin(t)
			p.setHealthState(s, "")

			resp, err := p.Match(context.Background(), nonEmptyGroupsRequest())
			if resp != nil {
				t.Fatalf("healthState(%d): want nil response on failure, got %+v", s, resp)
			}
			if err == nil {
				t.Fatalf("healthState(%d): want a non-nil error, got nil", s)
			}
			if got := status.Code(err); got != codes.Unavailable {
				t.Fatalf("healthState(%d): want codes.Unavailable, got %v (err=%v)", s, got, err)
			}
		})
	}
}

// TestDelink_HealthyEmptyMatchIsSuccessNotError is the companion case:
// a HEALTHY plugin whose store genuinely holds no matching chat returns a
// non-nil EMPTY MatchResponse and a NIL error — the outcome
// kernel/correlate/correlate.go treats oppositely from the error case
// above, and the two must stay distinguishable by this test suite itself.
func TestDelink_HealthyEmptyMatchIsSuccessNotError(t *testing.T) {
	p := newTestPlugin(t)
	p.setHealthState(healthStateLinked, "")

	resp, err := p.Match(context.Background(), nonEmptyGroupsRequest())
	if err != nil {
		t.Fatalf("want nil error for a healthy plugin with no matching chat, got: %v", err)
	}
	if resp == nil {
		t.Fatal("want a non-nil empty MatchResponse, got nil")
	}
	if len(resp.GetItems()) != 0 {
		t.Fatalf("want zero items (store has no chats at all), got %d", len(resp.GetItems()))
	}
}

// TestDelinkPreservesStore proves NO branch in eventhandler.go's
// handleEvent deletes, truncates, or otherwise empties this plugin's own
// message store when driven through each of the event-sourced failure
// states — appends rows, drives each failure event through handleEvent,
// and asserts the row count is UNCHANGED. A failure state changes what
// this plugin REPORTS, never what it has already captured.
func TestDelinkPreservesStore(t *testing.T) {
	cases := []struct {
		name string
		evt  any
	}{
		{"LoggedOut (delink)", &events.LoggedOut{OnConnect: true, Reason: events.ConnectFailureLoggedOut}},
		{"LoggedOut (inferred expiry)", &events.LoggedOut{OnConnect: true, Reason: events.ConnectFailureMainDeviceGone}},
		{"LoggedOut (inferred ban)", &events.LoggedOut{OnConnect: true, Reason: events.ConnectFailureUnknownLogout}},
		{"TemporaryBan", &events.TemporaryBan{Code: events.TempBanSentToTooManyPeople}},
		{"ConnectFailure (unrecognised)", &events.ConnectFailure{Reason: events.ConnectFailureReason(9999), Message: "synthetic"}},
		{"StreamReplaced", &events.StreamReplaced{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestPlugin(t)

			const chatJID = "123@g.us"
			if err := p.store.EnsureChat(chatJID, true); err != nil {
				t.Fatalf("EnsureChat: %v", err)
			}
			if err := p.store.Append(messageRecord{
				ID: "msg-1", ChatJID: chatJID, SenderName: "Alice", SentAtUnixMs: 1000, Body: "hello",
			}); err != nil {
				t.Fatalf("Append: %v", err)
			}

			before, err := p.store.MessagesForChats([]string{chatJID})
			if err != nil {
				t.Fatalf("MessagesForChats (before): %v", err)
			}
			if len(before) != 1 {
				t.Fatalf("setup: want 1 row before driving the event, got %d", len(before))
			}

			p.handleEvent(tc.evt)

			after, err := p.store.MessagesForChats([]string{chatJID})
			if err != nil {
				t.Fatalf("MessagesForChats (after): %v", err)
			}
			if len(after) != len(before) {
				t.Fatalf("%s: row count changed from %d to %d — a failure event must never delete captured messages", tc.name, len(before), len(after))
			}

			chats, err := p.store.Chats()
			if err != nil {
				t.Fatalf("Chats (after): %v", err)
			}
			if len(chats) != 1 {
				t.Fatalf("%s: chat row count changed, want 1, got %d", tc.name, len(chats))
			}
		})
	}
}
