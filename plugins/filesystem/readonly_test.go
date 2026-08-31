package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// disallowedOSSelectors are os-package selector names naming a
// filesystem-write operation. This plugin reads exclusively (os.Open,
// os.ReadFile, os.ReadDir, os.Stat, os.Lstat, filepath.WalkDir — none of
// which appear here), so none of the names below has a legitimate call
// site anywhere in this package's own non-test Go source — mirroring
// plugins/signal/readonly_test.go's identifier-selector-name idiom
// (structure) and plugins/paperless/readonly_test.go's package-qualified
// selector check (a call is only flagged when its receiver is the "os"
// package identifier itself, since these are package-level functions, not
// methods on some other type that merely happens to share a name).
// os.OpenFile is included: unlike os.Open (read-only), OpenFile can be
// given write-capable flags — this plugin has no legitimate need for it
// at all, read-only or otherwise, since os.Open already covers every
// read this package performs.
var disallowedOSSelectors = map[string]bool{
	"WriteFile": true,
	"Remove":    true,
	"RemoveAll": true,
	"Create":    true,
	"OpenFile":  true,
	"Rename":    true,
	"Mkdir":     true,
	"MkdirAll":  true,
	"Chmod":     true,
	"Chown":     true,
	"Truncate":  true,
	"Symlink":   true,
	"Link":      true,
}

// TestPluginIssuesNoWrite walks the Go AST (not text: a comment or a
// string built by concatenation cannot trip or defeat this check) of
// every non-test .go file in this package's own directory and fails the
// build if any file references an os-package write selector. This is
// PLUG-02's mechanical enforcement for the filesystem plugin
// (12-03-PLAN.md Task 3), mirroring plugins/signal/readonly_test.go's
// AST-walk mechanism, failure-message shape and negative-control
// discipline. Scoped to this package's own directory (the signal
// precedent), not the whole plugins tree — the repo-wide scans in
// internal/audit already cover cross-plugin concerns (outbound egress);
// this plugin makes no outbound requests at all, so it needs no entry in
// that allowlist either.
func TestPluginIssuesNoWrite(t *testing.T) {
	var offenses []string

	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		offenses = append(offenses, scanFileForOSWrite(t, path)...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk .: %v", err)
	}

	if len(offenses) > 0 {
		t.Fatalf(
			"PLUG-02 (12-03-PLAN.md): plugins never mutate source data stores — "+
				"found write-shaped os-package reference:\n%s",
			strings.Join(offenses, "\n"),
		)
	}

	// Negative controls: prove the scanner is not vacuous by running it
	// against fixture source strings that DO violate — through the
	// identical scan function used above, not a hand-simulated check —
	// and asserting each reports at least one offence.
	const fixtureWriteFile = `package fixture

import "os"

func mutate() {
	os.WriteFile("/tmp/x", nil, 0o644)
}
`
	if got := scanSourceForOSWrite(t, "fixture_writefile.go", fixtureWriteFile); len(got) == 0 {
		t.Fatal("negative control (os.WriteFile selector): expected the scanner to report at least one offence against a known-violating fixture, got none — the scanner may have become vacuous")
	}

	const fixtureRemove = `package fixture

import "os"

func mutate() {
	os.Remove("/tmp/x")
}
`
	if got := scanSourceForOSWrite(t, "fixture_remove.go", fixtureRemove); len(got) == 0 {
		t.Fatal("negative control (os.Remove selector): expected the scanner to report at least one offence against a known-violating fixture, got none — the scanner may have become vacuous")
	}
}

// scanFileForOSWrite parses the file at path from disk and returns one
// human-readable offense string per finding, naming the file, position
// and selector so the failure is actionable.
func scanFileForOSWrite(t *testing.T, path string) []string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return scanASTForOSWrite(fset, file)
}

// scanSourceForOSWrite parses src (a fixture string, not a real file on
// disk) as filename and returns one offense string per finding, via the
// identical AST-walk core (scanASTForOSWrite) scanFileForOSWrite uses.
func scanSourceForOSWrite(t *testing.T, filename, src string) []string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse fixture %s: %v", filename, err)
	}
	return scanASTForOSWrite(fset, file)
}

// scanASTForOSWrite walks file's AST and flags any *ast.SelectorExpr
// whose receiver is the "os" package identifier and whose selector name
// is in disallowedOSSelectors. The read-only open call (os.Open), the
// stat calls (os.Stat, os.Lstat) and the directory-listing call
// (os.ReadDir) are all legitimate and deliberately absent from
// disallowedOSSelectors, so none of them is ever flagged.
func scanASTForOSWrite(fset *token.FileSet, file *ast.File) []string {
	var offenses []string
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkgIdent, ok := sel.X.(*ast.Ident)
		if !ok || pkgIdent.Name != "os" {
			return true
		}
		if disallowedOSSelectors[sel.Sel.Name] {
			offenses = append(offenses, fmt.Sprintf(
				"%s: disallowed write-shaped os-package call os.%s referenced", fset.Position(sel.Pos()), sel.Sel.Name,
			))
		}
		return true
	})
	return offenses
}
