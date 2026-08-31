package main

import "testing"

// TestUpsertJoinedGroups_PopulatesChatNames proves upsertJoinedGroups
// writes a chat row (is_group=true) with the group's own subject for
// every fetched group — this plan's real-device spike found history sync
// alone never populates a group's name at all, making this the sole
// source match.go's group-name matching can ever succeed against.
func TestUpsertJoinedGroups_PopulatesChatNames(t *testing.T) {
	store, err := openMessageStore(t.TempDir())
	if err != nil {
		t.Fatalf("openMessageStore: %v", err)
	}
	defer store.Close()

	groups := []joinedGroup{
		{JID: "111@g.us", Name: "Family"},
		{JID: "222@g.us", Name: "Book Club"},
	}
	if err := upsertJoinedGroups(store, groups, 1000); err != nil {
		t.Fatalf("upsertJoinedGroups: %v", err)
	}

	chats, err := store.Chats()
	if err != nil {
		t.Fatalf("Chats: %v", err)
	}
	byJID := make(map[string]chatRecord, len(chats))
	for _, c := range chats {
		byJID[c.ChatJID] = c
	}

	for _, g := range groups {
		c, ok := byJID[g.JID]
		if !ok {
			t.Fatalf("chat %q not found after upsertJoinedGroups", g.JID)
		}
		if c.Name != g.Name {
			t.Fatalf("chat %q name = %q, want %q", g.JID, c.Name, g.Name)
		}
		if !c.IsGroup {
			t.Fatalf("chat %q IsGroup = false, want true", g.JID)
		}
	}
}

// TestUpsertJoinedGroups_UpdatesExistingChatRow proves a group already
// present (e.g. via EnsureChat from a captured message, with no name
// yet) gets its name filled in by a subsequent upsertJoinedGroups call —
// the exact sequence a real run hits (messages arrive and EnsureChat
// stubs the row before the first GetJoinedGroups IQ round trip
// completes).
func TestUpsertJoinedGroups_UpdatesExistingChatRow(t *testing.T) {
	store, err := openMessageStore(t.TempDir())
	if err != nil {
		t.Fatalf("openMessageStore: %v", err)
	}
	defer store.Close()

	if err := store.EnsureChat("111@g.us", true); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}

	if err := upsertJoinedGroups(store, []joinedGroup{{JID: "111@g.us", Name: "Family"}}, 1000); err != nil {
		t.Fatalf("upsertJoinedGroups: %v", err)
	}

	chats, err := store.Chats()
	if err != nil {
		t.Fatalf("Chats: %v", err)
	}
	if len(chats) != 1 {
		t.Fatalf("want exactly 1 chat row (update in place, not a duplicate), got %d", len(chats))
	}
	if chats[0].Name != "Family" {
		t.Fatalf("chat name = %q, want %q", chats[0].Name, "Family")
	}
}

// TestUpsertJoinedGroups_SkipsEmptyJID proves a defensive empty-JID entry
// (should never happen from a real GetJoinedGroups response) is skipped
// rather than written as a garbage row.
func TestUpsertJoinedGroups_SkipsEmptyJID(t *testing.T) {
	store, err := openMessageStore(t.TempDir())
	if err != nil {
		t.Fatalf("openMessageStore: %v", err)
	}
	defer store.Close()

	if err := upsertJoinedGroups(store, []joinedGroup{{JID: "", Name: "should be skipped"}}, 1000); err != nil {
		t.Fatalf("upsertJoinedGroups: %v", err)
	}

	chats, err := store.Chats()
	if err != nil {
		t.Fatalf("Chats: %v", err)
	}
	if len(chats) != 0 {
		t.Fatalf("want 0 chat rows for an empty-JID entry, got %d", len(chats))
	}
}
