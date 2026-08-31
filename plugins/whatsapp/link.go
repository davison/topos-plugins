package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/mdp/qrterminal/v3"
	"rsc.io/qr"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"

	_ "modernc.org/sqlite"
)

// postPairLoginTimeout bounds how long the shared link-flow core waits for
// a real *events.Connected after the QR channel reports "success"
// (PairSuccess) before giving up — generous because the post-pair
// reconnection and login handshake involves a fresh websocket connection
// and a full app-state/key exchange, not just a fast ack.
const postPairLoginTimeout = 60 * time.Second

// postPairGraceWindow is a short additional wait AFTER *events.Connected
// fires, before this process calls Disconnect() — giving the client's own
// initial post-login exchange (app state sync, key distribution) a moment
// to get underway on the same socket, rather than dropping it the instant
// Connected is observed.
const postPairGraceWindow = 5 * time.Second

// errLinkTimeout is the sentinel runLinkCore returns when the QR channel
// closes with a "timeout" event (whatsmeow ran out of rotating codes
// without a scan). Named distinctly from a generic link failure so
// runLinkJSON can emit a `timeout` linkEvent (Task 2) rather than an
// `error` one.
var errLinkTimeout = errors.New("whatsapp: pairing timed out — re-run to try again")

// linkEmitter receives link-flow lifecycle events from the shared
// runLinkCore so both the ASCII terminal mode (Plan 08-01, D-04's CLI
// recovery path) and the machine-readable JSON mode (this plan, D-01) can
// drive identical acquire-lock/open-store/QR-channel logic while differing
// only in how each presents an event — asciiLinkEmitter prints human text
// to a terminal; jsonLinkEmitter emits newline-delimited linkEvent JSON.
type linkEmitter interface {
	// code fires for each rotating QR code the channel emits, carrying the
	// whatsmeow-reported validity window for that specific code.
	code(code string, timeout time.Duration)
	// alreadyLinked fires when a device row already exists and the shared
	// core is about to reconnect to confirm the session is fully
	// established, before pairingAccepted/loggedIn.
	alreadyLinked(deviceID string)
	// pairingAccepted fires once the QR channel reports success — the
	// shared core still waits for a genuine post-pair login below this
	// before calling loggedIn.
	pairingAccepted()
	// loggedIn fires once the post-pair login handshake genuinely
	// completes (a real *events.Connected observed) — the terminal
	// success condition for both modes. freshlyPaired distinguishes a
	// brand-new pairing from an already-linked reconnect confirmation,
	// which the ASCII mode reports with two different final messages.
	loggedIn(freshlyPaired bool)
}

// runLinkCore is the shared link-flow core both link.go modes drive:
// acquire the store lock (the CONTEXT hard requirement's mutual-exclusion
// mechanism — storelock.go), open the SAME whatsmeow sqlstore serve-mode
// uses, and either confirm an already-linked device or drive the QR
// channel until the phone scans it, reporting every step through emit
// rather than printing/writing anything itself. Returns nil on a
// successful login, errLinkTimeout on a QR-channel timeout, ErrStoreInUse
// (via acquireStoreLock) on a lock conflict, or another wrapped error for
// any other failure.
func runLinkCore(ctx context.Context, dir string, emit linkEmitter) error {
	lock, err := acquireStoreLock(dir)
	if err != nil {
		return err
	}
	defer lock.Release()

	dbPath := filepath.Join(dir, "whatsmeow.db")
	container, err := sqlstore.New(ctx, "sqlite", whatsmeowSessionDSN(dbPath), newPluginLogger("whatsmeow/link"))
	if err != nil {
		return fmt.Errorf("whatsapp: open whatsmeow session store %s: %w", dbPath, err)
	}
	defer container.Close()

	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return fmt.Errorf("whatsapp: read linked device: %w", err)
	}

	client := whatsmeow.NewClient(device, newPluginLogger("whatsmeow/link"))

	// Registered BEFORE GetQRChannel/Connect (or, in the already-linked
	// branch below, before Connect alone), alongside the QR channel's own
	// internal event handler — both observe the same client's event
	// stream. See pairLoginWaiter's own doc comment: whatsmeow persists
	// Store.ID (device.ID becomes non-nil) at PairSuccess time, BEFORE the
	// post-pair websocket reconnection and login handshake completes — so
	// a device row already existing here does NOT by itself prove a prior
	// run ever finished linking.
	loginWaiter := newPairLoginWaiter()
	client.AddEventHandler(loginWaiter.handleEvent)

	if device.ID != nil {
		// A saved device row alone doesn't confirm the session is
		// actually usable — reconnect and wait for a real Connected (or a
		// definitive failure) before reporting anything. Safe and correct
		// whether the prior link fully completed (an ordinary reconnect,
		// identical to what serve mode does on every kernel restart) or
		// was left half-finished by the fixed premature-disconnect bug
		// (this completes it).
		emit.alreadyLinked(device.ID.String())
		if err := client.Connect(); err != nil {
			return fmt.Errorf("whatsapp: reconnect failed: %w", err)
		}
		defer client.Disconnect()

		if err := loginWaiter.wait(postPairLoginTimeout); err != nil {
			return err
		}
		time.Sleep(postPairGraceWindow)

		emit.loggedIn(false)
		return nil
	}

	qrChan, err := client.GetQRChannel(ctx)
	if err != nil {
		return fmt.Errorf("whatsapp: get QR channel: %w", err)
	}
	if err := client.Connect(); err != nil {
		return fmt.Errorf("whatsapp: connect: %w", err)
	}
	defer client.Disconnect()

	paired := false
	for evt := range qrChan {
		switch evt.Event {
		case "code":
			emit.code(evt.Code, evt.Timeout)
		case "success":
			// Do NOT return/disconnect here — see loginWaiter's own doc
			// comment above. The phone shows "Logging in…" until the wait
			// below observes the real post-pair Connected.
			paired = true
			emit.pairingAccepted()
		case "timeout":
			return errLinkTimeout
		case "error":
			return fmt.Errorf("whatsapp: pairing error: %w", evt.Error)
		default:
			return fmt.Errorf("whatsapp: pairing failed: %s", evt.Event)
		}
	}
	if !paired {
		return fmt.Errorf("whatsapp: QR channel closed before pairing completed")
	}

	if err := loginWaiter.wait(postPairLoginTimeout); err != nil {
		return err
	}

	// Grace window for the client's own initial post-login exchange
	// (app-state sync, key distribution) to get underway on this same
	// socket before this process calls Disconnect() (deferred above).
	time.Sleep(postPairGraceWindow)

	emit.loggedIn(true)
	return nil
}

// --- ASCII terminal mode (Plan 08-01, D-04's CLI recovery path) ---

// asciiLinkEmitter presents runLinkCore's events as human-readable text to
// out (os.Stdout in production; an injectable buffer in tests) — the exact
// wording Plan 08-01 shipped, unchanged by this plan's refactor into a
// shared core.
type asciiLinkEmitter struct {
	out io.Writer
}

func (e asciiLinkEmitter) code(code string, timeout time.Duration) {
	fmt.Fprintln(e.out, "Scan with your phone to link (valid for", timeout.Round(time.Second), "):")
	qrterminal.GenerateHalfBlock(code, qrterminal.L, e.out)
}

func (e asciiLinkEmitter) alreadyLinked(deviceID string) {
	fmt.Fprintln(e.out, "Already linked as", deviceID, "— reconnecting to confirm the session is fully established…")
}

func (e asciiLinkEmitter) pairingAccepted() {
	fmt.Fprintln(e.out, "Pairing accepted — completing login…")
}

func (e asciiLinkEmitter) loggedIn(freshlyPaired bool) {
	if freshlyPaired {
		fmt.Fprintln(e.out, "Linked successfully.")
		return
	}
	fmt.Fprintln(e.out, "Session confirmed. Re-run without -link to serve.")
}

// runLinkCLI implements the one-shot terminal QR link flow
// (-link -path <dir>): drives runLinkCore with an asciiLinkEmitter writing
// to os.Stdout — byte-for-byte the same behaviour Plan 08-01 shipped.
func runLinkCLI(ctx context.Context, dir string) error {
	return runLinkCore(ctx, dir, asciiLinkEmitter{out: os.Stdout})
}

// --- Machine-readable JSON mode (this plan, D-01) ---

// linkEventKind discriminates the six event shapes runLinkJSON ever
// emits. Three are terminal — paired, error, timeout — and end the link
// session (kernel/httpapi/whatsapplink.go's isTerminalKind is a
// hand-maintained mirror of exactly this fact; the two files must not
// drift). The other three — qr, pairing_accepted, already_linked — are
// non-terminal: observing one leaves the session live and pollable.
type linkEventKind string

const (
	linkEventKindQR              linkEventKind = "qr"
	linkEventKindPaired          linkEventKind = "paired"
	linkEventKindError           linkEventKind = "error"
	linkEventKindTimeout         linkEventKind = "timeout"
	linkEventKindPairingAccepted linkEventKind = "pairing_accepted"
	linkEventKindAlreadyLinked   linkEventKind = "already_linked"
)

// linkErrorCodeStoreInUse and linkErrorCodeFailed are the two error-event
// codes runLinkJSON ever emits — distinguishable so
// kernel/httpapi/whatsapplink.go (Task 3) can map the former to its own
// whatsapp_store_in_use API error code and the latter to a generic
// link_failed.
const (
	linkErrorCodeStoreInUse = "store_in_use"
	linkErrorCodeFailed     = "link_failed"
)

// linkEvent is one newline-delimited JSON object emitted on stdout by
// runLinkJSON — one line per event, no embedded newline (json.Marshal's
// compact encoding never inserts one). Only the fields relevant to a given
// Kind are populated; the rest are omitted (omitempty) rather than sent as
// zero values, so a `paired`/`timeout` line is exactly `{"kind":"..."}`.
type linkEvent struct {
	Kind linkEventKind `json:"kind"`
	// PNGDataURI and ExpiresInSeconds are populated only for Kind == "qr".
	// The QR payload string itself is NEVER carried on this type — only
	// the rendered image (a live pairing credential must never appear as
	// text in an emitted event or log line).
	PNGDataURI       string `json:"png_data_uri,omitempty"`
	ExpiresInSeconds int    `json:"expires_in_seconds,omitempty"`
	// Code and Message are populated only for Kind == "error".
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func pairedLinkEvent() linkEvent  { return linkEvent{Kind: linkEventKindPaired} }
func timeoutLinkEvent() linkEvent { return linkEvent{Kind: linkEventKindTimeout} }

// pairingAcceptedLinkEvent and alreadyLinkedLinkEvent carry nothing but
// their kind — linkEvent's existing omitempty tags already make that
// marshal to a bare single-key object. Neither carries a device
// identifier: a WhatsApp device JID embeds the user's own phone number,
// these events are relayed verbatim to the browser (kernel/httpapi/
// whatsapplink.go), and this route has no need for it.
func pairingAcceptedLinkEvent() linkEvent { return linkEvent{Kind: linkEventKindPairingAccepted} }
func alreadyLinkedLinkEvent() linkEvent   { return linkEvent{Kind: linkEventKindAlreadyLinked} }

func newErrorLinkEvent(code, message string) linkEvent {
	return linkEvent{Kind: linkEventKindError, Code: code, Message: message}
}

// newQRLinkEvent renders code (the raw whatsmeow pairing payload) into a
// PNG via the audited rsc.io/qr encoder (08-RESEARCH.md Package Legitimacy
// Audit, Plan 08-03 Task 1), base64-encodes it as a data: URI, and returns
// the resulting `qr` linkEvent. code itself is consumed here and never
// copied onto the returned value — only PNGDataURI (the rendered image)
// leaves this function.
func newQRLinkEvent(code string, timeout time.Duration) (linkEvent, error) {
	img, err := qr.Encode(code, qr.M)
	if err != nil {
		return linkEvent{}, fmt.Errorf("whatsapp: encode QR image: %w", err)
	}
	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(img.PNG())

	seconds := int(timeout.Round(time.Second) / time.Second)
	if seconds <= 0 {
		// whatsmeow always reports a positive validity window in
		// practice; this floor exists so a sub-second or zero-value
		// Timeout (which should never occur) can never produce a
		// non-positive expires_in_seconds the browser's countdown would
		// choke on.
		seconds = 1
	}

	return linkEvent{
		Kind:             linkEventKindQR,
		PNGDataURI:       dataURI,
		ExpiresInSeconds: seconds,
	}, nil
}

// marshalLinkEvent marshals ev to compact JSON — json.Marshal's default
// encoding never inserts a literal newline, so this is inherently
// single-line; the caller appends the line's own trailing "\n" when
// writing it.
func marshalLinkEvent(ev linkEvent) ([]byte, error) {
	return json.Marshal(ev)
}

// jsonLinkEmitter presents runLinkCore's events as newline-delimited JSON
// on out (os.Stdout in production) — well-formed event lines only, per
// Task 2's contract — with a short, code-free diagnostic line on stderr
// for operator visibility (never anything an operator couldn't already
// infer from the event stream itself, and never the raw QR payload).
type jsonLinkEmitter struct {
	out    io.Writer
	stderr io.Writer
}

func newJSONLinkEmitter(out, stderr io.Writer) *jsonLinkEmitter {
	return &jsonLinkEmitter{out: out, stderr: stderr}
}

func (e *jsonLinkEmitter) code(code string, timeout time.Duration) {
	ev, err := newQRLinkEvent(code, timeout)
	if err != nil {
		e.writeEvent(newErrorLinkEvent(linkErrorCodeFailed, fmt.Sprintf("whatsapp: render QR image: %v", err)))
		return
	}
	e.writeEvent(ev)
	fmt.Fprintf(e.stderr, "topos-plugin-whatsapp[link-json INFO]: QR code rotated (expires in %s)\n", timeout.Round(time.Second))
}

// alreadyLinked writes the already_linked event to stdout (device-id-free
// — see alreadyLinkedLinkEvent's own doc comment for why) and keeps the
// existing stderr diagnostic, which does name the device id, exactly as
// it was before this event existed.
func (e *jsonLinkEmitter) alreadyLinked(deviceID string) {
	e.writeEvent(alreadyLinkedLinkEvent())
	fmt.Fprintf(e.stderr, "topos-plugin-whatsapp[link-json INFO]: already linked as %s — reconnecting to confirm the session\n", deviceID)
}

// pairingAccepted writes the pairing_accepted event to stdout in addition
// to the stderr diagnostic it already wrote — announcing on the wire that
// the phone accepted the scan and a post-pair login is now under way.
func (e *jsonLinkEmitter) pairingAccepted() {
	e.writeEvent(pairingAcceptedLinkEvent())
	fmt.Fprintln(e.stderr, "topos-plugin-whatsapp[link-json INFO]: pairing accepted — completing login…")
}

// loggedIn is deliberately a no-op: runLinkJSON itself writes the single
// `paired` event once runLinkCore returns nil, rather than duplicating
// that write here — one call site for the terminal success event, whether
// it followed a fresh pairing or an already-linked reconnect confirmation
// (the JSON wire vocabulary makes no such distinction; only the ASCII
// mode's human text does).
func (e *jsonLinkEmitter) loggedIn(freshlyPaired bool) {}

// writeEvent marshals ev and writes it to out as one line, terminated by a
// single "\n", with no buffering layer of its own — out is os.Stdout in
// production, whose Write calls are unbuffered, so each event is visible
// to a reader immediately rather than only at process exit.
func (e *jsonLinkEmitter) writeEvent(ev linkEvent) {
	line, err := marshalLinkEvent(ev)
	if err != nil {
		// Should be unreachable given linkEvent's fixed, controlled field
		// set — but never silently drop a failure to report itself.
		fmt.Fprintf(e.stderr, "topos-plugin-whatsapp[link-json ERROR]: marshal link event: %v\n", err)
		return
	}
	_, _ = e.out.Write(line)
	_, _ = e.out.Write([]byte("\n"))
}

// runLinkJSON implements the machine-readable link flow (-link-json
// -path <dir>): drives the identical runLinkCore used by the ASCII mode
// with a jsonLinkEmitter, then emits exactly one terminal event — `paired`
// on success, `timeout` on a QR-channel timeout, or `error` (with a code
// distinguishing store-in-use from any other failure) otherwise — and
// returns the same error runLinkCore produced so main() can set the
// process's exit code accordingly (zero after paired, non-zero after
// error or timeout).
func runLinkJSON(ctx context.Context, dir string, out, stderr io.Writer) error {
	emitter := newJSONLinkEmitter(out, stderr)
	err := runLinkCore(ctx, dir, emitter)
	switch {
	case err == nil:
		emitter.writeEvent(pairedLinkEvent())
		return nil
	case errors.Is(err, errLinkTimeout):
		emitter.writeEvent(timeoutLinkEvent())
		return err
	case errors.Is(err, ErrStoreInUse):
		emitter.writeEvent(newErrorLinkEvent(linkErrorCodeStoreInUse, err.Error()))
		return err
	default:
		emitter.writeEvent(newErrorLinkEvent(linkErrorCodeFailed, err.Error()))
		return err
	}
}
