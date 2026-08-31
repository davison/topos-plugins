// Package main's changepoll_test.go proves changepoll.go's behavior. Task 1
// covers pollChanges' hand-rolled pagination and isStalePageToken's error
// classification; Task 2 adds membership-resolution coverage
// (applyChange/applyChanges/reachesRoot) and the ensureSynced delta-path
// wiring; Task 3 adds the traffic-shape and concurrency/interruption
// gates. Follows plugin_test.go's own idiom: Test<Thing>_<BehaviorInPlain
// English> names, plain t.Errorf/t.Fatalf assertions, no assertion
// library. Reuses drivefake_test.go's newFakeDriveService/newDriveRecorder
// directly — same package, no duplication.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/api/drive/v3"
)

// changesPage is one page changesFixtureHandler serves for a given
// incoming pageToken query value — letting a test control the exact
// page-to-page token sequence pollChanges' loop walks.
type changesPage struct {
	changes           []*drive.Change
	nextPageToken     string
	newStartPageToken string
}

// newChangesFixtureHandler serves changes.list only, keyed by the
// request's own "pageToken" query parameter against pages. An unexpected
// token (one not present in pages) is a fixture-setup error, surfaced as a
// 400 rather than silently returning an empty page.
func newChangesFixtureHandler(t *testing.T, pages map[string]changesPage) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/changes" {
			http.NotFound(w, r)
			return
		}
		token := r.URL.Query().Get("pageToken")
		page, ok := pages[token]
		if !ok {
			http.Error(w, "unexpected page token: "+token, http.StatusBadRequest)
			return
		}
		writeDriveJSON(t, w, &drive.ChangeList{
			Changes:           page.changes,
			NextPageToken:     page.nextPageToken,
			NewStartPageToken: page.newStartPageToken,
		})
	}
}

// recordedQueryValues returns every value recorded for query parameter
// name across every request r has seen, in request order.
func recordedQueryValues(r *driveRecorder, name string) []string {
	var vals []string
	for _, q := range r.queries {
		vals = append(vals, q[name]...)
	}
	return vals
}

// TestPollChanges_SinglePageTerminatesImmediatelyAndReturnsNewStartPageToken
// proves the base case: one page, no NextPageToken, the response's own
// NewStartPageToken returned as newToken.
func TestPollChanges_SinglePageTerminatesImmediatelyAndReturnsNewStartPageToken(t *testing.T) {
	pages := map[string]changesPage{
		"start-token": {
			changes:           []*drive.Change{{FileId: "f1"}},
			newStartPageToken: "next-start-token",
		},
	}
	recorder := newDriveRecorder(newChangesFixtureHandler(t, pages))
	svc := newFakeDriveService(t, recorder.ServeHTTP)

	changes, newToken, err := pollChanges(context.Background(), svc, "start-token")
	if err != nil {
		t.Fatalf("pollChanges: %v", err)
	}
	if len(changes) != 1 || changes[0].FileId != "f1" {
		t.Errorf("changes = %+v, want one change with FileId f1", changes)
	}
	if newToken != "next-start-token" {
		t.Errorf("newToken = %q, want %q", newToken, "next-start-token")
	}
	if got := recorder.count("/changes"); got != 1 {
		t.Errorf("changes.list call count = %d, want 1", got)
	}
}

// TestPollChanges_ThreePagesDrainedInOrderWithFinalNewStartPageToken proves
// a multi-page response is fully drained, concatenated in page-then-index
// order, with only the FINAL page's NewStartPageToken returned.
func TestPollChanges_ThreePagesDrainedInOrderWithFinalNewStartPageToken(t *testing.T) {
	pages := map[string]changesPage{
		"start": {
			changes:       []*drive.Change{{FileId: "p1-a"}, {FileId: "p1-b"}},
			nextPageToken: "page-2",
		},
		"page-2": {
			changes:       []*drive.Change{{FileId: "p2-a"}},
			nextPageToken: "page-3",
		},
		"page-3": {
			changes:           []*drive.Change{{FileId: "p3-a"}, {FileId: "p3-b"}},
			newStartPageToken: "final-token",
		},
	}
	svc := newFakeDriveService(t, newChangesFixtureHandler(t, pages))

	changes, newToken, err := pollChanges(context.Background(), svc, "start")
	if err != nil {
		t.Fatalf("pollChanges: %v", err)
	}
	want := []string{"p1-a", "p1-b", "p2-a", "p3-a", "p3-b"}
	if len(changes) != len(want) {
		t.Fatalf("len(changes) = %d, want %d (%+v)", len(changes), len(want), changes)
	}
	for i, id := range want {
		if changes[i].FileId != id {
			t.Errorf("changes[%d].FileId = %q, want %q (page-then-index order)", i, changes[i].FileId, id)
		}
	}
	if newToken != "final-token" {
		t.Errorf("newToken = %q, want %q", newToken, "final-token")
	}
}

// TestPollChanges_ZeroChangesReturnsEmptySliceTokenAndNilError proves an
// empty page still returns the response's own token and a nil error —
// zero changes is a legitimate, successful outcome, not an error.
func TestPollChanges_ZeroChangesReturnsEmptySliceTokenAndNilError(t *testing.T) {
	pages := map[string]changesPage{
		"start": {newStartPageToken: "next"},
	}
	svc := newFakeDriveService(t, newChangesFixtureHandler(t, pages))

	changes, newToken, err := pollChanges(context.Background(), svc, "start")
	if err != nil {
		t.Fatalf("pollChanges: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("len(changes) = %d, want 0", len(changes))
	}
	if newToken != "next" {
		t.Errorf("newToken = %q, want %q", newToken, "next")
	}
}

// TestPollChanges_EmptyNewStartPageTokenReturnsEmptyNewToken proves a
// response whose NewStartPageToken is empty returns an empty newToken —
// never fabricating one — so the caller can decide to keep the previously
// persisted token instead.
func TestPollChanges_EmptyNewStartPageTokenReturnsEmptyNewToken(t *testing.T) {
	pages := map[string]changesPage{
		"start": {changes: []*drive.Change{{FileId: "f1"}}}, // no newStartPageToken set
	}
	svc := newFakeDriveService(t, newChangesFixtureHandler(t, pages))

	_, newToken, err := pollChanges(context.Background(), svc, "start")
	if err != nil {
		t.Fatalf("pollChanges: %v", err)
	}
	if newToken != "" {
		t.Errorf("newToken = %q, want empty", newToken)
	}
}

// TestPollChanges_MidDrainFailureReturnsErrorAndNoPartialChanges proves a
// failure on the SECOND page of a drain (after the first page already
// succeeded) returns a wrapped error and a nil change slice — never a
// partial batch presented as complete.
func TestPollChanges_MidDrainFailureReturnsErrorAndNoPartialChanges(t *testing.T) {
	callCount := 0
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/changes" {
			http.NotFound(w, r)
			return
		}
		callCount++
		if callCount == 1 {
			writeDriveJSON(t, w, &drive.ChangeList{
				Changes:       []*drive.Change{{FileId: "p1-a"}},
				NextPageToken: "page-2",
			})
			return
		}
		// 400, not 500: this test proves a mid-drain failure aborts the
		// whole drain with no partial batch, a property independent of
		// whether the failing status happens to be retryable. A
		// non-retryable status keeps callCount pinned at exactly 2
		// (05-02's withRetry now wraps this call per page — a retryable
		// status here would retry this second page indefinitely against
		// context.Background()'s unbounded deadline instead of failing
		// once).
		http.Error(w, "simulated mid-drain failure", http.StatusBadRequest)
	}
	svc := newFakeDriveService(t, handler)

	changes, newToken, err := pollChanges(context.Background(), svc, "start")
	if err == nil {
		t.Fatal("pollChanges: got nil error, want a wrapped mid-drain failure")
	}
	if changes != nil {
		t.Errorf("changes = %+v, want nil on a mid-drain failure", changes)
	}
	if newToken != "" {
		t.Errorf("newToken = %q, want empty on a mid-drain failure", newToken)
	}
	if callCount != 2 {
		t.Errorf("callCount = %d, want 2 (first page succeeded, second failed)", callCount)
	}
}

// TestIsStalePageToken_410SatisfiesAnd500DoesNot proves isStalePageToken
// distinguishes a 410 Gone response (satisfies) from every other failure
// (a 500, does not satisfy) — the one HTTP-status branch this plugin's
// sync path takes.
func TestIsStalePageToken_410SatisfiesAnd500DoesNot(t *testing.T) {
	t.Run("410Gone", func(t *testing.T) {
		handler := func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":{"code":410,"message":"stale token"}}`, http.StatusGone)
		}
		svc := newFakeDriveService(t, handler)
		_, _, err := pollChanges(context.Background(), svc, "stale")
		if err == nil {
			t.Fatal("pollChanges: got nil error against a 410 response")
		}
		if !isStalePageToken(err) {
			t.Errorf("isStalePageToken(%v) = false, want true for a 410 Gone error", err)
		}
	})
	t.Run("500InternalServerError", func(t *testing.T) {
		// 500 is one of withRetry's four retryable statuses (05-02) — this
		// endpoint fails every request, so without a bounded context and a
		// fast backoff this subtest would retry against
		// context.Background()'s unbounded deadline forever. A short
		// deadline paired with withFastRetryBackoff keeps the retry
		// behavior genuinely exercised (several real retries happen before
		// the deadline cuts it short) while keeping the subtest itself in
		// the milliseconds.
		withFastRetryBackoff(t)
		handler := func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":{"code":500,"message":"internal"}}`, http.StatusInternalServerError)
		}
		svc := newFakeDriveService(t, handler)
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		_, _, err := pollChanges(ctx, svc, "start")
		if err == nil {
			t.Fatal("pollChanges: got nil error against a 500 response")
		}
		if isStalePageToken(err) {
			t.Errorf("isStalePageToken(%v) = true, want false for a 500 error", err)
		}
	})
}

// TestPollChanges_EveryRequestCarriesIncludeRemovedAndPageSizeNoSharedDriveParams
// asserts the recorded query parameters on every changes.list request
// across a multi-page drain carry includeRemoved=true and pageSize=1000,
// and carry none of supportsAllDrives, includeItemsFromAllDrives, driveId,
// or corpora — v1 is My Drive only, the recorded disposition of the
// deferred SYNC-V2-01.
func TestPollChanges_EveryRequestCarriesIncludeRemovedAndPageSizeNoSharedDriveParams(t *testing.T) {
	pages := map[string]changesPage{
		"start": {
			changes:       []*drive.Change{{FileId: "p1-a"}},
			nextPageToken: "page-2",
		},
		"page-2": {
			changes:           []*drive.Change{{FileId: "p2-a"}},
			newStartPageToken: "final",
		},
	}
	recorder := newDriveRecorder(newChangesFixtureHandler(t, pages))
	svc := newFakeDriveService(t, recorder.ServeHTTP)

	if _, _, err := pollChanges(context.Background(), svc, "start"); err != nil {
		t.Fatalf("pollChanges: %v", err)
	}
	if got := recorder.count("/changes"); got != 2 {
		t.Fatalf("changes.list call count = %d, want 2", got)
	}

	for _, want := range []string{"true"} {
		found := false
		for _, v := range recordedQueryValues(recorder, "includeRemoved") {
			if v == want {
				found = true
			}
		}
		if !found {
			t.Errorf("no changes.list request carried includeRemoved=%s", want)
		}
	}
	found := false
	for _, v := range recordedQueryValues(recorder, "pageSize") {
		if v == "1000" {
			found = true
		}
	}
	if !found {
		t.Error("no changes.list request carried pageSize=1000")
	}

	for _, param := range []string{"supportsAllDrives", "includeItemsFromAllDrives", "driveId", "corpora"} {
		if recorder.sawQueryParam(param) {
			t.Errorf("a changes.list request carried the Shared-Drive parameter %q", param)
		}
	}
}

// TestIsStalePageToken_NonGoogleapiErrorIsNeverStale is a defensive guard:
// an error that is not a *googleapi.Error at all (e.g. a plain transport
// error, or nil) must never satisfy isStalePageToken.
func TestIsStalePageToken_NonGoogleapiErrorIsNeverStale(t *testing.T) {
	if isStalePageToken(nil) {
		t.Error("isStalePageToken(nil) = true, want false")
	}
	if isStalePageToken(plainError("unrelated failure")) {
		t.Error("isStalePageToken(a plain, non-googleapi error) = true, want false")
	}
}

type plainError string

func (e plainError) Error() string { return string(e) }

// --- Task 2: membership resolution (reachesRoot/applyChange/applyChanges) ---

func TestReachesRoot_ParentIsRootReturnsTrue(t *testing.T) {
	if !reachesRoot(map[string]*driveNode{}, "root", "root") {
		t.Error("reachesRoot(parentID == rootID) = false, want true")
	}
}

func TestReachesRoot_UnknownAncestorReturnsFalse(t *testing.T) {
	if reachesRoot(map[string]*driveNode{}, "unknown-parent", "root") {
		t.Error("reachesRoot with an ancestor absent from tree = true, want false (default-deny)")
	}
}

func TestReachesRoot_EmptyParentIDReturnsFalse(t *testing.T) {
	if reachesRoot(map[string]*driveNode{}, "", "root") {
		t.Error(`reachesRoot("") = true, want false`)
	}
}

func TestReachesRoot_MultiLevelChainReachesRoot(t *testing.T) {
	tree := map[string]*driveNode{
		"sub-a": {ParentID: "root"},
		"sub-b": {ParentID: "sub-a"},
	}
	if !reachesRoot(tree, "sub-b", "root") {
		t.Error("reachesRoot(sub-b -> sub-a -> root) = false, want true")
	}
}

// TestReachesRoot_CyclicChainTerminatesAndReturnsFalse proves T-03-09's
// mitigation: a self-referential or cyclic parent chain (never occurs
// against real Drive data, but a malformed or hostile persisted tree could
// contain one) must terminate rather than loop forever, and report false
// (it never reaches root).
func TestReachesRoot_CyclicChainTerminatesAndReturnsFalse(t *testing.T) {
	tree := map[string]*driveNode{
		"a": {ParentID: "b"},
		"b": {ParentID: "a"}, // cycle: never reaches "root"
	}
	if reachesRoot(tree, "a", "root") {
		t.Error("reachesRoot over a cyclic chain = true, want false")
	}
}

func TestReachesRoot_SelfParentedEntryTerminatesAndReturnsFalse(t *testing.T) {
	tree := map[string]*driveNode{
		"self": {ParentID: "self"},
	}
	if reachesRoot(tree, "self", "root") {
		t.Error("reachesRoot over a self-parented entry = true, want false")
	}
}

func TestApplyChange_RemovedDeletesEntry(t *testing.T) {
	tree := map[string]*driveNode{"f1": {Name: "old", ParentID: "root"}}
	if resolved := applyChange(tree, "root", &drive.Change{FileId: "f1", Removed: true}); !resolved {
		t.Error("resolved = false, want true")
	}
	if _, ok := tree["f1"]; ok {
		t.Error("f1 still present in tree after a Removed change")
	}
}

func TestApplyChange_TrashedFileDeletesEntry(t *testing.T) {
	tree := map[string]*driveNode{"f1": {Name: "old", ParentID: "root"}}
	ch := &drive.Change{FileId: "f1", File: &drive.File{Trashed: true, Parents: []string{"root"}}}
	if resolved := applyChange(tree, "root", ch); !resolved {
		t.Error("resolved = false, want true")
	}
	if _, ok := tree["f1"]; ok {
		t.Error("f1 still present in tree after a Trashed change")
	}
}

func TestApplyChange_NilFileDeletesEntry(t *testing.T) {
	tree := map[string]*driveNode{"f1": {Name: "old", ParentID: "root"}}
	if resolved := applyChange(tree, "root", &drive.Change{FileId: "f1", File: nil}); !resolved {
		t.Error("resolved = false, want true")
	}
	if _, ok := tree["f1"]; ok {
		t.Error("f1 still present in tree after a nil-File change")
	}
}

func TestApplyChange_EmptyParentsSliceDeletesEntryAndDoesNotPanic(t *testing.T) {
	tree := map[string]*driveNode{"f1": {Name: "old", ParentID: "root"}}
	ch := &drive.Change{FileId: "f1", File: &drive.File{Name: "f1.txt", Parents: nil}}
	if resolved := applyChange(tree, "root", ch); !resolved {
		t.Error("resolved = false, want true")
	}
	if _, ok := tree["f1"]; ok {
		t.Error("f1 still present in tree after an empty-Parents change")
	}
}

func TestApplyChange_DirectChildOfRootUpsertsNode(t *testing.T) {
	tree := map[string]*driveNode{}
	ch := &drive.Change{FileId: "f1", File: &drive.File{
		Name: "new.txt", MimeType: "text/plain", Parents: []string{"root"},
		ModifiedTime: "2026-08-17T00:00:00Z", WebViewLink: "https://drive.google.com/file/d/f1/view",
	}}
	if resolved := applyChange(tree, "root", ch); !resolved {
		t.Fatal("resolved = false, want true")
	}
	node, ok := tree["f1"]
	if !ok {
		t.Fatal("f1 not present in tree after a resolvable change")
	}
	if node.Name != "new.txt" || node.ParentID != "root" {
		t.Errorf("node = %+v, want Name=new.txt ParentID=root", node)
	}
}

func TestApplyChange_ParentUnknownToTreeIsDeferredNotResolved(t *testing.T) {
	tree := map[string]*driveNode{}
	ch := &drive.Change{FileId: "f1", File: &drive.File{Name: "child.txt", Parents: []string{"not-yet-seen"}}}
	if resolved := applyChange(tree, "root", ch); resolved {
		t.Error("resolved = true, want false (parent unknown to tree must defer to applyChanges' fixpoint loop)")
	}
	if _, ok := tree["f1"]; ok {
		t.Error("f1 present in tree despite being deferred, not resolved")
	}
}

func TestApplyChange_ParentKnownButOutOfScopeDeletesEntry(t *testing.T) {
	tree := map[string]*driveNode{
		"other-root": {Name: "Other", ParentID: ""}, // known, but its own chain terminates before reaching "root"
		"f1":         {Name: "old", ParentID: "root"},
	}
	ch := &drive.Change{FileId: "f1", File: &drive.File{Name: "moved.txt", Parents: []string{"other-root"}}}
	if resolved := applyChange(tree, "root", ch); !resolved {
		t.Error("resolved = false, want true (parent known, chain definitively doesn't reach root)")
	}
	if _, ok := tree["f1"]; ok {
		t.Error("f1 still present after moving to a known-but-out-of-scope parent")
	}
}

// TestApplyChanges_ChildArrivingBeforeItsNewParentResolvesByFixpoint proves
// 03-RESEARCH.md Pitfall 3: a batch where a child's change entry precedes
// its own newly created parent folder's entry still resolves both in ONE
// sync via applyChanges' fixpoint re-resolution, never deferred to the
// next sync.
func TestApplyChanges_ChildArrivingBeforeItsNewParentResolvesByFixpoint(t *testing.T) {
	tree := map[string]*driveNode{}
	changes := []*drive.Change{
		{FileId: "child", File: &drive.File{Name: "child.txt", MimeType: "text/plain", Parents: []string{"new-folder"}}},
		{FileId: "new-folder", File: &drive.File{Name: "New Folder", MimeType: folderMimeType, Parents: []string{"root"}}},
	}
	applyChanges(tree, "root", changes)

	if _, ok := tree["new-folder"]; !ok {
		t.Error("new-folder missing from tree after fixpoint resolution")
	}
	child, ok := tree["child"]
	if !ok {
		t.Fatal("child missing from tree — should resolve in the SAME sync via fixpoint, not deferred")
	}
	if child.ParentID != "new-folder" {
		t.Errorf("child.ParentID = %q, want %q", child.ParentID, "new-folder")
	}
}

func TestApplyChanges_UnresolvableParentChainIsExcludedNotIncluded(t *testing.T) {
	tree := map[string]*driveNode{}
	changes := []*drive.Change{
		{FileId: "orphan", File: &drive.File{Name: "orphan.txt", Parents: []string{"never-seen-parent"}}},
	}
	applyChanges(tree, "root", changes)
	if _, ok := tree["orphan"]; ok {
		t.Error("orphan present in tree despite an unresolvable parent chain — default-deny requires exclusion")
	}
}

func TestApplyChanges_UnrelatedChangeElsewhereInDriveAddsNothing(t *testing.T) {
	tree := map[string]*driveNode{"existing": {Name: "keep.txt", ParentID: "root"}}
	changes := []*drive.Change{
		{FileId: "elsewhere", File: &drive.File{Name: "not-ours.txt", Parents: []string{"some-other-drive-folder"}}},
	}
	applyChanges(tree, "root", changes)
	if _, ok := tree["elsewhere"]; ok {
		t.Error("a change to a file elsewhere in the operator's Drive added an item")
	}
	if len(tree) != 1 {
		t.Errorf("len(tree) = %d, want 1 (only the pre-existing entry)", len(tree))
	}
}

// TestApplyChanges_AppliesStrictlyInOrderAcrossDuplicateFileIDs proves that
// when one file id appears twice in a single drained batch, the LAST entry
// in Drive's own feed order determines its final state.
func TestApplyChanges_AppliesStrictlyInOrderAcrossDuplicateFileIDs(t *testing.T) {
	tree := map[string]*driveNode{}
	changes := []*drive.Change{
		{FileId: "f1", File: &drive.File{Name: "first-name.txt", Parents: []string{"root"}}},
		{FileId: "f1", File: &drive.File{Name: "final-name.txt", Parents: []string{"root"}}},
	}
	applyChanges(tree, "root", changes)
	node, ok := tree["f1"]
	if !ok {
		t.Fatal("f1 missing from tree")
	}
	if node.Name != "final-name.txt" {
		t.Errorf("Name = %q, want %q (last entry in feed order wins)", node.Name, "final-name.txt")
	}
}

// TestApplyChanges_SubfolderMoveUpdatesOnlyThatFoldersOwnEntry proves
// 03-RESEARCH.md Pitfall 6: renaming/moving a subfolder updates only that
// folder's own tree entry — a descendant's ParentID is left untouched,
// because its resolved path is derived fresh at Match time, not
// propagated here.
func TestApplyChanges_SubfolderMoveUpdatesOnlyThatFoldersOwnEntry(t *testing.T) {
	tree := map[string]*driveNode{
		"sub":    {Name: "Sub", MimeType: folderMimeType, ParentID: "root"},
		"nested": {Name: "nested.txt", ParentID: "sub"},
	}
	changes := []*drive.Change{
		{FileId: "sub", File: &drive.File{Name: "Sub Renamed", MimeType: folderMimeType, Parents: []string{"root"}}},
	}
	applyChanges(tree, "root", changes)

	if tree["sub"].Name != "Sub Renamed" {
		t.Errorf("sub.Name = %q, want %q", tree["sub"].Name, "Sub Renamed")
	}
	nested, ok := tree["nested"]
	if !ok {
		t.Fatal("nested missing from tree after an unrelated subfolder rename")
	}
	if nested.ParentID != "sub" {
		t.Errorf("nested.ParentID = %q, want %q (untouched — no descendant propagation needed)", nested.ParentID, "sub")
	}
}

// TestApplyChanges_DeletingAnIntermediateFolderCascadesToItsResidentDescendants
// is the deletion mirror of the SubfolderMove test above: Drive emits a
// change ONLY for the trashed folder itself, never for its resident
// descendants, so applyChanges must cascade the removal — otherwise the
// descendant sits orphaned in the tree forever (CR-01 / T-03-20).
func TestApplyChanges_DeletingAnIntermediateFolderCascadesToItsResidentDescendants(t *testing.T) {
	tree := map[string]*driveNode{
		"sub":    {Name: "Sub", MimeType: folderMimeType, ParentID: "root"},
		"nested": {Name: "nested.txt", ParentID: "sub"},
	}
	changes := []*drive.Change{
		{FileId: "sub", File: &drive.File{Trashed: true}},
	}
	applyChanges(tree, "root", changes)

	if _, ok := tree["sub"]; ok {
		t.Error("sub still present in tree after being trashed")
	}
	if _, ok := tree["nested"]; ok {
		t.Error("nested still present in tree after its containing folder was trashed — cascade did not run")
	}
}

// TestApplyChanges_CascadeReachesADeeplyNestedDescendantChain proves the
// cascade is transitive, not one level deep: trashing the topmost folder
// of a three-level chain removes every entry beneath it in the same sync.
func TestApplyChanges_CascadeReachesADeeplyNestedDescendantChain(t *testing.T) {
	tree := map[string]*driveNode{
		"a": {Name: "A", MimeType: folderMimeType, ParentID: "root"},
		"b": {Name: "B", MimeType: folderMimeType, ParentID: "a"},
		"c": {Name: "c.txt", ParentID: "b"},
	}
	changes := []*drive.Change{
		{FileId: "a", File: &drive.File{Trashed: true}},
	}
	applyChanges(tree, "root", changes)

	for _, id := range []string{"a", "b", "c"} {
		if _, ok := tree[id]; ok {
			t.Errorf("%s still present in tree after the chain's topmost folder was trashed", id)
		}
	}
}

// TestApplyChanges_PruneLeavesEveryStillReachableEntryUntouched is the
// over-broad-prune guard: deleting one sibling subtree must leave every
// entry of the other subtree present with Name and ParentID unchanged.
func TestApplyChanges_PruneLeavesEveryStillReachableEntryUntouched(t *testing.T) {
	tree := map[string]*driveNode{
		"doomed":       {Name: "Doomed", MimeType: folderMimeType, ParentID: "root"},
		"doomed-child": {Name: "doomed.txt", ParentID: "doomed"},
		"kept":         {Name: "Kept", MimeType: folderMimeType, ParentID: "root"},
		"kept-child":   {Name: "kept.txt", ParentID: "kept"},
	}
	changes := []*drive.Change{
		{FileId: "doomed", File: &drive.File{Trashed: true}},
	}
	applyChanges(tree, "root", changes)

	if _, ok := tree["doomed"]; ok {
		t.Error("doomed still present after being trashed")
	}
	if _, ok := tree["doomed-child"]; ok {
		t.Error("doomed-child still present after its containing folder was trashed")
	}
	kept, ok := tree["kept"]
	if !ok {
		t.Fatal("kept missing — prune removed a still-reachable sibling subtree")
	}
	if kept.Name != "Kept" || kept.ParentID != "root" {
		t.Errorf("kept = %+v, want Name=Kept ParentID=root (untouched)", kept)
	}
	keptChild, ok := tree["kept-child"]
	if !ok {
		t.Fatal("kept-child missing — prune removed a still-reachable descendant")
	}
	if keptChild.Name != "kept.txt" || keptChild.ParentID != "kept" {
		t.Errorf("kept-child = %+v, want Name=kept.txt ParentID=kept (untouched)", keptChild)
	}
}

// --- Task 2: ensureSynced's delta path, against the fake Drive service ---

// newDeltaOnlyHandler serves only GET /changes with resp — used against a
// plugin whose syncstate.json is pre-seeded directly (bypassing a real
// first-run walk via saveSyncState), so these tests exercise ONLY
// resolveSyncState's persisted-state delta-poll branch.
func newDeltaOnlyHandler(t *testing.T, resp *drive.ChangeList) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/changes" {
			http.NotFound(w, r)
			return
		}
		writeDriveJSON(t, w, resp)
	}
}

// TestEnsureSynced_FileMovedOutOfConfiguredSubtreeDisappearsFromTree
// proves T-03-08's move-out mitigation end to end through ensureSynced: a
// change reporting the file's parent as no longer within the tree removes
// it from the persisted, returned tree.
func TestEnsureSynced_FileMovedOutOfConfiguredSubtreeDisappearsFromTree(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, filepath.Join(isolatedDir, dataDirName, tokenFileName))

	const rootID = "root-move-out"
	statePath := filepath.Join(isolatedDir, dataDirName, syncStateFileName)
	seed := &syncState{
		RootID:      rootID,
		RootName:    "Team Docs",
		ChangeToken: "token-1",
		Tree: map[string]*driveNode{
			"file-1": {Name: "q1.pdf", MimeType: "application/pdf", ParentID: rootID},
		},
	}
	if err := saveSyncState(statePath, seed); err != nil {
		t.Fatalf("seed saveSyncState: %v", err)
	}

	resp := &drive.ChangeList{
		Changes: []*drive.Change{
			{FileId: "file-1", File: &drive.File{Name: "q1.pdf", MimeType: "application/pdf", Parents: []string{"outside-root"}}},
		},
		NewStartPageToken: "token-2",
	}
	svc := newFakeDriveService(t, newDeltaOnlyHandler(t, resp))
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, rootID), svc)

	st, err := p.ensureSynced(context.Background(), rootID)
	if err != nil {
		t.Fatalf("ensureSynced: %v", err)
	}
	if _, ok := st.Tree["file-1"]; ok {
		t.Error("file-1 still present in tree after moving out of the configured subtree")
	}
	if st.ChangeToken != "token-2" {
		t.Errorf("ChangeToken = %q, want %q", st.ChangeToken, "token-2")
	}
}

// TestEnsureSynced_OrphanedDescendantsDoNotAccumulateInSyncStateJSON is
// the storage-hygiene gate for T-03-20: after a sync whose polled batch
// trashes an intermediate folder, the state read back OFF DISK contains
// neither the folder nor its resident child — the orphan never reaches
// syncstate.json, it is not merely filtered at read time.
func TestEnsureSynced_OrphanedDescendantsDoNotAccumulateInSyncStateJSON(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, filepath.Join(isolatedDir, dataDirName, tokenFileName))

	const rootID = "root-orphan-hygiene"
	statePath := filepath.Join(isolatedDir, dataDirName, syncStateFileName)
	seed := &syncState{
		RootID:      rootID,
		RootName:    "Team Docs",
		ChangeToken: "token-1",
		Tree: map[string]*driveNode{
			"sub":    {Name: "Sub", MimeType: folderMimeType, ParentID: rootID},
			"nested": {Name: "nested.pdf", MimeType: "application/pdf", ParentID: "sub"},
		},
	}
	if err := saveSyncState(statePath, seed); err != nil {
		t.Fatalf("seed saveSyncState: %v", err)
	}

	resp := &drive.ChangeList{
		Changes: []*drive.Change{
			{FileId: "sub", File: &drive.File{Trashed: true}},
		},
		NewStartPageToken: "token-2",
	}
	svc := newFakeDriveService(t, newDeltaOnlyHandler(t, resp))
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, rootID), svc)

	if _, err := p.ensureSynced(context.Background(), rootID); err != nil {
		t.Fatalf("ensureSynced: %v", err)
	}

	persisted, err := loadSyncState(statePath)
	if err != nil {
		t.Fatalf("loadSyncState after sync: %v", err)
	}
	if _, ok := persisted.Tree["sub"]; ok {
		t.Error("sub still present in the persisted syncstate.json after being trashed")
	}
	if _, ok := persisted.Tree["nested"]; ok {
		t.Error("nested still present in the persisted syncstate.json — the orphan reached disk")
	}
}

// TestEnsureSynced_NonStalePollErrorLeavesSyncStateByteIdentical proves
// T-03-10's mitigation: a poll failure that is NOT a stale (410) page
// token returns the error and leaves the previously persisted
// syncstate.json completely untouched — the token must never advance past
// a poll that errored partway.
func TestEnsureSynced_NonStalePollErrorLeavesSyncStateByteIdentical(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, filepath.Join(isolatedDir, dataDirName, tokenFileName))

	const rootID = "root-poll-fail"
	statePath := filepath.Join(isolatedDir, dataDirName, syncStateFileName)
	seed := &syncState{
		RootID:      rootID,
		RootName:    "Team Docs",
		ChangeToken: "token-1",
		Tree:        map[string]*driveNode{"file-1": {Name: "q1.pdf", ParentID: rootID}},
	}
	if err := saveSyncState(statePath, seed); err != nil {
		t.Fatalf("seed saveSyncState: %v", err)
	}
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("ReadFile before: %v", err)
	}

	// 400, not 500: this test proves persistence integrity on a non-stale
	// poll error, a property independent of whether the failing status
	// happens to be retryable. A non-retryable status keeps this test at
	// exactly one request instead of retrying against 05-02's withRetry
	// (and syncengine.go's 2-minute syncRetryDeadline) before failing.
	handler := func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "simulated non-stale failure", http.StatusBadRequest)
	}
	svc := newFakeDriveService(t, handler)
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, rootID), svc)

	if _, err := p.ensureSynced(context.Background(), rootID); err == nil {
		t.Fatal("ensureSynced: got nil error, want the wrapped poll failure")
	}

	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("ReadFile after: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("syncstate.json changed after a non-410 poll failure:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
}

// TestEnsureSynced_EmptyNewStartPageTokenKeepsThePreviouslyStoredToken
// proves must_have truth 4 end to end through ensureSynced (not merely at
// pollChanges' own level): a changes.list response whose NewStartPageToken
// is empty must never overwrite the persisted token with an empty one —
// the previously stored token is kept in place instead.
func TestEnsureSynced_EmptyNewStartPageTokenKeepsThePreviouslyStoredToken(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, filepath.Join(isolatedDir, dataDirName, tokenFileName))

	const rootID = "root-empty-new-token"
	statePath := filepath.Join(isolatedDir, dataDirName, syncStateFileName)
	seed := &syncState{
		RootID:      rootID,
		RootName:    "Team Docs",
		ChangeToken: "token-must-survive",
		Tree:        map[string]*driveNode{"file-1": {Name: "q1.pdf", ParentID: rootID}},
	}
	if err := saveSyncState(statePath, seed); err != nil {
		t.Fatalf("seed saveSyncState: %v", err)
	}

	// A response with a change but NO NewStartPageToken at all — the
	// caller must fall back to keeping the previously persisted token.
	resp := &drive.ChangeList{
		Changes: []*drive.Change{
			{FileId: "file-2", File: &drive.File{Name: "q2.pdf", Parents: []string{rootID}}},
		},
	}
	svc := newFakeDriveService(t, newDeltaOnlyHandler(t, resp))
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, rootID), svc)

	st, err := p.ensureSynced(context.Background(), rootID)
	if err != nil {
		t.Fatalf("ensureSynced: %v", err)
	}
	if st.ChangeToken != "token-must-survive" {
		t.Errorf("ChangeToken = %q, want %q (an empty newStartPageToken must never overwrite the previously stored token)", st.ChangeToken, "token-must-survive")
	}
}

// TestEnsureSynced_410PollTriggersExactlyOneFullRewalkAndProducesCorrectTree
// proves the 410 fallback end to end: a stale page token discards the
// previous state entirely and performs exactly one full files.list
// re-walk (via the shared resync helper), producing a tree that reflects
// the FRESH walk, not the stale persisted one.
func TestEnsureSynced_410PollTriggersExactlyOneFullRewalkAndProducesCorrectTree(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, filepath.Join(isolatedDir, dataDirName, tokenFileName))

	const rootID = "root-410"
	statePath := filepath.Join(isolatedDir, dataDirName, syncStateFileName)
	seed := &syncState{
		RootID:      rootID,
		RootName:    "Stale Name",
		ChangeToken: "stale-token",
		Tree:        map[string]*driveNode{"stale-file": {Name: "stale.txt", ParentID: rootID}},
	}
	if err := saveSyncState(statePath, seed); err != nil {
		t.Fatalf("seed saveSyncState: %v", err)
	}

	fx := driveFixture{rootID: rootID, rootName: "Team Docs", fileID: "fresh-file", fileName: "fresh.pdf"}
	fixtureHandler := newSingleFileFixtureHandler(t, fx)
	recorder := newDriveRecorder(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/changes" {
			http.Error(w, `{"error":{"code":410,"message":"stale token"}}`, http.StatusGone)
			return
		}
		fixtureHandler(w, r)
	})
	svc := newFakeDriveService(t, recorder.ServeHTTP)
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, fx.rootID), svc)

	st, err := p.ensureSynced(context.Background(), fx.rootID)
	if err != nil {
		t.Fatalf("ensureSynced: %v", err)
	}
	if got := recorder.count("/files"); got != 1 {
		t.Errorf("files.list call count = %d, want exactly 1 (one full re-walk)", got)
	}
	if _, ok := st.Tree["stale-file"]; ok {
		t.Error("stale-file (from before the 410) still present after resync")
	}
	if _, ok := st.Tree["fresh-file"]; !ok {
		t.Error("fresh-file missing after resync")
	}
	if st.RootName != "Team Docs" {
		t.Errorf("RootName = %q, want %q (freshly re-walked)", st.RootName, "Team Docs")
	}
}

// --- Task 3: traffic shape and interruption/concurrency guarantees ---

// TestMatch_FilesListCountUnchangedAcrossFiveSyncsChangesListIncreasesByOnePerSync
// is SYNC-02's success criterion turned into a standing gate rather than a
// prose claim: after the first sync, five FURTHER syncs against the same
// fake Drive service issue exactly the first walk's own files.list count
// (never more), while the changes.list count increases by exactly one per
// sync.
func TestMatch_FilesListCountUnchangedAcrossFiveSyncsChangesListIncreasesByOnePerSync(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, filepath.Join(isolatedDir, dataDirName, tokenFileName))

	fx := driveFixture{rootID: "root-traffic", rootName: "Team Docs", fileID: "file-1", fileName: "q1.pdf"}
	recorder := newDriveRecorder(newSingleFileFixtureHandler(t, fx))
	svc := newFakeDriveService(t, recorder.ServeHTTP)
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, fx.rootID), svc)

	if _, err := p.ensureSynced(context.Background(), fx.rootID); err != nil {
		t.Fatalf("first ensureSynced: %v", err)
	}
	filesAfterFirst := recorder.count("/files")
	if filesAfterFirst == 0 {
		t.Fatal("files.list call count after the first sync = 0, want > 0")
	}
	if got := recorder.count("/changes"); got != 0 {
		t.Errorf("changes.list call count after the first sync = %d, want 0 (the first sync is a full walk, not a delta poll)", got)
	}

	for i := 1; i <= 5; i++ {
		if _, err := p.ensureSynced(context.Background(), fx.rootID); err != nil {
			t.Fatalf("sync %d: ensureSynced: %v", i, err)
		}
		if got := recorder.count("/files"); got != filesAfterFirst {
			t.Errorf("sync %d: files.list count = %d, want %d (unchanged since the first walk)", i, got, filesAfterFirst)
		}
		if got := recorder.count("/changes"); got != i {
			t.Errorf("sync %d: changes.list count = %d, want %d", i, got, i)
		}
	}
}

// TestEnsureSynced_EmptyChangeBatchStillAdvancesTokenWithNoFilesListRequest
// proves the second half of the traffic-shape guarantee: an empty delta
// poll result is not "nothing happened" — it still advances the stored
// token, and issues no files.list request.
func TestEnsureSynced_EmptyChangeBatchStillAdvancesTokenWithNoFilesListRequest(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, filepath.Join(isolatedDir, dataDirName, tokenFileName))

	const rootID = "root-empty-batch"
	statePath := filepath.Join(isolatedDir, dataDirName, syncStateFileName)
	seed := &syncState{
		RootID:      rootID,
		RootName:    "Team Docs",
		ChangeToken: "token-1",
		Tree:        map[string]*driveNode{"file-1": {Name: "q1.pdf", ParentID: rootID}},
	}
	if err := saveSyncState(statePath, seed); err != nil {
		t.Fatalf("seed saveSyncState: %v", err)
	}

	recorder := newDriveRecorder(newDeltaOnlyHandler(t, &drive.ChangeList{NewStartPageToken: "token-2"}))
	svc := newFakeDriveService(t, recorder.ServeHTTP)
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, rootID), svc)

	st, err := p.ensureSynced(context.Background(), rootID)
	if err != nil {
		t.Fatalf("ensureSynced: %v", err)
	}
	if st.ChangeToken != "token-2" {
		t.Errorf("ChangeToken = %q, want %q (an empty batch must still advance the token)", st.ChangeToken, "token-2")
	}
	if got := recorder.count("/files"); got != 0 {
		t.Errorf("files.list call count = %d, want 0", got)
	}
}

// TestMatch_ConcurrentCallsSerializeSyncsWithNoDataRace launches several
// concurrent ensureSynced calls against one SourcePlugin and asserts the
// total changes.list request count is exactly what a fully serialized
// implementation would issue: the winner of the race to run first performs
// the resync (no changes.list at all), and every OTHER call performs
// exactly one delta poll — never more, because syncMu holds the whole
// load-poll-apply-save sequence for the entire operation, so no two
// goroutines ever observe or interleave two overlapping syncs. Run with
// -race (Task 3's own verify command) to actually enforce the absence of
// a data race, not merely the request-count invariant.
func TestMatch_ConcurrentCallsSerializeSyncsWithNoDataRace(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, filepath.Join(isolatedDir, dataDirName, tokenFileName))

	fx := driveFixture{rootID: "root-concurrent", rootName: "Team Docs", fileID: "file-1", fileName: "q1.pdf"}
	recorder := newDriveRecorder(newSingleFileFixtureHandler(t, fx))
	svc := newFakeDriveService(t, recorder.ServeHTTP)
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, fx.rootID), svc)

	const n = 10
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := p.ensureSynced(context.Background(), fx.rootID)
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: ensureSynced: %v", i, err)
		}
	}

	if got, want := recorder.count("/changes"), n-1; got != want {
		t.Errorf("changes.list call count = %d, want %d (exactly one delta poll per sync after the first, no more than a serialized implementation would issue)", got, want)
	}
	if got := recorder.count("/files"); got != 1 {
		t.Errorf("files.list call count = %d, want 1 (only the one winning resync ever walks)", got)
	}
}

// TestEnsureSynced_FailedPostPollStateWriteLeavesSyncStateByteIdentical
// proves T-03-10's interruption guarantee directly: a saveSyncState call
// that fails AFTER a successful poll (the containing directory made
// unwritable, so the temp file's O_CREATE cannot succeed — mirroring
// syncstate_test.go's own technique) leaves the previously persisted
// syncstate.json byte-identical, proving the change token never advanced
// past changes whose tree update was never persisted.
func TestEnsureSynced_FailedPostPollStateWriteLeavesSyncStateByteIdentical(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, filepath.Join(isolatedDir, dataDirName, tokenFileName))

	const rootID = "root-interrupt"
	dir := filepath.Join(isolatedDir, dataDirName)
	statePath := filepath.Join(dir, syncStateFileName)
	seed := &syncState{
		RootID:      rootID,
		RootName:    "Team Docs",
		ChangeToken: "token-1",
		Tree:        map[string]*driveNode{"file-1": {Name: "q1.pdf", ParentID: rootID}},
	}
	if err := saveSyncState(statePath, seed); err != nil {
		t.Fatalf("seed saveSyncState: %v", err)
	}
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("ReadFile before: %v", err)
	}

	resp := &drive.ChangeList{
		Changes: []*drive.Change{
			{FileId: "file-2", File: &drive.File{Name: "q2.pdf", Parents: []string{rootID}}},
		},
		NewStartPageToken: "token-2",
	}
	svc := newFakeDriveService(t, newDeltaOnlyHandler(t, resp))
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, rootID), svc)

	// Make the directory unwritable so saveSyncState's O_CREATE temp-file
	// step fails AFTER the poll itself has already succeeded — the write
	// phase, not the poll, is what fails here.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("Chmod(dir, 0500): %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	_, syncErr := p.ensureSynced(context.Background(), rootID)

	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("Chmod(dir, 0700) restore: %v", err)
	}

	if syncErr == nil {
		t.Fatal("ensureSynced with an unwritable data directory: got nil error, want non-nil")
	}

	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("ReadFile after: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("syncstate.json changed after a failed post-poll write:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
}

// TestApplyChanges_ApplyingSameBatchTwiceYieldsSameTree proves the
// idempotence claim the interruption guarantee leans on: re-polling an
// already-applied change (as happens after a failed save forces a re-poll
// of the same window) is harmless because every apply is idempotent.
func TestApplyChanges_ApplyingSameBatchTwiceYieldsSameTree(t *testing.T) {
	changes := []*drive.Change{
		{FileId: "f1", File: &drive.File{
			Name: "a.txt", MimeType: "text/plain", Parents: []string{"root"},
			ModifiedTime: "2026-08-17T00:00:00Z", WebViewLink: "https://drive.google.com/file/d/f1/view",
		}},
		{FileId: "f2", File: &drive.File{
			Name: "b.txt", MimeType: "text/plain", Parents: []string{"root"},
			ModifiedTime: "2026-08-17T00:00:00Z", WebViewLink: "https://drive.google.com/file/d/f2/view",
		}},
	}

	once := map[string]*driveNode{}
	applyChanges(once, "root", changes)

	twice := map[string]*driveNode{}
	applyChanges(twice, "root", changes)
	applyChanges(twice, "root", changes) // re-apply the identical batch

	onceJSON, err := json.Marshal(once)
	if err != nil {
		t.Fatalf("Marshal(once): %v", err)
	}
	twiceJSON, err := json.Marshal(twice)
	if err != nil {
		t.Fatalf("Marshal(twice): %v", err)
	}
	if string(onceJSON) != string(twiceJSON) {
		t.Errorf("applying the same batch twice produced a different tree than applying it once:\n--- once ---\n%s\n--- twice ---\n%s", onceJSON, twiceJSON)
	}
}
