package main

import (
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types/events"
)

// TestPairLoginWaiter_ConnectedSignalsSuccess proves an *events.Connected
// (the event whatsmeow's own PairSuccess doc comment says to wait for)
// unblocks wait with a nil error.
func TestPairLoginWaiter_ConnectedSignalsSuccess(t *testing.T) {
	w := newPairLoginWaiter()
	w.handleEvent(&events.Connected{})

	if err := w.wait(time.Second); err != nil {
		t.Fatalf("wait() after Connected = %v, want nil", err)
	}
}

// TestPairLoginWaiter_LoggedOutSignalsFailure proves a LoggedOut event
// firing immediately after pairing (rather than a genuine Connected)
// surfaces as a named, actionable error instead of hanging or falsely
// reporting success.
func TestPairLoginWaiter_LoggedOutSignalsFailure(t *testing.T) {
	w := newPairLoginWaiter()
	w.handleEvent(&events.LoggedOut{OnConnect: true})

	err := w.wait(time.Second)
	if err == nil {
		t.Fatal("wait() after LoggedOut = nil, want an error")
	}
	if !strings.Contains(err.Error(), "logged out") {
		t.Fatalf("wait() error = %q, want it to name the logged-out cause", err.Error())
	}
}

// TestPairLoginWaiter_StreamReplacedSignalsFailure proves a
// StreamReplaced event surfaces as a named error.
func TestPairLoginWaiter_StreamReplacedSignalsFailure(t *testing.T) {
	w := newPairLoginWaiter()
	w.handleEvent(&events.StreamReplaced{})

	err := w.wait(time.Second)
	if err == nil {
		t.Fatal("wait() after StreamReplaced = nil, want an error")
	}
	if !strings.Contains(err.Error(), "stream replaced") {
		t.Fatalf("wait() error = %q, want it to name the stream-replaced cause", err.Error())
	}
}

// TestPairLoginWaiter_TimeoutWhenNoQualifyingEventArrives proves that if
// neither a success nor failure event ever arrives, wait returns a named
// timeout error rather than blocking forever — the exact failure mode
// this plugin's own doc comment says to guard against (a stranded phone
// showing "Logging in…" forever with no plugin-side signal).
func TestPairLoginWaiter_TimeoutWhenNoQualifyingEventArrives(t *testing.T) {
	w := newPairLoginWaiter()
	// Deliberately deliver an irrelevant event first, proving the waiter
	// doesn't false-positive on just any event.
	w.handleEvent(&events.KeepAliveTimeout{})

	err := w.wait(20 * time.Millisecond)
	if err == nil {
		t.Fatal("wait() with no qualifying event = nil, want a timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("wait() error = %q, want it to name the timeout", err.Error())
	}
}

// TestPairLoginWaiter_SignalIsIdempotent proves a second qualifying event
// after the first has already signalled does not block, panic on a
// closed/full channel, or change the already-delivered result.
func TestPairLoginWaiter_SignalIsIdempotent(t *testing.T) {
	w := newPairLoginWaiter()
	w.handleEvent(&events.Connected{})
	w.handleEvent(&events.LoggedOut{}) // must not block or panic

	if err := w.wait(time.Second); err != nil {
		t.Fatalf("wait() = %v, want the FIRST signal (nil, from Connected) to win", err)
	}
}
