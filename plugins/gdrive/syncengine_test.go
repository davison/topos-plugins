// Package main's syncengine_test.go is the Task 2 end-to-end proof: one
// fixture folder holding one file, served by a fake Drive service, proven
// all the way through a real Match call — files.list walk,
// changes.getStartPageToken capture, atomic syncstate.json persistence,
// and a fully-populated returned Item — with zero real Google network
// access. Follows plugin_test.go's own idiom: Test<Thing>_<BehaviorInPlain
// English> names, plain t.Errorf/t.Fatalf assertions, no assertion
// library, t.TempDir()/injected-getenv isolation.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// driveFixture models one small fixture folder tree served by the fake
// Drive service: a root folder with a single child file. fileSize, when
// non-zero, is threaded into the fixture's files.list response as the
// file's own Size — fetchcontent_test.go's Task 3 tests use it to exercise
// fetchcontent.go's maxFetchBytes pre-flight cap; every existing caller
// that leaves it unset gets Drive's own zero value, unchanged behavior.
type driveFixture struct {
	rootID, rootName string
	fileID, fileName string
	fileSize         int64
}

// newSingleFileFixtureHandler serves the four Drive REST endpoints this
// plugin's first-run walk and later delta polls touch: GET
// /changes/startPageToken, GET /files/{rootID} (the root's own metadata,
// folderwalk.go's rootFolderName), GET /files (files.list, filtered by the
// query's parent-id clause), and GET /changes (changes.list — a second and
// any later sync's delta poll, plan 03-02). This fixture's /changes always
// reports zero changes: syncengine_test.go's own proof is about the
// first-run walk and persistence shape, not delta application, which
// changepoll_test.go covers directly.
func newSingleFileFixtureHandler(t *testing.T, fx driveFixture) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/changes/startPageToken":
			writeDriveJSON(t, w, &drive.StartPageToken{StartPageToken: "start-token-1"})
		case r.URL.Path == "/changes":
			writeDriveJSON(t, w, &drive.ChangeList{NewStartPageToken: "start-token-1"})
		case r.URL.Path == "/files/"+fx.rootID:
			writeDriveJSON(t, w, &drive.File{Id: fx.rootID, Name: fx.rootName, MimeType: folderMimeType})
		case r.URL.Path == "/files":
			parent := parentFromQuery(r.URL.Query().Get("q"))
			var files []*drive.File
			if parent == fx.rootID && fx.fileID != "" {
				files = []*drive.File{{
					Id:           fx.fileID,
					Name:         fx.fileName,
					MimeType:     "text/plain",
					ModifiedTime: "2026-08-17T00:00:00Z",
					WebViewLink:  "https://drive.google.com/file/d/" + fx.fileID + "/view",
				}}
			}
			writeDriveJSON(t, w, &drive.FileList{Files: files})
		default:
			http.NotFound(w, r)
		}
	}
}

// parentFromQuery extracts the parent id out of the exact query shape
// walkFolder builds: '<parentID>' in parents and trashed = false.
func parentFromQuery(q string) string {
	const prefix = "'"
	if !strings.HasPrefix(q, prefix) {
		return ""
	}
	rest := q[len(prefix):]
	end := strings.Index(rest, "'")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func writeDriveJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode drive fixture response: %v", err)
	}
}

// seedValidToken writes an already-valid (unexpired) token file at path
// so tokenSource resolves with no OAuth refresh network call — the fake
// Drive service above stands in for Drive itself, not for Google's OAuth
// token endpoint, which this end-to-end test never needs to reach.
func seedValidToken(t *testing.T, path string) {
	t.Helper()
	tok := &oauth2.Token{
		AccessToken:  "fixture-access-token",
		RefreshToken: "fixture-refresh-token",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour),
	}
	if err := saveToken(path, tok); err != nil {
		t.Fatalf("seedValidToken: saveToken: %v", err)
	}
}

// sourceConfigJSON builds the WEBSPACES_SOURCE_CONFIG payload this test's
// plugin reads its client credentials and folder_id from.
func sourceConfigJSON(t *testing.T, folderID string) string {
	t.Helper()
	cfg := map[string]any{
		"extras": map[string]string{
			"client_id":     "fixture-client-id",
			"client_secret": "fixture-client-secret",
			"folder_id":     folderID,
		},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal source config: %v", err)
	}
	return string(data)
}

// pluginWithFakeDrive builds a SourcePlugin whose driveService is
// short-circuited to svc — bypassing driveService's own real
// drive.NewService(option.WithTokenSource(...)) construction, which would
// otherwise dial the real Google endpoint — while every other RPC path
// (tokenSource, ensureSynced, Match) runs unmodified, real production
// code.
func pluginWithFakeDrive(t *testing.T, isolatedDir, cfgJSON string, svc *drive.Service) *SourcePlugin {
	t.Helper()
	getenv := staticGetenv(map[string]string{
		"HOME":             isolatedDir,
		"XDG_DATA_HOME":    isolatedDir,
		sourceConfigEnvVar: cfgJSON,
	})
	p := NewSourcePluginWithEnv(getenv)
	p.driveOnce.Do(func() {}) // pre-mark resolved; production driveService never runs
	p.svc = svc
	return p
}

// TestMatch_ReturnsOneFullyPopulatedItemForFixtureFolder is this plan's
// end-to-end proof: a Match call against a fake Drive folder holding one
// file returns exactly one fully-populated Item, syncstate.json exists at
// 0600 under a 0700 directory, and a second Match against that persisted
// state issues zero further files.list requests.
func TestMatch_ReturnsOneFullyPopulatedItemForFixtureFolder(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, filepath.Join(isolatedDir, dataDirName, tokenFileName))

	fx := driveFixture{rootID: "root-1", rootName: "Team Docs", fileID: "file-1", fileName: "q1.pdf"}
	recorder := newDriveRecorder(newSingleFileFixtureHandler(t, fx))
	svc := newFakeDriveService(t, recorder.ServeHTTP)

	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, fx.rootID), svc)

	req := &toposv1.MatchRequest{
		MatchFields: map[string]*toposv1.StringList{
			"folders": {Values: []string{fx.rootName}},
		},
	}

	resp, err := p.Match(context.Background(), req)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	items := resp.GetItems()
	if len(items) != 1 {
		t.Fatalf("len(Items) = %d, want 1", len(items))
	}
	it := items[0]
	if it.GetSourceId() != fx.fileID {
		t.Errorf("SourceId = %q, want %q", it.GetSourceId(), fx.fileID)
	}
	if it.GetDeepLink() == "" {
		t.Error("DeepLink is empty, want non-empty")
	}
	if it.GetFidelity() != toposv1.LinkFidelity_LINK_FIDELITY_EXACT {
		t.Errorf("Fidelity = %v, want LINK_FIDELITY_EXACT", it.GetFidelity())
	}
	if len(it.GetProvenance()) != 5 {
		t.Errorf("len(Provenance) = %d, want 5", len(it.GetProvenance()))
	}

	// syncstate.json exists at 0600 under a 0700 directory.
	statePath := filepath.Join(isolatedDir, dataDirName, syncStateFileName)
	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("Stat(%s): %v", statePath, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("syncstate.json mode = %v, want 0600", perm)
	}
	dirInfo, err := os.Stat(filepath.Dir(statePath))
	if err != nil {
		t.Fatalf("Stat(%s): %v", filepath.Dir(statePath), err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("data dir mode = %v, want 0700", perm)
	}

	// A second Match against the same persisted state issues zero further
	// files.list requests.
	if _, err := p.Match(context.Background(), req); err != nil {
		t.Fatalf("second Match: %v", err)
	}
	if got := recorder.count("/files"); got != 1 {
		t.Errorf("files.list call count after second Match = %d, want 1 (no further files.list traffic)", got)
	}
}

// TestMatch_NeverSendsASharedDriveParameter asserts that no request the
// fake Drive server saw during a first-run Match carried
// supportsAllDrives, includeItemsFromAllDrives, driveId, or corpora — v1
// is My-Drive-only, the recorded disposition of the deferred SYNC-V2-01.
func TestMatch_NeverSendsASharedDriveParameter(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, filepath.Join(isolatedDir, dataDirName, tokenFileName))

	fx := driveFixture{rootID: "root-2", rootName: "Team Docs", fileID: "file-2", fileName: "q2.pdf"}
	recorder := newDriveRecorder(newSingleFileFixtureHandler(t, fx))
	svc := newFakeDriveService(t, recorder.ServeHTTP)

	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, fx.rootID), svc)

	if _, err := p.Match(context.Background(), &toposv1.MatchRequest{}); err != nil {
		t.Fatalf("Match: %v", err)
	}

	for _, param := range []string{"supportsAllDrives", "includeItemsFromAllDrives", "driveId", "corpora"} {
		if recorder.sawQueryParam(param) {
			t.Errorf("a Drive request carried the Shared-Drive parameter %q", param)
		}
	}
}

// TestMatch_EmptyConfiguredFolderYieldsZeroItemsAndNilError proves the
// empty-folder truth: a configured folder containing no files yields an
// empty tree, a persisted non-empty start page token, and a Match
// response with zero items and a nil error.
func TestMatch_EmptyConfiguredFolderYieldsZeroItemsAndNilError(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, filepath.Join(isolatedDir, dataDirName, tokenFileName))

	fx := driveFixture{rootID: "root-3", rootName: "Empty Root"} // no fileID/fileName: no children
	recorder := newDriveRecorder(newSingleFileFixtureHandler(t, fx))
	svc := newFakeDriveService(t, recorder.ServeHTTP)

	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, fx.rootID), svc)

	resp, err := p.Match(context.Background(), &toposv1.MatchRequest{
		MatchFields: map[string]*toposv1.StringList{
			"folders": {Values: []string{fx.rootName}},
		},
	})
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(resp.GetItems()) != 0 {
		t.Errorf("len(Items) = %d, want 0", len(resp.GetItems()))
	}

	statePath := filepath.Join(isolatedDir, dataDirName, syncStateFileName)
	st, err := loadSyncState(statePath)
	if err != nil {
		t.Fatalf("loadSyncState: %v", err)
	}
	if st.ChangeToken == "" {
		t.Error("ChangeToken is empty, want a persisted non-empty start page token")
	}
}

// --- Task 3: syncRetryDeadline and the no-short-item-set proof (RES-02) ---

// TestMatch_ForeverRateLimitedReturnsExactSentenceWithNoItemsAndUnchangedState
// proves RES-02's central guarantee: a sync whose Drive endpoint returns
// 429 forever makes Match return codes.Unavailable carrying the verbatim
// rate-limited sentence, with no items (a non-nil error means no
// MatchResponse is ever returned — the two can never be confused), and
// leaves the previously persisted syncstate.json byte-identical to before
// the failed call. Uses retry_test.go's cancelContextAfterNRoundTrips
// rather than a wall-clock context.WithTimeout, for the same reason its own
// doc comment gives: a blind duration can land while a request is in
// flight rather than during withRetry's backoff sleep, surfacing a bare
// context error instead of the classifiable rate-limited one this test
// exists to prove.
func TestMatch_ForeverRateLimitedReturnsExactSentenceWithNoItemsAndUnchangedState(t *testing.T) {
	withFastRetryBackoff(t)
	isolatedDir := t.TempDir()
	seedValidToken(t, filepath.Join(isolatedDir, dataDirName, tokenFileName))

	const rootID = "root-forever-429"
	statePath := filepath.Join(isolatedDir, dataDirName, syncStateFileName)
	seed := &syncState{
		RootID:      rootID,
		RootName:    "Team Docs",
		ChangeToken: "token-before-rate-limit",
		Tree:        map[string]*driveNode{"file-1": {Name: "q1.pdf", ParentID: rootID}},
	}
	if err := saveSyncState(statePath, seed); err != nil {
		t.Fatalf("seed saveSyncState: %v", err)
	}
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("ReadFile before: %v", err)
	}

	svc, ctx := cancelContextAfterNRoundTrips(t, statusOnlyHandler(http.StatusTooManyRequests), 3)
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, rootID), svc)

	resp, err := p.Match(ctx, &toposv1.MatchRequest{})
	if err == nil {
		t.Fatalf("Match: got nil error and %d items, want the rate-limited sentence", len(resp.GetItems()))
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("Match error is not a gRPC status: %v", err)
	}
	if st.Code() != codes.Unavailable {
		t.Errorf("code = %v, want Unavailable", st.Code())
	}
	if st.Message() != sentenceRateLimited {
		t.Errorf("message = %q, want %q", st.Message(), sentenceRateLimited)
	}

	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("ReadFile after: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("syncstate.json changed after a forever-429 sync failure:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
}

// TestMatch_RecoveredAfterRetryReturnsCompleteItemSetIdenticalToNoFailureBaseline
// proves the recovery half of RES-02: a sync that recovers after two
// transient 429s on its start-page-token capture returns the complete item
// set, element-by-element identical in SourceId and order to the same
// fixture served with no failures at all — a retried, recovered sync must
// never be confused with a partial or reordered result.
func TestMatch_RecoveredAfterRetryReturnsCompleteItemSetIdenticalToNoFailureBaseline(t *testing.T) {
	withFastRetryBackoff(t)

	buildAndMatch := func(t *testing.T, rootID string, handler http.HandlerFunc) []string {
		t.Helper()
		isolatedDir := t.TempDir()
		seedValidToken(t, filepath.Join(isolatedDir, dataDirName, tokenFileName))
		svc := newFakeDriveService(t, handler)
		p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, rootID), svc)
		resp, err := p.Match(context.Background(), &toposv1.MatchRequest{})
		if err != nil {
			t.Fatalf("Match: %v", err)
		}
		ids := make([]string, len(resp.GetItems()))
		for i, it := range resp.GetItems() {
			ids[i] = it.GetSourceId()
		}
		return ids
	}

	fx := driveFixture{rootID: "root-recovered", rootName: "Team Docs", fileID: "file-1", fileName: "q1.pdf"}
	baseHandler := newSingleFileFixtureHandler(t, fx)
	baselineIDs := buildAndMatch(t, fx.rootID, baseHandler)

	flakyStartToken := flakyRateLimitHandler(2, func(w http.ResponseWriter, r *http.Request) {
		writeDriveJSON(t, w, &drive.StartPageToken{StartPageToken: "start-token-1"})
	})
	retriedHandler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/changes/startPageToken" {
			flakyStartToken(w, r)
			return
		}
		baseHandler(w, r)
	}
	retriedIDs := buildAndMatch(t, fx.rootID, retriedHandler)

	if len(retriedIDs) != len(baselineIDs) {
		t.Fatalf("retried Match returned %d items, baseline returned %d", len(retriedIDs), len(baselineIDs))
	}
	for i := range baselineIDs {
		if retriedIDs[i] != baselineIDs[i] {
			t.Errorf("item %d SourceId = %q, want %q (order must match the no-failure baseline)", i, retriedIDs[i], baselineIDs[i])
		}
	}
}

// TestMatch_404RootFolderReturnsFolderInaccessibleSentenceNotRateLimited
// proves the two Drive-classified sentences are never confused with each
// other: a 404 on the root folder's own files.get call (rootFolderName,
// folderwalk.go) makes Match return the folder-inaccessible sentence, not
// the rate-limited one.
func TestMatch_404RootFolderReturnsFolderInaccessibleSentenceNotRateLimited(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, filepath.Join(isolatedDir, dataDirName, tokenFileName))

	const rootID = "root-404"
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/changes/startPageToken":
			writeDriveJSON(t, w, &drive.StartPageToken{StartPageToken: "start-token-404"})
		case r.URL.Path == "/files/"+rootID:
			http.Error(w, `{"error":{"code":404,"message":"not found"}}`, http.StatusNotFound)
		default:
			http.NotFound(w, r)
		}
	}
	svc := newFakeDriveService(t, handler)
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, rootID), svc)

	_, err := p.Match(context.Background(), &toposv1.MatchRequest{})
	if err == nil {
		t.Fatal("Match: got nil error, want the folder-inaccessible sentence")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("Match error is not a gRPC status: %v", err)
	}
	if st.Code() != codes.Unavailable {
		t.Errorf("code = %v, want Unavailable", st.Code())
	}
	if st.Message() != sentenceFolderInaccessible {
		t.Errorf("message = %q, want %q", st.Message(), sentenceFolderInaccessible)
	}
}

// callTagKey tags a Match call's own context with a caller identity
// (perCallCanceler below reads it back out of req.Context()) so two
// concurrent calls sharing ONE *drive.Service/http.Client — the exact
// production shape ensureSynced's p.syncMu serializes (syncengine.go) —
// can still be distinguished and canceled independently.
type callTagKey struct{}

func withCallTag(ctx context.Context, tag string) context.Context {
	return context.WithValue(ctx, callTagKey{}, tag)
}

// perCallCanceler is an http.RoundTripper wrapper letting a test
// deterministically cancel ONE SPECIFIC caller's own context after that
// caller's Nth completed round trip, with NO race against gax-go/v2's own
// retry/sleep decision: the cancellation happens SYNCHRONOUSLY inside
// RoundTrip, in the same call stack as the just-completed response,
// strictly BEFORE control ever returns to gax's retry loop — so gax cannot
// possibly check ctx.Err() before this RoundTrip call has already returned
// its valid 429 response for withRetry's lastCallErr to capture. Earlier
// versions of this test instead raced a wall-clock context.WithTimeout, and
// then an asynchronous watcher goroutine polling in-flight state, against
// gax's own internal timing from a SEPARATE goroutine, and both
// intermittently still lost that race (retry_test.go's own doc comment on
// TestStartPageToken_429UntilDeadlineClassifiesAsRateLimitedNotContextDeadline
// names the underlying race: a deadline landing mid-request rather than
// mid-sleep surfaces a bare context error instead of the classifiable
// Drive error). Calling cancel() from inside the very RoundTrip gax is
// about to process removes the race by construction, not by tuning a
// margin.
type perCallCanceler struct {
	next http.RoundTripper

	mu      sync.Mutex
	counts  map[string]int
	limits  map[string]int
	cancels map[string]context.CancelFunc
}

func newPerCallCanceler(next http.RoundTripper) *perCallCanceler {
	return &perCallCanceler{
		next:    next,
		counts:  map[string]int{},
		limits:  map[string]int{},
		cancels: map[string]context.CancelFunc{},
	}
}

// cancelAfterNRoundTrips returns a context tagged tag: once that tag's
// caller has completed n round trips (observed via RoundTrip below),
// THIS context is canceled.
func (c *perCallCanceler) cancelAfterNRoundTrips(tag string, n int) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.limits[tag] = n
	c.cancels[tag] = cancel
	c.mu.Unlock()
	return withCallTag(ctx, tag)
}

func (c *perCallCanceler) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := c.next.RoundTrip(req)
	tag, _ := req.Context().Value(callTagKey{}).(string)
	c.mu.Lock()
	c.counts[tag]++
	n := c.counts[tag]
	limit, hasLimit := c.limits[tag]
	cancel := c.cancels[tag]
	c.mu.Unlock()
	if hasLimit && cancel != nil && n >= limit {
		cancel()
	}
	return resp, err
}

// newFakeDriveServiceWithPerCallCanceler builds a *drive.Service exactly
// like drivefake_test.go's own newFakeDriveService, except its
// http.Client's Transport is a *perCallCanceler.
func newFakeDriveServiceWithPerCallCanceler(t *testing.T, handler http.HandlerFunc) (*drive.Service, *perCallCanceler) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	pc := newPerCallCanceler(http.DefaultTransport)
	svc, err := drive.NewService(t.Context(),
		option.WithHTTPClient(&http.Client{Transport: pc}),
		option.WithEndpoint(srv.URL),
	)
	if err != nil {
		t.Fatalf("drive.NewService: %v", err)
	}
	return svc, pc
}

// TestMatch_ConcurrentCallsAgainstForeverRateLimitedEndpointBothReturnSentenceWithNoRace
// proves the "process never crashes" half of RES-02: two simultaneous Match
// calls against a forever-429 endpoint both return the rate-limited
// sentence and neither panics nor deadlocks, run under -race.
// ensureSynced serializes the two calls on p.syncMu (syncengine.go), so
// only one caller is ever actually issuing requests at a time — but because
// each call's own cancellation is tied to ITS OWN round-trip count (2 for
// the first, 3 for the second) via perCallCanceler's per-tag bookkeeping,
// this is independent of acquisition order or interleaving: whichever call
// wins the mutex makes exactly its own N requests, gets synchronously
// canceled between its Nth response and gax's next retry decision, and
// releases the mutex; the other call then does the same with its own
// budget.
func TestMatch_ConcurrentCallsAgainstForeverRateLimitedEndpointBothReturnSentenceWithNoRace(t *testing.T) {
	withFastRetryBackoff(t)
	isolatedDir := t.TempDir()
	seedValidToken(t, filepath.Join(isolatedDir, dataDirName, tokenFileName))

	const rootID = "root-concurrent-429"
	svc, pc := newFakeDriveServiceWithPerCallCanceler(t, statusOnlyHandler(http.StatusTooManyRequests))
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, rootID), svc)

	ctxA := pc.cancelAfterNRoundTrips("callA", 2)
	ctxB := pc.cancelAfterNRoundTrips("callB", 3)

	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := p.Match(ctxA, &toposv1.MatchRequest{})
		errs[0] = err
	}()
	go func() {
		defer wg.Done()
		_, err := p.Match(ctxB, &toposv1.MatchRequest{})
		errs[1] = err
	}()
	wg.Wait()

	for i, err := range errs {
		if err == nil {
			t.Fatalf("call %d: got nil error, want the rate-limited sentence", i)
		}
		st, ok := status.FromError(err)
		if !ok {
			t.Fatalf("call %d: error is not a gRPC status: %v", i, err)
		}
		if st.Code() != codes.Unavailable {
			t.Errorf("call %d: code = %v, want Unavailable", i, st.Code())
		}
		if st.Message() != sentenceRateLimited {
			t.Errorf("call %d: message = %q, want %q", i, st.Message(), sentenceRateLimited)
		}
	}
}
