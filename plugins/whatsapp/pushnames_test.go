package main

import "testing"

// TestPushNameCache_MergeThenLookup proves a merged jid->name entry is
// retrievable via lookup.
func TestPushNameCache_MergeThenLookup(t *testing.T) {
	c := newPushNameCache()
	c.merge(map[string]string{"111@s.whatsapp.net": "Alice"})

	if got := c.lookup("111@s.whatsapp.net"); got != "Alice" {
		t.Fatalf("lookup() = %q, want %q", got, "Alice")
	}
}

// TestPushNameCache_LookupUnknownReturnsEmpty proves an unknown jid
// returns "" rather than panicking or a zero-value surprise.
func TestPushNameCache_LookupUnknownReturnsEmpty(t *testing.T) {
	c := newPushNameCache()
	if got := c.lookup("nobody@s.whatsapp.net"); got != "" {
		t.Fatalf("lookup(unknown) = %q, want \"\"", got)
	}
}

// TestPushNameCache_MergeNeverOverwritesWithEmpty proves a later merge
// carrying an empty name for an already-known jid does not erase the
// previously cached name.
func TestPushNameCache_MergeNeverOverwritesWithEmpty(t *testing.T) {
	c := newPushNameCache()
	c.merge(map[string]string{"111@s.whatsapp.net": "Alice"})
	c.merge(map[string]string{"111@s.whatsapp.net": ""})

	if got := c.lookup("111@s.whatsapp.net"); got != "Alice" {
		t.Fatalf("lookup() after empty-name merge = %q, want %q (unchanged)", got, "Alice")
	}
}

// TestPushNameCache_MergeIsFirstSeenWins proves a later merge for an
// already-known jid does not replace the first-cached name (a stability
// choice against churn across repeated history syncs).
func TestPushNameCache_MergeIsFirstSeenWins(t *testing.T) {
	c := newPushNameCache()
	c.merge(map[string]string{"111@s.whatsapp.net": "Alice"})
	c.merge(map[string]string{"111@s.whatsapp.net": "Alice Smith"})

	if got := c.lookup("111@s.whatsapp.net"); got != "Alice" {
		t.Fatalf("lookup() after second merge = %q, want %q (first-seen wins)", got, "Alice")
	}
}
