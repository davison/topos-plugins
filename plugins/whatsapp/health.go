package main

import "go.mau.fi/whatsmeow/types/events"

// healthState is the named failure-cause taxonomy replacing 08-01-PLAN.md's
// single non-healthy flag (the old healthy bool / lastError string pair on
// SourcePlugin). SRC-03 criterion 4 — this plan's whole "managed risk"
// premise — hinges on Match (plugin.go) treating every non-healthy state
// identically at the RPC boundary (always codes.Unavailable, never an
// empty success) while still surfacing a SPECIFIC, honest, per-cause
// message through Health's LastError (08-UI-SPEC.md "New Health-State
// Taxonomy").
type healthState int

const (
	// healthStateConnecting means a paired device row WAS found and the
	// socket dial succeeded, but whatsmeow has not yet delivered
	// *events.Connected — a further server round trip (whatsmeow
	// dispatches Connected only from handleConnectSuccess on the server's
	// <success> node). Declared FIRST in this iota block so it is this
	// plugin's Go zero value, replacing healthStateNotLinked in that role:
	// gap G-08-4 (.planning/debug/whatsapp-paired-session-not-picked-up.md)
	// found a zero-value *SourcePlugin reproducing the exact false
	// "Not linked — pair this device..." message byte-for-byte for an
	// already-paired, actively-connecting device, because the OLD zero
	// value (healthStateNotLinked, iota == 0) doubled as "uninitialised"
	// and "never paired" at once. connect.go's startBackgroundClient now
	// also explicitly assigns this state before dialing (belt and braces
	// with the zero-value fix), and 08-11's bounded serve-mode login wait
	// means the kernel normally never observes this state at all — it is
	// transient and self-clearing, offering no recovery action.
	healthStateConnecting healthState = iota
	// healthStateNotLinked is the state before any device has ever been
	// paired (connect.go's own "no device found" branch), or an
	// ALREADY-paired device whose very first Connect() dial fails
	// transiently (see connect.go's own comment at that call site for why
	// this state, not a new enum value, is the deliberate choice there).
	// No longer this plugin's zero-value default (see healthStateConnecting
	// above, gap G-08-4) — it is reached only by explicit assignment.
	healthStateNotLinked
	// healthStateLinked is the only Healthy() state.
	healthStateLinked
	// healthStateDelinked is a remote unpair — the user (or someone with
	// the phone) removed this device from WhatsApp > Linked Devices —
	// or ANY logout reason this plugin does not otherwise recognise
	// (healthStateFromLogoutReason's own doc comment: never silently
	// mapped to healthy).
	healthStateDelinked
	// healthStateBanned is WhatsApp's own temporary or longer-lived
	// account restriction (events.TemporaryBan, or a LoggedOut reason
	// whatsmeow's own source comments identify as a ban under the hood —
	// see healthStateFromLogoutReason).
	healthStateBanned
	// healthStateExpired is a logout specifically attributable to the
	// primary phone's own session anchor going away (08-RESEARCH.md
	// Pitfall 2) — reads completely differently to the user than a
	// deliberate de-link, so it is named distinctly.
	healthStateExpired
	// healthStateStreamReplaced signals a DISTINCT cause from all of the
	// above: TWO connections were opened against the same local whatsmeow
	// session store at once (topos's own operational bug — e.g. two
	// kernel processes pointed at the same [sources.whatsapp].path), not
	// a WhatsApp-side event at all.
	healthStateStreamReplaced
)

// Healthy reports true for exactly one state.
func (s healthState) Healthy() bool { return s == healthStateLinked }

// healthMessages holds the fixed, per-state template Message() reads.
// health_test.go's uniqueness assertion checks THIS map's own values (via
// Message(), never a hand-duplicated literal) so the "no two causes
// produce the same text" property can never silently drift out of sync
// with what Health/Match actually emit.
//
// Templates for the not-linked/de-linked/banned/session-expired rows
// start from 08-UI-SPEC.md's "New Health-State Taxonomy" recommended
// (non-binding) templates, extended per D-03/D-04: the not-linked/
// de-linked/expired rows point at BOTH the source chip's "Re-link…" entry
// (D-03) and this binary's own -link flag (D-04, the CLI fallback/
// recovery path) — banned deliberately does NOT offer a re-link action
// (re-linking does not fix a ban; the UI-SPEC's own "re-check later"
// wording is kept verbatim). The ONE binding rule (08-UI-SPEC.md,
// restated in 08-CONTEXT.md): no message may state or imply that
// previously captured messages were lost or are now inaccessible — every
// non-healthy template below says the opposite explicitly.
var healthMessages = map[healthState]string{
	healthStateLinked: "",
	healthStateConnecting: "Linked — connecting to WhatsApp… syncing starts as soon as the connection completes. " +
		"Previously captured messages are still here.",
	healthStateNotLinked: "Not linked — pair this device with WhatsApp to start syncing. " +
		"Use this source's chip menu (\"Re-link…\") or run this plugin binary's -link flag.",
	healthStateDelinked: "Unlinked from WhatsApp — re-link to resume syncing. Previously captured messages are still here. " +
		"Re-link from this source's chip menu (\"Re-link…\") or run this plugin binary's -link flag.",
	healthStateBanned: "WhatsApp restricted this account — re-check later. Previously captured messages are still here.",
	healthStateExpired: "Linked session expired (the phone that originally linked this device went offline too long). Re-link to resume syncing. Previously captured messages are still here. " +
		"Re-link from this source's chip menu (\"Re-link…\") or run this plugin binary's -link flag.",
	healthStateStreamReplaced: "This WhatsApp session was replaced by another connection — likely two topos processes pointed at the same local data directory at once, a topos configuration issue rather than a WhatsApp-side failure. Previously captured messages are still here. Stop the duplicate process, then restart this source.",
}

// unrecognisedStateMessage is Message()'s fallback for a healthState value
// outside the six named constants above (should never happen in practice —
// defensive only, and still honours the one binding "never implies data
// loss" rule).
const unrecognisedStateMessage = "This WhatsApp source is unavailable for an unrecognised reason. Previously captured messages are still here."

// Message returns s's own fixed, honest, per-cause template — never a
// shared generic string. Combine with a plugin instance's own dynamic
// detail (SourcePlugin.currentMessage, plugin.go) for the actual LastError
// text a specific failure produces.
func (s healthState) Message() string {
	if msg, ok := healthMessages[s]; ok {
		return msg
	}
	return unrecognisedStateMessage
}

// healthStateFromLogoutReason translates a *events.LoggedOut event's own
// Reason code (events.ConnectFailureReason) into a named healthState.
//
// Empirically confirmed (08-01-PLAN.md Task 3's real-device spike,
// 2026-08-10, recorded in 08-01-SUMMARY.md's "Task 3 Spike Answers" #4,
// observed live during the round-2 half-linked-session recovery): a
// remote unpair surfaces live as reason 401 (events.ConnectFailureLoggedOut),
// whose own whatsmeow-authored String() reads "401: logged out from
// another device" — mapped to healthStateDelinked below. Locally captured
// rows survived that event intact (messages.db untouched; only
// whatsmeow.db's own device row was the casualty) — the empirical basis
// for this task's own "capture must never become exposure, and a failure
// state must never delete what was already captured" requirement. The
// spike's airplane-mode (phone-offline) sub-test was NOT performed (it is
// optional and phone-side only — 08-01-SUMMARY.md's own Task 3 spike
// answer #3 records this explicitly), so no reason code below is
// empirically confirmed to correspond to that specific scenario.
//
// The other two mappings are INFERRED, not empirically confirmed by any
// spike round — recorded here so a future reader (or a future spike round)
// can tell an empirically confirmed mapping from an inferred one and
// update this comment when better evidence exists:
//   - events.ConnectFailureMainDeviceGone (403): whatsmeow's own source
//     comment (types/events/events.go) says this "is now called LOCKED in
//     the whatsapp web code" — read as the primary phone's own
//     registration disappearing out from under this linked session
//     (closest available match to 08-RESEARCH.md Pitfall 2's "primary
//     phone stayed offline past WhatsApp's own session-durability
//     window" framing), not a deliberate unlink action taken on THIS
//     device. Mapped to healthStateExpired.
//   - events.ConnectFailureUnknownLogout (406): despite its Go constant
//     name, whatsmeow's own source comment says this "is now called
//     BANNED in the whatsapp web code" — distinct from the dedicated
//     events.TemporaryBan event (reason 402, handled separately in
//     eventhandler.go), presumably a longer-lived restriction that also
//     force-logs-out the session. Mapped to healthStateBanned.
//
// Every other reason, including any future WhatsApp-side reason code this
// plugin does not yet recognise, maps to healthStateDelinked — NEVER
// silently to healthy (this task's own explicit requirement).
func healthStateFromLogoutReason(reason events.ConnectFailureReason) healthState {
	switch reason {
	case events.ConnectFailureLoggedOut:
		return healthStateDelinked
	case events.ConnectFailureMainDeviceGone:
		return healthStateExpired
	case events.ConnectFailureUnknownLogout:
		return healthStateBanned
	default:
		return healthStateDelinked
	}
}
