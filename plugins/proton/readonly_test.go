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

// disallowedIMAPIdents are github.com/emersion/go-imap/client identifiers
// naming a mutating IMAP command — message-mutating (Store/its UID variant,
// Expunge, Move/its UID variant, Append, Copy/its UID variant) and
// mailbox-mutating (Create, Delete, Rename, Subscribe, Unsubscribe). A
// selector matching any of these names anywhere in this package's non-test
// Go source is a PLUG-02 violation: this plugin is read-only by
// construction, never by convention, and IMAP's own mutating commands are
// not caught by the repo-wide net/http AST scan
// (plugins/paperless/readonly_test.go), because IMAP does not use net/http
// at all (03-RESEARCH.md "Don't Hand-Roll").
//
// The scan below matches on selector name regardless of receiver type or
// package — this is deliberate (a variable renamed or re-aliased cannot
// silently defeat the check) but means this package's own non-test Go
// files must avoid two stdlib identifiers that collide with this set:
// io.Copy (use io.ReadAll over an io.LimitReader instead, as body.go
// already does) and the "maps" package's Delete helper (use the builtin
// lowercase map-delete operator, delete(m, k), instead). Both constraints
// are already satisfied by this package's existing production code.
var disallowedIMAPIdents = map[string]bool{
	"Store":       true,
	"UidStore":    true,
	"Expunge":     true,
	"Move":        true,
	"UidMove":     true,
	"Append":      true,
	"Delete":      true,
	"Copy":        true,
	"UidCopy":     true,
	"Create":      true,
	"Rename":      true,
	"Subscribe":   true,
	"Unsubscribe": true,
}

// TestPluginIssuesNoIMAPMutatingCommands walks the Go AST (not text: a
// comment or a string literal cannot trip or defeat this check) of every
// non-test .go file in this package's own directory and fails the build if
// any file references a disallowed IMAP-mutating identifier. This is the
// IMAP-specific mechanical enforcement of PLUG-02 (03-RESEARCH.md "Don't
// Hand-Roll"), mirroring plugins/paperless/readonly_test.go's AST-walk
// mechanism and failure-message shape but targeting IMAP mutating methods
// instead of non-GET net/http identifiers.
func TestPluginIssuesNoIMAPMutatingCommands(t *testing.T) {
	var offenses []string

	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		offenses = append(offenses, scanFileForIMAPMutation(t, path)...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk .: %v", err)
	}

	if len(offenses) > 0 {
		t.Fatalf(
			"PLUG-02 (03-02-PLAN.md): plugins never mutate source data stores — "+
				"found IMAP-mutating identifier reference:\n%s",
			strings.Join(offenses, "\n"),
		)
	}

	// Negative control: prove the scanner is not vacuous by running it
	// against a fixture source string that DOES reference a disallowed
	// identifier — through the identical scan function used above, not a
	// hand-simulated check — and asserting it reports at least one
	// offence. If this ever silently starts returning zero offences, the
	// scanner itself is broken, not just this package's production code.
	const fixture = `package fixture

func mutate(c *imapClient) {
	c.Store(nil, "", nil, nil)
}
`
	if got := scanSourceForIMAPMutation(t, "fixture.go", fixture); len(got) == 0 {
		t.Fatal("negative control: expected the scanner to report at least one offence against a known-violating fixture, got none — the scanner may have become vacuous")
	}
}

// scanFileForIMAPMutation parses the file at path from disk and returns one
// human-readable offense string per disallowed-identifier finding.
func scanFileForIMAPMutation(t *testing.T, path string) []string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return scanASTForIMAPMutation(fset, file)
}

// scanSourceForIMAPMutation parses src (a fixture string, not a real file
// on disk) as filename and returns one offense string per finding, via the
// identical AST-walk core (scanASTForIMAPMutation) scanFileForIMAPMutation
// uses — the negative control above exercises this exact code path.
func scanSourceForIMAPMutation(t *testing.T, filename, src string) []string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse fixture %s: %v", filename, err)
	}
	return scanASTForIMAPMutation(fset, file)
}

// scanASTForIMAPMutation walks file's AST and flags any *ast.SelectorExpr
// whose selector name is in disallowedIMAPIdents, regardless of receiver.
func scanASTForIMAPMutation(fset *token.FileSet, file *ast.File) []string {
	var offenses []string
	ast.Inspect(file, func(n ast.Node) bool {
		if expr, ok := n.(*ast.SelectorExpr); ok && disallowedIMAPIdents[expr.Sel.Name] {
			offenses = append(offenses, fmt.Sprintf(
				"%s: disallowed IMAP-mutating identifier %q referenced", fset.Position(expr.Pos()), expr.Sel.Name,
			))
		}
		return true
	})
	return offenses
}
