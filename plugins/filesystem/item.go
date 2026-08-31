package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// relPathSourceID returns full's path relative to root, using forward
// slashes and never a leading "./" or an absolute path (D-01, 12-CONTEXT.md)
// — the per-source stable item identity. full must be inside root; if
// filepath.Rel fails (a genuinely unrelated path — never expected in normal
// operation since every caller derives full from a root-rooted walk), the
// bare file name is returned as a defensive fallback rather than panicking.
func relPathSourceID(root, full string) string {
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return filepath.Base(full)
	}
	return filepath.ToSlash(rel)
}

// folderLabels returns the D-05 folder-vocabulary labels for full. Every
// file — top-level or nested, at any depth — carries the configured root's
// own base name FIRST: naming that value in a webspace's `folders` match
// block (or the keywords fallback) is how an operator says "everything
// from this instance", which was structurally impossible for a recursive
// source before 12-08-PLAN.md (gap ids G-12-1, G-12-3) — no single value
// could previously cover both a top-level file and a nested one. A
// top-level file's label set stays byte-identical to before this change:
// exactly one label, the root's own base name. A nested file additionally
// carries one label per containing-directory path segment, plus the
// cumulative relative directory path when more than one segment exists —
// so a match block can still name a bare subfolder or a full relative path
// exactly as 12-03-PLAN.md shipped. Ordering is part of the contract: root
// base name first, then segments, then the cumulative path. A file at
// "receipts/2026/inv.pdf" under a root named "household-docs" carries the
// labels "household-docs", "receipts", "2026" and "receipts/2026". No
// label ever names a directory ABOVE the configured root — every label is
// derived from the cleaned root itself or from filepath.Rel(root, dir),
// which is structurally incapable of naming an ancestor. The result passes
// through dedupeLabels so a subfolder sharing the root's own name (e.g.
// root "docs" containing "docs/") reports that value once, not twice.
func folderLabels(root, full string) []string {
	cleanRoot := filepath.Clean(root)
	labels := []string{filepath.Base(cleanRoot)}

	dir := filepath.Dir(full)
	if dir == cleanRoot {
		return dedupeLabels(labels)
	}

	relDir, err := filepath.Rel(cleanRoot, dir)
	if err != nil {
		labels = append(labels, filepath.Base(dir))
		return dedupeLabels(labels)
	}
	relDir = filepath.ToSlash(relDir)

	segments := strings.Split(relDir, "/")
	labels = append(labels, segments...)
	if len(segments) > 1 {
		labels = append(labels, relDir)
	}
	return dedupeLabels(labels)
}

// dedupeLabels drops later case-insensitive duplicates from labels while
// preserving both order and the FIRST occurrence's own spelling — so a
// subfolder named like its root (or any other label collision) reports one
// value rather than two identical-looking chips in the UI (12-08-PLAN.md
// Task 1, G-12-1/G-12-3).
func dedupeLabels(labels []string) []string {
	seen := make(map[string]struct{}, len(labels))
	result := make([]string, 0, len(labels))
	for _, l := range labels {
		key := strings.ToLower(l)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, l)
	}
	return result
}

// fileDeepLink builds a file:// URI over the real absolute path (root
// joined with sourceID, a D-01 relative path) — the mechanism the Task 1
// checkpoint locked (option-a, 12-RESEARCH.md Architecture Pattern 3): the
// kernel rewrites this to the loopback open route at serve time
// (kernel/httpapi/stream.go's resolveStreamLinkURL), keyed on the URL
// scheme alone, never on source_type.
func fileDeepLink(root, sourceID string) string {
	full := filepath.Join(root, filepath.FromSlash(sourceID))
	return "file://" + filepath.ToSlash(full)
}

// resolveRoot resolves root's real, symlink-free path with
// filepath.EvalSymlinks, falling back to the lexically cleaned root when
// resolution fails (a root that does not exist yet, or is unreadable) — the
// containment comparison in resolvePath and walk.go must still have a value
// to compare against, and the lexical clean is the conservative one. Hand-
// kept twin of kernel/httpapi/fsopen.go's identically-named, identically-
// behaved helper: plugins/filesystem is a separate Go module from the
// kernel and cannot import it, so this discipline is duplicated by hand,
// exactly like resolvePath's own lexical guard used to be duplicated before
// this task. D-01's relative-path sourceID is what makes the join in
// resolvePath meaningful in the first place — resolveRoot only ever
// resolves the CONFIGURED root, never a joined path.
func resolveRoot(root string) string {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return filepath.Clean(root)
	}
	return resolved
}

// resolvePath joins root with sourceID (a D-01 relative path), re-validates
// the result is still lexically inside root (refusing an escape via ".."
// segments as a cheap first refusal ahead of any filesystem I/O), and then
// re-checks containment a second time against the RESOLVED path and the
// RESOLVED root — inside root after symlink resolution, failing closed when
// resolution is impossible (CR-02, 12-06-PLAN.md Task 1). This closes the
// gap the lexical check alone cannot: a file indexed as legitimate and then
// swapped on disk for a symlink pointing outside root.
//
// Returns TWO paths naming the same file: full is the LEXICAL identity path
// — callers, the index, fileDeepLink and the kernel's own join all key on
// this one, and it must never start reporting resolved paths (D-01) —
// resolved is the symlink-free real path every read/exec should target, so
// the path validated and the path used are one and the same (WR-02,
// 12-07-PLAN.md Task 2: closes the gap where resolvePath validated the
// resolved path but callers then read/exec'd the lexical one, leaving a
// narrow window for the final path component to be swapped between
// validation and the syscall). A resolution failure is wrapped with %w so
// callers (fetch.go's fetchByKind) can distinguish a vanished file
// (errors.Is(err, fs.ErrNotExist)) from a genuine containment escape; on any
// error both results are empty strings, so a caller that ignores the error
// cannot accidentally use a half-validated path. Mirrors
// kernel/httpapi/fsopen.go's identical guard on the kernel side of this
// same join.
func resolvePath(root, sourceID string) (full, resolved string, err error) {
	cleanRoot := filepath.Clean(root)
	full = filepath.Join(cleanRoot, filepath.FromSlash(sourceID))
	if full != cleanRoot && !strings.HasPrefix(full, cleanRoot+string(filepath.Separator)) {
		return "", "", fmt.Errorf("resolved path escapes source root")
	}

	resolved, err = filepath.EvalSymlinks(full)
	if err != nil {
		return "", "", fmt.Errorf("resolve path: %w", err)
	}
	resolvedRoot := resolveRoot(cleanRoot)
	if resolved != resolvedRoot && !strings.HasPrefix(resolved, resolvedRoot+string(filepath.Separator)) {
		return "", "", fmt.Errorf("resolved path escapes source root")
	}

	return full, resolved, nil
}
