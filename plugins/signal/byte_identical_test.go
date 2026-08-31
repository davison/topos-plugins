package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// fixtureKeyHex is a fixed, non-secret 32-byte SQLCipher raw key used
// only by this package's own fixture databases — never a real Signal
// Desktop key. Shared across this file, dsn_test.go and
// schema_version_fixture_test.go (Task 3).
const fixtureKeyHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd"

// buildFixtureDatabase creates a fresh SQLCipher database at path (via
// the same driver/DSN shape the plugin's own dsn.go uses, opened
// read-write only for this one-time build), with PRAGMA user_version set
// to userVersion, one group and one 1:1 conversation matching the column
// shape 04-01-SUMMARY.md recorded from the real database, and messages
// for each spanning two distinct local calendar days. Shared by this
// file's byte-identical test and schema_version_fixture_test.go's
// negative control (Task 3) — no committed binary fixture exists (see
// testdata/README.md): every fixture is built at test time from the
// driver, so no encrypted blob and no key material is ever committed.
func buildFixtureDatabase(t *testing.T, path string, userVersion int) {
	t.Helper()

	dsn := fmt.Sprintf("file:%s?_key=x'%s'&_cipher_page_size=4096", path, fixtureKeyHex)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	defer db.Close()

	schemaStmts := []string{
		`CREATE TABLE conversations (
			id TEXT PRIMARY KEY,
			type TEXT,
			name TEXT,
			profileName TEXT,
			profileFamilyName TEXT,
			e164 TEXT,
			serviceId TEXT,
			json TEXT
		)`,
		`CREATE TABLE messages (
			id TEXT PRIMARY KEY,
			conversationId TEXT,
			sent_at INTEGER,
			type TEXT,
			sourceServiceId TEXT,
			body TEXT,
			isErased INTEGER,
			json TEXT
		)`,
		`CREATE TABLE items (
			id TEXT PRIMARY KEY,
			json TEXT
		)`,
		// message_attachments/reactions: real Signal Desktop keeps
		// attachments and reactions in these dedicated tables, never in
		// the message row's own json blob (message.go's
		// messageBlobFields doc comment) — schema-only here since none
		// of this file's fixture messages carry either.
		`CREATE TABLE message_attachments (
			messageId TEXT,
			conversationId TEXT,
			editHistoryIndex INTEGER,
			attachmentType TEXT,
			orderInMessage INTEGER,
			fileName TEXT,
			contentType TEXT
		)`,
		`CREATE TABLE reactions (
			messageId TEXT,
			conversationId TEXT,
			emoji TEXT,
			fromId TEXT,
			timestamp INTEGER
		)`,
	}
	for _, stmt := range schemaStmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("create fixture schema: %v", err)
		}
	}

	if _, err := db.Exec(
		`INSERT INTO conversations (id, type, name, profileName, profileFamilyName, e164, serviceId, json) VALUES (?, 'group', ?, '', '', '', '', '{}')`,
		"conv-group", "House Move",
	); err != nil {
		t.Fatalf("insert fixture group conversation: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO conversations (id, type, name, profileName, profileFamilyName, e164, serviceId, json) VALUES (?, 'private', '', '', '', '', ?, ?)`,
		"conv-private", "svc-alice", `{"systemGivenName":"Alice","systemFamilyName":"Smith"}`,
	); err != nil {
		t.Fatalf("insert fixture 1:1 conversation: %v", err)
	}

	day1 := time.Date(2026, 1, 5, 12, 0, 0, 0, time.Local).UnixMilli()
	day2 := time.Date(2026, 1, 8, 12, 0, 0, 0, time.Local).UnixMilli()

	messages := []struct {
		id             string
		conversationID string
		sentAt         int64
		body           string
	}{
		{"msg-1", "conv-group", day1, "let's book the van"},
		{"msg-2", "conv-group", day2, "van is booked"},
		{"msg-3", "conv-private", day1, "see you at 3pm"},
	}
	for _, m := range messages {
		if _, err := db.Exec(
			`INSERT INTO messages (id, conversationId, sent_at, type, sourceServiceId, body, isErased, json) VALUES (?, ?, ?, 'incoming', ?, ?, 0, '{}')`,
			m.id, m.conversationID, m.sentAt, "svc-alice", m.body,
		); err != nil {
			t.Fatalf("insert fixture message: %v", err)
		}
	}

	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, userVersion)); err != nil {
		t.Fatalf("set fixture user_version: %v", err)
	}
}

// conversationsMatchReq builds a MatchRequest carrying only the
// "conversations" field — the shape this plugin declares
// (matchVocabulary = ["conversations"]) and every fixture-database test in
// this file uses to drive Match.
func conversationsMatchReq(values []string) *toposv1.MatchRequest {
	return &toposv1.MatchRequest{MatchFields: map[string]*toposv1.StringList{
		"conversations": {Values: values},
	}}
}

// hashFile returns the hex-encoded SHA-256 of the file at path.
func hashFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s for hashing: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TestDatabaseByteIdenticalAfterMatchAndFetch is the strongest read-only
// guarantee in this repository (04-02-PLAN.md Task 1, ROADMAP criterion
// 3): a fixture SQLCipher database populated with conversations and
// messages has an identical SHA-256 before and after a full Match
// followed by a Fetch of every returned item.
//
// The assertion is deliberately scoped to db.sqlite alone, never its
// -wal/-shm sidecars: SQLite's own documented WAL-reader protocol writes
// each reader's end mark into the shared-memory file even for a
// genuinely read-only connection (04-RESEARCH.md Pitfall 2), so widening
// this assertion to the whole directory would produce a false failure —
// a future reader must not "fix" it that way.
func TestDatabaseByteIdenticalAfterMatchAndFetch(t *testing.T) {
	configDir := t.TempDir()
	sqlDir := filepath.Join(configDir, "sql")
	if err := os.MkdirAll(sqlDir, 0o755); err != nil {
		t.Fatalf("create fixture sql dir: %v", err)
	}
	dbPath := filepath.Join(sqlDir, "db.sqlite")
	buildFixtureDatabase(t, dbPath, highestSupportedSchemaVersion)

	configJSON := fmt.Sprintf(`{"key":%q}`, fixtureKeyHex)
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configJSON), 0o600); err != nil {
		t.Fatalf("write fixture config.json: %v", err)
	}

	before := hashFile(t, dbPath)

	plugin := NewSourcePlugin(configDir)
	ctx := context.Background()

	matchResp, err := plugin.Match(ctx, conversationsMatchReq([]string{"House Move", "Alice Smith"}))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(matchResp.GetItems()) == 0 {
		t.Fatal("expected Match to return at least one digest from the fixture database")
	}
	for _, item := range matchResp.GetItems() {
		if _, err := plugin.Fetch(ctx, &toposv1.FetchRequest{
			SourceId: item.GetSourceId(),
			Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
		}); err != nil {
			t.Fatalf("Fetch(%s): %v", item.GetSourceId(), err)
		}
	}

	after := hashFile(t, dbPath)
	if before != after {
		t.Fatalf("db.sqlite hash changed after Match+Fetch: before=%s after=%s", before, after)
	}
}

// TestDescribe_DeclaresConversationsVocabulary proves Describe reports the
// single declared match field this plugin reads from match_fields.
func TestDescribe_DeclaresConversationsVocabulary(t *testing.T) {
	plugin := NewSourcePlugin(t.TempDir())
	resp, err := plugin.Describe(context.Background(), &toposv1.DescribeRequest{})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if len(resp.GetMatchVocabulary()) != 1 || resp.GetMatchVocabulary()[0] != "conversations" {
		t.Errorf("expected match_vocabulary [\"conversations\"], got %v", resp.GetMatchVocabulary())
	}
}

// TestMatch_UndeclaredKeyIsIgnored proves a match_fields key outside this
// plugin's declared vocabulary ("tags", which signal never declares) is
// ignored entirely — only "conversations" is read (D-05).
func TestMatch_UndeclaredKeyIsIgnored(t *testing.T) {
	configDir := t.TempDir()
	sqlDir := filepath.Join(configDir, "sql")
	if err := os.MkdirAll(sqlDir, 0o755); err != nil {
		t.Fatalf("create fixture sql dir: %v", err)
	}
	dbPath := filepath.Join(sqlDir, "db.sqlite")
	buildFixtureDatabase(t, dbPath, highestSupportedSchemaVersion)

	configJSON := fmt.Sprintf(`{"key":%q}`, fixtureKeyHex)
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configJSON), 0o600); err != nil {
		t.Fatalf("write fixture config.json: %v", err)
	}

	plugin := NewSourcePlugin(configDir)
	req := &toposv1.MatchRequest{MatchFields: map[string]*toposv1.StringList{
		"conversations": {Values: []string{"House Move"}},
		"tags":          {Values: []string{"should-be-ignored"}},
	}}
	resp, err := plugin.Match(context.Background(), req)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(resp.GetItems()) == 0 {
		t.Fatal("expected the undeclared 'tags' key to be ignored and the 'conversations' match to still return the House Move group's digest")
	}
}

// TestMatch_ProfileNameOnlyAndNoteToSelfNeverMatch_SurvivesContractChange
// is Task 3's required regression test: it proves Phase 4 D-06's 1:1
// matching rule (nickname/system-contact name only, never the contact's
// own profile name, never Note to Self) still holds end to end through
// Plugin.Match's NEW typed "conversations" match_fields entry point — not
// just at match.go's eligibleConversations unit-test layer (match_test.go),
// which never touched the wire contract in the first place. A keyword
// equal to the profile-name-only contact's profile name, and a keyword
// equal to the Note-to-Self conversation's own name, are both supplied;
// neither must produce an item.
func TestMatch_ProfileNameOnlyAndNoteToSelfNeverMatch_SurvivesContractChange(t *testing.T) {
	configDir := t.TempDir()
	sqlDir := filepath.Join(configDir, "sql")
	if err := os.MkdirAll(sqlDir, 0o755); err != nil {
		t.Fatalf("create fixture sql dir: %v", err)
	}
	dbPath := filepath.Join(sqlDir, "db.sqlite")

	dsn := fmt.Sprintf("file:%s?_key=x'%s'&_cipher_page_size=4096", dbPath, fixtureKeyHex)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}

	schemaStmts := []string{
		`CREATE TABLE conversations (
			id TEXT PRIMARY KEY, type TEXT, name TEXT, profileName TEXT,
			profileFamilyName TEXT, e164 TEXT, serviceId TEXT, json TEXT
		)`,
		`CREATE TABLE messages (
			id TEXT PRIMARY KEY, conversationId TEXT, sent_at INTEGER, type TEXT,
			sourceServiceId TEXT, body TEXT, isErased INTEGER, json TEXT
		)`,
		`CREATE TABLE items (id TEXT PRIMARY KEY, json TEXT)`,
		`CREATE TABLE message_attachments (
			messageId TEXT, conversationId TEXT, editHistoryIndex INTEGER,
			attachmentType TEXT, orderInMessage INTEGER, fileName TEXT, contentType TEXT
		)`,
		`CREATE TABLE reactions (messageId TEXT, conversationId TEXT, emoji TEXT, fromId TEXT, timestamp INTEGER)`,
	}
	for _, stmt := range schemaStmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("create fixture schema: %v", err)
		}
	}

	// profile-name-only: no nickname, no system contact name — only
	// ProfileName equals the keyword below. D-06: must never match.
	if _, err := db.Exec(
		`INSERT INTO conversations (id, type, name, profileName, profileFamilyName, e164, serviceId, json) VALUES (?, 'private', '', ?, '', '', ?, '{}')`,
		"conv-profile-only", "Renamed Person", "svc-profile-only",
	); err != nil {
		t.Fatalf("insert profile-name-only conversation: %v", err)
	}
	// Note to Self: own account's own conversation, carrying a NICKNAME
	// ("Myself") that WOULD be a valid candidate name if this conversation
	// were an ordinary 1:1 — the account's own service id is set as
	// serviceId and, via items.uuid_id below, as ownAci, so
	// readConversations marks IsNoteToSelf true regardless of the nickname
	// being present. D-05/D-06: candidateNames must still return nil.
	if _, err := db.Exec(
		`INSERT INTO conversations (id, type, name, profileName, profileFamilyName, e164, serviceId, json) VALUES (?, 'private', '', '', '', '', ?, ?)`,
		"conv-note-to-self", "svc-self", `{"nicknameGivenName":"Myself"}`,
	); err != nil {
		t.Fatalf("insert note-to-self conversation: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO items (id, json) VALUES ('uuid_id', ?)`, `{"value":"svc-self.1"}`,
	); err != nil {
		t.Fatalf("insert own-account identity item: %v", err)
	}
	db.Close()

	configJSON := fmt.Sprintf(`{"key":%q}`, fixtureKeyHex)
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configJSON), 0o600); err != nil {
		t.Fatalf("write fixture config.json: %v", err)
	}

	plugin := NewSourcePlugin(configDir)
	resp, err := plugin.Match(context.Background(), conversationsMatchReq([]string{"Renamed Person", "Myself"}))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(resp.GetItems()) != 0 {
		t.Fatalf("expected zero items — profile-name-only and Note-to-Self conversations must never match (D-06), got %+v", resp.GetItems())
	}
}

// TestLiveDatabaseByteIdentical is the live, opt-in counterpart: the
// identical before/after hash assertion against the user's real
// ~/.config/Signal/sql/db.sqlite, guarded by WEBSPACES_SIGNAL_LIVE_IT in
// the exact t.Skip-with-instructions shape
// plugins/proton/live_bridge_test.go uses. Never copies the database
// anywhere.
//
// How to run:
//
//	WEBSPACES_SIGNAL_LIVE_IT=1 go test -run TestLiveDatabaseByteIdentical -v ./...
func TestLiveDatabaseByteIdentical(t *testing.T) {
	if os.Getenv("WEBSPACES_SIGNAL_LIVE_IT") != "1" {
		t.Skip("live-Signal-Desktop test skipped: set WEBSPACES_SIGNAL_LIVE_IT=1 to run it against your real ~/.config/Signal database — see this file's doc comment")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve home directory: %v", err)
	}
	configDir := filepath.Join(home, ".config", "Signal")
	dbPath := filepath.Join(configDir, "sql", "db.sqlite")

	before := hashFile(t, dbPath)

	plugin := NewSourcePlugin(configDir)
	ctx := context.Background()

	// A keyword unlikely to match any real conversation is fine here —
	// this test only proves the read path never mutates the real
	// database, not that digests are produced against real content; the
	// conversations table is still read in full regardless of match
	// count (plugin.go's Match reads every conversation before filtering).
	matchResp, err := plugin.Match(ctx, conversationsMatchReq([]string{"webspaces-signal-live-it-byte-identical-probe"}))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	for _, item := range matchResp.GetItems() {
		if _, err := plugin.Fetch(ctx, &toposv1.FetchRequest{
			SourceId: item.GetSourceId(),
			Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
		}); err != nil {
			t.Fatalf("Fetch(%s): %v", item.GetSourceId(), err)
		}
	}

	after := hashFile(t, dbPath)
	if before != after {
		t.Fatalf("SRC-02 criterion 3 violation: db.sqlite hash changed — before=%s after=%s", before, after)
	}
}
