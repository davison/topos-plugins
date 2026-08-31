// Package main's auth_test.go is the regression net for auth.go's failure
// paths and its callback handler, run entirely offline: no browser opened,
// no listener left bound past a test, no network call made. Follows
// plugin_test.go's Test<Fn>_<BehaviorInPlainEnglish> naming idiom.
package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// recordingOpener is an injected browser-open stub that never launches
// anything — it only records the URL it was handed, so a test can assert
// on whether/what open was called with.
type recordingOpener struct {
	called bool
	url    string
}

func (r *recordingOpener) open(u string) error {
	r.called = true
	r.url = u
	return nil
}

func TestRunAuthWith_UnsetClientIDFailsNamingTheVariable(t *testing.T) {
	isolatedDir := t.TempDir()
	getenv := staticGetenv(map[string]string{
		"HOME":          isolatedDir,
		"XDG_DATA_HOME": isolatedDir,
		// GDRIVE_CLIENT_ID intentionally absent
		"GDRIVE_CLIENT_SECRET": "some-secret-sentinel",
	})
	var out bytes.Buffer
	opener := &recordingOpener{}

	err := runAuthWith(getenv, &out, opener.open, time.Second)
	if err == nil {
		t.Fatal("runAuthWith with unset GDRIVE_CLIENT_ID: want non-nil error")
	}
	if !strings.Contains(err.Error(), "GDRIVE_CLIENT_ID") {
		t.Errorf("error %q does not name GDRIVE_CLIENT_ID", err.Error())
	}
	if strings.Contains(err.Error(), "some-secret-sentinel") {
		t.Errorf("error %q leaks the client-secret sentinel", err.Error())
	}
	assertNoFilesystemEntries(t, isolatedDir)
	if opener.called {
		t.Error("browser-open stub was called, want it never called")
	}
}

func TestRunAuthWith_EmptyStringClientIDBehavesLikeUnset(t *testing.T) {
	isolatedDir := t.TempDir()
	getenv := staticGetenv(map[string]string{
		"HOME":                 isolatedDir,
		"XDG_DATA_HOME":        isolatedDir,
		"GDRIVE_CLIENT_ID":     "",
		"GDRIVE_CLIENT_SECRET": "some-secret-sentinel",
	})
	var out bytes.Buffer
	opener := &recordingOpener{}

	err := runAuthWith(getenv, &out, opener.open, time.Second)
	if err == nil {
		t.Fatal("runAuthWith with empty-string GDRIVE_CLIENT_ID: want non-nil error")
	}
	if !strings.Contains(err.Error(), "GDRIVE_CLIENT_ID") {
		t.Errorf("error %q does not name GDRIVE_CLIENT_ID", err.Error())
	}
	assertNoFilesystemEntries(t, isolatedDir)
	if opener.called {
		t.Error("browser-open stub was called, want it never called")
	}
}

func TestRunAuthWith_UnsetClientSecretFailsNamingTheVariable(t *testing.T) {
	isolatedDir := t.TempDir()
	getenv := staticGetenv(map[string]string{
		"HOME":             isolatedDir,
		"XDG_DATA_HOME":    isolatedDir,
		"GDRIVE_CLIENT_ID": "some-id-sentinel",
		// GDRIVE_CLIENT_SECRET intentionally absent
	})
	var out bytes.Buffer
	opener := &recordingOpener{}

	err := runAuthWith(getenv, &out, opener.open, time.Second)
	if err == nil {
		t.Fatal("runAuthWith with unset GDRIVE_CLIENT_SECRET: want non-nil error")
	}
	if !strings.Contains(err.Error(), "GDRIVE_CLIENT_SECRET") {
		t.Errorf("error %q does not name GDRIVE_CLIENT_SECRET", err.Error())
	}
	assertNoFilesystemEntries(t, isolatedDir)
	if opener.called {
		t.Error("browser-open stub was called, want it never called")
	}
}

func TestRunAuthWith_EmptyStringClientSecretBehavesLikeUnset(t *testing.T) {
	isolatedDir := t.TempDir()
	getenv := staticGetenv(map[string]string{
		"HOME":                 isolatedDir,
		"XDG_DATA_HOME":        isolatedDir,
		"GDRIVE_CLIENT_ID":     "some-id-sentinel",
		"GDRIVE_CLIENT_SECRET": "",
	})
	var out bytes.Buffer
	opener := &recordingOpener{}

	err := runAuthWith(getenv, &out, opener.open, time.Second)
	if err == nil {
		t.Fatal("runAuthWith with empty-string GDRIVE_CLIENT_SECRET: want non-nil error")
	}
	if !strings.Contains(err.Error(), "GDRIVE_CLIENT_SECRET") {
		t.Errorf("error %q does not name GDRIVE_CLIENT_SECRET", err.Error())
	}
	assertNoFilesystemEntries(t, isolatedDir)
	if opener.called {
		t.Error("browser-open stub was called, want it never called")
	}
}

// TestRunAuthWith_SpuriousMismatchedRequestDoesNotAbortTheFlow pins
// 02-REVIEW.md CR-01 / threat T-02-01: a state-mismatched local request
// reaching the loopback callback listener before the genuine Google
// redirect must not abort the authorization attempt. It fires the spurious
// request from a goroutine — never synchronously inside the open stub,
// which would deadlock: runAuthWith calls open before it calls
// srv.Serve(listener), so a synchronous request would connect into the
// listener's backlog and then block waiting for a server that cannot start
// until the stub returns.
func TestRunAuthWith_SpuriousMismatchedRequestDoesNotAbortTheFlow(t *testing.T) {
	isolatedDir := t.TempDir()
	getenv := staticGetenv(map[string]string{
		"HOME":                 isolatedDir,
		"XDG_DATA_HOME":        isolatedDir,
		"GDRIVE_CLIENT_ID":     "some-id-sentinel",
		"GDRIVE_CLIENT_SECRET": "some-secret-sentinel",
	})
	var out bytes.Buffer
	statusCh := make(chan int, 1)

	open := func(authURL string) error {
		u, err := url.Parse(authURL)
		if err != nil {
			t.Errorf("url.Parse(%q): %v", authURL, err)
			return nil
		}
		q := u.Query()
		redirectURI := q.Get("redirect_uri")
		genuineState := q.Get("state")

		go func() {
			spuriousURL := redirectURI + "?code=spurious-code&state=" + genuineState + "-wrong"
			resp, err := http.Get(spuriousURL)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			statusCh <- resp.StatusCode
		}()
		return nil
	}

	err := runAuthWith(getenv, &out, open, 500*time.Millisecond)

	if err == nil {
		t.Fatal("runAuthWith: want non-nil error (deadline timeout, no genuine callback ever arrives), got nil")
	}
	if !strings.Contains(err.Error(), "timed out waiting for browser authorization") {
		t.Errorf("error = %q, want the verbatim timeout message", err.Error())
	}
	if strings.Contains(err.Error(), "state mismatch") {
		t.Errorf("error %q mentions a state mismatch — the spurious request must not become the flow's outcome", err.Error())
	}
	if strings.Contains(err.Error(), "some-id-sentinel") || strings.Contains(err.Error(), "some-secret-sentinel") {
		t.Errorf("error %q leaks a credential sentinel value", err.Error())
	}

	select {
	case status := <-statusCh:
		if status != http.StatusBadRequest {
			t.Errorf("spurious request status = %d, want %d", status, http.StatusBadRequest)
		}
	case <-time.After(3 * time.Second):
		t.Error("spurious request goroutine never reported a status — the GET may never have been answered")
	}

	assertNoFilesystemEntries(t, isolatedDir)
}

// assertNoFilesystemEntries fails the test if dir is not empty.
func assertNoFilesystemEntries(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = filepath.Join(dir, e.Name())
		}
		t.Errorf("isolated dir is not empty, want no filesystem entries: %v", names)
	}
}

func TestCallbackHandler_NeitherCodeNorErrorGets204AndSignalsNothing(t *testing.T) {
	sig := newCallbackSignals()
	handler := newCallbackHandler("expected-state", sig)

	req := httptest.NewRequest(http.MethodGet, "/?favicon=1", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	select {
	case c := <-sig.code:
		t.Errorf("sig.code received %q, want nothing sent", c)
	default:
	}
	select {
	case e := <-sig.fatal:
		t.Errorf("sig.fatal received %v, want nothing sent", e)
	default:
	}
	select {
	case <-sig.spurious:
		t.Error("sig.spurious received a signal, want nothing sent")
	default:
	}
}

func TestCallbackHandler_StateMismatchGets400AndSubsequentCorrectCallbackStillDelivers(t *testing.T) {
	sig := newCallbackSignals()
	handler := newCallbackHandler("expected-state", sig)

	// First: a spurious request with the wrong state.
	badReq := httptest.NewRequest(http.MethodGet, "/?code=spurious-code&state=wrong-state", nil)
	badRec := httptest.NewRecorder()
	handler(badRec, badReq)

	if badRec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", badRec.Code, http.StatusBadRequest)
	}
	select {
	case <-sig.spurious:
	default:
		t.Error("sig.spurious received nothing, want a state-mismatch signal")
	}
	select {
	case e := <-sig.fatal:
		t.Errorf("sig.fatal received %v on a state-mismatch request, want nothing sent", e)
	default:
	}
	select {
	case c := <-sig.code:
		t.Errorf("sig.code received %q on a state-mismatch request, want nothing sent", c)
	default:
	}

	// Second: the genuine callback, through the same handler.
	goodReq := httptest.NewRequest(http.MethodGet, "/?code=genuine-code&state=expected-state", nil)
	goodRec := httptest.NewRecorder()
	handler(goodRec, goodReq)

	select {
	case c := <-sig.code:
		if c != "genuine-code" {
			t.Errorf("sig.code received %q, want %q", c, "genuine-code")
		}
	default:
		t.Error("sig.code received nothing after the genuine callback, want the code delivered")
	}
}

func TestCallbackHandler_GoogleErrorParameterForwarded(t *testing.T) {
	sig := newCallbackSignals()
	handler := newCallbackHandler("expected-state", sig)

	req := httptest.NewRequest(http.MethodGet, "/?error=access_denied&state=expected-state", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	select {
	case e := <-sig.fatal:
		if !strings.Contains(e.Error(), "access_denied") {
			t.Errorf("sig.fatal error = %v, want it to mention access_denied", e)
		}
	default:
		t.Error("sig.fatal received nothing, want the forwarded Google error")
	}
	select {
	case c := <-sig.code:
		t.Errorf("sig.code received %q, want nothing sent", c)
	default:
	}
}

func TestCallbackHandler_CorrectCallbackBodyLeaksNoCodeStateOrToken(t *testing.T) {
	sig := newCallbackSignals()
	handler := newCallbackHandler("expected-state", sig)

	req := httptest.NewRequest(http.MethodGet, "/?code=secret-code-value&state=expected-state", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "secret-code-value") {
		t.Errorf("response body %q contains the authorization code", body)
	}
	if strings.Contains(body, "expected-state") {
		t.Errorf("response body %q contains the state value", body)
	}

	select {
	case c := <-sig.code:
		if c != "secret-code-value" {
			t.Errorf("sig.code received %q, want %q", c, "secret-code-value")
		}
	default:
		t.Error("sig.code received nothing, want the code delivered")
	}
}

func TestWarnIfDataDirIsInvisibleToServeMode_WarnsWhenXDGDataHomeDiverges(t *testing.T) {
	isolatedDir := t.TempDir()
	nonDefaultXDG := filepath.Join(isolatedDir, "custom-xdg")
	getenv := staticGetenv(map[string]string{
		"HOME":          isolatedDir,
		"XDG_DATA_HOME": nonDefaultXDG,
	})
	writtenPath, err := tokenPath(getenv)
	if err != nil {
		t.Fatalf("tokenPath: %v", err)
	}

	var out bytes.Buffer
	warnIfDataDirIsInvisibleToServeMode(getenv, writtenPath, &out)

	msg := out.String()
	if msg == "" {
		t.Fatal("warnIfDataDirIsInvisibleToServeMode wrote nothing, want a warning")
	}
	if !strings.Contains(msg, "XDG_DATA_HOME") {
		t.Errorf("warning %q does not name XDG_DATA_HOME", msg)
	}
	serveModeGetenv := func(key string) string {
		if key == "XDG_DATA_HOME" {
			return ""
		}
		return getenv(key)
	}
	serveModePath, err := tokenPath(serveModeGetenv)
	if err != nil {
		t.Fatalf("tokenPath (serve-mode shape): %v", err)
	}
	if !strings.Contains(msg, writtenPath) {
		t.Errorf("warning %q does not contain the written path %q", msg, writtenPath)
	}
	if !strings.Contains(msg, serveModePath) {
		t.Errorf("warning %q does not contain the serve-mode path %q", msg, serveModePath)
	}
}

func TestWarnIfDataDirIsInvisibleToServeMode_SilentWhenXDGDataHomeUnset(t *testing.T) {
	isolatedDir := t.TempDir()
	getenv := staticGetenv(map[string]string{
		"HOME": isolatedDir,
		// XDG_DATA_HOME intentionally unset — both contexts resolve
		// identically, so no divergence warning is expected.
	})
	writtenPath, err := tokenPath(getenv)
	if err != nil {
		t.Fatalf("tokenPath: %v", err)
	}

	var out bytes.Buffer
	warnIfDataDirIsInvisibleToServeMode(getenv, writtenPath, &out)

	if out.String() != "" {
		t.Errorf("warnIfDataDirIsInvisibleToServeMode wrote %q, want nothing when XDG_DATA_HOME is unset", out.String())
	}
}

// awaitCallbackResult bundles awaitCallback's two return values onto one
// channel so a goroutine running it can report both at once.
type awaitCallbackResult struct {
	code string
	err  error
}

// TestAwaitCallback_SpuriousRequestThenGenuineCallbackStillDelivers drives
// the real newCallbackHandler behind a real 127.0.0.1 listener — never a
// hand-rolled stand-in — to pin the positive half of 02-REVIEW.md CR-01:
// resilience to a spurious request must still let the genuine callback
// that follows complete the flow.
func TestAwaitCallback_SpuriousRequestThenGenuineCallbackStillDelivers(t *testing.T) {
	const state = "expected-state"
	sig := newCallbackSignals()
	srv := httptest.NewServer(newCallbackHandler(state, sig))
	defer srv.Close()

	// 1. A state-mismatched request lands first — the only queued signal
	// at the instant awaitCallback starts is the spurious one. Against
	// the old inlined select, this would have already become the flow's
	// returned error.
	badResp, err := http.Get(srv.URL + "?code=spurious-code&state=wrong-state")
	if err != nil {
		t.Fatalf("GET spurious request: %v", err)
	}
	badResp.Body.Close()
	if badResp.StatusCode != http.StatusBadRequest {
		t.Errorf("spurious request status = %d, want %d", badResp.StatusCode, http.StatusBadRequest)
	}

	resultCh := make(chan awaitCallbackResult, 1)
	go func() {
		code, err := awaitCallback(sig, time.After(5*time.Second))
		resultCh <- awaitCallbackResult{code: code, err: err}
	}()

	// 2. The genuine callback, over the same real HTTP server.
	goodResp, err := http.Get(srv.URL + "?code=genuine-code&state=" + state)
	if err != nil {
		t.Fatalf("GET genuine callback: %v", err)
	}
	goodResp.Body.Close()
	if goodResp.StatusCode != http.StatusOK {
		t.Errorf("genuine callback status = %d, want %d", goodResp.StatusCode, http.StatusOK)
	}

	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Errorf("awaitCallback error = %v, want nil", result.err)
		}
		if result.code != "genuine-code" {
			t.Errorf("awaitCallback code = %q, want %q", result.code, "genuine-code")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("awaitCallback never returned")
	}
}

// TestAwaitCallback_GoogleReportedErrorWithMatchingStateAbortsImmediately
// is the counterweight to the spurious-request resilience test: the fix
// must distinguish the two cases, not simply become permissive. A
// Google-reported error against the CORRECT state still ends the wait
// immediately and surfaces its cause.
func TestAwaitCallback_GoogleReportedErrorWithMatchingStateAbortsImmediately(t *testing.T) {
	const state = "expected-state"
	sig := newCallbackSignals()
	srv := httptest.NewServer(newCallbackHandler(state, sig))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "?error=access_denied&state=" + state)
	if err != nil {
		t.Fatalf("GET Google-reported error callback: %v", err)
	}
	resp.Body.Close()

	code, err := awaitCallback(sig, time.After(5*time.Second))
	if err == nil {
		t.Fatal("awaitCallback: want non-nil error naming access_denied, got nil")
	}
	if !strings.Contains(err.Error(), "access_denied") {
		t.Errorf("awaitCallback error = %v, want it to mention access_denied", err)
	}
	if code != "" {
		t.Errorf("awaitCallback code = %q, want empty", code)
	}
}

// TestAwaitCallback_FiredDeadlineEndsTheWaitAfterRepeatedSpuriousSignals
// pins T-02-21: after many spurious signals have been drained, a fired
// deadline still ends the wait with the timeout error — the bound is not
// extended per iteration. No HTTP is involved; the channels are driven
// directly. The ~50 BLOCKING sends only proceed as awaitCallback drains
// them one at a time off a capacity-1 channel, so completing every send is
// itself proof the waiter is looping-and-discarding rather than returning
// early.
func TestAwaitCallback_FiredDeadlineEndsTheWaitAfterRepeatedSpuriousSignals(t *testing.T) {
	sig := newCallbackSignals()
	deadline := make(chan time.Time, 1)

	resultCh := make(chan awaitCallbackResult, 1)
	go func() {
		code, err := awaitCallback(sig, deadline)
		resultCh <- awaitCallbackResult{code: code, err: err}
	}()

	for i := 0; i < 50; i++ {
		sig.spurious <- struct{}{}
	}

	deadline <- time.Now()

	select {
	case result := <-resultCh:
		if result.code != "" {
			t.Errorf("awaitCallback code = %q, want empty", result.code)
		}
		if result.err == nil || result.err.Error() != "timed out waiting for browser authorization" {
			t.Errorf("awaitCallback error = %v, want the verbatim timeout message", result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("awaitCallback never returned after the deadline fired")
	}
}
