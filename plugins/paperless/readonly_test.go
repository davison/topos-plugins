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

// pluginsRoot is the tree this test walks, relative to this test file's own
// package directory (plugins/paperless): ".." resolves to plugins/, which
// today holds only paperless/ but will hold every future source plugin
// too. Go's test binaries always run with the package's source directory
// as their working directory, so this relative path is stable regardless
// of how "go test" is invoked (repo root via go.work, or cd'd into this
// module directly).
const pluginsRoot = ".."

// disallowedHTTPIdents are net/http package-level identifiers naming a
// non-GET method constant or a non-GET request-issuing convenience
// function. A plugin referencing any of these could issue a mutating
// request against its source system, which would violate PLUG-02: plugins
// are read-only by construction, never by convention. See
// 01-04-PLAN.md Task 1. http.MethodGet and http.MethodHead are the only
// method identifiers not listed here (Head is excluded too — this scan
// intentionally only allows the literal verb GET).
var disallowedHTTPIdents = map[string]bool{
	"MethodPost":    true,
	"MethodPut":     true,
	"MethodPatch":   true,
	"MethodDelete":  true,
	"MethodConnect": true,
	"MethodOptions": true,
	"MethodTrace":   true,
	"MethodHead":    true,
	"Post":          true, // http.Post(url, contentType, body)
	"PostForm":      true, // http.PostForm(url, data)
}

// TestPluginsIssueOnlyGetRequests walks the Go AST (not text: a comment or
// a string literal cannot trip or defeat this check) of every .go file
// under plugins/ and fails if any file references a non-GET net/http
// identifier, or constructs an http.NewRequest/http.NewRequestWithContext
// call whose literal method argument is not "GET". This is the mechanical
// half of the read-only guarantee; sdk/contract_test.go covers the other
// half at the contract level.
func TestPluginsIssueOnlyGetRequests(t *testing.T) {
	var offenses []string

	err := filepath.WalkDir(pluginsRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		offenses = append(offenses, scanFileForNonGET(t, path)...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", pluginsRoot, err)
	}

	if len(offenses) > 0 {
		t.Fatalf(
			"PLUG-02 (01-04-PLAN.md): plugins never mutate source data stores — "+
				"found non-GET HTTP request construction:\n%s",
			strings.Join(offenses, "\n"),
		)
	}
}

// scanFileForNonGET parses path and walks its AST, returning one
// human-readable offense string per finding.
func scanFileForNonGET(t *testing.T, path string) []string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var offenses []string
	ast.Inspect(file, func(n ast.Node) bool {
		switch expr := n.(type) {
		case *ast.SelectorExpr:
			if pkgIdent, ok := expr.X.(*ast.Ident); ok && pkgIdent.Name == "http" && disallowedHTTPIdents[expr.Sel.Name] {
				offenses = append(offenses, fmt.Sprintf(
					"%s: non-GET identifier http.%s referenced", fset.Position(expr.Pos()), expr.Sel.Name,
				))
			}
		case *ast.CallExpr:
			offenses = append(offenses, checkNewRequestCall(fset, expr)...)
		}
		return true
	})
	return offenses
}

// checkNewRequestCall flags http.NewRequest(method, ...) and
// http.NewRequestWithContext(ctx, method, ...) calls whose method argument
// is a string literal other than "GET". A non-literal (variable) method
// argument cannot be statically resolved and is out of this check's scope
// by design — the disallowed-identifier check above already covers the
// common case of passing a named http.MethodXxx constant.
func checkNewRequestCall(fset *token.FileSet, call *ast.CallExpr) []string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok || pkgIdent.Name != "http" {
		return nil
	}

	methodArgIdx := -1
	switch sel.Sel.Name {
	case "NewRequest":
		methodArgIdx = 0
	case "NewRequestWithContext":
		methodArgIdx = 1
	default:
		return nil
	}
	if len(call.Args) <= methodArgIdx {
		return nil
	}

	lit, ok := call.Args[methodArgIdx].(*ast.BasicLit)
	if !ok {
		return nil
	}
	val := strings.Trim(lit.Value, `"`)
	if val == "GET" {
		return nil
	}
	return []string{fmt.Sprintf(
		"%s: http.%s called with literal method %q, want \"GET\"", fset.Position(call.Pos()), sel.Sel.Name, val,
	)}
}
