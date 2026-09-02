// Package main integration tests drive the built binary exactly as the
// host does: over a real go-plugin gRPC handshake, dispensing the
// "source" plugin and calling its RPCs across the process boundary. This
// file proves the whole tracer end to end — module resolution, dispatch,
// serve wiring, handshake, gRPC transport, and the RPC body all have to be
// right for TestServeMode_HostClientDispensesAndDescribes to pass.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/davison/topos/sdk"
	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// handshakeLinePrefix is the leading token of every go-plugin handshake
// line written to stdout on entering serve mode: "<CoreProtocolVersion>|
// <AppProtocolVersion>|<Network>|<Address>|<Protocol>|...". The core
// protocol version has been the literal "1" since go-plugin's inception —
// distinct from sdk.Handshake.ProtocolVersion (the plugin's own app-level
// protocol number, currently 2) — so a line beginning "1|" is the
// unambiguous signal that this binary has entered serve mode.
const handshakeLinePrefix = "1|"

// buildPlugin returns a path to the topos-plugin-gdrive binary to exercise.
// If TEST_BIN_OVERRIDE is set, it is used as-is and no build runs — this is
// how 01-02-PLAN.md Task 2's `make verify-install` points
// TestServeMode_HostClientDispensesAndDescribes at the exact bytes just
// installed into the host's external plugin directory, rather than a fresh
// build-tree copy. With the variable unset, behavior is unchanged from
// plan 01-01: build into a fresh temp directory and return that path.
// Shared by every subprocess-level test in this file (Task 3 reuses this
// helper for the dispatch matrix rather than duplicating the build step).
func buildPlugin(t *testing.T) string {
	t.Helper()
	if override := os.Getenv("TEST_BIN_OVERRIDE"); override != "" {
		return override
	}
	binPath := filepath.Join(t.TempDir(), "topos-plugin-gdrive")
	cmd := exec.Command("go", "build", "-o", binPath, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return binPath
}

// TestServeMode_HostClientDispensesAndDescribes builds the binary, launches
// it through a go-plugin client configured exactly as a host would be,
// dispenses the "source" plugin, and calls Describe over a real gRPC
// connection. This is the whole Task 2 tracer in one assertion: module
// resolution, dispatch, serve wiring, handshake, gRPC transport, and the
// RPC body all have to be right for it to pass.
func TestServeMode_HostClientDispensesAndDescribes(t *testing.T) {
	binPath := buildPlugin(t)

	client := goplugin.NewClient(&goplugin.ClientConfig{
		HandshakeConfig:  sdk.Handshake,
		Plugins:          map[string]goplugin.Plugin{"source": &sdk.SourcePluginGRPCPlugin{}},
		Cmd:              exec.Command(binPath),
		AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC},
	})
	defer client.Kill()

	rpcClient, err := client.Client()
	if err != nil {
		t.Fatalf("client.Client(): %v", err)
	}

	raw, err := rpcClient.Dispense("source")
	if err != nil {
		t.Fatalf("Dispense(%q): %v", "source", err)
	}

	sourcePlugin, ok := raw.(sdk.SourcePlugin)
	if !ok {
		t.Fatalf("dispensed value does not implement sdk.SourcePlugin: %T", raw)
	}

	resp, err := sourcePlugin.Describe(context.Background(), &toposv1.DescribeRequest{})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if resp == nil {
		t.Fatal("Describe returned a nil response with no error")
	}

	if got, want := resp.GetSourceType(), "gdrive"; got != want {
		t.Errorf("SourceType = %q, want %q", got, want)
	}
	if got, want := resp.GetDisplayName(), "Google Drive"; got != want {
		t.Errorf("DisplayName = %q, want %q", got, want)
	}
	if got, want := resp.GetContractVersion(), "topos.v2"; got != want {
		t.Errorf("ContractVersion = %q, want %q", got, want)
	}

	if vocab := resp.GetMatchVocabulary(); len(vocab) != 1 || vocab[0] != "folders" {
		t.Errorf("MatchVocabulary = %v, want [\"folders\"]", vocab)
	}

	extras := resp.GetExtras()
	if len(extras) != 3 {
		t.Fatalf("len(Extras) = %d, want 3", len(extras))
	}
	wantExtras := []struct {
		key, label, placeholder string
		required, secret        bool
	}{
		{"client_id", "OAuth Client ID", "GDRIVE_CLIENT_ID", true, true},
		{"client_secret", "OAuth Client Secret", "GDRIVE_CLIENT_SECRET", true, true},
		{"folder_id", "Drive Folder ID", "e.g. 1a2B3cD4EfGhIjKlmNoPQRstuVwxYZ — the id segment of the folder's Drive URL", true, false},
	}
	for i, want := range wantExtras {
		got := extras[i]
		if got.GetKey() != want.key {
			t.Errorf("Extras[%d].Key = %q, want %q", i, got.GetKey(), want.key)
		}
		if got.GetLabel() != want.label {
			t.Errorf("Extras[%d].Label = %q, want %q", i, got.GetLabel(), want.label)
		}
		if got.GetRequired() != want.required {
			t.Errorf("Extras[%d].Required = %v, want %v", i, got.GetRequired(), want.required)
		}
		if got.GetSecret() != want.secret {
			t.Errorf("Extras[%d].Secret = %v, want %v", i, got.GetSecret(), want.secret)
		}
		if got.GetPlaceholder() != want.placeholder {
			t.Errorf("Extras[%d].Placeholder = %q, want %q", i, got.GetPlaceholder(), want.placeholder)
		}
	}
}

// cookieEnvVar returns the go-plugin magic-cookie environment variable
// (KEY=VALUE form) from the imported sdk.Handshake value — never a
// hand-copied literal, since that would defeat the point of proving the
// dispatch matrix against the real handshake constants. Without this
// variable set, goplugin.Serve refuses to proceed at all (it assumes it
// was launched directly rather than by a plugin host) and a serve-mode
// fallthrough would never reach the point of emitting a handshake line —
// exactly the false negative these dispatch tests must avoid.
// childEnv is the environment every spawned-binary test uses in place of
// a wholesale os.Environ() inherit (tp#5 / davison/topos#72 M3-R4): the
// parent's environment minus every GDRIVE_* entry, plus the handshake
// cookie. On the operator's machine GDRIVE_CLIENT_ID/SECRET are exported
// for real use; inheriting them turned the fail-loud credential check
// into a live OAuth flow and hung the suite. Tests that need credentials
// set their own fakes explicitly on top of this scrubbed base.
func childEnv(extra ...string) []string {
	env := make([]string, 0, len(os.Environ())+1+len(extra))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GDRIVE_") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, cookieEnvVar())
	return append(env, extra...)
}

func cookieEnvVar() string {
	return sdk.Handshake.MagicCookieKey + "=" + sdk.Handshake.MagicCookieValue
}

// firstStdoutLineOrTimeout starts cmd (which must not yet be started) and
// returns the first line written to its stdout, or reports timedOut=true
// if no line appears within timeout. The caller is responsible for
// killing/waiting on cmd afterward in either case.
func firstStdoutLineOrTimeout(t *testing.T, cmd *exec.Cmd, timeout time.Duration) (line string, timedOut bool) {
	t.Helper()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	lineCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			lineCh <- scanner.Text()
		}
	}()

	select {
	case l := <-lineCh:
		return l, false
	case <-time.After(timeout):
		return "", true
	}
}

// TestDispatch_AuthArgumentExitsWithoutServing runs the built binary with
// the single argument "auth", with the handshake cookie environment
// variable set so that a serve-mode fallthrough would produce a handshake
// line on stdout. A process that hangs here — never returning to the shell
// because it fell through into goplugin.Serve after running the auth
// stub — is the failure this test exists to catch.
func TestDispatch_AuthArgumentExitsWithoutServing(t *testing.T) {
	binPath := buildPlugin(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, "auth")
	cmd.Env = childEnv()
	out, err := cmd.CombinedOutput()

	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("running %q auth did not exit within 10s (hung instead of exiting) — output so far:\n%s", binPath, out)
	}
	// A non-zero exit is expected here: GDRIVE_CLIENT_ID/GDRIVE_CLIENT_SECRET
	// are not set in this test's environment, and auth.go's fail-loud
	// credential check (AUTH-02, added Phase 2) correctly exits 1 rather
	// than proceeding. This test's job is only to prove the process exits
	// promptly without falling through to serve mode — a full authorization
	// round trip is plan 02-01 Task 3's job, not this one's. Any error
	// other than a plain non-zero exit (e.g. the binary failing to start at
	// all) is still a real failure.
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("running %q auth: %v\noutput:\n%s", binPath, err, out)
		}
	}

	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, handshakeLinePrefix) {
			t.Fatalf("stdout contains a handshake line (%q) — the auth path fell through into serve mode instead of exiting", l)
		}
	}
}

// TestDispatch_OnlyExactLowercaseAuthAtPositionOneIsHonored is the
// executable form of RPC-02's manually classified edge probe: "auth" is
// honored ONLY as the exact lowercase literal at os.Args[1]. Every other
// shape — no arguments at all (how the host launches a plugin subprocess),
// a different case, a flag form, or "auth" at any later position — must
// fall through to ordinary serve mode.
func TestDispatch_OnlyExactLowercaseAuthAtPositionOneIsHonored(t *testing.T) {
	binPath := buildPlugin(t)

	tests := []struct {
		name      string
		args      []string
		serveMode bool // true: expect fallthrough to serve mode; false: expect a clean exit without serving
	}{
		{"ZeroArguments", nil, true},
		{"UppercaseAUTH", []string{"AUTH"}, true},
		{"DoubleDashAuthFlag", []string{"--auth"}, true},
		{"AuthAtSecondPosition", []string{"serve", "auth"}, true},
		{"ExactLowercaseAuth", []string{"auth"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.serveMode {
				cmd := exec.Command(binPath, tt.args...)
				cmd.Env = childEnv()

				line, timedOut := firstStdoutLineOrTimeout(t, cmd, 5*time.Second)
				defer func() {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}()

				if timedOut {
					t.Fatalf("args %v: expected serve mode (a handshake line on stdout) but none appeared within 5s", tt.args)
				}
				if !strings.HasPrefix(line, handshakeLinePrefix) {
					t.Fatalf("args %v: first stdout line = %q, want a handshake line beginning %q", tt.args, line, handshakeLinePrefix)
				}
				return
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binPath, tt.args...)
			cmd.Env = childEnv()
			out, err := cmd.CombinedOutput()

			if ctx.Err() == context.DeadlineExceeded {
				t.Fatalf("args %v: did not exit within 10s (hung instead of exiting)", tt.args)
			}
			// A non-zero exit is expected for the "auth" case:
			// GDRIVE_CLIENT_ID/GDRIVE_CLIENT_SECRET are not set in this
			// test's environment, and auth.go's fail-loud credential check
			// (AUTH-02, added Phase 2) correctly exits 1 rather than
			// proceeding. Only a non-*exec.ExitError failure (e.g. the
			// binary not starting at all) is a real test failure.
			if err != nil {
				if _, ok := err.(*exec.ExitError); !ok {
					t.Fatalf("args %v: exited with error: %v\noutput:\n%s", tt.args, err, out)
				}
			}
			for _, l := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(l, handshakeLinePrefix) {
					t.Fatalf("args %v: stdout contains a handshake line (%q) — expected a clean exit without serving", tt.args, l)
				}
			}
		})
	}
}

// launchAllowlist is the exact nine environment variable names
// contract/plugin-contract.md's "The launch environment" section
// documents as copied present-only into a plugin subprocess: PATH, HOME,
// LANG, LC_ALL, LC_CTYPE, TZ, TMPDIR, XDG_RUNTIME_DIR,
// DBUS_SESSION_BUS_ADDRESS. Declared once here so the live test below can
// construct cmd.Env explicitly, one name at a time via os.LookupEnv,
// rather than inheriting os.Environ() wholesale.
var launchAllowlist = []string{
	"PATH", "HOME", "LANG", "LC_ALL", "LC_CTYPE", "TZ", "TMPDIR",
	"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS",
}

// allowlistedEnvPresentInParent copies launchAllowlist's names out of this
// test process's own environment, present-only (an unset allowlisted name
// contributes nothing, never an empty-string entry) — matching the
// contract's stated copying rule exactly. Never calls os.Environ().
func allowlistedEnvPresentInParent() []string {
	var env []string
	for _, name := range launchAllowlist {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	return env
}

// TestServeMode_MintsAnAccessTokenFromThePersistedTokenWithNoBrowser is the
// live proof that a host-launched serve-mode subprocess — receiving
// nothing but the contract's own nine allowlisted variables and a
// WEBSPACES_SOURCE_CONFIG payload — mints a working access token from the
// token file plan 02-01's live "auth" run produced, with no browser and no
// prompt. It is opt-in via GDRIVE_LIVE_TOKEN_TEST=1 so `go test .` stays
// green on a machine that has never authorized.
func TestServeMode_MintsAnAccessTokenFromThePersistedTokenWithNoBrowser(t *testing.T) {
	if os.Getenv("GDRIVE_LIVE_TOKEN_TEST") != "1" {
		t.Skip("set GDRIVE_LIVE_TOKEN_TEST=1 to run this live test; it needs a real token file (produced by \"topos-plugin-gdrive auth\") and outbound network access to Google's token endpoint")
	}

	// A missing credential here is a hard failure, never a silent skip —
	// GDRIVE_LIVE_TOKEN_TEST=1 is the operator's explicit signal that this
	// live proof must actually run; a silent skip would let the phase's
	// central claim ("survives host restarts without re-authorization")
	// go unproven.
	clientID := os.Getenv("GDRIVE_CLIENT_ID")
	if clientID == "" {
		t.Fatal("GDRIVE_CLIENT_ID: not set — export it before running with GDRIVE_LIVE_TOKEN_TEST=1")
	}
	clientSecret := os.Getenv("GDRIVE_CLIENT_SECRET")
	if clientSecret == "" {
		t.Fatal("GDRIVE_CLIENT_SECRET: not set — export it before running with GDRIVE_LIVE_TOKEN_TEST=1")
	}

	binPath := buildPlugin(t)

	// Stands in for the kernel's own extras payload: in a real host launch
	// the kernel has already expanded the operator's own
	// ${GDRIVE_CLIENT_ID}/${GDRIVE_CLIENT_SECRET} config references into
	// this exact shape before the subprocess ever sees it.
	sourceConfigJSON, err := json.Marshal(struct {
		Extras map[string]string `json:"extras"`
	}{Extras: map[string]string{
		"client_id":     clientID,
		"client_secret": clientSecret,
	}})
	if err != nil {
		t.Fatalf("marshal WEBSPACES_SOURCE_CONFIG: %v", err)
	}

	// The subprocess environment this test constructs: exactly the
	// allowlisted names present in this test process, plus one
	// WEBSPACES_SOURCE_CONFIG entry — nothing else. In particular, no
	// GDRIVE_CLIENT_ID, no GDRIVE_CLIENT_SECRET, no XDG_DATA_HOME reach
	// the subprocess as raw variables: their absence is what proves the
	// extras path is doing the work, and what exercises the GAP-08
	// HOME-derived path resolution.
	allowlisted := allowlistedEnvPresentInParent()
	subprocessEnv := append([]string{}, allowlisted...)
	subprocessEnv = append(subprocessEnv, "WEBSPACES_SOURCE_CONFIG="+string(sourceConfigJSON))

	if len(subprocessEnv) != len(allowlisted)+1 {
		t.Fatalf("constructed subprocess environment has %d entries, want exactly %d (allowlist present in parent) + 1 (WEBSPACES_SOURCE_CONFIG)", len(subprocessEnv), len(allowlisted))
	}
	for _, kv := range subprocessEnv {
		key := strings.SplitN(kv, "=", 2)[0]
		if key == "GDRIVE_CLIENT_ID" || key == "GDRIVE_CLIENT_SECRET" || key == "XDG_DATA_HOME" {
			t.Fatalf("constructed subprocess environment contains forbidden key %q", key)
		}
	}

	// cookieEnvVar() is appended only to the exec.Cmd's own Env, never to
	// subprocessEnv above — it is a go-plugin transport requirement (how
	// this test harness's client proves to the child it was launched by a
	// plugin host at all), not part of the contract's own launch-
	// environment allowlist, so it must not be counted in the assertions
	// above.
	cmd := exec.Command(binPath)
	cmd.Env = append(append([]string{}, subprocessEnv...), cookieEnvVar())

	var stderrBuf bytes.Buffer
	client := goplugin.NewClient(&goplugin.ClientConfig{
		HandshakeConfig:  sdk.Handshake,
		Plugins:          map[string]goplugin.Plugin{"source": &sdk.SourcePluginGRPCPlugin{}},
		Cmd:              cmd,
		AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC},
		// SkipHostEnv is what makes go-plugin's own transport enforce the
		// explicit cmd.Env above rather than silently reintroducing this
		// test process's full environment behind it
		// (contract/plugin-contract.md's own stated enforcement mechanism).
		SkipHostEnv: true,
		Stderr:      &stderrBuf,
	})
	defer client.Kill()

	// client.Client() blocks on the handshake read from the subprocess's
	// stdout; go-plugin never reads from the subprocess's stdin at any
	// point in this flow, so this call structurally cannot involve any
	// stdin interaction with the child.
	rpcClient, err := client.Client()
	if err != nil {
		t.Fatalf("client.Client(): %v\nstderr:\n%s", err, stderrBuf.String())
	}

	raw, err := rpcClient.Dispense("source")
	if err != nil {
		t.Fatalf("Dispense(%q): %v\nstderr:\n%s", "source", err, stderrBuf.String())
	}
	sourcePlugin, ok := raw.(sdk.SourcePlugin)
	if !ok {
		t.Fatalf("dispensed value does not implement sdk.SourcePlugin: %T", raw)
	}

	resp, err := sourcePlugin.Health(context.Background(), &toposv1.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v\nstderr:\n%s", err, stderrBuf.String())
	}
	if resp.GetLastError() != healthAuthorized {
		t.Fatalf("Health LastError = %q, want %q (only reachable when a token was loaded AND a refresh grant succeeded)\nstderr:\n%s", resp.GetLastError(), healthAuthorized, stderrBuf.String())
	}

	stderrOutput := stderrBuf.String()
	if strings.Contains(stderrOutput, "accounts.google.com") {
		t.Errorf("subprocess stderr contains \"accounts.google.com\" — a consent URL should never be printed during a token-file-driven restart:\n%s", stderrOutput)
	}
	if strings.Contains(stderrOutput, "xdg-open") {
		t.Errorf("subprocess stderr contains \"xdg-open\" — a browser-launch attempt should never occur during a token-file-driven restart:\n%s", stderrOutput)
	}
}

// TestDispatch_AuthExitsEvenWithOperatorCredentialsExported is the
// regression pin for the scrub itself (tp#5): with GDRIVE_* exported in
// the PARENT environment — exactly the operator's machine — the child
// must still see none of it, so the auth path fails loud and exits
// instead of entering a live OAuth flow and hanging the suite.
func TestDispatch_AuthExitsEvenWithOperatorCredentialsExported(t *testing.T) {
	t.Setenv("GDRIVE_CLIENT_ID", "fake-operator-client-id")
	t.Setenv("GDRIVE_CLIENT_SECRET", "fake-operator-secret")

	for _, kv := range childEnv() {
		if strings.HasPrefix(kv, "GDRIVE_") {
			t.Fatalf("childEnv leaked %q into the child environment", kv)
		}
	}

	binPath := buildPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, "auth")
	cmd.Env = childEnv()
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("auth with exported parent credentials did not exit within 10s — the scrub failed:\n%s", out)
	}
	if err == nil {
		t.Fatalf("auth exited 0 without credentials in the child env — want the fail-loud non-zero exit; output:\n%s", out)
	}
}
