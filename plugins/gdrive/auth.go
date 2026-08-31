package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"golang.org/x/oauth2"
)

// authCallbackTimeout bounds how long runAuthWith waits for the operator to
// complete the browser consent flow and for the loopback listener to
// receive the redirect. Two minutes is generous for a human clicking
// through a consent screen without leaving an unattended process running
// indefinitely (threat T-02-07, accepted as low severity: the operator
// simply re-runs the command on timeout).
const authCallbackTimeout = 2 * time.Minute

// runAuth is the entry point for the standalone `auth` subcommand
// (PRD.md "Locked Decisions": authorization is a standalone CLI
// subcommand of the same binary; the host must never see or compose an
// OAuth URL of any kind). main.go dispatches to this exact signature — it
// must not change. The real work lives in runAuthWith, injected with the
// production os.Getenv/os.Stdout/openBrowser so the failure paths (missing
// env vars, state mismatch, timeout) are testable without a browser or a
// network connection.
func runAuth() error {
	return runAuthWith(os.Getenv, os.Stdout, openBrowser, authCallbackTimeout)
}

// runAuthWith performs the entire loopback OAuth authorization flow
// (PRD.md Design Guidance; 02-RESEARCH.md Pattern 1): read credentials from
// the operator's shell, bind a loopback listener, build a PKCE-protected
// consent URL, open the operator's browser, capture and validate the
// callback, exchange the code for a token, assert it carries a refresh
// token, and persist it atomically at 0600. callbackTimeout is injected
// (production runAuth passes the unchanged authCallbackTimeout constant) so
// the wait's deadline is testable without a two-minute-long test run,
// consistent with this function's existing os.Getenv/os.Stdout/openBrowser
// injection philosophy.
func runAuthWith(getenv func(string) string, out io.Writer, open func(string) error, callbackTimeout time.Duration) error {
	clientID := getenv("GDRIVE_CLIENT_ID")
	if clientID == "" {
		return errors.New("GDRIVE_CLIENT_ID: not set — export it in your shell before running \"auth\"")
	}
	clientSecret := getenv("GDRIVE_CLIENT_SECRET")
	if clientSecret == "" {
		return errors.New("GDRIVE_CLIENT_SECRET: not set — export it in your shell before running \"auth\"")
	}

	// Literal IPv4 loopback address, never a resolvable hostname — Google's
	// own guidance flags the name form as prone to client-side firewall/
	// DNS-resolution problems the literal address avoids (PRD.md Design
	// Guidance; 02-RESEARCH.md Anti-Patterns). Port 0 lets the OS assign a
	// free port; binding only 127.0.0.1 (never a wildcard) keeps the
	// callback listener unreachable from any other host on the network
	// (threat T-02-06).
	//
	// PRD Open Question 1 (loopback redirect-URI pre-registration) was
	// resolved by direct observation, not by assumption: on 2026-08-16 the
	// operator ran this exact flow against a real, Production-published
	// Cloud Console Desktop-app OAuth client with nothing pre-registered
	// beyond the client itself, and reported the flow succeeding — no
	// `redirect_uri_mismatch` was encountered (02-01-SUMMARY.md Task 3).
	// That observation reached this repository as a summary-form
	// confirmation rather than a verbatim per-item report, so only its
	// qualitative direction is recorded here — an unregistered,
	// OS-assigned loopback port was observed to work — not an exact port
	// number or console transcript. A future reader must not convert this
	// port-0 bind to a fixed, pre-registered port on the strength of
	// Google's general redirect-URI-must-match documentation alone; that
	// general guidance is exactly what this observation empirically
	// overrode for a Desktop-app client.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("bind loopback listener: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	verifier := oauth2.GenerateVerifier()
	state, err := randomState()
	if err != nil {
		return fmt.Errorf("generate state parameter: %w", err)
	}

	conf := newOAuthConfig(clientID, clientSecret, redirectURL)

	// AccessTypeOffline is what makes Google issue a refresh token at all;
	// prompt=consent is what makes it issue one again on a re-authorization
	// rather than returning only an access token (02-RESEARCH.md Pitfall 3).
	authURL := conf.AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("prompt", "consent"),
	)

	if err := open(authURL); err != nil {
		// A headless box with no opener must still be able to complete the
		// flow by copy-paste, not abort.
		fmt.Fprintf(out, "Could not open a browser automatically (%s).\nOpen this URL to authorize:\n%s\n", err, authURL)
	}

	sig := newCallbackSignals()
	srv := &http.Server{Handler: newCallbackHandler(state, sig)}
	go srv.Serve(listener)
	defer srv.Close()

	// Exactly one timer per authorization attempt, started before the wait
	// and handed to awaitCallback as a receive-only channel — no loop
	// iteration inside awaitCallback can re-arm it, so a spurious-request
	// flood cannot extend the bound threat T-02-07/T-02-21 relies on.
	timer := time.NewTimer(callbackTimeout)
	defer timer.Stop()

	code, err := awaitCallback(sig, timer.C)
	if err != nil {
		return err
	}

	tok, err := conf.Exchange(context.Background(), code, oauth2.VerifierOption(verifier))
	if err != nil {
		return fmt.Errorf("exchange authorization code: %w", err)
	}
	if tok.RefreshToken == "" {
		return errors.New("Google did not return a refresh token — the access token would stop working in about an hour; re-run \"auth\"")
	}

	path, err := tokenPath(getenv)
	if err != nil {
		return fmt.Errorf("resolve token path: %w", err)
	}
	if err := saveToken(path, tok); err != nil {
		return fmt.Errorf("save token: %w", err)
	}

	warnIfDataDirIsInvisibleToServeMode(getenv, path, out)

	fmt.Fprintf(out, "Authorization complete — token saved to %s\n", path)
	return nil
}

// callbackSignals splits the loopback callback listener's possible outcomes
// by TYPE, not by convention. A state-mismatched request is
// attacker-influenced input (a local probe, or an attacker sharing the
// host) — spurious deliberately carries no payload, because there is
// nothing about it to return as the flow's error and nothing to write to
// the operator's terminal (threat T-02-22). That absence of a payload is
// itself the fix for 02-REVIEW.md CR-01 / threat T-02-01: the type system,
// not a doc comment, now prevents a mismatch from becoming the flow's
// outcome. fatal carries a Google-reported error against a MATCHING state
// (a genuine failure, e.g. the operator denying consent) and always ends
// the wait immediately.
type callbackSignals struct {
	code     chan string
	fatal    chan error
	spurious chan struct{}
}

// newCallbackSignals allocates a callbackSignals with each channel at
// capacity 1, matching newCallbackHandler's existing non-blocking-send
// discipline: at most one signal is ever in flight per attempt, so a send
// can never block the HTTP handler goroutine.
func newCallbackSignals() callbackSignals {
	return callbackSignals{
		code:     make(chan string, 1),
		fatal:    make(chan error, 1),
		spurious: make(chan struct{}, 1),
	}
}

// newCallbackHandler builds the one-shot HTTP handler that receives
// Google's redirect back to the loopback listener. It ignores any request
// whose query carries neither "code" nor "error" (a browser's favicon
// request must not be mistaken for a failed callback) and replies 204;
// rejects a state mismatch with 400 and signals sig.spurious — a
// payload-free signal that awaitCallback drains and discards, so a
// spurious local request cannot preempt the genuine callback (threat
// T-02-01, T-02-22) without shutting the listener down; forwards a
// Google-reported error (matching state) on sig.fatal; and otherwise sends
// the authorization code on sig.code.
func newCallbackHandler(state string, sig callbackSignals) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		code := q.Get("code")
		oauthErr := q.Get("error")
		if code == "" && oauthErr == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if q.Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			select {
			case sig.spurious <- struct{}{}:
			default:
			}
			return
		}

		if oauthErr != "" {
			fmt.Fprintln(w, "Authorization failed — you can close this tab.")
			select {
			case sig.fatal <- fmt.Errorf("Google returned an authorization error: %s", oauthErr):
			default:
			}
			return
		}

		fmt.Fprintln(w, "Authorization complete — you can close this tab.")
		select {
		case sig.code <- code:
		default:
		}
	}
}

// awaitCallback is the wait runAuthWith delegates to: it loops until it has
// a definitive outcome, discarding any number of spurious (state-mismatch)
// signals along the way rather than letting them end the wait. deadline is
// a receive-only channel the caller creates and owns — awaitCallback cannot
// re-arm it on any loop iteration, which is what keeps the wait bounded by
// exactly one deadline per authorization attempt regardless of how many
// spurious requests arrive (threat T-02-21).
func awaitCallback(sig callbackSignals, deadline <-chan time.Time) (string, error) {
	for {
		select {
		case code := <-sig.code:
			return code, nil
		case err := <-sig.fatal:
			return "", err
		case <-sig.spurious:
			// Attacker-influenced input with nothing to return or log
			// (threat T-02-22) — discard and keep waiting for the
			// genuine callback.
			continue
		case <-deadline:
			return "", errors.New("timed out waiting for browser authorization")
		}
	}
}

// randomState draws 32 bytes from a cryptographically secure source (never
// the non-cryptographic, predictable "math" random package, which is
// unsuitable for a CSRF-protection value) and returns them base64url-
// encoded, for use as the OAuth "state" parameter.
func randomState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// openBrowser launches the operator's default browser at url, switching on
// runtime.GOOS the same way the Go toolchain's own internal implementation
// of this need does (go.dev/src/cmd/internal/browser/browser.go). It uses
// cmd.Start, never cmd.Run — this function must never block on the browser
// process.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

// warnIfDataDirIsInvisibleToServeMode recomputes the token path a
// host-launched serve-mode subprocess would resolve (XDG_DATA_HOME treated
// as unset, per the reduced launch environment contract/plugin-contract.md
// documents) and, when it differs from the path the auth subcommand just
// wrote to, prints a loud warning naming XDG_DATA_HOME, both absolute
// paths, and the remedy — citing CONTRACT-GAPS.md GAP-08. This runs after
// the successful write, never instead of it, and never prints a token or
// secret value.
func warnIfDataDirIsInvisibleToServeMode(getenv func(string) string, writtenPath string, out io.Writer) {
	serveModeGetenv := func(key string) string {
		if key == "XDG_DATA_HOME" {
			return ""
		}
		return getenv(key)
	}
	serveModePath, err := tokenPath(serveModeGetenv)
	if err != nil || serveModePath == writtenPath {
		return
	}
	fmt.Fprintf(out, "WARNING: XDG_DATA_HOME is set in this shell, but a topos-launched plugin subprocess does not receive it (CONTRACT-GAPS.md GAP-08). "+
		"The token was written to %s, but serve mode will look for it at %s. "+
		"Re-run \"auth\" with XDG_DATA_HOME unset, or symlink %s to %s.\n",
		writtenPath, serveModePath, serveModePath, writtenPath)
}
