package main

import (
	"fmt"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types/events"
)

// pairLoginWaiter detects when the post-pairing login handshake actually
// completes. whatsmeow's own *events.PairSuccess doc comment: "this is
// generally followed by a websocket reconnection, so you should wait for
// [*events.]Connected before trying to send anything" — the QR channel's
// own "success" event fires on PairSuccess, BEFORE that reconnection and
// login handshake completes. Disconnecting the moment "success" arrives
// (this plugin's original bug, live-reported 2026-08-10: the phone stayed
// on "Logging in…" with a stranded socket) drops the client mid-handshake.
// pairLoginWaiter is registered as an additional whatsmeow event handler
// (before Client.Connect(), alongside GetQRChannel's own internal
// handler) so runLinkCLI can block until the SAME client observes a real
// *events.Connected — or a definitive failure — before ever calling
// Disconnect().
type pairLoginWaiter struct {
	done chan error
	once sync.Once
}

func newPairLoginWaiter() *pairLoginWaiter {
	return &pairLoginWaiter{done: make(chan error, 1)}
}

// signal delivers err (nil for success) to the first caller only —
// idempotent, since more than one qualifying event could arrive (e.g. a
// LoggedOut after an already-signalled Connected must not block on a full
// channel or panic on a double-close).
func (w *pairLoginWaiter) signal(err error) {
	w.once.Do(func() { w.done <- err })
}

// handleEvent is registered via Client.AddEventHandler and translates the
// events that determine whether the post-pair login handshake succeeded
// or definitively failed into a signal. Every other event is ignored —
// this handler runs for the client's entire lifetime, not just during
// linking, so it must never assume it only ever sees pairing-adjacent
// events.
func (w *pairLoginWaiter) handleEvent(evt any) {
	switch e := evt.(type) {
	case *events.Connected:
		w.signal(nil)
	case *events.LoggedOut:
		w.signal(fmt.Errorf("logged out immediately after pairing (reason: %s)", e.Reason))
	case *events.StreamReplaced:
		w.signal(fmt.Errorf("stream replaced by another session immediately after pairing"))
	case *events.ConnectFailure:
		w.signal(fmt.Errorf("connect failure immediately after pairing (reason: %s)", e.Reason))
	}
}

// wait blocks until handleEvent signals success or failure, or timeout
// elapses — whichever comes first — returning a named, actionable error
// in the failure and timeout cases.
func (w *pairLoginWaiter) wait(timeout time.Duration) error {
	select {
	case err := <-w.done:
		if err != nil {
			return fmt.Errorf("whatsapp: pairing accepted but login failed to complete: %w", err)
		}
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("whatsapp: pairing accepted but timed out after %s waiting for login to complete (the phone may still show \"Logging in…\" — check WhatsApp > Linked devices on the phone)", timeout)
	}
}
