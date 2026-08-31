package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// disallowedSQLSelectors are database/sql selector names naming a write.
// This package uses database/sql exclusively for reads (Query/QueryRow),
// so Exec/ExecContext have no legitimate call site anywhere in this
// package's own non-test Go source — mirroring
// plugins/proton/readonly_test.go's identifier-selector-name idiom, but
// against database/sql's own read/write verb split rather than IMAP's.
var disallowedSQLSelectors = map[string]bool{
	"Exec":        true,
	"ExecContext": true,
}

// disallowedSQLSubstrings are upper-cased SQL keywords that, if found
// inside any string literal in this package's non-test Go source, mean a
// mutation was smuggled in as SQL text rather than a flagged Exec call.
// VACUUM and wal_checkpoint are named explicitly (04-02-PLAN.md Task 1):
// ROADMAP.md forbids both by name for this live, actively-written
// database, on top of the ordinary DML/DDL verbs.
var disallowedSQLSubstrings = []string{
	"INSERT ", "UPDATE ", "DELETE ", "DROP ", "ALTER ", "CREATE ", "REPLACE ", "VACUUM", "WAL_CHECKPOINT",
}

// TestPluginIssuesNoWriteShapedSQL walks the Go AST (not text: a comment
// or a string built by concatenation cannot trip or defeat this check) of
// every non-test .go file in this package's own directory and fails the
// build if any file references Exec/ExecContext, or carries a string
// literal whose upper-cased content contains a write-shaped SQL keyword.
// This is PLUG-02's mechanical enforcement for the Signal plugin
// (04-02-PLAN.md Task 1), mirroring plugins/proton/readonly_test.go's
// AST-walk mechanism and failure-message shape.
func TestPluginIssuesNoWriteShapedSQL(t *testing.T) {
	var offenses []string

	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		offenses = append(offenses, scanFileForWriteShapedSQL(t, path)...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk .: %v", err)
	}

	if len(offenses) > 0 {
		t.Fatalf(
			"PLUG-02 (04-02-PLAN.md): plugins never mutate source data stores — "+
				"found write-shaped SQL reference:\n%s",
			strings.Join(offenses, "\n"),
		)
	}

	// Negative controls: prove the scanner is not vacuous by running it
	// against fixture source strings that DO violate — through the
	// identical scan function used above, not a hand-simulated check —
	// and asserting each reports at least one offence.
	const fixtureExec = `package fixture

func mutate(db *sqlDB) {
	db.Exec("DELETE FROM messages")
}
`
	if got := scanSourceForWriteShapedSQL(t, "fixture_exec.go", fixtureExec); len(got) == 0 {
		t.Fatal("negative control (Exec selector): expected the scanner to report at least one offence against a known-violating fixture, got none — the scanner may have become vacuous")
	}

	const fixtureSQLLiteral = `package fixture

func mutate(db *sqlDB) {
	db.Query("INSERT INTO messages (body) VALUES (?)", "x")
}
`
	if got := scanSourceForWriteShapedSQL(t, "fixture_sql.go", fixtureSQLLiteral); len(got) == 0 {
		t.Fatal("negative control (write-shaped SQL string literal): expected the scanner to report at least one offence against a known-violating fixture, got none — the scanner may have become vacuous")
	}
}

// scanFileForWriteShapedSQL parses the file at path from disk and returns
// one human-readable offense string per finding.
func scanFileForWriteShapedSQL(t *testing.T, path string) []string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return scanASTForWriteShapedSQL(fset, file)
}

// scanSourceForWriteShapedSQL parses src (a fixture string, not a real
// file on disk) as filename and returns one offense string per finding,
// via the identical AST-walk core (scanASTForWriteShapedSQL)
// scanFileForWriteShapedSQL uses.
func scanSourceForWriteShapedSQL(t *testing.T, filename, src string) []string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse fixture %s: %v", filename, err)
	}
	return scanASTForWriteShapedSQL(fset, file)
}

// scanASTForWriteShapedSQL walks file's AST and flags any
// *ast.SelectorExpr whose selector name is in disallowedSQLSelectors
// (regardless of receiver), plus any *ast.BasicLit string literal whose
// upper-cased content contains a disallowedSQLSubstrings entry.
func scanASTForWriteShapedSQL(fset *token.FileSet, file *ast.File) []string {
	var offenses []string
	ast.Inspect(file, func(n ast.Node) bool {
		switch expr := n.(type) {
		case *ast.SelectorExpr:
			if disallowedSQLSelectors[expr.Sel.Name] {
				offenses = append(offenses, fmt.Sprintf(
					"%s: disallowed write-shaped SQL call %q referenced", fset.Position(expr.Pos()), expr.Sel.Name,
				))
			}
		case *ast.BasicLit:
			if expr.Kind != token.STRING {
				return true
			}
			val, unquoteErr := strconv.Unquote(expr.Value)
			if unquoteErr != nil {
				return true
			}
			upper := strings.ToUpper(val)
			for _, tok := range disallowedSQLSubstrings {
				if strings.Contains(upper, tok) {
					offenses = append(offenses, fmt.Sprintf(
						"%s: write-shaped SQL string literal contains %q: %q", fset.Position(expr.Pos()), tok, val,
					))
					break
				}
			}
		}
		return true
	})
	return offenses
}
