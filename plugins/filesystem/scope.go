package main

import (
	"fmt"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// scope resolves which files a single filesystem source instance includes,
// and which classification each included file gets, from the extras map
// the launch envelope carries (D-03, 12-CONTEXT.md). Task 1's
// checkpoint:human-verify gate approved github.com/bmatcuk/doublestar/v4
// pinned at v4.10.0 for its arbitrary-depth "**" glob support, which
// stdlib path/filepath.Match does not have.
type scope struct {
	includePatterns []string
	excludePatterns []string
}

// newScope parses extras["include_glob"] and extras["exclude_glob"] — each
// a single comma-separated string (extras values are string-only, D-13,
// so this plugin owns the split) — into pattern lists, trimming whitespace
// and dropping empty segments. Compile-once-per-Match-call discipline: a
// caller builds exactly one *scope per Match invocation and reuses it for
// every candidate file, never re-splitting the extras strings per file.
func newScope(extras map[string]string) *scope {
	return &scope{
		includePatterns: splitGlobList(extras["include_glob"]),
		excludePatterns: splitGlobList(extras["exclude_glob"]),
	}
}

func splitGlobList(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	for _, seg := range strings.Split(raw, ",") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		out = append(out, seg)
	}
	return out
}

// includes reports whether relPath (a forward-slash path relative to the
// source root — the same D-01 shape as an item's source_id) is included in
// this instance's scope, and the classification decided for it.
// Resolution order (12-02-PLAN.md Task 2 behavior): exclude first, then
// include-if-declared (which REPLACES the extension test entirely, not
// widens it), then the default extension allowlist alone. An unknown
// extension included only via include_glob classifies as metadata-only —
// never a guessed mime type. A malformed pattern surfaces as a named
// error rather than silently matching everything or nothing.
func (s *scope) includes(relPath string) (classification, bool, error) {
	excluded, err := matchesAny(s.excludePatterns, relPath)
	if err != nil {
		return classification{}, false, err
	}
	if excluded {
		return classification{}, false, nil
	}

	c, knownExt := classify(relPath)

	if len(s.includePatterns) > 0 {
		included, err := matchesAny(s.includePatterns, relPath)
		if err != nil {
			return classification{}, false, err
		}
		if !included {
			return classification{}, false, nil
		}
		if !knownExt {
			return classification{kind: previewKindMetadataOnly}, true, nil
		}
		return c, true, nil
	}

	if !knownExt {
		return classification{}, false, nil
	}
	return c, true, nil
}

// matchesAny reports whether relPath matches any pattern in patterns,
// using doublestar's "**" (arbitrary-depth) glob semantics. A malformed
// pattern is wrapped with the offending pattern text and returned as an
// error rather than treated as a non-match.
func matchesAny(patterns []string, relPath string) (bool, error) {
	for _, p := range patterns {
		ok, err := doublestar.Match(p, relPath)
		if err != nil {
			return false, fmt.Errorf("filesystem: malformed glob pattern %q: %w", p, err)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}
