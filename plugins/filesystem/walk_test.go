package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// noScope returns a *scope with no include/exclude overrides — the
// default-allowlist-only shape every test below uses unless it is
// specifically exercising include_glob/exclude_glob behavior.
func noScope() *scope {
	return newScope(nil)
}

func sourceIDs(results []walkResult) []string {
	ids := make([]string, 0, len(results))
	for _, r := range results {
		ids = append(ids, r.sourceID)
	}
	sort.Strings(ids)
	return ids
}

func mustWriteFile(t *testing.T, root, relPath string, body []byte) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", relPath, err)
	}
	if err := os.WriteFile(full, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", relPath, err)
	}
}

// --- Non-recursive: only files directly in root become items ---

func TestWalk_NonRecursiveOnlyTopLevelFilesBecomeItems(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, root, "invoice.pdf", []byte("x"))
	mustWriteFile(t, root, "receipts/nested.pdf", []byte("x"))

	results, skipped, err := walk(t.Context(), root, false, noScope())
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if skipped != 0 {
		t.Errorf("expected 0 skipped, got %d", skipped)
	}
	got := sourceIDs(results)
	want := []string{"invoice.pdf"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

// --- Recursive: files at every depth become items, forward-slash source_id ---

func TestWalk_RecursiveFilesAtEveryDepthBecomeItems(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, root, "invoice.pdf", []byte("x"))
	mustWriteFile(t, root, "receipts/2026/nested.pdf", []byte("x"))

	results, _, err := walk(t.Context(), root, true, noScope())
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	got := sourceIDs(results)
	want := []string{"invoice.pdf", "receipts/2026/nested.pdf"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

// --- Dot-file/dot-directory hidden policy ---

func TestWalk_DotFileInRootSkippedByDefault(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, root, ".hidden.md", []byte("x"))
	mustWriteFile(t, root, "visible.md", []byte("x"))

	results, _, err := walk(t.Context(), root, false, noScope())
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	got := sourceIDs(results)
	if len(got) != 1 || got[0] != "visible.md" {
		t.Fatalf("expected only visible.md, got %v", got)
	}
}

func TestWalk_FileInsideDotDirectorySkippedByDefault(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, root, ".git/config.md", []byte("x"))
	mustWriteFile(t, root, "visible.md", []byte("x"))

	results, _, err := walk(t.Context(), root, true, noScope())
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	got := sourceIDs(results)
	if len(got) != 1 || got[0] != "visible.md" {
		t.Fatalf("expected only visible.md (dot-directory contents skipped), got %v", got)
	}
}

func TestWalk_IncludeGlobBringsBackDotFileAndDotDirectoryContents(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, root, ".hidden.md", []byte("x"))
	mustWriteFile(t, root, ".config/notes.md", []byte("x"))

	sc := newScope(map[string]string{"include_glob": "**/*.md"})
	results, _, err := walk(t.Context(), root, true, sc)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	got := sourceIDs(results)
	want := []string{".config/notes.md", ".hidden.md"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

// --- Symlinked directory never descended into ---

func TestWalk_SymlinkedDirectoryNeverDescendedInto(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on windows")
	}
	root := t.TempDir()
	mustWriteFile(t, root, "real/inside.pdf", []byte("x"))
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link-to-real")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	results, _, err := walk(t.Context(), root, true, noScope())
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	got := sourceIDs(results)
	want := []string{"real/inside.pdf"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("expected only the real path's file (symlinked dir never descended), got %v", got)
	}
}

// TestWalk_SymlinkPointingAtAncestorCompletesWithoutHanging is the
// load-bearing proof that an in-tree symlink cycle can never cause
// unbounded recursion: a symlink inside a subdirectory points back at an
// ancestor of itself, and the walk must complete (not hang, not recurse
// without bound).
func TestWalk_SymlinkPointingAtAncestorCompletesWithoutHanging(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on windows")
	}
	root := t.TempDir()
	mustWriteFile(t, root, "sub/inside.pdf", []byte("x"))
	// sub/loop -> root (an ancestor of sub itself).
	if err := os.Symlink(root, filepath.Join(root, "sub", "loop")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	done := make(chan struct{})
	var results []walkResult
	var walkErr error
	go func() {
		results, _, walkErr = walk(t.Context(), root, true, noScope())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("walk did not complete within 5s — likely an unbounded symlink-cycle recursion")
	}

	if walkErr != nil {
		t.Fatalf("walk: %v", walkErr)
	}
	got := sourceIDs(results)
	if len(got) != 1 || got[0] != "sub/inside.pdf" {
		t.Fatalf("expected only sub/inside.pdf, got %v", got)
	}
}

// --- Symlink pointing at a regular file inside root: classified/included ---

func TestWalk_SymlinkToRegularFileInsideRootIsIncluded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on windows")
	}
	root := t.TempDir()
	mustWriteFile(t, root, "real.pdf", []byte("x"))
	if err := os.Symlink(filepath.Join(root, "real.pdf"), filepath.Join(root, "linked.pdf")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	results, _, err := walk(t.Context(), root, false, noScope())
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	got := sourceIDs(results)
	want := []string{"linked.pdf", "real.pdf"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

// TestWalk_SymlinkToFileOutsideRootIsExcluded proves T-12-12: a symlink
// resolving outside the configured root is never a candidate, even though
// its own name sits inside root.
func TestWalk_SymlinkToFileOutsideRootIsExcluded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	mustWriteFile(t, outside, "secret.pdf", []byte("x"))
	if err := os.Symlink(filepath.Join(outside, "secret.pdf"), filepath.Join(root, "escape.pdf")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	results, _, err := walk(t.Context(), root, false, noScope())
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected the outside-root symlink target to be excluded, got %v", sourceIDs(results))
	}
}

// TestWalk_InTreeSymlinkUnderASymlinkedRootIsStillIncluded proves WR-01 is
// closed: a configured root that is itself a symlink or bind mount (the
// common `~/Documents` -> `~/dotfiles/Documents` dotfile-manager pattern)
// still contributes its legitimately in-tree symlinked files to the walk's
// corpus, instead of silently dropping every one of them. Under the
// pre-fix code, the resolved symlink target never shared the unresolved
// root's literal prefix, so linked.pdf would be silently dropped and this
// test would fail with one result instead of two.
func TestWalk_InTreeSymlinkUnderASymlinkedRootIsStillIncluded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on windows")
	}
	tmp := t.TempDir()
	real := filepath.Join(tmp, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("mkdir real: %v", err)
	}
	if err := os.WriteFile(filepath.Join(real, "target.pdf"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write target.pdf: %v", err)
	}
	if err := os.Symlink(filepath.Join(real, "target.pdf"), filepath.Join(real, "linked.pdf")); err != nil {
		t.Fatalf("symlink linked.pdf: %v", err)
	}
	linkRoot := filepath.Join(tmp, "linkroot")
	if err := os.Symlink(real, linkRoot); err != nil {
		t.Fatalf("symlink linkroot: %v", err)
	}

	results, _, err := walk(t.Context(), linkRoot, false, noScope())
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	got := sourceIDs(results)
	want := []string{"linked.pdf", "target.pdf"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

// --- Permission-denied subtree is skipped, walk completes ---

func TestWalk_PermissionDeniedSubdirectoryIsSkippedWalkCompletes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-bit semantics differ on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root user bypasses permission checks")
	}
	root := t.TempDir()
	mustWriteFile(t, root, "visible.pdf", []byte("x"))
	deniedDir := filepath.Join(root, "denied")
	mustWriteFile(t, root, "denied/secret.pdf", []byte("x"))
	if err := os.Chmod(deniedDir, 0o000); err != nil {
		t.Fatalf("chmod denied dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(deniedDir, 0o755) })

	results, skipped, err := walk(t.Context(), root, true, noScope())
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	got := sourceIDs(results)
	if len(got) != 1 || got[0] != "visible.pdf" {
		t.Fatalf("expected only visible.pdf (denied subtree skipped, walk completed), got %v", got)
	}
	if skipped == 0 {
		t.Error("expected a non-zero skipped-subtree count for the permission-denied subdirectory")
	}
}

// --- Root itself unreadable is an error, not an empty result ---

func TestWalk_NonExistentRootReturnsErrorNotEmptyResult(t *testing.T) {
	root := filepath.Join(t.TempDir(), "does-not-exist")

	results, _, err := walk(t.Context(), root, true, noScope())
	if err == nil {
		t.Fatal("expected an error for a non-existent root, got nil")
	}
	if results != nil {
		t.Errorf("expected nil results on error, got %v", results)
	}
}

func TestWalk_UnreadableRootReturnsErrorNotEmptyResult(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-bit semantics differ on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root user bypasses permission checks")
	}
	root := t.TempDir()
	mustWriteFile(t, root, "sub/visible.pdf", []byte("x")) // ensure root has content
	if err := os.Chmod(root, 0o000); err != nil {
		t.Fatalf("chmod root: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

	results, _, err := walk(t.Context(), root, true, noScope())
	if err == nil {
		t.Fatal("expected an error for an unreadable root, got nil")
	}
	if results != nil {
		t.Errorf("expected nil results on error, got %v", results)
	}
}

// --- Context cancellation aborts with an error, no partial set ---

func TestWalk_CancelledContextAbortsWithErrorAndNoPartialSet(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 50; i++ {
		mustWriteFile(t, root, filepathN(i), []byte("x"))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the walk even starts

	results, _, err := walk(ctx, root, true, noScope())
	if err == nil {
		t.Fatal("expected an error for a cancelled context, got nil")
	}
	if results != nil {
		t.Errorf("expected nil (no partial set) on cancellation, got %v", results)
	}
}

func filepathN(i int) string {
	return "file-" + string(rune('a'+i%26)) + ".pdf"
}

// --- Per-sync item cap ---

func TestWalk_ExceedingItemCapReturnsErrorNamingCapAndExcludeGlob(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		mustWriteFile(t, root, filepathN(i), []byte("x"))
	}

	orig := maxWalkItems
	maxWalkItems = 3
	t.Cleanup(func() { maxWalkItems = orig })

	_, _, err := walk(t.Context(), root, false, noScope())
	if err == nil {
		t.Fatal("expected an error for a tree exceeding the item cap, got nil")
	}
	if !strings.Contains(err.Error(), "3") || !strings.Contains(err.Error(), "exclude_glob") {
		t.Errorf("expected the error to name the cap and exclude_glob, got: %v", err)
	}
}

// --- Stability across consecutive calls; no carried state ---

func TestWalk_StableAcrossConsecutiveCallsDeletedFileAbsentFromSecond(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, root, "stays.pdf", []byte("x"))
	mustWriteFile(t, root, "goes.pdf", []byte("x"))

	first, _, err := walk(t.Context(), root, false, noScope())
	if err != nil {
		t.Fatalf("walk (first): %v", err)
	}
	if got := sourceIDs(first); len(got) != 2 {
		t.Fatalf("expected 2 items on first call, got %v", got)
	}

	if err := os.Remove(filepath.Join(root, "goes.pdf")); err != nil {
		t.Fatalf("remove goes.pdf: %v", err)
	}

	second, _, err := walk(t.Context(), root, false, noScope())
	if err != nil {
		t.Fatalf("walk (second): %v", err)
	}
	got := sourceIDs(second)
	if len(got) != 1 || got[0] != "stays.pdf" {
		t.Fatalf("expected only stays.pdf on second call (goes.pdf deleted, no carried state), got %v", got)
	}
}
