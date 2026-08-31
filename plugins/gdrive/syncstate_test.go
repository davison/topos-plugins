// Package main's syncstate_test.go mirrors token_test.go's durability
// assertions for syncstate.json: mode 0600, atomic replace, no orphaned
// temp file, the malformed-vs-not-found load distinction, a full field
// round trip, the interrupted-write guarantee (a failed save must never
// corrupt or discard the previously persisted state), and the standing
// privacy prohibition that the persisted tree carries identity/structure
// keys only — never content, preview, or rendition bytes. Follows
// plugin_test.go's own idiom: Test<Thing>_<BehaviorInPlainEnglish> names,
// plain t.Errorf/t.Fatalf assertions, no assertion library, t.TempDir()
// isolation.
package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func sampleSyncState() *syncState {
	return &syncState{
		RootID:      "root-1",
		RootName:    "Team Docs",
		ChangeToken: "start-token-1",
		Tree: map[string]*driveNode{
			"file-1": {
				Name:         "q1.pdf",
				MimeType:     "application/pdf",
				ParentID:     "root-1",
				ModifiedTime: "2026-08-17T00:00:00Z",
				WebViewLink:  "https://drive.google.com/file/d/file-1/view",
				Size:         12345,
			},
		},
	}
}

func TestSaveSyncState_RoundTripsEveryField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", syncStateFileName)
	want := sampleSyncState()

	if err := saveSyncState(path, want); err != nil {
		t.Fatalf("saveSyncState: %v", err)
	}

	got, err := loadSyncState(path)
	if err != nil {
		t.Fatalf("loadSyncState: %v", err)
	}

	if got.RootID != want.RootID {
		t.Errorf("RootID = %q, want %q", got.RootID, want.RootID)
	}
	if got.RootName != want.RootName {
		t.Errorf("RootName = %q, want %q", got.RootName, want.RootName)
	}
	if got.ChangeToken != want.ChangeToken {
		t.Errorf("ChangeToken = %q, want %q", got.ChangeToken, want.ChangeToken)
	}
	if len(got.Tree) != len(want.Tree) {
		t.Fatalf("len(Tree) = %d, want %d", len(got.Tree), len(want.Tree))
	}
	gotNode, ok := got.Tree["file-1"]
	if !ok {
		t.Fatal(`Tree["file-1"] missing after round trip`)
	}
	wantNode := want.Tree["file-1"]
	if gotNode.Name != wantNode.Name {
		t.Errorf("Name = %q, want %q", gotNode.Name, wantNode.Name)
	}
	if gotNode.MimeType != wantNode.MimeType {
		t.Errorf("MimeType = %q, want %q", gotNode.MimeType, wantNode.MimeType)
	}
	if gotNode.ParentID != wantNode.ParentID {
		t.Errorf("ParentID = %q, want %q", gotNode.ParentID, wantNode.ParentID)
	}
	if gotNode.ModifiedTime != wantNode.ModifiedTime {
		t.Errorf("ModifiedTime = %q, want %q", gotNode.ModifiedTime, wantNode.ModifiedTime)
	}
	if gotNode.WebViewLink != wantNode.WebViewLink {
		t.Errorf("WebViewLink = %q, want %q", gotNode.WebViewLink, wantNode.WebViewLink)
	}
	if gotNode.Size != wantNode.Size {
		t.Errorf("Size = %d, want %d", gotNode.Size, wantNode.Size)
	}
}

func TestSaveSyncState_WritesMode0600OnFreshFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, syncStateFileName)

	if err := saveSyncState(path, sampleSyncState()); err != nil {
		t.Fatalf("saveSyncState: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %v, want 0600", perm)
	}
}

func TestSaveSyncState_ReplacingAnExistingWiderModeFileStaysAt0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, syncStateFileName)

	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("seed WriteFile: %v", err)
	}

	if err := saveSyncState(path, sampleSyncState()); err != nil {
		t.Fatalf("saveSyncState (replace): %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode after replace = %v, want 0600 (atomic replace must never inherit a wider mode)", perm)
	}
}

func TestSaveSyncState_ParentDirectoryModeIs0700(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "nested", "deeper")
	path := filepath.Join(nested, syncStateFileName)

	if err := saveSyncState(path, sampleSyncState()); err != nil {
		t.Fatalf("saveSyncState: %v", err)
	}

	info, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("Stat(%s): %v", nested, err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("parent dir mode = %v, want 0700", perm)
	}
}

func TestSaveSyncState_LeavesNoOrphanedTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, syncStateFileName)

	if err := saveSyncState(path, sampleSyncState()); err != nil {
		t.Fatalf("saveSyncState: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("directory contains %d entries after save, want 1 (no orphaned temp file): %v", len(entries), names)
	}
	if entries[0].Name() != syncStateFileName {
		t.Errorf("directory entry = %q, want %q", entries[0].Name(), syncStateFileName)
	}
}

func TestLoadSyncState_AbsentFileSatisfiesErrorsIsNotExist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, syncStateFileName)

	_, err := loadSyncState(path)
	if err == nil {
		t.Fatal("loadSyncState on absent file: got nil error, want non-nil")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("loadSyncState error = %v, want errors.Is(err, fs.ErrNotExist)", err)
	}
}

func TestLoadSyncState_ZeroByteFileSatisfiesErrorsIsMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, syncStateFileName)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("seed WriteFile: %v", err)
	}

	_, err := loadSyncState(path)
	if err == nil {
		t.Fatal("loadSyncState on zero-byte file: got nil error, want non-nil")
	}
	if !errors.Is(err, errSyncStateMalformed) {
		t.Errorf("loadSyncState error = %v, want errors.Is(err, errSyncStateMalformed)", err)
	}
	if got := err.Error(); !contains(got, path) {
		t.Errorf("error %q does not name the path %q", got, path)
	}
}

func TestLoadSyncState_MalformedJSONSatisfiesErrorsIsMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, syncStateFileName)
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("seed WriteFile: %v", err)
	}

	_, err := loadSyncState(path)
	if err == nil {
		t.Fatal("loadSyncState on malformed JSON: got nil error, want non-nil")
	}
	if !errors.Is(err, errSyncStateMalformed) {
		t.Errorf("loadSyncState error = %v, want errors.Is(err, errSyncStateMalformed)", err)
	}
	if got := err.Error(); !contains(got, path) {
		t.Errorf("error %q does not name the path %q", got, path)
	}
}

// TestLoadSyncState_ValidJSONWithNilTreeOrEmptyRootIDSatisfiesErrorsIsMalformed
// proves the load path is total: JSON that parses cleanly but carries a nil
// Tree map or an empty RootID (neither of which a successful saveSyncState
// call ever produces) is also routed to errSyncStateMalformed, never a
// nil-map panic downstream.
func TestLoadSyncState_ValidJSONWithNilTreeOrEmptyRootIDSatisfiesErrorsIsMalformed(t *testing.T) {
	cases := map[string]string{
		"nil tree":     `{"rootId": "root-1", "rootName": "Team Docs", "changeToken": "t"}`,
		"empty rootId": `{"rootId": "", "rootName": "Team Docs", "changeToken": "t", "tree": {}}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, syncStateFileName)
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatalf("seed WriteFile: %v", err)
			}

			_, err := loadSyncState(path)
			if err == nil {
				t.Fatal("loadSyncState: got nil error, want non-nil")
			}
			if !errors.Is(err, errSyncStateMalformed) {
				t.Errorf("loadSyncState error = %v, want errors.Is(err, errSyncStateMalformed)", err)
			}
		})
	}
}

// TestSaveSyncState_FailedSaveLeavesThePreviousFileByteIdenticalAndNoOrphanedTempFile
// proves the interrupted-write guarantee directly: with a pre-existing
// complete state file, a save whose write phase is made to fail (the
// containing directory is made unwritable, so the O_EXCL temp-file create
// itself cannot succeed) leaves the previous file byte-identical on disk
// and no .tmp file behind.
func TestSaveSyncState_FailedSaveLeavesThePreviousFileByteIdenticalAndNoOrphanedTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, syncStateFileName)

	if err := saveSyncState(path, sampleSyncState()); err != nil {
		t.Fatalf("seed saveSyncState: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile before failed save: %v", err)
	}

	// Make the directory unwritable so the temp file's O_CREATE cannot
	// succeed — simulating a write-phase failure.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("Chmod(dir, 0500): %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	next := sampleSyncState()
	next.ChangeToken = "a-different-token-that-must-never-land"
	saveErr := saveSyncState(path, next)

	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("Chmod(dir, 0700) restore: %v", err)
	}

	if saveErr == nil {
		t.Fatal("saveSyncState with an unwritable directory: got nil error, want non-nil")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after failed save: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("previously persisted file changed after a failed save:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("directory contains %d entries after a failed save, want 1 (no orphaned temp file): %v", len(entries), names)
	}
}

// TestSaveSyncState_PersistedNodeObjectsCarryNoContentBearingKey pins this
// plan's standing privacy prohibition: the sync store holds identity and
// structure metadata only — never a Drive document's content, preview
// text, or rendition bytes.
func TestSaveSyncState_PersistedNodeObjectsCarryNoContentBearingKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, syncStateFileName)

	if err := saveSyncState(path, sampleSyncState()); err != nil {
		t.Fatalf("saveSyncState: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var raw struct {
		Tree map[string]map[string]any `json:"tree"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	allowed := map[string]bool{
		"name": true, "mimeType": true, "parentId": true,
		"modifiedTime": true, "webViewLink": true,
		// size (04-01, Task 3): Drive's own internal representation size —
		// structure metadata, the same category as modifiedTime, never a
		// content/preview/rendition byte. See driveNode's own doc comment.
		"size": true,
	}
	for id, node := range raw.Tree {
		for key := range node {
			if !allowed[key] {
				t.Errorf("node %q carries unexpected key %q — the sync store holds identity/structure metadata only, never content, preview, or rendition bytes", id, key)
			}
		}
	}
}
