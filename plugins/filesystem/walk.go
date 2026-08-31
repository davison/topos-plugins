package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// maxWalkItems is the fixed per-sync item cap (12-CONTEXT.md Claude's
// Discretion, 12-03-PLAN.md Task 2): bounds a single walk's response so a
// pathologically large tree fails with a named, actionable error rather
// than silently returning a truncated set — the kernel's
// ReplaceWebspaceSourceItems treats whatever walk returns as the complete
// truth, so a silent truncation would delete real items on the next sync.
// A package-level var, not a const, so tests can shrink it rather than
// materializing 25,000 fixture files to exercise the cap.
var maxWalkItems = 25000

// walkResult is one candidate file found during a walk: its D-01
// forward-slash relative source_id, and the os.FileInfo already stat'd so
// Match never re-stats it.
type walkResult struct {
	sourceID string
	info     os.FileInfo
}

// walk traverses root — every depth when recursive is true, the root's own
// top level only otherwise — applying sc's include/exclude/hidden policy
// to every candidate, and returns the COMPLETE current set (never a
// partial one) plus a count of subtrees skipped for a permission error
// (for the caller to log; never fatal on its own).
//
// Policy decisions this function commits to (12-CONTEXT.md Claude's
// Discretion items, settled here so a later reader does not have to
// guess):
//
//   - Recursion off means the root's own directory entries only; on means
//     every depth. Built on filepath.WalkDir (never the older
//     filepath.Walk).
//   - A directory entry whose name begins with a dot is not descended
//     into UNLESS this instance declares at least one include_glob
//     pattern (which might reach a file inside it) — and even then, an
//     individual dot-prefixed candidate (file or path segment) is only
//     kept when it explicitly matches an include_glob pattern, never via
//     the default extension allowlist alone. This keeps version-control
//     and application-metadata directories out of a document folder by
//     default, with no special-cased name list.
//   - A symlinked directory is never descended into — filepath.WalkDir
//     already never auto-follows a symlink, so this closes the
//     ancestor-symlink-loop class structurally rather than by detecting
//     cycles, and prevents an in-tree link from silently widening the
//     folder the operator consented to expose (T-12-12). A symlinked
//     regular file IS classified and included like any other file, but
//     its resolved real path must still be inside the RESOLVED configured
//     root before it becomes an item (T-12-12 again, for the file case).
//     A configured root which is itself a symlink or bind mount (the
//     common `~/Documents` -> `~/dotfiles/Documents` dotfile-manager
//     pattern) is fully supported: the root is resolved once before the
//     walk begins, so its in-tree symlinked files are included rather
//     than silently dropped (WR-01).
//   - A per-entry error (most often permission denied on a shared mount
//     with mixed ACLs) skips that entry or subtree and continues; it
//     never aborts the walk. An error reading the root itself DOES abort
//     — that is the mount-is-gone case and must not look like an empty
//     folder.
//   - Context cancellation aborts with an error. Never a partial set.
//   - Exceeding maxWalkItems is an error naming the cap and pointing at
//     exclude_glob, never a silent truncation.
//
// No persisted state is read or written anywhere in this function or its
// caller: the freshness bound for a network mount is the sync interval,
// not a plugin-side cache that could itself get stuck on a stale
// attribute (12-RESEARCH.md Pitfall 4).
func walk(ctx context.Context, root string, recursive bool, sc *scope) ([]walkResult, int, error) {
	if _, err := os.Stat(root); err != nil {
		return nil, 0, fmt.Errorf("filesystem: read root: %w", err)
	}

	cleanRoot := filepath.Clean(root)
	// Resolved ONCE per walk, before filepath.WalkDir is entered, never per
	// entry (WR-01, 12-06-PLAN.md Task 3). This resolved root serves two
	// purposes: it is the base the in-tree symlink containment check below
	// compares a symlink's resolved target against (instead of the merely
	// lexical cleanRoot); and it is the path filepath.WalkDir itself is
	// started from, because WalkDir uses Lstat semantics on the ROOT
	// argument too — if root itself is a symlink, WalkDir sees a
	// non-directory at the top and never descends into it at all, however
	// the walk otherwise handles in-tree symlinks. Without resolving root
	// before starting the walk, a configured root that is itself a symlink
	// or bind mount (the common `~/Documents` -> `~/dotfiles/Documents`
	// dotfile-manager pattern) would silently walk NOTHING, not just drop
	// its symlinked files. relPathSourceID is computed against this same
	// resolved base, so source_id stays a correct forward-slash relative
	// path for every file actually visited — the relative subtree
	// structure is identical whichever name (configured or resolved) the
	// top of the tree is addressed by. resolveRoot is defined in item.go,
	// shared by this package.
	resolvedRoot := resolveRoot(cleanRoot)
	var (
		results []walkResult
		skipped int
	)

	walkErr := filepath.WalkDir(resolvedRoot, func(path string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		if err != nil {
			if path == resolvedRoot {
				// The root itself failed to read — the mount-is-gone
				// case; must abort rather than present as an empty
				// folder.
				return err
			}
			skipped++
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		if path == resolvedRoot {
			return nil
		}

		relPath := relPathSourceID(resolvedRoot, path)
		isSymlink := d.Type()&fs.ModeSymlink != 0

		if d.IsDir() {
			// A REAL (non-symlink) directory — WalkDir uses Lstat
			// semantics, so a symlink pointing at a directory lands in
			// the isSymlink branch below, never here.
			if !recursive {
				return fs.SkipDir
			}
			if isHiddenPath(relPath) && len(sc.includePatterns) == 0 {
				return fs.SkipDir
			}
			return nil
		}

		if isSymlink {
			target, statErr := os.Stat(path) // follows the link
			if statErr != nil {
				// Dangling symlink: skip, don't fail the walk.
				skipped++
				return nil
			}
			if target.IsDir() {
				// A symlinked directory: WalkDir already never
				// descends into it (Lstat semantics); it is also never
				// itself a file candidate.
				return nil
			}
			real, evalErr := filepath.EvalSymlinks(path)
			if evalErr != nil {
				skipped++
				return nil
			}
			if real != resolvedRoot && !strings.HasPrefix(real, resolvedRoot+string(filepath.Separator)) {
				// Resolves outside the RESOLVED configured root: never
				// widen the folder the operator consented to expose
				// (T-12-12). Comparing against resolvedRoot rather than
				// the merely lexical cleanRoot is what lets a symlinked or
				// bind-mounted root's own in-tree symlinked files through
				// (WR-01) while still refusing a genuine escape.
				return nil
			}
		}

		if isHiddenPath(relPath) {
			if len(sc.includePatterns) == 0 {
				return nil
			}
			matched, matchErr := matchesAny(sc.includePatterns, relPath)
			if matchErr != nil {
				return matchErr
			}
			if !matched {
				return nil
			}
		}

		_, included, incErr := sc.includes(relPath)
		if incErr != nil {
			return incErr
		}
		if !included {
			return nil
		}

		info, infoErr := d.Info()
		if infoErr != nil {
			// A file that vanished or became unreadable between the
			// directory read and Info() is skipped for this sync — the
			// next poll picks it back up if it reappears (stat-diff via
			// full-replace).
			skipped++
			return nil
		}

		if len(results) >= maxWalkItems {
			return fmt.Errorf("filesystem: tree exceeds the %d-item per-sync cap; narrow scope with exclude_glob", maxWalkItems)
		}

		results = append(results, walkResult{sourceID: relPath, info: info})
		return nil
	})
	if walkErr != nil {
		return nil, 0, walkErr
	}

	return results, skipped, nil
}

// isHiddenPath reports whether relPath (a forward-slash, D-01 source_id
// shaped path) contains any dot-prefixed path segment — the default
// hidden-file/hidden-directory policy this file's doc comment states.
func isHiddenPath(relPath string) bool {
	for _, seg := range strings.Split(relPath, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}
