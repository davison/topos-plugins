// Package main's folderwalk_test.go proves the full SYNC-01 walk shape this
// plan's Task 3 hardens: draining the queue to arbitrary depth, surviving
// pagination, excluding trashed files even when a response reports one
// despite the query filter, never crashing on an empty Parents slice,
// keeping identically-named files in different subfolders distinct, an
// empty configured folder producing an empty tree with a nil error, a
// mid-walk failure aborting with a wrapped error and no persisted state, and
// the order Drive returns children in never leaking into the persisted
// syncstate.json. Follows plugin_test.go's own idiom: Test<Thing>_<Behavior
// InPlainEnglish> names, plain t.Errorf/t.Fatalf assertions, no assertion
// library, t.TempDir()/injected-getenv isolation. Reuses drivefake_test.go's
// newFakeDriveService/newDriveRecorder and syncengine_test.go's
// driveFixture/newSingleFileFixtureHandler/parentFromQuery/writeDriveJSON/
// seedValidToken/sourceConfigJSON/pluginWithFakeDrive helpers directly —
// same package, no duplication.
package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"google.golang.org/api/drive/v3"
)

// walkFixtureFile describes one fixture Drive object served by
// newWalkFixtureHandler, keyed into a parent-id-to-children map.
type walkFixtureFile struct {
	id, name, mimeType, modifiedTime, webViewLink string
	trashed                                       bool
}

// newWalkFixtureHandler serves files.list only, keyed by the parent id
// walkFolder's own query embeds (parentFromQuery), from the supplied
// children map. When pageSize > 0 and a parent has more children than
// pageSize, the response is split across multiple pages via nextPageToken/
// pageToken, exercising FilesListCall.Pages's own draining loop.
func newWalkFixtureHandler(t *testing.T, children map[string][]walkFixtureFile, pageSize int) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/files" {
			http.NotFound(w, r)
			return
		}
		parent := parentFromQuery(r.URL.Query().Get("q"))
		all := children[parent]

		start := 0
		if pt := r.URL.Query().Get("pageToken"); pt != "" {
			n, err := strconv.Atoi(pt)
			if err != nil {
				http.Error(w, "bad page token", http.StatusBadRequest)
				return
			}
			start = n
		}

		end := len(all)
		next := ""
		if pageSize > 0 && start+pageSize < len(all) {
			end = start + pageSize
			next = strconv.Itoa(end)
		}

		var files []*drive.File
		for _, f := range all[start:end] {
			files = append(files, &drive.File{
				Id:           f.id,
				Name:         f.name,
				MimeType:     f.mimeType,
				ModifiedTime: f.modifiedTime,
				WebViewLink:  f.webViewLink,
				Trashed:      f.trashed,
			})
		}
		writeDriveJSON(t, w, &drive.FileList{Files: files, NextPageToken: next})
	}
}

// TestWalkFolder_FileThreeLevelsBelowRootIsPresentAfterOneWalk proves the
// queue drains to arbitrary depth (Pitfall 1): 'X' in parents is not
// recursive, so walkFolder itself must chase every discovered subfolder.
func TestWalkFolder_FileThreeLevelsBelowRootIsPresentAfterOneWalk(t *testing.T) {
	children := map[string][]walkFixtureFile{
		"root":  {{id: "sub-a", name: "A", mimeType: folderMimeType}},
		"sub-a": {{id: "sub-b", name: "B", mimeType: folderMimeType}},
		"sub-b": {{
			id: "deep-file", name: "deep.pdf", mimeType: "application/pdf",
			modifiedTime: "2026-08-17T00:00:00Z",
			webViewLink:  "https://drive.google.com/file/d/deep-file/view",
		}},
	}
	svc := newFakeDriveService(t, newWalkFixtureHandler(t, children, 0))

	tree, err := walkFolder(context.Background(), svc, "root")
	if err != nil {
		t.Fatalf("walkFolder: %v", err)
	}
	node, ok := tree["deep-file"]
	if !ok {
		t.Fatal("deep-file not present in tree after one walk")
	}
	if node.ParentID != "sub-b" {
		t.Errorf("ParentID = %q, want %q", node.ParentID, "sub-b")
	}
	if _, ok := tree["sub-a"]; !ok {
		t.Error("sub-a not present in tree")
	}
	if _, ok := tree["sub-b"]; !ok {
		t.Error("sub-b not present in tree")
	}
}

// TestWalkFolder_ChildrenAcrossTwoPagesAreAllPresent proves the generated
// client's own FilesListCall.Pages helper drains every page for one folder
// level.
func TestWalkFolder_ChildrenAcrossTwoPagesAreAllPresent(t *testing.T) {
	children := map[string][]walkFixtureFile{
		"root": {
			{id: "f1", name: "one.txt", mimeType: "text/plain", modifiedTime: "2026-08-17T00:00:00Z", webViewLink: "https://drive.google.com/file/d/f1/view"},
			{id: "f2", name: "two.txt", mimeType: "text/plain", modifiedTime: "2026-08-17T00:00:00Z", webViewLink: "https://drive.google.com/file/d/f2/view"},
			{id: "f3", name: "three.txt", mimeType: "text/plain", modifiedTime: "2026-08-17T00:00:00Z", webViewLink: "https://drive.google.com/file/d/f3/view"},
		},
	}
	svc := newFakeDriveService(t, newWalkFixtureHandler(t, children, 2))

	tree, err := walkFolder(context.Background(), svc, "root")
	if err != nil {
		t.Fatalf("walkFolder: %v", err)
	}
	for _, id := range []string{"f1", "f2", "f3"} {
		if _, ok := tree[id]; !ok {
			t.Errorf("%s not present in tree after a paginated files.list response", id)
		}
	}
}

// TestWalkFolder_TrashedFileExcludedDespiteAppearingInResponse proves
// Pitfall 2: files.list returns all files by default, including trashed
// ones, regardless of the query's own trashed = false clause — walkFolder
// must check File.Trashed explicitly on every returned file.
func TestWalkFolder_TrashedFileExcludedDespiteAppearingInResponse(t *testing.T) {
	children := map[string][]walkFixtureFile{
		"root": {
			{id: "keep", name: "keep.txt", mimeType: "text/plain", modifiedTime: "2026-08-17T00:00:00Z", webViewLink: "https://drive.google.com/file/d/keep/view"},
			{id: "trashed-file", name: "gone.txt", mimeType: "text/plain", trashed: true},
		},
	}
	svc := newFakeDriveService(t, newWalkFixtureHandler(t, children, 0))

	tree, err := walkFolder(context.Background(), svc, "root")
	if err != nil {
		t.Fatalf("walkFolder: %v", err)
	}
	if _, ok := tree["trashed-file"]; ok {
		t.Error("trashed file present in tree despite Trashed: true in the response")
	}
	if _, ok := tree["keep"]; !ok {
		t.Error("non-trashed file missing from tree")
	}
}

// TestWalkFolder_FileWithEmptyParentsSliceDoesNotCrashTheWalk proves
// Pitfall 9: walkFolder never indexes File.Parents at all — ParentID is
// always the walk's own current queue parent, this plugin's own scoping
// decision — so a file whose Parents slice is empty or absent (a real,
// if uncommon, Drive state) must never crash or misattribute the walk.
func TestWalkFolder_FileWithEmptyParentsSliceDoesNotCrashTheWalk(t *testing.T) {
	children := map[string][]walkFixtureFile{
		"root": {{
			id: "orphan-shaped", name: "orphan.txt", mimeType: "text/plain",
			modifiedTime: "2026-08-17T00:00:00Z",
			webViewLink:  "https://drive.google.com/file/d/orphan-shaped/view",
		}},
	}
	svc := newFakeDriveService(t, newWalkFixtureHandler(t, children, 0))

	tree, err := walkFolder(context.Background(), svc, "root")
	if err != nil {
		t.Fatalf("walkFolder: %v", err)
	}
	node, ok := tree["orphan-shaped"]
	if !ok {
		t.Fatal("file with an empty Parents slice missing from tree")
	}
	if node.ParentID != "root" {
		t.Errorf("ParentID = %q, want %q (queue-derived scoping, never the response's own Parents slice)", node.ParentID, "root")
	}
}

// TestWalkFolder_IdenticalNamesInDifferentSubfoldersAreDistinctEntries
// proves the tree is keyed by Drive file id, never by name — names are
// collaborator-controllable metadata and are not unique (Assumption A3).
func TestWalkFolder_IdenticalNamesInDifferentSubfoldersAreDistinctEntries(t *testing.T) {
	children := map[string][]walkFixtureFile{
		"root": {
			{id: "sub-a", name: "A", mimeType: folderMimeType},
			{id: "sub-b", name: "B", mimeType: folderMimeType},
		},
		"sub-a": {{id: "file-in-a", name: "report.pdf", mimeType: "application/pdf", modifiedTime: "2026-08-17T00:00:00Z", webViewLink: "https://drive.google.com/file/d/file-in-a/view"}},
		"sub-b": {{id: "file-in-b", name: "report.pdf", mimeType: "application/pdf", modifiedTime: "2026-08-17T00:00:00Z", webViewLink: "https://drive.google.com/file/d/file-in-b/view"}},
	}
	svc := newFakeDriveService(t, newWalkFixtureHandler(t, children, 0))

	tree, err := walkFolder(context.Background(), svc, "root")
	if err != nil {
		t.Fatalf("walkFolder: %v", err)
	}
	a, aok := tree["file-in-a"]
	b, bok := tree["file-in-b"]
	if !aok || !bok {
		t.Fatalf("both identically-named files must be present as distinct id-keyed entries: aok=%v bok=%v", aok, bok)
	}
	if a.Name != b.Name {
		t.Fatalf("fixture setup error: names should match (%q vs %q)", a.Name, b.Name)
	}
	if a.ParentID == b.ParentID {
		t.Error("both entries report the same ParentID, want distinct subfolders")
	}
}

// TestWalkFolder_EmptyConfiguredFolderYieldsEmptyTreeAndNilError proves the
// empty-folder truth at the walk level (syncengine_test.go's
// TestMatch_EmptyConfiguredFolderYieldsZeroItemsAndNilError proves it at the
// Match level).
func TestWalkFolder_EmptyConfiguredFolderYieldsEmptyTreeAndNilError(t *testing.T) {
	svc := newFakeDriveService(t, newWalkFixtureHandler(t, map[string][]walkFixtureFile{}, 0))

	tree, err := walkFolder(context.Background(), svc, "root")
	if err != nil {
		t.Fatalf("walkFolder: %v", err)
	}
	if len(tree) != 0 {
		t.Errorf("len(tree) = %d, want 0", len(tree))
	}
}

// newMidWalkFailureHandler serves a fixture where the root's own children
// list successfully (one subfolder), but that subfolder's own files.list
// call fails — proving a failure partway through the walk, not just on the
// very first call.
func newMidWalkFailureHandler(t *testing.T, fx driveFixture, childFolderID string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/changes/startPageToken":
			writeDriveJSON(t, w, &drive.StartPageToken{StartPageToken: "start-token-fail"})
		case r.URL.Path == "/files/"+fx.rootID:
			writeDriveJSON(t, w, &drive.File{Id: fx.rootID, Name: fx.rootName, MimeType: folderMimeType})
		case r.URL.Path == "/files":
			parent := parentFromQuery(r.URL.Query().Get("q"))
			switch parent {
			case childFolderID:
				// 400, not 500: this test proves a mid-walk failure aborts
				// with a wrapped error and no persisted state, a property
				// independent of whether the failing status happens to be
				// retryable. A non-retryable status keeps this test fast —
				// 05-02's withRetry now wraps walkFolder's paged list call,
				// and a retryable status here would retry against
				// syncengine.go's 2-minute syncRetryDeadline before
				// failing.
				http.Error(w, "simulated mid-walk failure", http.StatusBadRequest)
			case fx.rootID:
				writeDriveJSON(t, w, &drive.FileList{Files: []*drive.File{{
					Id: childFolderID, Name: "Sub", MimeType: folderMimeType,
				}}})
			default:
				writeDriveJSON(t, w, &drive.FileList{})
			}
		default:
			http.NotFound(w, r)
		}
	}
}

// TestEnsureSynced_MidWalkFailureAbortsWithWrappedErrorAndWritesNoStateFile
// proves a files.list failure partway through the walk (after the root's
// own children were already listed successfully) returns a wrapped error
// and — because ensureSynced only calls saveSyncState after a fully
// successful walk — leaves no syncstate.json behind at all, never a
// partial tree that looks intentional.
func TestEnsureSynced_MidWalkFailureAbortsWithWrappedErrorAndWritesNoStateFile(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, filepath.Join(isolatedDir, dataDirName, tokenFileName))

	fx := driveFixture{rootID: "root-fail", rootName: "Root"}
	svc := newFakeDriveService(t, newMidWalkFailureHandler(t, fx, "sub-fail"))
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, fx.rootID), svc)

	_, err := p.ensureSynced(context.Background(), fx.rootID)
	if err == nil {
		t.Fatal("ensureSynced: got nil error, want a wrapped files.list failure")
	}
	if !contains(err.Error(), "list children of") {
		t.Errorf("error %q does not name the failed operation", err.Error())
	}

	statePath := filepath.Join(isolatedDir, dataDirName, syncStateFileName)
	if _, statErr := os.Stat(statePath); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("syncstate.json exists after a failed walk (stat err = %v), want no file at all", statErr)
	}
}

// TestEnsureSynced_ChildOrderDrivesReturnsInNeverLeaksIntoSyncState proves
// two first-run walks over the same unchanged fixture folder produce
// byte-identical syncstate.json even when Drive returns the same children
// in a different order across the two runs — the persisted JSON's
// map-keyed Tree is always marshaled in sorted key order by
// encoding/json, so the order Drive returns children in never leaks into
// persisted state.
func TestEnsureSynced_ChildOrderDrivesReturnsInNeverLeaksIntoSyncState(t *testing.T) {
	build := func(t *testing.T, reversed bool) []byte {
		t.Helper()
		isolatedDir := t.TempDir()
		seedValidToken(t, filepath.Join(isolatedDir, dataDirName, tokenFileName))

		const rootID, fileAID, fileBID = "root-order", "file-a", "file-b"
		files := []*drive.File{
			{Id: fileAID, Name: "a.txt", MimeType: "text/plain", ModifiedTime: "2026-08-17T00:00:00Z", WebViewLink: "https://drive.google.com/file/d/file-a/view"},
			{Id: fileBID, Name: "b.txt", MimeType: "text/plain", ModifiedTime: "2026-08-17T00:00:00Z", WebViewLink: "https://drive.google.com/file/d/file-b/view"},
		}
		if reversed {
			files[0], files[1] = files[1], files[0]
		}

		handler := func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/changes/startPageToken":
				writeDriveJSON(t, w, &drive.StartPageToken{StartPageToken: "start-token-order"})
			case r.URL.Path == "/files/"+rootID:
				writeDriveJSON(t, w, &drive.File{Id: rootID, Name: "Team Docs", MimeType: folderMimeType})
			case r.URL.Path == "/files":
				writeDriveJSON(t, w, &drive.FileList{Files: files})
			default:
				http.NotFound(w, r)
			}
		}
		svc := newFakeDriveService(t, handler)
		p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, rootID), svc)

		if _, err := p.ensureSynced(context.Background(), rootID); err != nil {
			t.Fatalf("ensureSynced: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(isolatedDir, dataDirName, syncStateFileName))
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		return data
	}

	forward := build(t, false)
	reversed := build(t, true)
	if !bytes.Equal(forward, reversed) {
		t.Errorf("walks whose files.list responses differed only in child order produced non-identical syncstate.json:\n--- forward ---\n%s\n--- reversed ---\n%s", forward, reversed)
	}
}
