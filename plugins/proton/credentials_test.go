package main

import (
	"context"
	"strings"
	"testing"
	"time"

	imapclient "github.com/emersion/go-imap/client"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// newTestPluginWithToken builds a SourcePlugin whose client.dial is
// substituted to connect directly to serverAddr — the same seam
// item_test.go's newTestPluginDialingServer uses, except the token is a
// parameter (so these tests can exercise well-shaped, shape-suspect, and
// correct tokens against the same fixture) and the substituted dial func
// counts every dial it issues through the returned *int, so a test can
// prove the connection was actually attempted rather than pre-empted by
// the shape check. newTestPluginDialingServer itself is left unmodified —
// other tests in this package depend on its exact signature.
func newTestPluginWithToken(t *testing.T, serverAddr, token string) (*SourcePlugin, *int) {
	t.Helper()

	plugin, err := NewSourcePlugin("imap://bridge.invalid:143", "username", token, "", "https://mail.proton.me/u/0")
	if err != nil {
		t.Fatalf("NewSourcePlugin: %v", err)
	}
	// A shape-suspect token constructing cleanly (nil error, non-nil
	// plugin) is itself part of what this helper proves for every caller:
	// no constructor rejects a token on shape (this plan's must_haves).

	dialCount := 0
	plugin.client.dial = func(timeout time.Duration) (*imapclient.Client, error) {
		dialCount++
		conn, err := imapclient.Dial(serverAddr)
		if err != nil {
			return nil, err
		}
		conn.Timeout = timeout
		return conn, nil
	}
	return plugin, &dialCount
}

// TestBridgeTokenShapeWarning_AlphabetBoundary pins bridgeTokenShapeWarning's
// exact boundary: every byte in base64url's alphabet (A-Za-z0-9-_) must
// clear a token, and everything else must not — including the two
// characters ('+' and '/') that a "looks like base64" implementation would
// wrongly accept because they belong to *standard* base64 but not to
// base64url, and '=' (RawURLEncoding padding, which Bridge's encoder never
// emits).
func TestBridgeTokenShapeWarning_AlphabetBoundary(t *testing.T) {
	cases := []struct {
		name  string
		token string
		want  string
	}{
		{"empty token — a different, louder failure main.go already refuses to start on", "", ""},
		{"all-alphanumeric token", "abc123XYZ", ""},
		{"hyphen and underscore — the two symbols IN the alphabet", "abc-123_XYZ", ""},
		{"single-character token in the alphabet", "a", ""},
		{"plus sign — standard base64, not base64url", "abc+123XYZ", bridgeTokenShapeWarningText},
		{"forward slash — standard base64, not base64url", "abc/123XYZ", bridgeTokenShapeWarningText},
		{"equals padding — RawURLEncoding never emits it", "abc=123XYZ", bridgeTokenShapeWarningText},
		{"double quote — the character that broke the double-quoted TOML config", `abc"123XYZ`, bridgeTokenShapeWarningText},
		{"space", "abc 123XYZ", bridgeTokenShapeWarningText},
		{"non-ASCII rune", "abcé123XYZ", bridgeTokenShapeWarningText},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bridgeTokenShapeWarning(tc.token)
			if got != tc.want {
				t.Errorf("bridgeTokenShapeWarning(%q) = %q, want %q", tc.token, got, tc.want)
			}
		})
	}
}

// shapeSuspectTokenLiveTest is a token whose characters are individually
// distinctive (so a substring-presence assertion against LastError is
// meaningful) and which is provably outside the base64url alphabet.
const shapeSuspectTokenLiveTest = `Z9k#Qw!7mP$2vXeR&1LtY@8`

// TestHealth_ShapeSuspectTokenYieldsActionableLastErrorAndStillDials is the
// assertion that the connection is still attempted with a shape-suspect
// token — "warning-grade, never a gate" is the property most likely to be
// lost in a later refactor, so it is pinned here by name and by a dial
// counter, not left implicit in a broader test.
func TestHealth_ShapeSuspectTokenYieldsActionableLastErrorAndStillDials(t *testing.T) {
	serverAddr := newTestIMAPServer(t)
	plugin, dialCount := newTestPluginWithToken(t, serverAddr, shapeSuspectTokenLiveTest)

	ctx := context.Background()
	resp, err := plugin.Health(ctx, &toposv1.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if resp.GetReachable() {
		t.Fatalf("Health: Reachable = true, want false for a shape-suspect token")
	}
	if !strings.Contains(resp.GetLastError(), "Bridge-generated app password") {
		t.Errorf("Health: LastError = %q, want it to identify the token as not a Bridge-generated app password", resp.GetLastError())
	}
	if strings.Contains(resp.GetLastError(), shapeSuspectTokenLiveTest) {
		t.Errorf("Health: LastError = %q, must NOT contain the configured token (T-03-03)", resp.GetLastError())
	}
	if *dialCount != 1 {
		t.Errorf("dial count = %d, want exactly 1 — the shape check must never prevent a connection attempt", *dialCount)
	}
}

// TestHealth_WellShapedButWrongTokenGetsNoAddedAdvice is the discriminating-
// signal assertion: a token drawn only from the Bridge alphabet, even when
// it is the wrong password, must produce the server's own rejection
// unchanged — no added advice — or the new text would decorate every
// authentication failure instead of discriminating one specific
// misconfiguration.
func TestHealth_WellShapedButWrongTokenGetsNoAddedAdvice(t *testing.T) {
	serverAddr := newTestIMAPServer(t)
	plugin, dialCount := newTestPluginWithToken(t, serverAddr, "wrongPassword123")

	ctx := context.Background()
	resp, err := plugin.Health(ctx, &toposv1.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if resp.GetReachable() {
		t.Fatalf("Health: Reachable = true, want false for a wrong (but well-shaped) password")
	}
	if strings.Contains(resp.GetLastError(), bridgeTokenShapeWarningText) {
		t.Errorf("Health: LastError = %q, must NOT contain the shape-warning text for a well-shaped token", resp.GetLastError())
	}
	if *dialCount != 1 {
		t.Errorf("dial count = %d, want exactly 1", *dialCount)
	}
}

// TestHealth_CorrectTokenIsReachableWithNoLastError confirms the success
// path is untouched by this plan's change.
func TestHealth_CorrectTokenIsReachableWithNoLastError(t *testing.T) {
	serverAddr := newTestIMAPServer(t)
	plugin, dialCount := newTestPluginWithToken(t, serverAddr, "password")

	ctx := context.Background()
	resp, err := plugin.Health(ctx, &toposv1.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !resp.GetReachable() {
		t.Fatalf("Health: Reachable = false, want true for the fixture's correct token")
	}
	if resp.GetLastError() != "" {
		t.Errorf("Health: LastError = %q, want empty on a successful login", resp.GetLastError())
	}
	if *dialCount != 1 {
		t.Errorf("dial count = %d, want exactly 1", *dialCount)
	}
}
