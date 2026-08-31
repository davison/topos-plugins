package main

import (
	"database/sql"
	"testing"
)

// TestEligible_GroupMatchesOnlyOnGroupsField proves a group chat whose
// subject matches a "groups" value is eligible, and that the identical
// subject value placed in "contacts" does NOT make it eligible — the two
// D-05 fields address disjoint chat kinds.
func TestEligible_GroupMatchesOnlyOnGroupsField(t *testing.T) {
	group := chatRecord{ChatJID: "123@g.us", IsGroup: true, Name: "Book Club"}

	got := eligibleChats([]chatRecord{group}, []string{"Book Club"}, nil)
	if len(got) != 1 {
		t.Fatalf("group matched via groups field: want 1 eligible chat, got %d", len(got))
	}

	got = eligibleChats([]chatRecord{group}, nil, []string{"Book Club"})
	if len(got) != 0 {
		t.Fatalf("identical value placed in contacts field: want 0 eligible chats, got %d", len(got))
	}
}

// TestEligible_ContactMatchesOnlyOnContactsField is the 1:1 mirror of the
// above: a 1:1 chat's stored contact_name matching a "contacts" value is
// eligible, and the identical value placed in "groups" does NOT make it
// eligible.
func TestEligible_ContactMatchesOnlyOnContactsField(t *testing.T) {
	contact := chatRecord{ChatJID: "1234567890@s.whatsapp.net", IsGroup: false, ContactName: "Jordan Rivera"}

	got := eligibleChats([]chatRecord{contact}, nil, []string{"Jordan Rivera"})
	if len(got) != 1 {
		t.Fatalf("1:1 matched via contacts field: want 1 eligible chat, got %d", len(got))
	}

	got = eligibleChats([]chatRecord{contact}, []string{"Jordan Rivera"}, nil)
	if len(got) != 0 {
		t.Fatalf("identical value placed in groups field: want 0 eligible chats, got %d", len(got))
	}
}

// TestEligible_PushNameIsNeverACandidate proves D-06's anti-injection
// rule at the match layer: a 1:1 chat whose contact_name is EMPTY (this
// plugin never writes a remote-supplied push name into that column — see
// messagestore.go's chatRecord doc comment) is not matched by a keyword
// equal to what the remote party's own push name happens to be, even
// though that identical string WOULD have matched had it been written to
// contact_name instead. There is no push-name field on chatRecord at all
// to accidentally read — this test exercises the only surface where such
// a bypass could occur: candidateNames/eligibleChats reading anything
// other than ContactName.
func TestEligible_PushNameIsNeverACandidate(t *testing.T) {
	const attackerChosenPushName = "Definitely Not A Scammer"
	// A 1:1 chat with an unsaved contact: contact_name is empty exactly
	// as eventhandler.go's resolveContactName leaves it when the address
	// book has no saved name — regardless of what push name the remote
	// party broadcasts.
	unsaved := chatRecord{ChatJID: "19999999999@s.whatsapp.net", IsGroup: false, ContactName: ""}

	got := eligibleChats([]chatRecord{unsaved}, nil, []string{attackerChosenPushName})
	if len(got) != 0 {
		t.Fatalf("a chat with an unsaved contact must never match on any value, including an attacker's own chosen push name — got %d eligible chats", len(got))
	}
}

// TestEligible_UnsavedContactNeverMatchesIncludingOwnPhoneNumber proves
// D-07: a 1:1 chat with an empty contact_name (unsaved contact) is never
// eligible no matter what the match values contain — INCLUDING when a
// match value equals the chat's own phone number or bare JID. There is no
// phone-number matching rule of any kind.
func TestEligible_UnsavedContactNeverMatchesIncludingOwnPhoneNumber(t *testing.T) {
	const jid = "15551234567@s.whatsapp.net"
	unsaved := chatRecord{ChatJID: jid, IsGroup: false, ContactName: ""}

	for _, value := range []string{"15551234567", jid, "+15551234567"} {
		t.Run(value, func(t *testing.T) {
			got := eligibleChats([]chatRecord{unsaved}, nil, []string{value})
			if len(got) != 0 {
				t.Fatalf("unsaved contact matched on phone-number-shaped value %q — no phone-number matching rule exists (D-07)", value)
			}
		})
	}
}

// TestMatch_ExactCaseInsensitiveOnly proves the Phase 1 D-03 matching
// convention holds for both fields: differing case matches; a substring,
// a prefix, and a superstring of the stored name all fail.
func TestMatch_ExactCaseInsensitiveOnly(t *testing.T) {
	group := chatRecord{ChatJID: "1@g.us", IsGroup: true, Name: "Book Club"}

	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{"differing case matches", "BOOK CLUB", true},
		{"substring fails", "Book", false},
		{"prefix fails", "Book Clu", false},
		{"superstring fails", "Book Club Extra", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := len(eligibleChats([]chatRecord{group}, []string{tc.value}, nil)) == 1
			if got != tc.want {
				t.Fatalf("value %q: want matched=%v, got %v", tc.value, tc.want, got)
			}
		})
	}
}

// TestEligible_UnionOfBothFieldsNoDuplicates proves both fields populated
// in one match block yields the union of eligible chats with no duplicate
// chat appearing twice — guaranteed here by construction (a chat is
// either a group or a 1:1, never both), but pinned as an explicit
// behavioural contract.
func TestEligible_UnionOfBothFieldsNoDuplicates(t *testing.T) {
	chats := []chatRecord{
		{ChatJID: "1@g.us", IsGroup: true, Name: "Book Club"},
		{ChatJID: "2@g.us", IsGroup: true, Name: "Work Chat"},
		{ChatJID: "1234@s.whatsapp.net", IsGroup: false, ContactName: "Jordan Rivera"},
		{ChatJID: "5678@s.whatsapp.net", IsGroup: false, ContactName: "Taylor Kim"},
	}

	got := eligibleChats(chats, []string{"Book Club"}, []string{"Jordan Rivera"})
	if len(got) != 2 {
		t.Fatalf("want 2 eligible chats (one group, one contact), got %d", len(got))
	}

	seen := make(map[string]bool, len(got))
	for _, c := range got {
		if seen[c.ChatJID] {
			t.Fatalf("chat %q appeared more than once in the result", c.ChatJID)
		}
		seen[c.ChatJID] = true
	}
	if !seen["1@g.us"] || !seen["1234@s.whatsapp.net"] {
		t.Fatalf("want the group AND the contact both present, got %+v", got)
	}
}

// TestContactNameMigration proves opening a store created by Plan 08-01
// (before contact_name existed) succeeds and preserves its existing rows —
// simulated here by building a pre-08-02 schema by hand (no contact_name
// column), then re-opening it via openMessageStore, which must run
// migrateAddContactNameColumn idempotently.
func TestContactNameMigration(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/messages.db"

	// Build a pre-08-02 store: the exact schema shape Plan 08-01 shipped,
	// with a chat row and a message row already present.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open (pre-migration fixture): %v", err)
	}
	if _, err := db.Exec(`
CREATE TABLE chats (
	chat_jid TEXT PRIMARY KEY,
	is_group INTEGER NOT NULL DEFAULT 0,
	name TEXT NOT NULL DEFAULT '',
	updated_at_unix_ms INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE messages (
	message_id TEXT PRIMARY KEY,
	chat_jid TEXT NOT NULL,
	sender_jid TEXT NOT NULL DEFAULT '',
	sender_name TEXT NOT NULL DEFAULT '',
	is_from_me INTEGER NOT NULL DEFAULT 0,
	sent_at_unix_ms INTEGER NOT NULL,
	body TEXT NOT NULL DEFAULT '',
	is_deleted INTEGER NOT NULL DEFAULT 0,
	is_edited INTEGER NOT NULL DEFAULT 0
);
INSERT INTO chats (chat_jid, is_group, name, updated_at_unix_ms) VALUES ('1@g.us', 1, 'Book Club', 1000);
INSERT INTO messages (message_id, chat_jid, sender_name, sent_at_unix_ms, body) VALUES ('m1', '1@g.us', 'Alice', 1000, 'hello');
`); err != nil {
		t.Fatalf("build pre-08-02 fixture schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture db: %v", err)
	}

	// Re-open via the real production path — must succeed, run the
	// additive migration, and preserve the existing rows.
	store, err := openMessageStore(dir)
	if err != nil {
		t.Fatalf("openMessageStore against a pre-08-02 store: %v", err)
	}
	defer store.Close()

	chats, err := store.Chats()
	if err != nil {
		t.Fatalf("Chats after migration: %v", err)
	}
	if len(chats) != 1 {
		t.Fatalf("want 1 preserved chat row, got %d", len(chats))
	}
	if chats[0].ChatJID != "1@g.us" || chats[0].Name != "Book Club" || chats[0].ContactName != "" {
		t.Fatalf("preserved chat row mismatch: got %+v", chats[0])
	}

	msgs, err := store.MessagesForChats([]string{"1@g.us"})
	if err != nil {
		t.Fatalf("MessagesForChats after migration: %v", err)
	}
	if len(msgs) != 1 || msgs[0].ID != "m1" {
		t.Fatalf("want the pre-existing message row preserved, got %+v", msgs)
	}

	// Idempotency: opening it a SECOND time must not fail (the
	// column-existence guard must actually work, not just happen to not
	// collide once).
	store.Close()
	store2, err := openMessageStore(dir)
	if err != nil {
		t.Fatalf("second openMessageStore (migration must be idempotent): %v", err)
	}
	store2.Close()
}
