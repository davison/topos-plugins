package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/store/sqlstore"
)

// TestWhatsmeowSessionDSN_EnablesForeignKeys proves the DSN both link.go
// and connect.go build for whatsmeow's own sqlstore carries the
// modernc.org/sqlite pragma syntax (`_pragma=foreign_keys(1)`) — NOT the
// `_foreign_keys=on` shorthand whatsmeow's own doc comment illustrates,
// which is a DIFFERENT sqlite driver's DSN convention and is silently
// ignored by modernc.org/sqlite, leaving foreign keys off and
// sqlstore.Container.Upgrade refusing to run (observed live, 2026-08-10:
// "failed to upgrade database: foreign keys are not enabled").
func TestWhatsmeowSessionDSN_EnablesForeignKeys(t *testing.T) {
	dsn := whatsmeowSessionDSN("/tmp/example/whatsmeow.db")

	if !strings.Contains(dsn, "_pragma=foreign_keys(1)") {
		t.Fatalf("whatsmeowSessionDSN() = %q, want it to contain modernc.org/sqlite's _pragma=foreign_keys(1) syntax", dsn)
	}
	if strings.Contains(dsn, "_foreign_keys=on") {
		t.Fatalf("whatsmeowSessionDSN() = %q, contains the WRONG (mattn/go-sqlite3-style) _foreign_keys=on shorthand, which modernc.org/sqlite silently ignores", dsn)
	}
}

// TestWhatsmeowSessionDSN_MigrationsRunAgainstRealSQLStore actually opens
// whatsmeow's own sqlstore.New against a fresh temp-file database using
// whatsmeowSessionDSN — the same call link.go's runLinkCLI and
// connect.go's startBackgroundClient both make. This is the regression
// test for the live failure a real -link run hit: a wrong DSN fails HERE,
// at Container.Upgrade's own foreign-keys precondition check, without
// needing a phone or a network connection to reproduce.
func TestWhatsmeowSessionDSN_MigrationsRunAgainstRealSQLStore(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "whatsmeow.db")

	container, err := sqlstore.New(context.Background(), "sqlite", whatsmeowSessionDSN(dbPath), newPluginLogger("whatsmeow/test"))
	if err != nil {
		t.Fatalf("sqlstore.New with whatsmeowSessionDSN: %v (this is exactly the failure mode a wrong DSN produces: 'failed to upgrade database: foreign keys are not enabled')", err)
	}
	defer container.Close()

	// GetFirstDevice on a brand-new store creates and persists a fresh,
	// unlinked device row — proving the migrated schema is actually
	// usable, not just that Upgrade returned nil.
	if _, err := container.GetFirstDevice(context.Background()); err != nil {
		t.Fatalf("GetFirstDevice against freshly migrated store: %v", err)
	}
}

// TestStartBackgroundClient_ConnectingBeforeDialAndLoginWaitOffTheLaunchPath
// is an AST structural guard (mirroring readonly_test.go's own house
// pattern) pinning startBackgroundClient's already-paired success-path
// shape. RENAMES and replaces the former
// TestStartBackgroundClient_SuccessPathSetsConnectingAndWaitsForLogin: that
// test pinned a SYNCHRONOUS `loginWaiter.wait(serveLoginTimeout)` call on
// the launch path, which is exactly what plan 08-14 (gap G-08-5, review
// findings WR-01/WR-02) retires — every kernel restart with an
// already-linked WhatsApp source was paying up to serveLoginTimeout before
// ANY source's routes existed, because the go-plugin handshake blocks on
// startBackgroundClient returning. This guard proves the opposite ordering:
// the connecting assignment and waiter registration still happen before the
// dial (unchanged from G-08-4), but the wait itself is now DISPATCHED on its
// own goroutine rather than awaited inline, and the waiter's event handler
// is removed on both dial outcomes (WR-02). This remains a structural
// guard, not behavioural proof — a genuinely behavioral test would need a
// live WhatsApp server, precisely the blind spot the debug session
// (.planning/debug/whatsapp-paired-session-not-picked-up.md) recorded. The
// behavioural proof for the cross-source consequence (a slow/blocking
// plugin launch delaying every other source) lives in
// kernel/supervisor/launchlatency_test.go (plan 08-13), which drives a real
// slow-launching plugin subprocess.
func TestStartBackgroundClient_ConnectingBeforeDialAndLoginWaitOffTheLaunchPath(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "connect.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse connect.go: %v", err)
	}

	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "startBackgroundClient" {
			continue
		}
		fn = fd
		break
	}
	if fn == nil {
		t.Fatal("G-08-4/G-08-5: could not find startBackgroundClient FuncDecl in connect.go")
	}

	var (
		setConnectingPos token.Pos
		addHandlerPos    token.Pos
		connectPos       token.Pos
		waitPos          token.Pos
		waitInGoStmt     bool
		goStmtCount      int
		removeInGoStmt   bool
		removeOutsideGo  bool
	)

	// removeHandlerCalls collects the position of every RemoveEventHandler
	// call in the function body, plus whether it lies inside the single
	// *ast.GoStmt's function literal — walked via a dedicated pass below so
	// "inside the go statement" can be determined precisely rather than by
	// position-only heuristics.
	var goStmtLits []*ast.FuncLit

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if goStmt, ok := n.(*ast.GoStmt); ok {
			goStmtCount++
			if lit, ok := goStmt.Call.Fun.(*ast.FuncLit); ok {
				goStmtLits = append(goStmtLits, lit)
			}
			// Do not descend further here — the dedicated walk below over
			// each collected FuncLit handles the interior; returning true
			// still lets ast.Inspect naturally reach the GoStmt's own
			// children for the outer-scope call detection to still see
			// calls textually inside it via the recursive Inspect call
			// this returns into (ast.Inspect always visits children
			// regardless, so this is just documentation of intent).
		}
		return true
	})

	insideGoStmtLit := func(pos token.Pos) bool {
		for _, lit := range goStmtLits {
			if lit.Pos() <= pos && pos < lit.End() {
				return true
			}
		}
		return false
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		switch sel.Sel.Name {
		case "setHealthState":
			if setConnectingPos == token.NoPos && len(call.Args) > 0 {
				if id, ok := call.Args[0].(*ast.Ident); ok && id.Name == "healthStateConnecting" {
					setConnectingPos = call.Pos()
				}
			}
		case "AddEventHandler":
			if addHandlerPos == token.NoPos && len(call.Args) == 1 {
				// The login waiter's handler is registered as
				// `loginWaiter.handleEvent` — a *ast.SelectorExpr whose
				// Sel.Name is "handleEvent" AND whose receiver identifier
				// is "loginWaiter" (distinguishing it from the earlier
				// `client.AddEventHandler(p.handleEvent)` registration,
				// whose argument selector's receiver is "p").
				if argSel, ok := call.Args[0].(*ast.SelectorExpr); ok && argSel.Sel.Name == "handleEvent" {
					if x, ok := argSel.X.(*ast.Ident); ok && x.Name == "loginWaiter" {
						addHandlerPos = call.Pos()
					}
				}
			}
		case "Connect":
			if connectPos == token.NoPos && len(call.Args) == 0 {
				connectPos = call.Pos()
			}
		case "wait":
			if waitPos == token.NoPos && len(call.Args) == 1 {
				if id, ok := call.Args[0].(*ast.Ident); ok && id.Name == "serveLoginTimeout" {
					waitPos = call.Pos()
					waitInGoStmt = insideGoStmtLit(call.Pos())
				}
			}
		case "RemoveEventHandler":
			if insideGoStmtLit(call.Pos()) {
				removeInGoStmt = true
			} else {
				removeOutsideGo = true
			}
		}
		return true
	})

	if setConnectingPos == token.NoPos {
		t.Fatal("G-08-4: no setHealthState(healthStateConnecting, ...) call found in startBackgroundClient")
	}
	if addHandlerPos == token.NoPos {
		t.Fatal("G-08-4: no AddEventHandler(loginWaiter.handleEvent) call found in startBackgroundClient")
	}
	if connectPos == token.NoPos {
		t.Fatal("G-08-4: no client.Connect() call found in startBackgroundClient")
	}
	if waitPos == token.NoPos {
		t.Fatal("G-08-5: no wait(serveLoginTimeout) call found in startBackgroundClient")
	}

	if !(setConnectingPos < connectPos) {
		t.Fatalf("G-08-4: setHealthState(healthStateConnecting, ...) at %s must appear BEFORE client.Connect() at %s — the connecting state must be assigned before dialing", fset.Position(setConnectingPos), fset.Position(connectPos))
	}
	if !(addHandlerPos < connectPos) {
		t.Fatalf("G-08-4: AddEventHandler(loginWaiter.handleEvent) at %s must appear BEFORE client.Connect() at %s — the waiter must be registered before dialing so it observes the SAME client's events", fset.Position(addHandlerPos), fset.Position(connectPos))
	}

	// (c) WR-01/G-08-5: the wait must be DISPATCHED (inside a *ast.GoStmt's
	// function literal), never awaited synchronously on the launch path — a
	// blocking wait here delays the go-plugin handshake, and through it
	// kernel boot and every relaunch.
	if !waitInGoStmt {
		t.Fatalf("WR-01/G-08-5: wait(serveLoginTimeout) at %s must be dispatched inside a `go` statement's function literal, not awaited synchronously — a blocking wait here delays the go-plugin handshake, and through it kernel boot and every relaunch", fset.Position(waitPos))
	}
	if goStmtCount != 1 {
		t.Fatalf("WR-01/G-08-5: expected exactly one `go` statement in startBackgroundClient, found %d", goStmtCount)
	}

	// (d) WR-02: the waiter's handler must be retired on BOTH dial
	// outcomes — once inside the background goroutine (the success path)
	// and once outside it (the client.Connect() error branch).
	if !removeInGoStmt {
		t.Fatal("WR-02: no RemoveEventHandler call found inside the background `go` statement's function literal — the success-path dial outcome must retire the waiter's handler after the wait completes")
	}
	if !removeOutsideGo {
		t.Fatal("WR-02: no RemoveEventHandler call found outside the background `go` statement — the client.Connect() error branch must retire the waiter's handler immediately, since there is no wait to serve on that branch")
	}
}
