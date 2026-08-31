// Package main's retry_test.go proves RES-01/RES-02's retry decorator
// (driveclient.go's withRetry) end to end through its four sanctioned call
// sites (folderwalk.go's startPageToken/rootFolderName/walkFolder,
// changepoll.go's pollChanges), and carries the two standing source-level
// gates that keep it that way: the four-call-sites AST gate
// (TestRetryDecorator_WrapsExactlyTheFourSyncCriticalCallSites) and the
// no-delay-of-its-own gate (TestRetryPath_ComputesNoDelayOfItsOwn), both
// built on the exact go/ast walker idiom secrets_test.go's own source
// scanner already establishes (nonTestGoFiles), never a text search — a
// mention inside a comment can never satisfy or break either gate. Follows
// this repository's own idiom: Test<Thing>_<BehaviorInPlainEnglish> names,
// plain t.Errorf/t.Fatalf assertions, no assertion library. Reuses
// drivefake_test.go's newFakeDriveService/newDriveRecorder and
// syncengine_test.go's/folderwalk_test.go's/changepoll_test.go's own
// fixture helpers directly — same package, no duplication.
//
// This is Task 1's own slice: withRetry, traced end to end through its
// first call site, startPageToken. Task 2 extends this file with the
// remaining three call sites and the two standing scope gates.
package main

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/googleapis/gax-go/v2"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// withFastRetryBackoff substitutes retryBackoff (driveclient.go) with a
// millisecond-scale value for the duration of t, restoring the original via
// t.Cleanup — so a test proving retry BEHAVIOR (a recovery, a classification,
// a deadline expiry) never waits on the production 1s-30s backoff window.
// Tests using this helper must not run in parallel with each other:
// retryBackoff is a single, package-level variable, never per-call state.
// Initial/Max are tens-of-milliseconds, not single milliseconds: under
// `-race`, a local httptest round trip's own latency is occasionally
// comparable to a single-digit-millisecond sleep, which made the
// context-expiry-until-deadline test flaky (the deadline landing mid-request
// rather than mid-sleep, verified empirically this phase). Tens of
// milliseconds keeps every sleep comfortably longer than a `-race`-slowed
// round trip while still keeping the whole suite in a fraction of a second.
func withFastRetryBackoff(t *testing.T) {
	t.Helper()
	original := retryBackoff
	retryBackoff = gax.Backoff{Initial: 20 * time.Millisecond, Max: 50 * time.Millisecond, Multiplier: 2}
	t.Cleanup(func() { retryBackoff = original })
}

// flakyRateLimitHandler returns a structured 429 response, carrying Drive's
// documented rateLimitExceeded reason token, for the first `failures`
// requests it serves, then delegates to ok — the fixture RES-01's
// "recovers after N transient failures" tests are built on.
func flakyRateLimitHandler(failures int, ok http.HandlerFunc) http.HandlerFunc {
	var n int
	return func(w http.ResponseWriter, r *http.Request) {
		if n < failures {
			n++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"code":429,"errors":[{"reason":"rateLimitExceeded"}]}}`)
			return
		}
		ok(w, r)
	}
}

// statusOnlyHandler always answers with a structured googleapi-shaped error
// body at the given HTTP status and no populated Errors[].Reason — used for
// the non-retryable-status proofs (400, 401, 403, 404, 410) where request
// count must stay pinned at exactly one, and for the forever-failing 429
// case that must classify via the fallback Code == 429 branch alone.
func statusOnlyHandler(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		fmt.Fprintf(w, `{"error":{"code":%d,"message":"simulated failure"}}`, code)
	}
}

// --- Task 1: withRetry traced end to end through startPageToken ---

func TestStartPageToken_RecoversFromTwoTransient429sWithThreeTotalRequests(t *testing.T) {
	withFastRetryBackoff(t)
	recorder := newDriveRecorder(flakyRateLimitHandler(2, func(w http.ResponseWriter, r *http.Request) {
		writeDriveJSON(t, w, &drive.StartPageToken{StartPageToken: "recovered-token"})
	}))
	svc := newFakeDriveService(t, recorder.ServeHTTP)

	token, err := startPageToken(context.Background(), svc)
	if err != nil {
		t.Fatalf("startPageToken: %v", err)
	}
	if token != "recovered-token" {
		t.Errorf("token = %q, want %q", token, "recovered-token")
	}
	if got := recorder.count("/changes/startPageToken"); got != 3 {
		t.Errorf("request count = %d, want 3 (two 429s then success)", got)
	}
}

func TestStartPageToken_NonRetryableStatusesFailAfterExactlyOneRequest(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			recorder := newDriveRecorder(statusOnlyHandler(code))
			svc := newFakeDriveService(t, recorder.ServeHTTP)

			if _, err := startPageToken(context.Background(), svc); err == nil {
				t.Fatalf("startPageToken: got nil error, want a wrapped %d", code)
			}
			if got := recorder.count("/changes/startPageToken"); got != 1 {
				t.Errorf("request count = %d, want 1 (status %d must not be retried)", got, code)
			}
		})
	}
}

func TestStartPageToken_410FailsAfterExactlyOneRequestAndIsStillRecognisedAsStale(t *testing.T) {
	recorder := newDriveRecorder(statusOnlyHandler(http.StatusGone))
	svc := newFakeDriveService(t, recorder.ServeHTTP)

	_, err := startPageToken(context.Background(), svc)
	if err == nil {
		t.Fatal("startPageToken: got nil error, want a wrapped 410")
	}
	if !isStalePageToken(err) {
		t.Errorf("isStalePageToken(%v) = false, want true — the decorator must not disturb this classification", err)
	}
	if got := recorder.count("/changes/startPageToken"); got != 1 {
		t.Errorf("request count = %d, want 1 (410 is not in the retryable set)", got)
	}
}

func TestStartPageToken_Recovers5xxThenSucceeds(t *testing.T) {
	withFastRetryBackoff(t)
	for _, code := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var failed bool
			recorder := newDriveRecorder(func(w http.ResponseWriter, r *http.Request) {
				if !failed {
					failed = true
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(code)
					fmt.Fprintf(w, `{"error":{"code":%d,"message":"simulated transient failure"}}`, code)
					return
				}
				writeDriveJSON(t, w, &drive.StartPageToken{StartPageToken: "recovered"})
			})
			svc := newFakeDriveService(t, recorder.ServeHTTP)

			token, err := startPageToken(context.Background(), svc)
			if err != nil {
				t.Fatalf("startPageToken: %v", err)
			}
			if token != "recovered" {
				t.Errorf("token = %q, want %q", token, "recovered")
			}
			if got := recorder.count("/changes/startPageToken"); got != 2 {
				t.Errorf("request count = %d, want 2 (one failure then success)", got)
			}
		})
	}
}

// syncRoundTripperFunc adapts a plain function to http.RoundTripper, mirroring
// http.HandlerFunc's own adapter shape — used only by
// cancelContextAfterNRoundTrips below, where the wrapped function must run
// synchronously inside the client's own request path.
type syncRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f syncRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// cancelContextAfterNRoundTrips builds a fake Drive service around handler
// whose http.Client cancels the returned context SYNCHRONOUSLY, inside
// RoundTrip, immediately after handler has answered its Nth request — with
// NO race against gax-go/v2's own retry/sleep decision, because the
// cancellation happens in the same call stack as the just-completed
// response, strictly BEFORE control ever returns to gax's retry loop. A
// single caller only ever issues requests sequentially, so the plain int
// counter here needs no synchronization.
//
// This replaces an earlier version of the test below, which raced a
// wall-clock context.WithTimeout against gax's own sleep-interrupt from a
// separate goroutine and intermittently lost: when the deadline happened to
// land WHILE a request was in flight rather than during the backoff sleep
// between two requests, the local http.Client aborted the in-progress read
// with a bare context error, and withRetry had no already-completed 429
// response to join it with — surfacing a plain "context deadline exceeded"
// instead of the classifiable rate-limited error this test exists to
// prove withRetry preserves. A wider backoff window made that race rarer
// but never impossible; calling cancel() from inside the very RoundTrip
// gax is about to process removes it by construction, not by tuning a
// margin (syncengine_test.go's perCallCanceler documents this same
// reasoning for its own, two-caller version of this technique).
func cancelContextAfterNRoundTrips(t *testing.T, handler http.HandlerFunc, n int) (*drive.Service, context.Context) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	count := 0
	rt := syncRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		resp, err := http.DefaultTransport.RoundTrip(req)
		count++
		if count >= n {
			cancel()
		}
		return resp, err
	})
	svc, err := drive.NewService(context.Background(),
		option.WithHTTPClient(&http.Client{Transport: rt}),
		option.WithEndpoint(srv.URL),
	)
	if err != nil {
		t.Fatalf("drive.NewService: %v", err)
	}
	return svc, ctx
}

// TestStartPageToken_429UntilDeadlineClassifiesAsRateLimitedNotContextDeadline
// proves this decorator's most consequential correctness property: when a
// bounded context expires while withRetry is still retrying a sustained
// 429, the returned error still classifies as rate-limited through
// classifyDriveError — never merely a bare context deadline — because
// withRetry joins gax.Invoke's own ctx.Err() with the last observed call
// error (driveclient.go's own doc comment explains why gax.Invoke discards
// it otherwise). cancelContextAfterNRoundTrips cancels the context after 3
// real 429 responses have already been received and classified — placing
// the cancellation deterministically between two completed round trips, so
// classifyDriveError always has a genuine googleapi.Error to find.
func TestStartPageToken_429UntilDeadlineClassifiesAsRateLimitedNotContextDeadline(t *testing.T) {
	withFastRetryBackoff(t)
	recorder := newDriveRecorder(statusOnlyHandler(http.StatusTooManyRequests))
	svc, ctx := cancelContextAfterNRoundTrips(t, recorder.ServeHTTP, 3)

	_, err := startPageToken(ctx, svc)
	if err == nil {
		t.Fatal("startPageToken: got nil error, want a wrapped rate-limited failure")
	}
	state, ok := classifyDriveError(err)
	if !ok || state != stateRateLimited {
		t.Errorf("classifyDriveError(%v) = (%v, %v), want (stateRateLimited, true)", err, state, ok)
	}
	if got := recorder.count("/changes/startPageToken"); got < 2 {
		t.Errorf("request count = %d, want at least 2 (proves at least one retry actually happened before the deadline)", got)
	}
}

// --- Task 2: withRetry extended to the remaining three call sites ---

func TestRootFolderName_RecoversFromTwoTransient429sWithThreeTotalRequests(t *testing.T) {
	withFastRetryBackoff(t)
	const rootID = "root-retry"
	recorder := newDriveRecorder(flakyRateLimitHandler(2, func(w http.ResponseWriter, r *http.Request) {
		writeDriveJSON(t, w, &drive.File{Id: rootID, Name: "Team Docs", MimeType: folderMimeType})
	}))
	svc := newFakeDriveService(t, recorder.ServeHTTP)

	name, err := rootFolderName(context.Background(), svc, rootID)
	if err != nil {
		t.Fatalf("rootFolderName: %v", err)
	}
	if name != "Team Docs" {
		t.Errorf("name = %q, want %q", name, "Team Docs")
	}
	if got := recorder.count("/files/" + rootID); got != 3 {
		t.Errorf("request count = %d, want 3 (two 429s then success)", got)
	}
}

func TestRootFolderName_NonRetryableStatusFailsAfterExactlyOneRequest(t *testing.T) {
	const rootID = "root-fail"
	recorder := newDriveRecorder(statusOnlyHandler(http.StatusNotFound))
	svc := newFakeDriveService(t, recorder.ServeHTTP)

	if _, err := rootFolderName(context.Background(), svc, rootID); err == nil {
		t.Fatal("rootFolderName: got nil error, want a wrapped 404")
	}
	if got := recorder.count("/files/" + rootID); got != 1 {
		t.Errorf("request count = %d, want 1 (404 must not be retried)", got)
	}
}

func TestPollChanges_RecoversFromTwoTransient429sWithThreeTotalRequests(t *testing.T) {
	withFastRetryBackoff(t)
	recorder := newDriveRecorder(flakyRateLimitHandler(2, func(w http.ResponseWriter, r *http.Request) {
		writeDriveJSON(t, w, &drive.ChangeList{NewStartPageToken: "final-token"})
	}))
	svc := newFakeDriveService(t, recorder.ServeHTTP)

	changes, newToken, err := pollChanges(context.Background(), svc, "start")
	if err != nil {
		t.Fatalf("pollChanges: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("len(changes) = %d, want 0", len(changes))
	}
	if newToken != "final-token" {
		t.Errorf("newToken = %q, want %q", newToken, "final-token")
	}
	if got := recorder.count("/changes"); got != 3 {
		t.Errorf("request count = %d, want 3 (two 429s then success)", got)
	}
}

// TestPollChanges_410FailsAfterExactlyOneRequestAndIsStillRecognisedAsStale
// proves 410 stays outside withRetry's retryable set on pollChanges' own
// wrapped call site, mirroring TestStartPageToken_410Fails... above — this
// exact request-count assertion is what changepoll_test.go's own
// TestIsStalePageToken_410SatisfiesAnd500DoesNot subtest never checked.
func TestPollChanges_410FailsAfterExactlyOneRequestAndIsStillRecognisedAsStale(t *testing.T) {
	recorder := newDriveRecorder(statusOnlyHandler(http.StatusGone))
	svc := newFakeDriveService(t, recorder.ServeHTTP)

	_, _, err := pollChanges(context.Background(), svc, "stale")
	if err == nil {
		t.Fatal("pollChanges: got nil error, want a wrapped 410")
	}
	if !isStalePageToken(err) {
		t.Errorf("isStalePageToken(%v) = false, want true — the decorator must not disturb this classification", err)
	}
	if got := recorder.count("/changes"); got != 1 {
		t.Errorf("request count = %d, want 1 (410 is not in the retryable set)", got)
	}
}

// TestWalkFolder_RetriedSecondPageProducesIdenticalTreeToUnfailedRun proves
// T-05-07's idempotency guarantee end to end: a fixture whose second
// files.list page is rejected once with 429 must yield exactly the same
// node count and the same set of discovered child folders as the identical
// fixture served with no failures at all — a retried page must never
// double-enqueue a folder or double-count a file (RES-01, 05-RESEARCH.md
// Pitfall 5). Because withRetry wraps the WHOLE paged call, a failure on
// page two replays page one too, so the root folder level alone
// legitimately sees four files.list requests (page1, page2-429,
// page1-retry, page2-retry); the walk then continues on to query each of
// the two discovered (empty) child folders, one request apiece, for six
// total — the assertion below pins that shape explicitly so a future
// change to the retry scope (e.g. wrapping only the failing page, or a
// double-enqueue regression that queued a child folder twice) would be
// caught here rather than silently changing this plugin's own Drive
// traffic volume.
func TestWalkFolder_RetriedSecondPageProducesIdenticalTreeToUnfailedRun(t *testing.T) {
	withFastRetryBackoff(t)

	children := map[string][]walkFixtureFile{
		"root": {
			{id: "f1", name: "one.txt", mimeType: "text/plain", modifiedTime: "2026-08-17T00:00:00Z", webViewLink: "https://drive.google.com/file/d/f1/view"},
			{id: "f2", name: "two.txt", mimeType: "text/plain", modifiedTime: "2026-08-17T00:00:00Z", webViewLink: "https://drive.google.com/file/d/f2/view"},
			{id: "sub-a", name: "A", mimeType: folderMimeType},
			{id: "sub-b", name: "B", mimeType: folderMimeType},
		},
	}

	baselineHandler := newWalkFixtureHandler(t, children, 2)
	baselineSvc := newFakeDriveService(t, baselineHandler)
	baselineTree, err := walkFolder(context.Background(), baselineSvc, "root")
	if err != nil {
		t.Fatalf("baseline walkFolder: %v", err)
	}

	var secondPageFailures int
	flaky := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("pageToken") == "2" && secondPageFailures < 1 {
			secondPageFailures++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"code":429,"errors":[{"reason":"rateLimitExceeded"}]}}`)
			return
		}
		baselineHandler(w, r)
	}
	recorder := newDriveRecorder(flaky)
	flakySvc := newFakeDriveService(t, recorder.ServeHTTP)

	retriedTree, err := walkFolder(context.Background(), flakySvc, "root")
	if err != nil {
		t.Fatalf("retried walkFolder: %v", err)
	}

	if len(retriedTree) != len(baselineTree) {
		t.Fatalf("retried tree has %d nodes, baseline has %d", len(retriedTree), len(baselineTree))
	}
	baselineChildFolders := map[string]bool{}
	for id, node := range baselineTree {
		if node.MimeType == folderMimeType {
			baselineChildFolders[id] = true
		}
	}
	retriedChildFolders := map[string]bool{}
	for id, node := range retriedTree {
		if node.MimeType == folderMimeType {
			retriedChildFolders[id] = true
		}
	}
	if len(retriedChildFolders) != len(baselineChildFolders) {
		t.Fatalf("retried discovered %d child folders, baseline discovered %d", len(retriedChildFolders), len(baselineChildFolders))
	}
	for id := range baselineChildFolders {
		if !retriedChildFolders[id] {
			t.Errorf("retried tree missing child folder %q present in the baseline", id)
		}
	}
	if got := recorder.count("/files"); got != 6 {
		t.Errorf("files.list request count = %d, want 6 (root: page1, page2-429, page1-retry, page2-retry; plus one query apiece for the two discovered, empty child folders)", got)
	}
}

// functionsCallingIdent returns the name of every top-level function
// declared in a non-test .go file in this package whose body directly
// calls a function named ident via a bare identifier call (e.g.
// withRetry(...), never a selector such as pkg.withRetry(...), which this
// codebase never has reason to produce for its own package-local
// functions) — the shared walker both of this file's standing scope gates
// below build on. Reuses secrets_test.go's own nonTestGoFiles helper
// directly (same package) rather than a second, differently-scoped file
// lister.
func functionsCallingIdent(t *testing.T, ident string) map[string]bool {
	t.Helper()
	files := nonTestGoFiles(t)
	fset := token.NewFileSet()
	got := map[string]bool{}
	for _, name := range files {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", name, err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parser.ParseFile(%s): %v", name, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			found := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == ident {
					found = true
				}
				return true
			})
			if found {
				got[fn.Name.Name] = true
			}
		}
	}
	return got
}

// sortedKeys returns m's keys sorted, for a stable, readable test failure
// message regardless of map iteration order.
func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// mapsEqual reports whether a and b hold exactly the same set of keys.
func mapsEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// TestRetryDecorator_WrapsExactlyTheFourSyncCriticalCallSites is the
// standing source-level gate for RES-01's scope boundary (T-05-10): it
// walks every non-test .go file's AST, collecting the name of every
// function whose body contains a call to withRetry, and asserts that set
// equals exactly {startPageToken, rootFolderName, walkFolder, pollChanges}
// — no fewer (a missing sanctioned site) and no more (an unsanctioned new
// one). This assertion fails in BOTH directions by construction: adding a
// withRetry call to a fifth function makes the "no more" branch fail;
// removing one of the four sanctioned calls makes the "no fewer" branch
// fail. Deliberately excludes plugin.go's probeDriveReachable (Health's own
// live, uncached probe — contract/plugin-contract.md:806-808 requires a
// rate-limited dashboard poll to report the rate-limited sentence
// immediately, not block behind a backoff whose Max alone is 30 seconds)
// and preview.go's/workspaceexport.go's preview-attachment paths
// (exportPreview, rangePreview — CONT-03/CONT-04's silent degrade-to-empty
// design, not part of the sync-completeness guarantee RES-01/RES-02
// exist to protect). Uses the exact go/ast walker idiom secrets_test.go's
// nonTestGoFiles/TestSource_NeverInterpolatesACredentialIntoAPrintOrLogCall
// already establish — a mention of "withRetry" inside a comment can never
// satisfy or break this gate, only an actual ast.CallExpr can.
//
// Fail-first proof performed by hand this plan: a withRetry(ctx, func(ctx
// context.Context) error { return nil }) call was temporarily added inside
// plugin.go's probeDriveReachable, this test was re-run and observed to
// fail (want {startPageToken, rootFolderName, walkFolder, pollChanges}, got
// the same four PLUS probeDriveReachable), and the temporary call was then
// reverted before this commit.
func TestRetryDecorator_WrapsExactlyTheFourSyncCriticalCallSites(t *testing.T) {
	got := functionsCallingIdent(t, "withRetry")
	want := map[string]bool{
		"startPageToken": true,
		"rootFolderName": true,
		"walkFolder":     true,
		"pollChanges":    true,
	}
	if !mapsEqual(got, want) {
		t.Errorf("functions calling withRetry = %v, want %v", sortedKeys(got), sortedKeys(want))
	}
}

// sleepOrRandomCallSites collects, for the functions named in names, any
// ast.CallExpr within their body that calls time.Sleep or any function in a
// package named rand (math/rand or math/rand/v2) — the two call shapes that
// would mean this plugin computes some delay or jitter of its own, rather
// than relying exclusively on gax-go/v2's own Backoff.Pause
// (driveclient.go's retryBackoff doc comment). Returns one "file:line:
// function calls pkg.Selector" string per violation found, empty when none.
func sleepOrRandomCallSites(t *testing.T, names map[string]bool) []string {
	t.Helper()
	files := nonTestGoFiles(t)
	fset := token.NewFileSet()
	var violations []string
	for _, name := range files {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", name, err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parser.ParseFile(%s): %v", name, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !names[fn.Name.Name] {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkgIdent, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				if (pkgIdent.Name == "time" && sel.Sel.Name == "Sleep") || pkgIdent.Name == "rand" {
					pos := fset.Position(call.Pos())
					violations = append(violations, fmt.Sprintf("%s:%d: %s calls %s.%s", pos.Filename, pos.Line, fn.Name.Name, pkgIdent.Name, sel.Sel.Name))
				}
				return true
			})
		}
	}
	return violations
}

// TestRetryPath_ComputesNoDelayOfItsOwn is the standing AST gate proving
// driveclient.go's retryBackoff doc comment true: no function in the four
// sanctioned withRetry call sites, or withRetry itself, ever calls
// time.Sleep or a math/rand function — gax-go/v2's own Backoff.Pause (an
// already-verified full-jitter exponential implementation,
// call_option.go:184-208 of the pinned v2.23.0 source) is the single source
// of every retry delay this plugin ever waits.
func TestRetryPath_ComputesNoDelayOfItsOwn(t *testing.T) {
	names := map[string]bool{
		"withRetry":      true,
		"startPageToken": true,
		"rootFolderName": true,
		"walkFolder":     true,
		"pollChanges":    true,
	}
	if violations := sleepOrRandomCallSites(t, names); len(violations) > 0 {
		t.Errorf("retry path computes its own delay/jitter, want gax-go/v2's Backoff.Pause as the only source:\n%s", strings.Join(violations, "\n"))
	}
}
