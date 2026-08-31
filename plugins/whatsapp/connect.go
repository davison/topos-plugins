package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"

	_ "modernc.org/sqlite"
)

// serveLoginTimeout bounds how long the serve-mode login waiter's event
// handler stays registered on the long-lived client — NOT how long plugin
// launch takes. As of plan 08-14 (closing 08-REVIEW.md WR-01 and
// 08-VERIFICATION.md G-08-5), the wait that this constant bounds runs on
// its own goroutine, dispatched AFTER startBackgroundClient has already
// returned and goplugin.Serve has already been reached; nothing on the
// launch path waits on it, so go-plugin's own client StartTimeout ceiling
// (hashicorp/go-plugin's one-minute default) no longer constrains this
// value the way it once did. The wait moved because every kernel restart
// and hot-apply relaunch with an already-linked WhatsApp source was paying
// up to this long before ANY source's routes existed — the go-plugin
// handshake is what kernel/pluginhost.launch blocks on, and
// Discover/Reconcile launch sources sequentially. The mechanism that now
// covers a Match landing inside the connecting window this wait used to
// close synchronously is kernel/syncer/scheduler.go's bounded first-refresh
// retry schedule (defaultFirstRefreshRetryDelays), not this wait.
const serveLoginTimeout = 15 * time.Second

// pluginLogger implements waLog.Logger, writing to os.Stderr (never
// os.Stdout, which the go-plugin subprocess handshake protocol owns) at a
// fixed WARN-and-above level (Debugf/Infof are silently dropped). This
// bounds what whatsmeow's OWN internal logging can emit (T-08-03's
// mitigation) — every log call this plugin's own code makes
// (eventhandler.go, connect.go, plugin.go) is separately written to never
// include a message body, contact name, or session key material,
// regardless of level.
type pluginLogger struct {
	module string
}

func newPluginLogger(module string) waLog.Logger { return pluginLogger{module: module} }

func (l pluginLogger) Errorf(msg string, args ...any) { l.printf("ERROR", msg, args...) }
func (l pluginLogger) Warnf(msg string, args ...any)  { l.printf("WARN", msg, args...) }
func (l pluginLogger) Infof(msg string, args ...any)  {}
func (l pluginLogger) Debugf(msg string, args ...any) {}
func (l pluginLogger) Sub(module string) waLog.Logger {
	return pluginLogger{module: l.module + "/" + module}
}

func (l pluginLogger) printf(level, msg string, args ...any) {
	fmt.Fprintf(os.Stderr, "topos-plugin-whatsapp[%s %s]: "+msg+"\n", append([]any{l.module, level}, args...)...)
}

// whatsmeowSessionDSN builds the modernc.org/sqlite connection string for
// whatsmeow's own session store (whatsmeow.db). whatsmeow's own
// sqlstore.Container.Upgrade checks `PRAGMA foreign_keys` on the
// connection it is handed and refuses to run its migrations if it comes
// back off — confirmed live (2026-08-10): a bare `file:<path>` DSN (or the
// `?_foreign_keys=on` shorthand whatsmeow's own doc comment illustrates,
// which is mattn/go-sqlite3's query-param convention, not
// modernc.org/sqlite's) fails at container open with "failed to upgrade
// database: foreign keys are not enabled" before ever reaching the QR
// flow. modernc.org/sqlite's own DSN pragma syntax is
// `_pragma=<pragma-body>` — one query param per pragma, applied via
// `PRAGMA <body>` on every new pooled connection (sqlite.go's
// applyQueryParams) — so `_foreign_keys=on` is silently ignored as an
// unrecognised query param rather than erroring, which is what made this
// fail quietly instead of at compile/lint time. Both link.go's one-shot
// link flow and connect.go's persistent serve-mode connection MUST open
// whatsmeow's sqlstore with this identical DSN — the CONTEXT hard
// requirement is that both open it the same way.
func whatsmeowSessionDSN(dbPath string) string {
	return "file:" + dbPath + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)"
}

// startBackgroundClient acquires the store lock, opens whatsmeow's own
// sqlstore (whatsmeow.db — a file distinct from this plugin's own
// messages.db, messagestore.go), constructs a whatsmeow.Client, registers
// this plugin's own event handler (eventhandler.go), and — only when a
// device is already linked — connects and holds that connection for the
// plugin's entire process lifetime. When no device is linked yet, this
// records that state and returns without connecting; the operator links
// via this binary's own -link flag (link.go), never through this
// RPC-serving process — storelock.go's exclusive lock is what makes the
// two mutually exclusive.
func (p *SourcePlugin) startBackgroundClient(ctx context.Context) error {
	lock, err := acquireStoreLock(p.dir)
	if err != nil {
		return err // already-named (ErrStoreInUse) or wrapped
	}
	p.lock = lock

	dbPath := filepath.Join(p.dir, "whatsmeow.db")
	container, err := sqlstore.New(ctx, "sqlite", whatsmeowSessionDSN(dbPath), newPluginLogger("whatsmeow/store"))
	if err != nil {
		return fmt.Errorf("whatsapp: open whatsmeow session store %s: %w", dbPath, err)
	}
	p.container = container

	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return fmt.Errorf("whatsapp: read linked device: %w", err)
	}

	client := whatsmeow.NewClient(device, newPluginLogger("whatsmeow/client"))
	client.AddEventHandler(p.handleEvent)
	p.client = client

	if device.ID == nil {
		p.setHealthState(healthStateNotLinked, "")
		return nil
	}

	// G-08-4 (missing[0]): explicitly assign the connecting state BEFORE
	// dialing, belt and braces with health.go's zero-value fix — the
	// plugin's reported state is honest from the first instant of the
	// dial rather than only after Connect() returns.
	p.setHealthState(healthStateConnecting, "")

	// G-08-4 (missing[1]): register a pairLoginWaiter (pairwait.go — the
	// SAME primitive the link flow already proves against a real device)
	// AFTER p.handleEvent is already registered above. Ordering is
	// load-bearing: whatsmeow dispatches an event to its handlers
	// synchronously, in registration order, so by the time this waiter is
	// signalled, p.handleEvent's own *events.Connected case has ALREADY
	// assigned healthStateLinked. That is why the wait below adds no
	// post-wait state assignment of its own — doing so would risk
	// clobbering a LoggedOut or StreamReplaced that landed in the same
	// instant.
	//
	// Plan 08-14 (WR-01/G-08-5) trade, recorded here rather than left
	// implicit: from this point on, the go-plugin handshake no longer
	// implies login has completed — the wait that used to make that true
	// is dispatched below, not awaited. What keeps G-08-4's user-visible
	// symptom closed without it is two OTHER legs, both left completely
	// intact by this plan: healthStateConnecting being the Go zero value
	// plus the explicit pre-dial assignment above (so a connecting
	// instance can never emit the false pairing instruction), and
	// kernel/syncer/scheduler.go's bounded first-refresh retry (so a Match
	// that lands inside the connecting window is superseded by an ok sync
	// run within seconds, not pinned for the full sync interval).
	loginWaiter := newPairLoginWaiter()
	loginWaiterID := client.AddEventHandler(loginWaiter.handleEvent)

	if err := client.Connect(); err != nil {
		// Not fatal to process startup — a transient network failure at
		// boot should not crash-loop the plugin subprocess; Health/Match
		// report the unhealthy state until a future *events.Connected
		// fires (07-RESEARCH.md assumption A2's precedent: every plugin
		// in this repo defers live-connectivity failures past process
		// startup).
		//
		// Reported as healthStateNotLinked, DELIBERATELY not one of
		// Task 1's three new named causes (de-link/ban/expiry are all
		// events WhatsApp's OWN server explicitly told us about via a
		// LATER *events.LoggedOut/TemporaryBan/ConnectFailure —
		// eventhandler.go — this is simply "haven't yet completed a
		// dial" for a device that IS already paired). whatsmeow's own
		// Client already schedules a background auto-reconnect for a
		// retryable error (EnableAutoReconnect defaults true, confirmed
		// in whatsmeow's client.go) — once that succeeds,
		// *events.Connected (eventhandler.go) transitions to
		// healthStateLinked automatically with zero further code here.
		// The real dial error is carried in the detail so Health's
		// LastError stays specific rather than merely the fixed
		// not-linked template verbatim.
		p.setHealthState(healthStateNotLinked, fmt.Sprintf("initial connect failed, retrying: %v", err))
		// WR-02: the waiter has no wait to serve on this branch — there
		// was no successful dial for it to observe a login outcome on —
		// so its handler is retired here rather than left registered on a
		// client that lives for the whole process.
		client.RemoveEventHandler(loginWaiterID)
		return nil
	}

	// WR-01/G-08-5: the bounded wait for the SAME client to observe a real
	// *events.Connected (or a definitive login failure) now runs on its
	// own goroutine, dispatched here rather than awaited — so
	// startBackgroundClient returns immediately after a successful dial,
	// and main.go reaches goplugin.Serve without waiting on any network
	// event. Every kernel restart and hot-apply relaunch with an
	// already-linked WhatsApp source now costs what any other plugin's
	// launch costs. The wait still happens and still logs a definitive
	// failure or timeout exactly once, through the same p.logOut line
	// under this package's fixed pluginName prefix, carrying no chat name,
	// sender name, message body, or key material — only its position on
	// the launch path changes, not its behaviour. The deferred removal
	// inside the literal means a future early return from this goroutine
	// cannot skip retiring the waiter's handler.
	go func() {
		defer client.RemoveEventHandler(loginWaiterID)
		if err := loginWaiter.wait(serveLoginTimeout); err != nil {
			fmt.Fprintf(p.logOut, "%s: serve-mode startup: %v\n", pluginName, err)
		}
	}()

	return nil
}
