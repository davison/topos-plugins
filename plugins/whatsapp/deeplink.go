package main

import "strings"

// waStandardUserServer is WhatsApp's own standard 1:1-contact JID server
// component (mirrors go.mau.fi/whatsmeow/types.DefaultUserServer's value,
// duplicated here as a plain string constant rather than importing the
// whatsmeow types package into this file for one string comparison).
const waStandardUserServer = "s.whatsapp.net"

// conversationDeepLink builds a deep link at LINK_FIDELITY_CONVERSATION_ONLY
// for chatJID (isGroup distinguishes a group subject from a 1:1 contact).
//
// 08-01-PLAN.md Task 3's real-device spike (2026-08-10) closed
// 08-RESEARCH.md Open Question 3 and corrected this function: `xdg-mime
// query default x-scheme-handler/whatsapp` returned NOTHING on the
// spike's real desktop (no WhatsApp Linux client installed at all — a
// bare "whatsapp://" URI silently does nothing on click, never even an
// error). WhatsApp's own DOCUMENTED click-to-chat web API is the only
// reliable, officially supported link shape available:
//
//   - 1:1 (a JID on WhatsApp's standard server, waStandardUserServer):
//     "https://wa.me/<digits>", where <digits> is the JID's own User
//     portion — for a STANDARD (non-LID) WhatsApp JID this IS the
//     contact's E.164 phone number with no leading "+", exactly the
//     format wa.me's own documented API requires. This plan's Match
//     never actually reaches this branch — matchVocabulary is
//     groups-only (match.go's eligibleChats filters to IsGroup), D-05's
//     1:1 widening is Plan 08-02's job — implemented now purely so
//     08-02 doesn't need to touch this file. A LID-based 1:1 JID
//     (whatsmeow's types.HiddenUserServer) carries no real phone number
//     in its User portion at all; that case falls through to the group
//     fallback below until Plan 08-02 handles it explicitly.
//   - Group: wa.me's click-to-chat API is 1:1-only by WhatsApp's own
//     design — there is no WhatsApp-documented, or widely-adopted
//     third-party-bridge, JID-addressable group web link. (This
//     project's own research into the most mature WhatsApp bridge,
//     mautrix-whatsapp, found no per-group deep link either — it
//     exposes WhatsApp groups only as Matrix rooms with their own
//     Matrix-side links, not a forwardable WhatsApp-side URL.) The best
//     honest fallback is "https://web.whatsapp.com/", which raises
//     WhatsApp Web generally WITHOUT navigating to the specific group —
//     the identical "raises the app, no conversation targeting"
//     fidelity plugins/signal/deeplink.go's own bare "sgnl://" fallback
//     already accepts for its analogous case.
//     LINK_FIDELITY_CONVERSATION_ONLY remains the correct label here:
//     it is also the LOWEST tier the proto enum defines (there is no
//     "app-only" tier below it), and this is not a regression from what
//     a non-functional bare "whatsapp://" would have offered anyway —
//     it is a strict improvement for the 1:1 case and an honest,
//     unchanged-fidelity fallback for the group case.
func conversationDeepLink(isGroup bool, chatJID string) string {
	if !isGroup {
		if user, server, ok := splitJID(chatJID); ok && server == waStandardUserServer && user != "" {
			return "https://wa.me/" + user
		}
	}
	return "https://web.whatsapp.com/"
}

// splitJID splits a WhatsApp JID string ("<user>@<server>") into its two
// parts. Returns ok=false for anything that doesn't contain exactly one
// "@" (defensive — a malformed or empty chatJID falls back to the group
// case above rather than emitting a broken wa.me link).
func splitJID(jid string) (user, server string, ok bool) {
	parts := strings.SplitN(jid, "@", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}
