// Package main's authfailurespike_test.go is plan 05-01 Task 3's RPC-06
// live spike (PRD.md Open Question 2, CONTRACT-GAPS.md GAP-20): an
// operator-run instrument that forces a real refresh grant against Google
// using a token the operator has deliberately revoked (or let expire
// under Testing status), and observes exactly what Google's token-endpoint
// error response contains — never the raw body, never any client id,
// client secret, access token, or refresh token value. OPS-03's standing
// no-credential-leak gate applies to this harness's output with the same
// force it applies to production code (T-05-01). Gated on GDRIVE_LIVE_SPIKE
// being set to "1"; this is an ordinary Go test with no build tag — it is
// always compiled, always skipped by default, and run via
// `make spike-auth-failure`.
package main

import (
	"context"
	"errors"
	"os"
	"testing"

	"golang.org/x/oauth2"
)

// TestAuthFailure_ClassifiesGooglesRefreshErrorResponse is RPC-06's
// mandatory live spike. When GDRIVE_LIVE_SPIKE is unset, it skips with a
// message naming the make target that sets it — the same never-silently-
// skip discipline `verify-token` already enforces means that when the gate
// IS set but GDRIVE_CLIENT_ID, GDRIVE_CLIENT_SECRET, or the token file is
// missing, this test FAILS loudly instead.
//
// The harness resolves the real token path, loads the real token, builds
// the shared OAuth config through newOAuthConfig (never a second,
// independently constructed config), and calls the resulting token
// source's Token() to force a live refresh grant against Google's token
// endpoint. On the failure this spike is run to produce, it unwraps the
// error with errors.As into *oauth2.RetrieveError and reports exactly five
// observed facts — the HTTP status code, the error code field, the error
// description field, whether the error URI field is populated, and the
// byte LENGTH of the raw response body — never the raw body itself, and
// never any credential value. It then asserts classifyTokenError maps
// this failure to stateExpiredRevoked, the same single classifier
// Health/Match consume, so this spike proves the classification as well
// as observing the response.
//
// If the refresh unexpectedly SUCCEEDS, this test fails with a message
// telling the operator the token was not actually revoked yet — a spike
// that silently passes on a live token records nothing.
func TestAuthFailure_ClassifiesGooglesRefreshErrorResponse(t *testing.T) {
	if os.Getenv("GDRIVE_LIVE_SPIKE") != "1" {
		t.Skip(`GDRIVE_LIVE_SPIKE is not set to "1" — this live spike is opt-in only. Run "make spike-auth-failure" (after revoking this plugin's access at https://myaccount.google.com/permissions, or letting a Testing-status token expire) to execute it.`)
	}

	clientID := os.Getenv("GDRIVE_CLIENT_ID")
	clientSecret := os.Getenv("GDRIVE_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		t.Fatal(`GDRIVE_LIVE_SPIKE=1 but GDRIVE_CLIENT_ID and/or GDRIVE_CLIENT_SECRET is not exported — export both before running "make spike-auth-failure"; this spike fails loudly rather than silently skipping when the gate is set without credentials.`)
	}

	path, err := tokenPath(os.Getenv)
	if err != nil {
		t.Fatalf("resolve token path: %v", err)
	}
	tok, err := loadToken(path)
	if err != nil {
		t.Fatalf(`GDRIVE_LIVE_SPIKE=1 but no token file could be loaded at %s — run "topos-plugin-gdrive auth" once, then revoke that authorization before running this spike: %v`, path, err)
	}

	conf := newOAuthConfig(clientID, clientSecret, "")
	ts := conf.TokenSource(context.Background(), tok)

	if _, err := ts.Token(); err == nil {
		t.Fatal(`Token() against the configured refresh token SUCCEEDED — this spike proves nothing when the token has not actually been revoked yet. Revoke this plugin's access at https://myaccount.google.com/permissions (or let a Testing-status token expire) and re-run "make spike-auth-failure".`)
	} else {
		var retrieveErr *oauth2.RetrieveError
		if !errors.As(err, &retrieveErr) {
			t.Fatalf("Token() failed, but the error is not an *oauth2.RetrieveError (got %T): %v", err, err)
		}

		t.Logf("observed HTTP status code: %d", retrieveStatusCode(retrieveErr))
		t.Logf("observed error code: %q", retrieveErr.ErrorCode)
		t.Logf("observed error description: %q", retrieveErr.ErrorDescription)
		t.Logf("observed error URI populated: %v", retrieveErr.ErrorURI != "")
		t.Logf("observed raw response body length (bytes): %d", len(retrieveErr.Body))

		if state := classifyTokenError(retrieveErr); state != stateExpiredRevoked {
			t.Errorf("classifyTokenError(retrieveErr) = %v, want stateExpiredRevoked", state)
		}
	}
}

// retrieveStatusCode returns retrieveErr's HTTP response status code, or 0
// if the response is nil for some reason — never accesses or logs any
// other field of the response beyond the status code.
func retrieveStatusCode(retrieveErr *oauth2.RetrieveError) int {
	if retrieveErr.Response == nil {
		return 0
	}
	return retrieveErr.Response.StatusCode
}
