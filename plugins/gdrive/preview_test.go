// Package main's preview_test.go hardens preview.go against every case
// Task 2's <behavior> block names: rune-safe truncation at every boundary
// shape, the text-shaped MIME allowlist (including a parameter-qualified
// MIME value), and the degrade-never-fail discipline under a Drive
// transport failure, an ignored Range header, and a per-item timeout.
// Follows this repository's own idiom: Test<Thing>_<BehaviorInPlainEnglish>
// names, plain t.Errorf/t.Fatalf assertions, no assertion library,
// table-driven where the repo's own convention already is.
package main

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// --- truncateRunes: rune-boundary safety ---

func TestTruncateRunes_LongerThanLimitYieldsExactlyLimitRunesAndValidUTF8(t *testing.T) {
	s := strings.Repeat("hello ", previewRuneLimit) // far more than previewRuneLimit runes
	got := truncateRunes(s, previewRuneLimit)
	if n := utf8.RuneCountInString(got); n != previewRuneLimit {
		t.Fatalf("RuneCountInString(got) = %d, want %d", n, previewRuneLimit)
	}
	if !utf8.ValidString(got) {
		t.Errorf("truncateRunes result is not valid UTF-8: %q", got)
	}
}

func TestTruncateRunes_MultiByteBoundaryNeverSplitsARune(t *testing.T) {
	// Each "café" contributes 4 runes (c, a, f, é) but é is a 2-byte rune,
	// so a byte-index truncation at an arbitrary length would risk cutting
	// é in half. Build a string whose rune count lands exactly on
	// previewRuneLimit+1 so the cut point falls immediately after a
	// multi-byte rune.
	word := "café" // 4 runes, 5 bytes
	var b strings.Builder
	for utf8.RuneCountInString(b.String()) < previewRuneLimit+4 {
		b.WriteString(word)
	}
	s := b.String()

	got := truncateRunes(s, previewRuneLimit)
	if !utf8.ValidString(got) {
		t.Fatalf("truncateRunes result is not valid UTF-8: %q", got)
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Errorf("truncateRunes result contains U+FFFD (replacement character), want none introduced by truncation: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != previewRuneLimit {
		t.Errorf("RuneCountInString(got) = %d, want %d", n, previewRuneLimit)
	}
}

func TestTruncateRunes_EmptyStringYieldsEmptyNoError(t *testing.T) {
	if got := truncateRunes("", previewRuneLimit); got != "" {
		t.Errorf("truncateRunes(\"\", limit) = %q, want empty", got)
	}
}

func TestTruncateRunes_ExactlyAtLimitReturnsWhole(t *testing.T) {
	s := strings.Repeat("x", previewRuneLimit)
	got := truncateRunes(s, previewRuneLimit)
	if got != s {
		t.Errorf("truncateRunes at exactly the limit = %q, want the input returned whole (%q)", got, s)
	}
	if n := utf8.RuneCountInString(got); n != previewRuneLimit {
		t.Errorf("RuneCountInString(got) = %d, want %d", n, previewRuneLimit)
	}
}

// --- isTextShaped: the GAP-15 allowlist, including MIME parameters ---

func TestIsTextShaped_AllowlistMembership(t *testing.T) {
	cases := []struct {
		name     string
		mimeType string
		want     bool
	}{
		{"plain text", "text/plain", true},
		{"text with charset parameter", "text/plain; charset=utf-8", true},
		{"text with charset parameter and no space", "text/plain;charset=utf-8", true},
		{"json", "application/json", true},
		{"pdf", "application/pdf", false},
		{"png", "image/png", false},
		{"workspace doc (not this allowlist's job)", "application/vnd.google-apps.document", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTextShaped(tc.mimeType); got != tc.want {
				t.Errorf("isTextShaped(%q) = %v, want %v", tc.mimeType, got, tc.want)
			}
		})
	}
}

// --- rangePreview: direct, non-Match-level exercises of the bounded fetch ---

// staticBodyHandler serves body for every request, setting Content-Type to
// mimeType — enough for rangePreview's own Download() call, which never
// inspects the URL path (rangePreview is exercised directly here, not
// through the full Match/tree-lookup path).
func staticBodyHandler(mimeType string, body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", mimeType)
		_, _ = w.Write(body)
	}
}

// countingHandler wraps handler, incrementing n (via a shared, mutex-free
// int accessed only from this single-goroutine-per-test caller) each time
// it is invoked — used to prove a non-text-shaped MIME type issues zero
// Drive calls.
type countingHandler struct {
	mu    sync.Mutex
	count int
}

func (c *countingHandler) wrap(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.count++
		c.mu.Unlock()
		handler(w, r)
	}
}

func (c *countingHandler) get() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

func TestRangePreview_NonTextShapedMimeTypeYieldsEmptyPreviewAndZeroDriveCalls(t *testing.T) {
	for _, mimeType := range []string{"application/pdf", "image/png"} {
		t.Run(mimeType, func(t *testing.T) {
			ch := &countingHandler{}
			svc := newFakeDriveService(t, ch.wrap(staticBodyHandler(mimeType, []byte("binary bytes irrelevant"))))
			got := rangePreview(context.Background(), svc, "file-x", mimeType)
			if got != "" {
				t.Errorf("rangePreview(%q) = %q, want empty", mimeType, got)
			}
			if n := ch.get(); n != 0 {
				t.Errorf("Drive call count for a non-text-shaped MIME type = %d, want 0 (the MIME check must refuse before any request)", n)
			}
		})
	}
}

func TestRangePreview_JSONMimeTypeYieldsANonEmptyPreview(t *testing.T) {
	body := []byte(`{"hello":"world","this":"is bounded preview fixture json content"}`)
	svc := newFakeDriveService(t, staticBodyHandler("application/json", body))
	got := rangePreview(context.Background(), svc, "file-json", "application/json")
	if got == "" {
		t.Fatal("rangePreview(application/json) = empty, want a non-empty preview")
	}
	if !strings.HasPrefix(string(body), got) {
		t.Errorf("preview %q is not a prefix of the fixture body %q", got, body)
	}
}

func TestRangePreview_ZeroByteBodyYieldsEmptyPreviewNoError(t *testing.T) {
	svc := newFakeDriveService(t, staticBodyHandler("text/plain", nil))
	got := rangePreview(context.Background(), svc, "file-empty", "text/plain")
	if got != "" {
		t.Errorf("rangePreview against a zero-byte body = %q, want empty", got)
	}
}

// captureLog swaps the standard log package's output to a buffer for the
// duration of the test, restoring it on cleanup — the standard library log
// package is a process-wide singleton, so this is safe only because this
// suite does not run this test in parallel with another that also
// redirects it.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })
	return &buf
}

func TestRangePreview_MediaDownloadFailureYieldsEmptyPreviewAndLogsFileID(t *testing.T) {
	svc := newFakeDriveService(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	logBuf := captureLog(t)

	const fileID = "file-fails-500"
	got := rangePreview(context.Background(), svc, fileID, "text/plain")
	if got != "" {
		t.Errorf("rangePreview against a failing download = %q, want empty", got)
	}
	if !strings.Contains(logBuf.String(), fileID) {
		t.Errorf("log output %q does not contain the failing file id %q", logBuf.String(), fileID)
	}
}

// TestRangePreview_HandlerIgnoringRangeHeaderStillCompletesWithABoundedPreview
// proves the io.LimitReader defense-in-depth cap: the fixture handler never
// honors the Range header and streams indefinitely (never returning EOF on
// its own), yet rangePreview still returns promptly with a correctly
// bounded preview — which is only possible if the read itself is capped at
// previewRangeBytes rather than waiting for the handler to finish, which it
// never does.
func TestRangePreview_HandlerIgnoringRangeHeaderStillCompletesWithABoundedPreview(t *testing.T) {
	chunk := bytes.Repeat([]byte("A"), 1024)
	svc := newFakeDriveService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		flusher, _ := w.(http.Flusher)
		for {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	})

	done := make(chan string, 1)
	go func() {
		done <- rangePreview(context.Background(), svc, "file-infinite", "text/plain")
	}()

	select {
	case got := <-done:
		want := strings.Repeat("A", previewRuneLimit)
		if got != want {
			t.Errorf("rangePreview against an infinite, Range-ignoring stream = %q, want %d copies of %q", got, previewRuneLimit, "A")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("rangePreview did not return within 10s against a handler that never stops writing — the io.LimitReader cap did not bound the read")
	}
}

// --- attachPreviews: the degrade-never-fail discipline at the per-item level ---

// perFileMediaHandler serves the four Drive REST endpoints attachPreviews-
// level tests need (only the file-id media path matters here — no
// files.list/changes traffic since attachPreviews is called directly
// against an already-built tree and item slice, not through Match).
// failFileID's request always returns HTTP 500 (when set); hangFileID's
// request always blocks until the request's own context is cancelled
// (when set); every other id in bodies is served bodies[id] with
// Content-Type mimeType.
func perFileMediaHandler(mimeType string, bodies map[string][]byte, failFileID, hangFileID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for id := range bodies {
			path := "/files/" + id
			if r.URL.Path != path {
				continue
			}
			switch id {
			case failFileID:
				w.WriteHeader(http.StatusInternalServerError)
			case hangFileID:
				<-r.Context().Done()
			default:
				w.Header().Set("Content-Type", mimeType)
				_, _ = w.Write(bodies[id])
			}
			return
		}
		http.NotFound(w, r)
	}
}

// buildOrderedItems constructs items (with only SourceId set — the field
// attachPreviews itself touches) for exactly ids, in ids' own order,
// mirroring matchItems' own SourceId-ascending output shape.
func buildOrderedItems(ids ...string) []*toposv1.Item {
	items := make([]*toposv1.Item, len(ids))
	for i, id := range ids {
		items[i] = &toposv1.Item{SourceId: id}
	}
	return items
}

func TestAttachPreviews_MediaDownloadFailureDegradesOnlyThatItemPreviewOrderingUnaffected(t *testing.T) {
	bodies := map[string][]byte{
		"file-a": []byte("alpha body content for the preview fixture"),
		"file-b": []byte("bravo body content for the preview fixture"),
		"file-c": []byte("charlie body content for the preview fixture"),
	}
	const failID = "file-b"
	svc := newFakeDriveService(t, perFileMediaHandler("text/plain", bodies, failID, ""))
	logBuf := captureLog(t)

	tree := map[string]*driveNode{
		"file-a": {Name: "a.txt", MimeType: "text/plain"},
		"file-b": {Name: "b.txt", MimeType: "text/plain"},
		"file-c": {Name: "c.txt", MimeType: "text/plain"},
	}
	items := buildOrderedItems("file-a", "file-b", "file-c")

	attachPreviews(context.Background(), svc, tree, items)

	wantOrder := []string{"file-a", "file-b", "file-c"}
	for i, id := range wantOrder {
		if items[i].GetSourceId() != id {
			t.Fatalf("index %d: SourceId = %q, want %q — preview outcome must never reorder items", i, items[i].GetSourceId(), id)
		}
	}
	if items[0].GetPreview() == "" {
		t.Error("file-a's Preview is empty, want non-empty (its download succeeded)")
	}
	if items[1].GetPreview() != "" {
		t.Errorf("file-b's Preview = %q, want empty (its download failed)", items[1].GetPreview())
	}
	if items[2].GetPreview() == "" {
		t.Error("file-c's Preview is empty, want non-empty (its download succeeded)")
	}
	if !strings.Contains(logBuf.String(), failID) {
		t.Errorf("log output %q does not contain the failing file id %q", logBuf.String(), failID)
	}
}

func TestAttachPreviews_HangingItemDegradesOnlyThatItemAndStillReturns(t *testing.T) {
	bodies := map[string][]byte{
		"file-a": []byte("alpha body content for the preview fixture"),
		"file-b": []byte("bravo body content for the preview fixture, the one that hangs"),
	}
	const hangID = "file-b"
	svc := newFakeDriveService(t, perFileMediaHandler("text/plain", bodies, "", hangID))

	tree := map[string]*driveNode{
		"file-a": {Name: "a.txt", MimeType: "text/plain"},
		"file-b": {Name: "b.txt", MimeType: "text/plain"},
	}
	items := buildOrderedItems("file-a", "file-b")

	done := make(chan struct{})
	start := time.Now()
	go func() {
		attachPreviews(context.Background(), svc, tree, items)
		close(done)
	}()

	select {
	case <-done:
		elapsed := time.Since(start)
		if elapsed > 30*time.Second {
			t.Errorf("attachPreviews took %s against one hanging item, want it bounded near previewFetchTimeout (%s)", elapsed, previewFetchTimeout)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("attachPreviews did not return within 30s — the per-item previewFetchTimeout did not bound the hanging fetch")
	}

	if items[0].GetPreview() == "" {
		t.Error("file-a's Preview is empty, want non-empty (its download succeeded and was never affected by file-b's hang)")
	}
	if items[1].GetPreview() != "" {
		t.Errorf("file-b's Preview = %q, want empty (its download hung past previewFetchTimeout)", items[1].GetPreview())
	}
}

func TestAttachPreviews_NodeAbsentFromTreeIsSkippedPreviewLeftUnset(t *testing.T) {
	svc := newFakeDriveService(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected Drive request for a node absent from tree: %s", r.URL.Path)
	})
	tree := map[string]*driveNode{} // empty: every item's node lookup misses
	items := buildOrderedItems("file-missing")

	attachPreviews(context.Background(), svc, tree, items)

	if items[0].GetPreview() != "" {
		t.Errorf("Preview = %q, want empty (unset) for a node absent from tree", items[0].GetPreview())
	}
}

// --- Match-level: two concurrent calls hold no shared mutable preview state ---

// TestMatch_TwoConcurrentCallsBuildPreviewsWithNoDataRace is Task 2's -race
// gate: two Match calls against the same plugin and fixture, issued
// concurrently, must both succeed and return the same item count, with no
// data race in attachPreviews/rangePreview (run under `go test -race`).
func TestMatch_TwoConcurrentCallsBuildPreviewsWithNoDataRace(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, dataFilePath(isolatedDir, tokenFileName))

	fx := driveFixture{rootID: "root-race", rootName: "Team Docs", fileID: "file-race", fileName: "note.txt"}
	fileBody := []byte("concurrent preview fetch race proof body text content, used by two simultaneous Match calls.")
	rec := &contentFixtureRecorder{}
	svc := newFakeDriveService(t, newContentFixtureHandler(t, fx, "text/plain", fileBody, rec))
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, fx.rootID), svc)

	req := buildMatchRequest(fx.rootName)

	const calls = 2
	var wg sync.WaitGroup
	results := make([]*toposv1.MatchResponse, calls)
	errs := make([]error, calls)
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = p.Match(context.Background(), req)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent Match call %d: %v", i, err)
		}
	}
	for i, resp := range results {
		if got := len(resp.GetItems()); got != 1 {
			t.Fatalf("concurrent Match call %d returned %d items, want 1", i, got)
		}
	}
}
