// Package main's oauthconfig.go holds the single shared oauth2.Config
// constructor and the one read-only Drive scope constant this plugin is
// ever allowed to request. Both auth.go (the standalone `auth` subcommand)
// and plan 02-02's serve-mode token-source wiring build their oauth2.Config
// exclusively through newOAuthConfig, so the two runtime contexts documented
// in 02-RESEARCH.md's Architectural Responsibility Map cannot drift apart
// and independently request different scopes.
package main

import (
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// driveReadonlyScope is the exact, and only, OAuth scope this plugin ever
// requests. PRD.md prohibition 3 forbids any write-capable Drive scope;
// drive.metadata.readonly was considered and ruled out because it cannot
// download file content, which CONT-01/CONT-02 require in Phase 4. This is
// hardcoded rather than imported from google.golang.org/api/drive/v3
// (CONTRACT-GAPS.md-adjacent PROJECT.md decision: "Drive scope constant
// sourcing") — Phase 3 introduces that module when it makes its first real
// Drive call and may swap this constant for drive.DriveReadonlyScope then,
// with the pinning test in token_test.go unchanged.
const driveReadonlyScope = "https://www.googleapis.com/auth/drive.readonly"

// newOAuthConfig builds the one oauth2.Config shape this plugin ever
// constructs. It performs no I/O and reads no environment — the same
// purity discipline plugin.go's Describe already documents — so both the
// auth subcommand and serve-mode token-source wiring can call it identically
// without risking scope or endpoint drift between the two contexts.
//
// The returned Scopes slice is freshly allocated on every call (never a
// shared package-level backing array), matching the defensive-copy
// convention plugin.go's Describe established for MatchVocabulary — so a
// caller mutating one returned config's Scopes slice cannot corrupt a
// subsequently constructed config.
func newOAuthConfig(clientID, clientSecret, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Scopes:       []string{driveReadonlyScope},
		Endpoint:     google.Endpoint,
	}
}
