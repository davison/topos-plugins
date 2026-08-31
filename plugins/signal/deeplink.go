package main

import "regexp"

// e164Pattern mirrors the E.164 validator compiled into the installed
// Signal Desktop bundle itself (traced statically in
// .planning/debug/sgnl-link-didnt-make-sense.md Evidence:
// /usr/lib/signal-desktop/resources/app.asar's CPt() function, called
// with mustStartWithPlus=true: /^\+[1-9]\d{1,14}$/). A leading literal
// "+", a non-zero first digit, and one to fifteen digits total. Keeping
// our allowlist byte-for-byte identical to the consumer's own validator
// means a value we accept is guaranteed to be a value Signal accepts —
// diverge from this pattern only after re-diffing against a newer
// installed bundle.
var e164Pattern = regexp.MustCompile(`^\+[1-9][0-9]{1,14}$`)

// isValidE164 reports whether e164 matches e164Pattern.
func isValidE164(e164 string) bool {
	return e164Pattern.MatchString(e164)
}

// conversationDeepLink builds a sgnl:// deep link at
// LINK_FIDELITY_CONVERSATION_ONLY — Signal's own registered URI scheme
// (confirmed on this machine: signal-desktop's own .desktop entry
// registers x-scheme-handler/sgnl; 04-RESEARCH.md Runtime State
// Inventory). Signal Desktop has no per-message deep-link scheme at all,
// so every link this plugin builds can only ever open the surrounding
// conversation, never scroll to or highlight the specific digest's day —
// an honestly conversation-only fidelity (CONTEXT.md's locked decision),
// which 04-UI-SPEC.md's fidelity badge already communicates to the user
// rather than promising a precision this plugin cannot deliver.
//
// For a 1:1 conversation with a known E.164, the link targets that
// contact specifically via the documented "sgnl://signal.me/#p/<e164>"
// phone form (04-RESEARCH.md Sources: shkspr.mobi/blog/2023/02/
// signals-newish-uri-scheme, bugs.archlinux.org/task/69415). For a
// group, or a 1:1 with no E.164 on file, the bare "sgnl://" scheme is
// emitted — it raises Signal Desktop without navigating to any
// particular conversation. Both are still an honest, non-empty deep
// link: PLUG-03's sync-time validation rejects an item with an empty
// deep_link.
//
// 04-RESEARCH.md assumption A4, closed 2026-08-03, corrected 2026-08-04:
// both forms were invoked against this machine's installed, running
// Signal Desktop via its own registered scheme handler (`gio open`,
// confirmed the handler is `signal.desktop` via `gio mime
// x-scheme-handler/sgnl` / `xdg-mime query default
// x-scheme-handler/sgnl`). The bare "sgnl://" form's behavior was
// visually confirmed by the developer during 04-01-PLAN.md's own
// human-verify checkpoint (04-01-SUMMARY.md: raises Signal Desktop, does
// not navigate to a specific conversation for a group — the accepted,
// intended conversation-only fidelity limit, not a defect). The contact
// form's `gio open` exit-0 and Signal's single-instance-lock IPC handoff
// were also observed for the "sgnl://signal.me/#p/<e164>" form, but that
// only proved the handoff reached Signal Desktop's router — not that
// Signal accepted the payload. The developer's actual click during UAT
// surfaced Signal's own validation modal ("Something went wrong! Sorry,
// that sgnl:// link didn't make sense!"): the emitted value had its
// mandatory leading "+" percent-encoded to "%2B" by the previous
// (escape-based) implementation, and Signal's shipped route never
// percent-decodes the hash fragment before validating it, so the literal
// "+" the validator requires was never present. See
// .planning/debug/sgnl-link-didnt-make-sense.md for the full traced
// failure path. This plan replaces escaping with validate-and-refuse
// (isValidE164 / e164Pattern above), emitting the number verbatim only
// when it already matches the exact shape Signal's own validator
// accepts. Keep the existing conversation-only fidelity explanation
// above intact; it is unaffected by this correction.
func conversationDeepLink(conversationType, e164 string) string {
	if conversationType == "private" && isValidE164(e164) {
		return "sgnl://signal.me/#p/" + e164
	}
	return "sgnl://"
}
