// Package main's secrets_test.go is the standing OPS-03 gate: no OAuth
// client id, client secret, access token, refresh token, PKCE verifier, or
// OAuth state value this plugin ever holds may reach a log line, an error
// message, or any other human-visible output — at any log level, including
// debug (contract/plugin-contract.md's Logging section, quoted verbatim in
// 02-RESEARCH.md's Security Domain). This file carries three tests plus
// their shared helpers and is written to be extended, not replaced, as
// later phases add more failure modes and more print/log call sites:
//
//   - TestBinary_AuthFailurePathNeverEmitsACredentialValue runs the real
//     binary as a subprocess and proves its combined output never contains
//     a credential sentinel on a failure path.
//   - TestSerdeAndRPCErrors_NeverEmitACredentialValue runs in-process,
//     table-driven over this package's offline failure modes, and proves
//     every human-visible string those modes produce is sentinel-free.
//   - TestSource_NeverInterpolatesACredentialIntoAPrintOrLogCall is a
//     source-level AST scanner catching a leak that has not been written
//     yet: a future print/log call that interpolates a credential-named
//     variable or renders a struct via a bare %v verb.
//
// Every absence assertion in this file is an exact byte-substring
// comparison over the unmodified captured output: never ToLower'd,
// TrimSpace'd, Fields'd, or Unicode-normalized. The question is whether a
// value's bytes appear anywhere in what the process wrote — softening that
// comparison in any way silently weakens the gate this file exists to be.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/grpc/status"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// newSentinel returns a fresh, high-entropy value drawn from crypto/rand on
// every call: label plus 16 random bytes hex-encoded. No sentinel this
// suite asserts on can be accidentally hardcoded anywhere in the
// repository, so a passing run cannot be a false negative from a stale
// literal a future edit happens to also contain.
func newSentinel(t *testing.T, label string) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("crypto/rand.Read: %v", err)
	}
	return label + "-" + hex.EncodeToString(b)
}

// TestBinary_AuthFailurePathNeverEmitsACredentialValue builds the binary
// and runs it as a subprocess with the "auth" argument, twice: once with
// GDRIVE_CLIENT_ID set to a sentinel and GDRIVE_CLIENT_SECRET unset, and
// once with the reverse. Both runs fail fast (neither reaches a browser or
// a listener — the credential check is the first thing runAuthWith does)
// and complete entirely offline.
func TestBinary_AuthFailurePathNeverEmitsACredentialValue(t *testing.T) {
	binPath := buildPlugin(t)
	isolatedDir := t.TempDir()

	runCase := func(t *testing.T, clientID, clientSecret, sentinel string) {
		t.Helper()
		cmd := exec.Command(binPath, "auth")
		cmd.Env = []string{
			"HOME=" + isolatedDir,
			"XDG_DATA_HOME=" + isolatedDir,
		}
		if clientID != "" {
			cmd.Env = append(cmd.Env, "GDRIVE_CLIENT_ID="+clientID)
		}
		if clientSecret != "" {
			cmd.Env = append(cmd.Env, "GDRIVE_CLIENT_SECRET="+clientSecret)
		}

		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("running %q auth: want a non-zero exit status, got nil error; output:\n%s", binPath, out)
		}
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("running %q auth: %v (not a plain non-zero exit — the binary may not have started at all); output:\n%s", binPath, err, out)
		}
		if bytes.Contains(out, []byte(sentinel)) {
			t.Errorf("subprocess combined stdout+stderr contains the sentinel %q — a credential value reached the process's output:\n%s", sentinel, out)
		}
	}

	t.Run("ClientIDSetSecretMissing", func(t *testing.T) {
		sentinel := newSentinel(t, "sentinel-client-id")
		runCase(t, sentinel, "", sentinel)
	})
	t.Run("ClientSecretSetIDMissing", func(t *testing.T) {
		sentinel := newSentinel(t, "sentinel-client-secret")
		runCase(t, "", sentinel, sentinel)
	})
}

// assertNoSentinelInPluginOutput constructs a SourcePlugin with getenv and
// asserts that Health's LastError and the gRPC status messages Match and
// Fetch wrap their errors in never contain sentinel.
func assertNoSentinelInPluginOutput(t *testing.T, getenv func(string) string, sentinel string) {
	t.Helper()

	healthPlugin := NewSourcePluginWithEnv(getenv)
	healthResp, err := healthPlugin.Health(context.Background(), &toposv1.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if strings.Contains(healthResp.GetLastError(), sentinel) {
		t.Errorf("Health LastError %q contains the sentinel", healthResp.GetLastError())
	}

	matchPlugin := NewSourcePluginWithEnv(getenv)
	_, matchErr := matchPlugin.Match(context.Background(), &toposv1.MatchRequest{})
	if matchErr != nil {
		if st, ok := status.FromError(matchErr); ok && strings.Contains(st.Message(), sentinel) {
			t.Errorf("Match gRPC status message %q contains the sentinel", st.Message())
		}
		if strings.Contains(matchErr.Error(), sentinel) {
			t.Errorf("Match error %q contains the sentinel", matchErr.Error())
		}
	}

	fetchPlugin := NewSourcePluginWithEnv(getenv)
	_, fetchErr := fetchPlugin.Fetch(context.Background(), &toposv1.FetchRequest{})
	if fetchErr != nil {
		if st, ok := status.FromError(fetchErr); ok && strings.Contains(st.Message(), sentinel) {
			t.Errorf("Fetch gRPC status message %q contains the sentinel", st.Message())
		}
		if strings.Contains(fetchErr.Error(), sentinel) {
			t.Errorf("Fetch error %q contains the sentinel", fetchErr.Error())
		}
	}
}

// TestSerdeAndRPCErrors_NeverEmitACredentialValue runs in-process and is
// table-driven (via t.Run) over this package's offline failure modes. For
// each case it collects every string the plugin would show a human —
// Health's LastError and the gRPC status messages/errors Match and Fetch
// produce — and asserts sentinel absence with the same exact
// byte-substring discipline as the subprocess test above. Every case is
// offline: no browser, no real network call to Google.
func TestSerdeAndRPCErrors_NeverEmitACredentialValue(t *testing.T) {
	// Case 1: a malformed source-config payload whose text embeds a
	// sentinel. tokenSource resolves the token file BEFORE the source
	// config (token.go's own documented resolution order), so a valid
	// token file must exist first for loadSourceConfig's own error path to
	// actually be reached and exercised — otherwise this case would only
	// prove the (trivial) token-file-missing path, same as case 2 below.
	t.Run("MalformedSourceConfigPayload", func(t *testing.T) {
		sentinel := newSentinel(t, "sentinel-malformed-payload")
		isolatedDir := t.TempDir()

		seedPath, err := tokenPath(staticGetenv(map[string]string{"HOME": isolatedDir, "XDG_DATA_HOME": isolatedDir}))
		if err != nil {
			t.Fatalf("tokenPath: %v", err)
		}
		if err := saveToken(seedPath, &oauth2.Token{
			AccessToken:  "seed-access-token",
			RefreshToken: "seed-refresh-token",
			Expiry:       time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatalf("saveToken (seed): %v", err)
		}

		malformed := `{"extras": ` + sentinel // deliberately truncated/invalid JSON, embedding the sentinel
		getenv := staticGetenv(map[string]string{
			"HOME":             isolatedDir,
			"XDG_DATA_HOME":    isolatedDir,
			sourceConfigEnvVar: malformed,
		})
		assertNoSentinelInPluginOutput(t, getenv, sentinel)
	})

	// Case 2: a well-formed WEBSPACES_SOURCE_CONFIG payload whose extras
	// carry sentinel credentials, with no token file present. tokenSource
	// fails at the token-file-load step before ever reaching extras, so
	// this case is a defensive regression net: even though a sentinel
	// credential sits in the environment the plugin was launched with,
	// nothing this plugin emits may ever contain it, independent of
	// whether the current code path happens to read it.
	t.Run("WellFormedExtrasCarrySentinelCredentialsNoTokenFile", func(t *testing.T) {
		sentinel := newSentinel(t, "sentinel-extras-credential")
		isolatedDir := t.TempDir()

		payload, err := json.Marshal(struct {
			Extras map[string]string `json:"extras"`
		}{Extras: map[string]string{
			"client_id":     sentinel,
			"client_secret": sentinel,
		}})
		if err != nil {
			t.Fatalf("marshal WEBSPACES_SOURCE_CONFIG: %v", err)
		}
		getenv := staticGetenv(map[string]string{
			"HOME":             isolatedDir,
			"XDG_DATA_HOME":    isolatedDir,
			sourceConfigEnvVar: string(payload),
		})
		assertNoSentinelInPluginOutput(t, getenv, sentinel)
	})

	// Case 3: a token file holding sentinel access and refresh tokens,
	// paired with an httptest token endpoint that refuses the refresh
	// grant. This is tested one layer below SourcePlugin's RPCs, directly
	// against persistingTokenSource — the same shape plugin_test.go's own
	// refresh-persistence tests already use — because tokenStatusMessage
	// (which Health/Match/Fetch all call through) collapses every
	// ts.Token() error into the fixed healthRefreshFailed constant before
	// it ever reaches a human. That collapse is exactly why testing only
	// at the SourcePlugin layer would never be able to catch a future
	// regression that surfaces ts.Token()'s raw error text (which could
	// carry a token-endpoint response body embedding the very credential
	// this test seeds) — this case exists to guard the layer where that
	// raw text actually lives.
	t.Run("RefreshGrantRefused", func(t *testing.T) {
		sentinel := newSentinel(t, "sentinel-refresh-token")
		isolatedDir := t.TempDir()
		path := filepath.Join(isolatedDir, "token.json")
		seed := &oauth2.Token{
			AccessToken:  sentinel + "-access",
			RefreshToken: sentinel + "-refresh",
			Expiry:       time.Now().Add(-time.Hour), // already expired: forces a refresh attempt
		}
		if err := saveToken(path, seed); err != nil {
			t.Fatalf("saveToken (seed): %v", err)
		}

		refusingSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)
		}))
		defer refusingSrv.Close()

		conf := &oauth2.Config{
			ClientID:     "test-client-id",
			ClientSecret: "test-client-secret",
			Endpoint:     oauth2.Endpoint{TokenURL: refusingSrv.URL},
		}
		ts := newPersistingTokenSource(conf.TokenSource(context.Background(), seed), path)

		_, err := ts.Token()
		if err == nil {
			t.Fatal("Token() against a refusing refresh endpoint: want a non-nil error")
		}
		if strings.Contains(err.Error(), sentinel) {
			t.Errorf("persistingTokenSource.Token() error %q contains the sentinel — a credential value leaked into a refresh-failure error", err.Error())
		}
	})

	// Case 4: a zero-byte token file. loadToken's own errTokenFileMalformed
	// path must name only the path, never echo the (empty, in this case)
	// contents — and, like case 2, sentinel credentials sitting unused in
	// the environment must still never appear anywhere in the output.
	t.Run("ZeroByteTokenFile", func(t *testing.T) {
		sentinel := newSentinel(t, "sentinel-zero-byte-token")
		isolatedDir := t.TempDir()
		dir := filepath.Join(isolatedDir, dataDirName)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("MkdirAll(%s): %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, tokenFileName), nil, 0o600); err != nil {
			t.Fatalf("WriteFile (zero-byte token file): %v", err)
		}
		getenv := staticGetenv(map[string]string{
			"HOME":                 isolatedDir,
			"XDG_DATA_HOME":        isolatedDir,
			"GDRIVE_CLIENT_ID":     sentinel,
			"GDRIVE_CLIENT_SECRET": sentinel,
		})
		assertNoSentinelInPluginOutput(t, getenv, sentinel)
	})
}

// credentialBearingNames is the exact identifier vocabulary, in both
// exported and unexported spellings, this scanner treats as
// credential-bearing: the client id, client secret, access token, refresh
// token, PKCE verifier, and OAuth state values this package ever holds.
// Widening this set to silence a real finding — rather than fixing the
// call site the scanner flagged, or adding a reasoned entry to
// allowedFormatVerbViolations below — is exactly the erosion this scanner
// exists to make visible (threat T-02-19).
var credentialBearingNames = map[string]bool{
	"clientID": true, "ClientID": true,
	"clientSecret": true, "ClientSecret": true,
	"accessToken": true, "AccessToken": true,
	"refreshToken": true, "RefreshToken": true,
	"verifier": true, "Verifier": true,
	"state": true, "State": true,
}

// printLogFamily reports whether call is a call this scanner must inspect:
// the fmt Print/Fprint/Sprint/Errorf group, any function in the log
// package, or a method named Trace, Debug, Info, Warn, or Error on any
// receiver (the shape a structured-logging library's leveled methods
// take, even though this package has no such logger yet — this scanner is
// written to still catch one introduced by a later phase). Matching a
// call here only makes it eligible for inspection; the credential-name and
// bare-verb checks below are what actually fail a test, so an
// over-inclusive match here (e.g. a non-logging "Error()" method call with
// no arguments) is harmless.
func printLogFamily(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if pkgIdent, ok := sel.X.(*ast.Ident); ok {
		switch pkgIdent.Name {
		case "fmt":
			name := sel.Sel.Name
			return strings.HasPrefix(name, "Print") || strings.HasPrefix(name, "Fprint") || strings.HasPrefix(name, "Sprint") || name == "Errorf"
		case "log":
			return true
		}
	}
	switch sel.Sel.Name {
	case "Trace", "Debug", "Info", "Warn", "Error":
		return true
	}
	return false
}

// formatStringArgIndex returns the ast.CallExpr argument index holding the
// printf-style format string for a known fmt formatting function, or -1
// for a function with no format string of its own (Print/Println/Sprint/
// Sprintln/Fprint/Fprintln each default-format every argument with the
// verb the credential-name identifier check below exists to catch
// instead). Fprintf's format string is Args[1], not Args[0] — its leading
// argument is the io.Writer — so a scanner that only ever inspected
// Args[0] would silently never catch a bare %v in any of auth.go's own
// fmt.Fprintf calls, which is exactly the call shape this package uses
// most for human-visible output.
func formatStringArgIndex(call *ast.CallExpr) int {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return -1
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok || pkgIdent.Name != "fmt" {
		return -1
	}
	switch sel.Sel.Name {
	case "Printf", "Sprintf", "Errorf":
		return 0
	case "Fprintf":
		return 1
	}
	return -1
}

// containsBareValueVerb reports whether s (an unquoted format-string
// literal) contains a bare value verb — %v, %+v, or %#v, with any
// additional width/precision digits — as opposed to %w (error wrapping,
// always allowed) or %s/%q (allowed on non-credential arguments, checked
// separately by the identifier scan). A struct rendered with a bare value
// verb prints every field it holds, which for an oauth2.Token or
// oauth2.Config means the secret itself. "%%" (an escaped literal percent)
// is never treated as the start of a verb.
func containsBareValueVerb(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			continue
		}
		if i+1 < len(s) && s[i+1] == '%' {
			i++
			continue
		}
		j := i + 1
		for j < len(s) && strings.ContainsRune("+-# 0", rune(s[j])) {
			j++
		}
		for j < len(s) && ((s[j] >= '0' && s[j] <= '9') || s[j] == '.') {
			j++
		}
		if j < len(s) && s[j] == 'v' {
			return true
		}
		if j > i {
			i = j - 1
		}
	}
	return false
}

// formatVerbViolationAllowance is one entry in allowedFormatVerbViolations:
// a specific file/line this scanner would otherwise flag, carrying the
// written reason it is permitted anyway.
type formatVerbViolationAllowance struct {
	file, reason string
	line         int
}

// allowedFormatVerbViolations is the standing escape hatch for a future,
// reasoned exception to this scanner's rules. Empty on creation. A failure
// answered by adding an entry here with no reason, or by silently widening
// credentialBearingNames or printLogFamily instead of fixing the flagged
// call site, is exactly the erosion this allowlist exists to make visible
// (threat T-02-19).
var allowedFormatVerbViolations = []formatVerbViolationAllowance{}

func (v formatVerbViolationAllowance) matches(file string, line int) bool {
	return v.file == file && v.line == line
}

// nonTestGoFiles returns every non-test .go file in this package's own
// directory, sorted, so the scanner's fail-first proof and the "at least 6
// files" acceptance criterion are both reproducible regardless of
// filesystem read order.
func nonTestGoFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir(.): %v", err)
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, name)
	}
	sort.Strings(files)
	return files
}

// credentialBearingArgName returns the final identifier name of arg when
// arg is a bare identifier or a selector expression (e.g. tok.RefreshToken
// -> "RefreshToken"), or "" for any other argument shape (a call
// expression, a literal, a binary expression, and so on — none of which
// can themselves be a credential-bearing variable reference).
func credentialBearingArgName(arg ast.Expr) string {
	switch e := arg.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}

// TestSource_NeverInterpolatesACredentialIntoAPrintOrLogCall parses every
// non-test .go file in this package with go/parser and walks the AST for
// call expressions in the print/log family (parser.ParseFile). It fails
// when either (a) an argument to such a call is an identifier or selector
// whose final name is in credentialBearingNames, or (b) the call's format
// string (resolved per-function via formatStringArgIndex, not always
// Args[0]) contains a bare value verb. Both are threat T-02-15's
// mitigation.
func TestSource_NeverInterpolatesACredentialIntoAPrintOrLogCall(t *testing.T) {
	for _, a := range allowedFormatVerbViolations {
		if a.reason == "" {
			t.Fatalf("allowedFormatVerbViolations entry for %s:%d carries no reason — every entry must be an explicit, written exception, never a silent one", a.file, a.line)
		}
	}

	files := nonTestGoFiles(t)
	if len(files) < 6 {
		t.Fatalf("nonTestGoFiles found %d files, want at least 6 (a shrinking file count would silently narrow this scanner's coverage)", len(files))
	}

	fset := token.NewFileSet()
	for _, name := range files {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", name, err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parser.ParseFile(%s): %v", name, err)
		}

		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !printLogFamily(call) {
				return true
			}
			pos := fset.Position(call.Pos())
			allowed := false
			for _, a := range allowedFormatVerbViolations {
				if a.matches(pos.Filename, pos.Line) {
					allowed = true
					break
				}
			}
			if allowed {
				return true
			}

			for _, arg := range call.Args {
				if name := credentialBearingArgName(arg); name != "" && credentialBearingNames[name] {
					t.Errorf("%s:%d: print/log call passes credential-bearing identifier %q as an argument", pos.Filename, pos.Line, name)
				}
			}

			if idx := formatStringArgIndex(call); idx >= 0 && idx < len(call.Args) {
				if lit, ok := call.Args[idx].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if unquoted, err := strconv.Unquote(lit.Value); err == nil && containsBareValueVerb(unquoted) {
						t.Errorf("%s:%d: print/log call's format string contains a bare value verb (%%v/%%+v/%%#v) — a struct rendered with it prints every field it holds", pos.Filename, pos.Line)
					}
				}
			}
			return true
		})
	}
}
