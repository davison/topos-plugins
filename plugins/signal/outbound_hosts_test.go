package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// schemeAuthority matches an absolute URL literal carrying a network
// scheme — mirrors internal/audit/outbound_hosts_test.go's identical
// pattern.
var schemeAuthority = regexp.MustCompile(`^(?:https?|wss?)://`)

// outboundHTTPIdents/outboundHTTPTypes are net/http identifiers that
// construct or issue an outbound request. This plugin has no legitimate
// use for any of them — its only remote-shaped call is a D-Bus round-trip
// on the local session bus (secretservice.go), which is not a network
// dial.
var outboundHTTPIdents = map[string]bool{
	"Get":                   true,
	"Head":                  true,
	"Post":                  true,
	"PostForm":              true,
	"NewRequest":            true,
	"NewRequestWithContext": true,
	"DefaultClient":         true,
	"DefaultTransport":      true,
}

var outboundHTTPTypes = map[string]bool{
	"Client":    true,
	"Transport": true,
}

// TestNoOutboundNetworkHosts inverts plugins/proton/outbound_hosts_test.go:
// proton asserts an allowlist of one permitted host, this plugin asserts
// the empty set. Walks every non-test .go file in this package's own
// directory via the AST for net/http request-construction identifiers and
// for any string literal carrying an absolute URL with a network scheme,
// and fails on any hit. The repo-wide scan in
// internal/audit/outbound_hosts_test.go must continue to pass without
// this package appearing in sanctionedEgressFiles.
func TestNoOutboundNetworkHosts(t *testing.T) {
	var offenses []string

	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		offenses = append(offenses, scanFileForOutboundHost(t, path)...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk .: %v", err)
	}

	if len(offenses) > 0 {
		t.Fatalf(
			"this plugin permits ZERO outbound network hosts — the only remote-shaped call it "+
				"ever makes is a D-Bus round-trip on the local session bus (secretservice.go), which "+
				"is not a network dial:\n%s",
			strings.Join(offenses, "\n"),
		)
	}

	// Negative control: prove the scanner is not vacuous.
	const fixture = `package fixture

import "net/http"

func call() {
	http.Get("https://exfil.example.invalid")
}
`
	if got := scanSourceForOutboundHost(t, "fixture.go", fixture); len(got) == 0 {
		t.Fatal("negative control: expected the scanner to report at least one offence against a known-violating fixture, got none — the scanner may have become vacuous")
	}
}

func scanFileForOutboundHost(t *testing.T, path string) []string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return scanASTForOutboundHost(fset, file)
}

func scanSourceForOutboundHost(t *testing.T, filename, src string) []string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse fixture %s: %v", filename, err)
	}
	return scanASTForOutboundHost(fset, file)
}

func scanASTForOutboundHost(fset *token.FileSet, file *ast.File) []string {
	var offenses []string
	ast.Inspect(file, func(n ast.Node) bool {
		switch expr := n.(type) {
		case *ast.BasicLit:
			if expr.Kind != token.STRING {
				return true
			}
			val, unquoteErr := strconv.Unquote(expr.Value)
			if unquoteErr != nil {
				return true
			}
			if schemeAuthority.MatchString(val) {
				offenses = append(offenses, fmt.Sprintf(
					"%s: absolute network-scheme URL literal %q", fset.Position(expr.Pos()), val,
				))
			}
		case *ast.SelectorExpr:
			if pkgIdent, ok := expr.X.(*ast.Ident); ok && pkgIdent.Name == "http" && outboundHTTPIdents[expr.Sel.Name] {
				offenses = append(offenses, fmt.Sprintf(
					"%s: outbound HTTP construction http.%s referenced", fset.Position(expr.Pos()), expr.Sel.Name,
				))
			}
		case *ast.CompositeLit:
			sel, ok := expr.Type.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkgIdent, ok := sel.X.(*ast.Ident); ok && pkgIdent.Name == "http" && outboundHTTPTypes[sel.Sel.Name] {
				offenses = append(offenses, fmt.Sprintf(
					"%s: outbound HTTP construction http.%s{} referenced", fset.Position(expr.Pos()), sel.Sel.Name,
				))
			}
		}
		return true
	})
	return offenses
}
