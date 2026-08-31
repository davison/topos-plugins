package main

import "testing"

// TestConversationDeepLink_OneOnOneStandardJID proves a 1:1 chat on
// WhatsApp's standard server builds a wa.me click-to-chat link from the
// JID's own User portion (no leading "+", matching wa.me's documented
// format).
func TestConversationDeepLink_OneOnOneStandardJID(t *testing.T) {
	got := conversationDeepLink(false, "447700900000@s.whatsapp.net")
	want := "https://wa.me/447700900000"
	if got != want {
		t.Fatalf("conversationDeepLink(false, standard JID) = %q, want %q", got, want)
	}
}

// TestConversationDeepLink_Group proves a group chat falls back to the
// honest, non-conversation-specific WhatsApp Web URL — wa.me has no
// per-group equivalent.
func TestConversationDeepLink_Group(t *testing.T) {
	got := conversationDeepLink(true, "123456789@g.us")
	want := "https://web.whatsapp.com/"
	if got != want {
		t.Fatalf("conversationDeepLink(true, group JID) = %q, want %q", got, want)
	}
}

// TestConversationDeepLink_LIDJIDFallsBackToGroupURL proves a 1:1 chat
// whose JID is NOT on WhatsApp's standard server (e.g. a LID-based JID,
// whose User portion carries no real phone number) falls back to the
// group URL rather than emitting a broken wa.me link built from an
// opaque LID.
func TestConversationDeepLink_LIDJIDFallsBackToGroupURL(t *testing.T) {
	got := conversationDeepLink(false, "123456789@lid")
	want := "https://web.whatsapp.com/"
	if got != want {
		t.Fatalf("conversationDeepLink(false, LID JID) = %q, want %q", got, want)
	}
}

// TestConversationDeepLink_MalformedJIDFallsBackSafely proves a malformed
// or empty chatJID never panics or produces a broken URL — it falls back
// to the group URL.
func TestConversationDeepLink_MalformedJIDFallsBackSafely(t *testing.T) {
	for _, jid := range []string{"", "no-at-sign", "@s.whatsapp.net"} {
		got := conversationDeepLink(false, jid)
		want := "https://web.whatsapp.com/"
		if got != want {
			t.Fatalf("conversationDeepLink(false, %q) = %q, want %q", jid, got, want)
		}
	}
}
