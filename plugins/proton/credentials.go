// credentials.go is the single source of truth for diagnosing a
// misconfigured Proton Mail Bridge credential — the shared authentication-
// order note, the shape-warning text built from it, and the byte-wise
// alphabet predicate that decides whether to append it.
//
// Authority for every claim below: upstream proton-bridge renders its app
// password with base64.RawURLEncoding (pkg/algo/encode.go), so the
// alphabet is exactly A-Za-z0-9-_. Its CheckAuth
// (internal/services/useridentity/state.go) base64url-decodes and compares
// the presented password BEFORE it ever looks at the presented address,
// and upstream gluon's getUserID (internal/backend/backend.go) returns one
// single rejection error for every rejected (username, password) pair —
// which is why a rejected LOGIN is never evidence about which username was
// tried. See .planning/debug/proton-bridge-no-such-user.md for the full
// evidence trail. Observed against Bridge 3.25.0 — the version this
// deployment runs, confirmed by its own IMAP greeting — at the time this
// file was written; this is a dated calibration point against one
// upstream release, not a claim that holds for every past or future
// Bridge version.
package main

// bridgeAuthOrderNote is the shared, surface-independent explanation of
// Bridge's real authentication order. It is reused verbatim (never
// restated) by both bridgeTokenShapeWarningText below and by
// live_bridge_test.go's LOGIN-failure hint, so the runtime advice and the
// test's own diagnostic text can never again drift into contradicting one
// another — the specific failure mode that let a wrong explanation
// survive four verification rounds.
const bridgeAuthOrderNote = "Proton Mail Bridge validates the password before the username, and returns the identical rejection for every rejected (username, password) pair — so this error is not evidence that the username is wrong. Read the real Bridge app password from the Bridge account view's mailbox-details panel, or from the Bridge CLI's info command: it is roughly 20-22 characters drawn only from A-Za-z0-9-_, and it is never your Proton account password."

// bridgeTokenShapeWarningText is the full warning appended to a LOGIN
// failure when the configured token's shape rules it out as a
// Bridge-generated app password. It is formed by compile-time constant
// concatenation of a shape-specific prefix and bridgeAuthOrderNote — a
// constant expression provably contains no runtime data, which is the
// mechanism (not merely a style choice) by which this text is guaranteed
// to never carry the token, a character of it, its length, or a count of
// its offending characters (client.go's standing T-03-03 guarantee that
// the password appears in no error string, log line, or
// HealthResponse.LastError).
const bridgeTokenShapeWarningText = "the configured token is not a Bridge-generated app password (Bridge-generated app passwords contain only the characters A-Za-z0-9-_) — " + bridgeAuthOrderNote

// bridgeTokenShapeWarning returns bridgeTokenShapeWarningText if token
// contains any byte outside base64url's alphabet (A-Za-z0-9-_), or the
// empty string otherwise — including for an empty token, which is a
// different and louder failure main.go already refuses to start on, so
// adding advice here would fire on a condition this text does not
// explain.
//
// The scan is deliberately byte-wise, not rune-wise or normalized:
// base64url is an ASCII alphabet, so every byte of every multi-byte UTF-8
// rune already falls outside A-Za-z0-9-_ and is classified correctly with
// no decoding step, no code-point iteration, and no normalization form to
// choose between.
func bridgeTokenShapeWarning(token string) string {
	if token == "" {
		return ""
	}
	for i := 0; i < len(token); i++ {
		b := token[i]
		inAlphabet := (b >= 'A' && b <= 'Z') ||
			(b >= 'a' && b <= 'z') ||
			(b >= '0' && b <= '9') ||
			b == '-' || b == '_'
		if !inAlphabet {
			return bridgeTokenShapeWarningText
		}
	}
	return ""
}
