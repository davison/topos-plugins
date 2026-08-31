package main

import "testing"

// TestMessageStore_AppendIdempotent proves a re-delivered WhatsApp message
// event (same message id, appended twice) leaves exactly one row — the
// idempotency guarantee eventhandler.go's background writer relies on when
// whatsmeow redelivers an event.
func TestMessageStore_AppendIdempotent(t *testing.T) {
	store, err := openMessageStore(t.TempDir())
	if err != nil {
		t.Fatalf("openMessageStore: %v", err)
	}
	defer store.Close()

	msg := messageRecord{
		ID:           "msg-1",
		ChatJID:      "123@g.us",
		SenderName:   "Alice",
		SentAtUnixMs: 1000,
		Body:         "hello",
	}
	if err := store.Append(msg); err != nil {
		t.Fatalf("first Append: %v", err)
	}
	// Re-deliver the identical event.
	if err := store.Append(msg); err != nil {
		t.Fatalf("second Append: %v", err)
	}

	got, err := store.MessagesForChats([]string{"123@g.us"})
	if err != nil {
		t.Fatalf("MessagesForChats: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want exactly 1 row after double-append, got %d", len(got))
	}
}

// TestMessageStore_ChatIsolationAndOrdering proves that reading one chat's
// messages back returns only that chat's rows, ordered by
// (sent_at_unix_ms, message_id) — a stable tie-break for two messages
// sharing an identical timestamp.
func TestMessageStore_ChatIsolationAndOrdering(t *testing.T) {
	store, err := openMessageStore(t.TempDir())
	if err != nil {
		t.Fatalf("openMessageStore: %v", err)
	}
	defer store.Close()

	msgs := []messageRecord{
		{ID: "b", ChatJID: "chat-a", SentAtUnixMs: 1000, Body: "second by time, id b"},
		{ID: "a", ChatJID: "chat-a", SentAtUnixMs: 1000, Body: "second by time, id a"},
		{ID: "z", ChatJID: "chat-a", SentAtUnixMs: 500, Body: "first by time"},
		{ID: "other", ChatJID: "chat-b", SentAtUnixMs: 750, Body: "different chat entirely"},
	}
	for _, m := range msgs {
		if err := store.Append(m); err != nil {
			t.Fatalf("Append %q: %v", m.ID, err)
		}
	}

	got, err := store.MessagesForChats([]string{"chat-a"})
	if err != nil {
		t.Fatalf("MessagesForChats: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 rows for chat-a (isolated from chat-b), got %d", len(got))
	}

	wantOrder := []string{"z", "a", "b"} // 500 first; then 1000-tied "a" before "b" by id
	for i, id := range wantOrder {
		if got[i].ID != id {
			t.Fatalf("row %d: want id %q, got %q (full order: %v)", i, id, got[i].ID, idsOf(got))
		}
	}
}

func idsOf(msgs []messageRecord) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.ID
	}
	return out
}
