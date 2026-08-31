package main

import (
	"context"
	"testing"

	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// nonHealthyStates is every named healthState this plan defines EXCEPT
// healthStateLinked — the set delink_test.go's per-state regression and
// this file's own per-state assertions both iterate.
var nonHealthyStates = []healthState{
	healthStateConnecting,
	healthStateNotLinked,
	healthStateDelinked,
	healthStateBanned,
	healthStateExpired,
	healthStateStreamReplaced,
}

// TestHealthState_HealthyExactlyOne proves Healthy() is true for exactly
// one of the six named states (healthStateLinked) and false for every
// other one — the boolean Match/Health both branch on.
func TestHealthState_HealthyExactlyOne(t *testing.T) {
	if !healthStateLinked.Healthy() {
		t.Fatal("healthStateLinked.Healthy() = false, want true")
	}
	for _, s := range nonHealthyStates {
		if s.Healthy() {
			t.Fatalf("healthState(%d).Healthy() = true, want false (only healthStateLinked should report healthy)", s)
		}
	}
}

// TestHealthState_MessagesNonEmptyAndDistinct proves each non-healthy
// state's Message() is non-empty and is drawn from ITS OWN template — a
// POSITIVE property (no two distinct states share the same text), not a
// text-grep for any one cause's specific wording. Reads healthMessages
// (health.go) — the exact data Message() itself reads — so this can never
// silently drift from what Health/Match actually emit.
func TestHealthState_MessagesNonEmptyAndDistinct(t *testing.T) {
	seen := make(map[string]healthState, len(nonHealthyStates))
	for _, s := range nonHealthyStates {
		msg := s.Message()
		if msg == "" {
			t.Fatalf("healthState(%d).Message() is empty, want a non-empty, cause-specific message", s)
		}
		if owner, dup := seen[msg]; dup {
			t.Fatalf("healthState(%d) and healthState(%d) produced the IDENTICAL Message() text %q — every non-healthy cause must have its own distinct template", s, owner, msg)
		}
		seen[msg] = s
	}
	if len(seen) != len(nonHealthyStates) {
		t.Fatalf("want %d distinct messages, got %d", len(nonHealthyStates), len(seen))
	}
}

// TestHealthState_MessagesNeverImplyDataLoss is the ONE binding rule
// 08-UI-SPEC.md and 08-CONTEXT.md both restate: no non-healthy message may
// state or imply that previously captured messages were lost or are now
// inaccessible.
func TestHealthState_MessagesNeverImplyDataLoss(t *testing.T) {
	forbidden := []string{"lost", "no longer available", "inaccessible", "deleted", "gone forever"}
	for _, s := range nonHealthyStates {
		msg := s.Message()
		lower := toLowerASCII(msg)
		for _, phrase := range forbidden {
			if contains(lower, phrase) {
				t.Fatalf("healthState(%d).Message() = %q contains forbidden data-loss-implying phrase %q", s, msg, phrase)
			}
		}
	}
}

// toLowerASCII and contains avoid importing strings twice for two tiny
// helpers used only by the data-loss check above.
func toLowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

func contains(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestHealthStateFromLogoutReason proves the three named mappings this
// task requires, plus the "never silently healthy" fallback for an
// unrecognised reason.
func TestHealthStateFromLogoutReason(t *testing.T) {
	cases := []struct {
		name   string
		reason events.ConnectFailureReason
		want   healthState
	}{
		{"remote unpair (empirically confirmed, 401)", events.ConnectFailureLoggedOut, healthStateDelinked},
		{"main device gone (inferred expiry, 403)", events.ConnectFailureMainDeviceGone, healthStateExpired},
		{"unknown logout (inferred ban, 406)", events.ConnectFailureUnknownLogout, healthStateBanned},
		{"unrecognised reason never silently healthy", events.ConnectFailureReason(9999), healthStateDelinked},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := healthStateFromLogoutReason(tc.reason)
			if got != tc.want {
				t.Fatalf("healthStateFromLogoutReason(%d) = %d, want %d", tc.reason, got, tc.want)
			}
			if got == healthStateLinked {
				t.Fatalf("healthStateFromLogoutReason(%d) must never return healthStateLinked", tc.reason)
			}
		})
	}
}

// newTestPlugin builds a SourcePlugin with a real (temp-dir) message store
// but no live whatsmeow client — sufficient for exercising Match/Health's
// pure state-branching logic without a real WhatsApp connection.
func newTestPlugin(t *testing.T) *SourcePlugin {
	t.Helper()
	store, err := openMessageStore(t.TempDir())
	if err != nil {
		t.Fatalf("openMessageStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return &SourcePlugin{
		dir:       t.TempDir(),
		logOut:    discardWriter{},
		store:     store,
		pushNames: newPushNameCache(),
	}
}

// discardWriter is a minimal io.Writer sink so tests never print to the
// real process stderr.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// TestHealth_ReachableFalseWithLastErrorPerState proves Health reports
// Reachable:false with a non-empty LastError for every non-healthy state,
// and Reachable:true for the healthy one.
func TestHealth_ReachableFalseWithLastErrorPerState(t *testing.T) {
	for _, s := range nonHealthyStates {
		t.Run("", func(t *testing.T) {
			p := newTestPlugin(t)
			p.setHealthState(s, "")
			resp, err := p.Health(context.Background(), &toposv1.HealthRequest{})
			if err != nil {
				t.Fatalf("Health must never return a gRPC error, got: %v", err)
			}
			if resp.GetReachable() {
				t.Fatalf("healthState(%d): want Reachable=false", s)
			}
			if resp.GetLastError() == "" {
				t.Fatalf("healthState(%d): want non-empty LastError", s)
			}
		})
	}

	p := newTestPlugin(t)
	p.setHealthState(healthStateLinked, "")
	resp, err := p.Health(context.Background(), &toposv1.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !resp.GetReachable() {
		t.Fatalf("healthStateLinked: want Reachable=true, got LastError=%q", resp.GetLastError())
	}
}

// TestConnectingState_IsTheZeroValue proves gap G-08-4's root cause 2 is
// closed: a fresh, never-assigned healthState value IS healthStateConnecting,
// not healthStateNotLinked — so a *SourcePlugin whose state was never
// explicitly set can never report the false "not linked" pairing
// instruction for an already-paired device.
func TestConnectingState_IsTheZeroValue(t *testing.T) {
	var s healthState
	if s != healthStateConnecting {
		t.Fatalf("G-08-4: var s healthState (the Go zero value) = %d, want healthStateConnecting (%d)", s, healthStateConnecting)
	}
	if healthState(0) == healthStateNotLinked {
		t.Fatalf("G-08-4: healthState(0) must NOT equal healthStateNotLinked — the zero value must never double as the never-paired state")
	}
}

// TestConnectingState_MatchMessageIsNotThePairingInstruction is the exact
// byte-level regression the debug session's E-04 experiment pinned against
// the user's verbatim G-08-4 report: a zero-value *SourcePlugin's Match
// error must carry the connecting template, never the not-linked template's
// device-pairing instruction.
func TestConnectingState_MatchMessageIsNotThePairingInstruction(t *testing.T) {
	p := newTestPlugin(t) // zero value healthState — nothing assigned

	resp, err := p.Match(context.Background(), nonEmptyGroupsRequest())
	if resp != nil {
		t.Fatalf("G-08-4: want nil response for a zero-value (connecting) plugin, got %+v", resp)
	}
	if err == nil {
		t.Fatal("G-08-4: want a non-nil error for a zero-value (connecting) plugin, got nil")
	}
	if got := status.Code(err); got != codes.Unavailable {
		t.Fatalf("G-08-4: want codes.Unavailable, got %v (err=%v)", got, err)
	}

	msg := status.Convert(err).Message()
	connectingText := healthStateConnecting.Message()
	notLinkedText := healthStateNotLinked.Message()

	if !contains(msg, connectingText) {
		t.Fatalf("G-08-4: Match error %q does not contain the connecting template %q", msg, connectingText)
	}
	if contains(msg, notLinkedText) {
		t.Fatalf("G-08-4: Match error %q contains the not-linked pairing instruction %q — a zero-value (actively-connecting) plugin must never claim the device isn't paired", msg, notLinkedText)
	}
}
