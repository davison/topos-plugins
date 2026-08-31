package main

import (
	"database/sql"
	"fmt"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// chatRecord is this plugin's own normalized view of one row from its
// chats table — a group's own cached subject (Name), or a 1:1's saved
// address-book contact name (ContactName, D-05/D-06). is_group is derived
// once, at capture time, from the message event's own IsGroup flag, never
// inferred later. Exactly one of Name/ContactName is ever meaningful for a
// given row (Name for is_group=true, ContactName for is_group=false) —
// match.go's candidateNames picks the right one, never both.
type chatRecord struct {
	ChatJID string
	IsGroup bool
	Name    string

	// ContactName is D-06's ONLY 1:1 match-candidate source: the
	// user's own address-book/system contact name for this chat's JID,
	// as synced to the linked device via whatsmeow's own local contact
	// store (eventhandler.go's resolveContactName) — NEVER the contact's
	// self-chosen push or profile name. Empty for an unsaved contact
	// (D-07: that chat is then unmatchable, with no phone-number
	// fallback) — deliberately never backfilled from any remote-supplied
	// name. There is no separate push-name/profile-name column on this
	// table at all: an absent column cannot accidentally become a
	// candidate name later.
	ContactName string
}

// messageRecord is this plugin's own normalized view of one row from its
// messages table — the single structure digest.go's tail snippet and
// render.go's transcript both read. Unlike plugins/signal's richer
// messageRecord (attachments, reactions, quotes), this plan's schema
// carries no separate attachment/reaction tables — an attachment-only
// message is captured with Body already set to a fixed placeholder at
// event-handling time (eventhandler.go), never as structured attachment
// data, per the hybrid data model's "never store attachment bytes" rule.
type messageRecord struct {
	ID           string
	ChatJID      string
	SenderJID    string
	SenderName   string // already resolved to ownSenderLabel for an outgoing (IsFromMe) message
	IsFromMe     bool
	SentAtUnixMs int64
	Body         string // "" for a deleted-for-everyone message, regardless of what it held before
	Deleted      bool
	Edited       bool // Body already carries the LATEST revision; this flag only marks that an edit happened
}

// messageStore wraps this plugin's own SQLite database — messages.db, a
// SECOND, separate file from whatsmeow's own sqlstore (whatsmeow.db,
// connect.go), never a table inside it (whatsmeow migrates that schema
// itself; this plugin must never touch it). WAL journal mode and a busy
// timeout let the background event-handler writer (eventhandler.go) and
// the Match/Fetch readers (plugin.go) share one *sql.DB without lock
// contention.
type messageStore struct {
	db *sql.DB
}

// openMessageStore opens (creating and migrating idempotently) messages.db
// under dir.
func openMessageStore(dir string) (*messageStore, error) {
	path := filepath.Join(dir, "messages.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: open message store %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)

	for _, pragma := range []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA busy_timeout = 5000`,
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("whatsapp: apply %q on message store: %w", pragma, err)
		}
	}

	if err := applyMessageStoreSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateAddContactNameColumn(db); err != nil {
		db.Close()
		return nil, err
	}

	return &messageStore{db: db}, nil
}

func applyMessageStoreSchema(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS chats (
	chat_jid TEXT PRIMARY KEY,
	is_group INTEGER NOT NULL DEFAULT 0,
	name TEXT NOT NULL DEFAULT '',
	contact_name TEXT NOT NULL DEFAULT '',
	updated_at_unix_ms INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS messages (
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
CREATE INDEX IF NOT EXISTS idx_messages_chat_day ON messages(chat_jid, sent_at_unix_ms);
`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("whatsapp: apply message store schema: %w", err)
	}
	return nil
}

// migrateAddContactNameColumn is the idempotent additive migration D-05/
// D-06 requires: a store created by Plan 08-01 (before contact_name
// existed) must open and preserve its existing rows unchanged, not fail
// or silently drop data. applyMessageStoreSchema's own CREATE TABLE IF
// NOT EXISTS already includes contact_name for a brand-new store, so this
// is a genuine no-op there — columnExists guards the ALTER TABLE so it
// only ever runs once, against a pre-08-02 store that predates the
// column.
func migrateAddContactNameColumn(db *sql.DB) error {
	exists, err := columnExists(db, "chats", "contact_name")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE chats ADD COLUMN contact_name TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("whatsapp: add contact_name column: %w", err)
	}
	return nil
}

// columnExists reports whether table already has a column named column,
// via SQLite's own PRAGMA table_info introspection (table is always a
// fixed, code-controlled constant at every call site in this file, never
// user input — safe to interpolate into the PRAGMA statement, which does
// not accept bind parameters for its own table-name argument).
func columnExists(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return false, fmt.Errorf("whatsapp: pragma table_info(%s): %w", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, fmt.Errorf("whatsapp: scan table_info(%s) row: %w", table, err)
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *messageStore) Close() error { return s.db.Close() }

// EnsureChat inserts a chat row for chatJID if one doesn't already exist,
// leaving name empty — called from the message-event path (eventhandler.go)
// so a chat is always present in the chats table even before any
// group-metadata event has resolved its name. Never clobbers an existing
// row's name.
func (s *messageStore) EnsureChat(chatJID string, isGroup bool) error {
	_, err := s.db.Exec(
		`INSERT INTO chats (chat_jid, is_group, name, updated_at_unix_ms) VALUES (?, ?, '', 0)
		 ON CONFLICT(chat_jid) DO NOTHING`,
		chatJID, boolToInt(isGroup),
	)
	if err != nil {
		return fmt.Errorf("whatsapp: ensure chat %q: %w", chatJID, err)
	}
	return nil
}

// UpsertChatName sets chatJID's cached display name — called from
// group-metadata events (eventhandler.go) and never from the plain
// message-capture path, so a chat's name only ever comes from the group's
// OWN subject (T-08-01's mitigation), never a message sender's
// self-asserted push name.
func (s *messageStore) UpsertChatName(chatJID string, isGroup bool, name string, updatedAtUnixMs int64) error {
	_, err := s.db.Exec(
		`INSERT INTO chats (chat_jid, is_group, name, updated_at_unix_ms) VALUES (?, ?, ?, ?)
		 ON CONFLICT(chat_jid) DO UPDATE SET name = excluded.name, is_group = excluded.is_group, updated_at_unix_ms = excluded.updated_at_unix_ms`,
		chatJID, boolToInt(isGroup), name, updatedAtUnixMs,
	)
	if err != nil {
		return fmt.Errorf("whatsapp: upsert chat name %q: %w", chatJID, err)
	}
	return nil
}

// UpsertContactName sets chatJID's cached ADDRESS-BOOK contact name — the
// ONLY path that ever writes contact_name (D-06's mitigation, mirroring
// UpsertChatName's identical shape for a group's own subject). Called from
// a 1:1 message event and from whatsmeow's own contact-update events
// (eventhandler.go's resolveContactName is the ONLY source this ever
// reads from — never a message's own PushName field). contactName=""
// (an unsaved contact) is a legitimate, expected value to write — D-07
// relies on it staying empty rather than being backfilled from anything
// remote-supplied.
func (s *messageStore) UpsertContactName(chatJID, contactName string, updatedAtUnixMs int64) error {
	_, err := s.db.Exec(
		`INSERT INTO chats (chat_jid, is_group, name, contact_name, updated_at_unix_ms) VALUES (?, 0, '', ?, ?)
		 ON CONFLICT(chat_jid) DO UPDATE SET contact_name = excluded.contact_name, updated_at_unix_ms = excluded.updated_at_unix_ms`,
		chatJID, contactName, updatedAtUnixMs,
	)
	if err != nil {
		return fmt.Errorf("whatsapp: upsert contact name %q: %w", chatJID, err)
	}
	return nil
}

// Chats returns every known chat row, in no particular order.
func (s *messageStore) Chats() ([]chatRecord, error) {
	rows, err := s.db.Query(`SELECT chat_jid, is_group, name, contact_name FROM chats`)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: query chats: %w", err)
	}
	defer rows.Close()

	var out []chatRecord
	for rows.Next() {
		var c chatRecord
		var isGroup int
		if err := rows.Scan(&c.ChatJID, &isGroup, &c.Name, &c.ContactName); err != nil {
			return nil, fmt.Errorf("whatsapp: scan chat row: %w", err)
		}
		c.IsGroup = isGroup != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

// Append idempotently inserts msg — a re-delivered event with the same ID
// updates the existing row in place (ON CONFLICT DO UPDATE) rather than
// creating a duplicate, so message_id is a true primary key: appending the
// same id twice always leaves exactly one row.
func (s *messageStore) Append(msg messageRecord) error {
	_, err := s.db.Exec(
		`INSERT INTO messages (message_id, chat_jid, sender_jid, sender_name, is_from_me, sent_at_unix_ms, body, is_deleted, is_edited)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(message_id) DO UPDATE SET
			chat_jid = excluded.chat_jid,
			sender_jid = excluded.sender_jid,
			sender_name = excluded.sender_name,
			is_from_me = excluded.is_from_me,
			sent_at_unix_ms = excluded.sent_at_unix_ms,
			body = excluded.body,
			is_deleted = excluded.is_deleted,
			is_edited = excluded.is_edited`,
		msg.ID, msg.ChatJID, msg.SenderJID, msg.SenderName, boolToInt(msg.IsFromMe),
		msg.SentAtUnixMs, msg.Body, boolToInt(msg.Deleted), boolToInt(msg.Edited),
	)
	if err != nil {
		return fmt.Errorf("whatsapp: append message %q: %w", msg.ID, err)
	}
	return nil
}

// MarkDeleted marks messageID (within chatJID) deleted-for-everyone and
// clears its body — called when a whatsmeow REVOKE protocol message
// arrives (eventhandler.go). A no-op (no error) if messageID is not
// present — a revoke for a message this plugin never captured (e.g. it
// arrived before this plugin was linked) is not a failure.
func (s *messageStore) MarkDeleted(chatJID, messageID string) error {
	_, err := s.db.Exec(
		`UPDATE messages SET is_deleted = 1, body = '' WHERE message_id = ? AND chat_jid = ?`,
		messageID, chatJID,
	)
	if err != nil {
		return fmt.Errorf("whatsapp: mark deleted %q: %w", messageID, err)
	}
	return nil
}

// MarkEdited updates messageID's (within chatJID) body to newBody and
// marks it edited — called when a whatsmeow MESSAGE_EDIT protocol message
// arrives (eventhandler.go). Like MarkDeleted, a no-op if messageID is not
// present.
func (s *messageStore) MarkEdited(chatJID, messageID, newBody string) error {
	_, err := s.db.Exec(
		`UPDATE messages SET body = ?, is_edited = 1 WHERE message_id = ? AND chat_jid = ?`,
		newBody, messageID, chatJID,
	)
	if err != nil {
		return fmt.Errorf("whatsapp: mark edited %q: %w", messageID, err)
	}
	return nil
}

// MessagesForChats reads every message for every chat in chatJIDs — no
// time window, full history — ordered by (sent_at_unix_ms, message_id) so
// two messages sharing a timestamp still sort deterministically (the
// message id, WhatsApp's own stable identifier, is the tie-break; never
// map/slice iteration order).
func (s *messageStore) MessagesForChats(chatJIDs []string) ([]messageRecord, error) {
	if len(chatJIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(chatJIDs))
	args := make([]any, len(chatJIDs))
	for i, jid := range chatJIDs {
		placeholders[i] = "?"
		args[i] = jid
	}

	query := fmt.Sprintf(
		`SELECT message_id, chat_jid, sender_jid, sender_name, is_from_me, sent_at_unix_ms, body, is_deleted, is_edited
		 FROM messages
		 WHERE chat_jid IN (%s)
		 ORDER BY sent_at_unix_ms ASC, message_id ASC`,
		joinPlaceholders(placeholders),
	)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: query messages: %w", err)
	}
	defer rows.Close()

	var out []messageRecord
	for rows.Next() {
		var m messageRecord
		var isFromMe, deleted, edited int
		if err := rows.Scan(&m.ID, &m.ChatJID, &m.SenderJID, &m.SenderName, &isFromMe, &m.SentAtUnixMs, &m.Body, &deleted, &edited); err != nil {
			return nil, fmt.Errorf("whatsapp: scan message row: %w", err)
		}
		m.IsFromMe = isFromMe != 0
		m.Deleted = deleted != 0
		m.Edited = edited != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func joinPlaceholders(ph []string) string {
	out := ph[0]
	for _, p := range ph[1:] {
		out += "," + p
	}
	return out
}
