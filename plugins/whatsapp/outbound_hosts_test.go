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
// scheme — mirrors plugins/signal/outbound_hosts_test.go's identical
// pattern.
var schemeAuthority = regexp.MustCompile(`^(?:https?|wss?)://`)

// outboundHTTPIdents/outboundHTTPTypes are net/http identifiers that
// construct or issue an outbound request. This plugin has NO legitimate
// use for any of them: its only remote-shaped traffic is whatsmeow's own
// WebSocket transport (not net/http-shaped, and not this package's own
// construction — whatsmeow dials it internally), so the assertion this
// package can honestly make is the same EMPTY-set assertion Signal's own
// variant makes for net/http specifically, even though (unlike Signal)
// this plugin is NOT a zero-outbound-host plugin overall — see
// allowedDeepLinkURLLiterals below for the one narrow exception.
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

// allowedDeepLinkURLLiterals is the ONE narrow exception to the
// zero-outbound-host-literal rule: deeplink.go returns these two
// WhatsApp-documented click-to-chat URLs as INERT DATA the user must
// click — this process never dials them itself (mirrors
// internal/audit/outbound_hosts_test.go's own sanctionedDeepLinkLiteralFiles
// distinction, "permitted to RETURN a foreign URL" vs "permitted to DIAL
// a foreign host," established by Plan 08-01's deep-link correction).
// Every OTHER absolute network-scheme URL literal in this package fails
// the build — a telemetry or analytics endpoint added later cannot be
// silently allowlisted alongside these two without editing this map by
// name, in a reviewed diff.
var allowedDeepLinkURLLiterals = map[string]bool{
	"https://wa.me/":            true,
	"https://web.whatsapp.com/": true,
}

// TestOutboundHosts_NoSelfConstructedHTTPClientOrUnlistedHostLiteral walks
// every non-test .go file in this package's own directory via the AST for
// (1) any net/http request-construction identifier — this package must
// construct ZERO outbound HTTP clients of its own, full stop — and (2) any
// string literal carrying an absolute network-scheme URL that is NOT in
// allowedDeepLinkURLLiterals. Mirrors plugins/signal/outbound_hosts_test.go's
// scan mechanism; the assertion narrows (not widens) Signal's zero-host
// rule to accommodate the one legitimate, already-audited deep-link
// exception instead of the empty set.
func TestOutboundHosts_NoSelfConstructedHTTPClientOrUnlistedHostLiteral(t *testing.T) {
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
			"this plugin permits ZERO self-constructed net/http clients and ZERO "+
				"non-allowlisted absolute network-scheme URL literals — only "+
				"deeplink.go's two WhatsApp click-to-chat URLs (allowedDeepLinkURLLiterals) "+
				"are permitted, and only as inert returned data, never dialed by this "+
				"process itself:\n%s",
			strings.Join(offenses, "\n"),
		)
	}

	// Negative control: prove the scanner is not vacuous — a synthetic
	// third-party host literal must be rejected.
	const fixture = `package fixture

import "net/http"

func call() {
	http.Get("https://exfil.example.invalid")
}
`
	if got := scanSourceForOutboundHost(t, "fixture.go", fixture); len(got) == 0 {
		t.Fatal("negative control: expected the scanner to report at least one offence against a known-violating fixture, got none — the scanner may have become vacuous")
	}

	// A second negative control targeting the allowlist mechanism itself:
	// a host literal that resembles an allowed one but is NOT byte-for-byte
	// in allowedDeepLinkURLLiterals must still be rejected — proving the
	// allowlist is an exact-match gate, not a prefix/substring bypass.
	const fixtureLookalike = `package fixture

const evil = "https://wa.me.exfil.example.invalid/"
`
	if got := scanSourceForOutboundHost(t, "fixture_lookalike.go", fixtureLookalike); len(got) == 0 {
		t.Fatal("negative control (lookalike host): expected the scanner to reject a non-allowlisted lookalike host literal, got none — the allowlist may have become a prefix match")
	}
}

func scanFileForOutboundHost(t *testing.T, path string) []string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return scanASTForOutboundHosts(fset, file)
}

func scanSourceForOutboundHost(t *testing.T, filename, src string) []string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse fixture %s: %v", filename, err)
	}
	return scanASTForOutboundHosts(fset, file)
}

// scanASTForOutboundHosts is this package's own AST-walk core
// (scanASTForDisallowedClientCalls in readonly_test.go is the sibling
// scan for send-capable Client selectors — kept as two separate functions
// per file, matching Signal's own one-scanner-per-concern convention).
func scanASTForOutboundHosts(fset *token.FileSet, file *ast.File) []string {
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
			if schemeAuthority.MatchString(val) && !allowedDeepLinkURLLiterals[val] {
				offenses = append(offenses, fmt.Sprintf(
					"%s: absolute network-scheme URL literal %q is not in allowedDeepLinkURLLiterals", fset.Position(expr.Pos()), val,
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
