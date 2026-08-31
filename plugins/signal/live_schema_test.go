package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// liveSchemaEnvVar is the opt-in environment variable that must be set to
// run TestLiveSchemaReadSet against the real, live Signal Desktop
// database. Unset (the default on every machine, including CI), the test
// skips loudly rather than failing (L-6) — see this file's own doc
// comment for how to run it.
const liveSchemaEnvVar = "WEBSPACES_SIGNAL_LIVE_SCHEMA"

// TestLiveSchemaReadSet is the opt-in live verification 260805-lry-PLAN.md
// Task 1 describes: it skips by default, and when opted in resolves the
// real SQLCipher key, opens the real ~/.config/Signal/sql/db.sqlite
// STRICTLY read-only via openReadOnly — deliberately NOT openGuarded,
// because openGuarded calls guardSchemaVersion, which is exactly what is
// refusing to run at the new schema version; routing this verification
// through it would make gathering the evidence needed to raise the
// ceiling impossible by construction.
//
// It then asserts every column in readSetColumns is present (via PRAGMA
// table_info), captures each read-set table's own CREATE statement (via
// sqlite_master), and functionally exercises the plugin's own read path
// (readOwnAci, readConversations, buildSenderNames, readMessages — which
// internally covers readAttachments and readReactions) against live rows,
// so a column that survived a rename in name but changed meaning is
// caught, not just a missing column.
//
// Logs schema and aggregate counts only — never message content, contact
// names, phone numbers, service identifiers, attachment filenames or key
// material (L-2).
//
// How to run:
//
//	WEBSPACES_SIGNAL_LIVE_SCHEMA=1 CGO_ENABLED=1 go test -tags libsqlcipher -run TestLiveSchemaReadSet -v ./...
func TestLiveSchemaReadSet(t *testing.T) {
	if os.Getenv(liveSchemaEnvVar) != "1" {
		t.Skipf("live Signal Desktop schema check skipped: set %s=1 to run it against your real ~/.config/Signal database — see this file's doc comment", liveSchemaEnvVar)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve home directory: %v", err)
	}
	configDir := filepath.Join(home, ".config", "Signal")
	configPath := filepath.Join(configDir, "config.json")
	dbPath := filepath.Join(configDir, "sql", "db.sqlite")

	if _, err := os.Stat(dbPath); err != nil {
		t.Skipf("live Signal Desktop database not found at %s, skipping: %v", dbPath, err)
	}

	cfg, err := readSignalConfig(configPath)
	if err != nil {
		t.Fatalf("read %s: %v", configPath, err)
	}
	rawHexKey, err := resolveKey(cfg)
	if err != nil {
		t.Fatalf("resolve key: %v", err)
	}

	// Deliberately openReadOnly, NOT openGuarded: openGuarded calls
	// guardSchemaVersion, which is exactly what is refusing to run at the
	// new version. Routing through it here would make the evidence this
	// test gathers impossible to collect.
	db, err := openReadOnly(dbPath, rawHexKey)
	if err != nil {
		t.Fatalf("open database read-only: %v", err)
	}
	defer db.Close()

	var userVersion int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&userVersion); err != nil {
		t.Fatalf("read PRAGMA user_version: %v", err)
	}
	t.Logf("OBSERVED PRAGMA user_version = %d", userVersion)

	tables := make([]string, 0, len(readSetColumns))
	for table := range readSetColumns {
		tables = append(tables, table)
	}
	sort.Strings(tables)

	for _, table := range tables {
		requiredCols := readSetColumns[table]

		present, err := tableColumns(db, table)
		if err != nil {
			t.Fatalf("read table_info for %s: %v", table, err)
		}
		if len(present) == 0 {
			t.Errorf("table %s: not found in the live database (table_info returned no rows)", table)
			continue
		}

		presentSet := make(map[string]bool, len(present))
		for _, c := range present {
			presentSet[c] = true
		}
		for _, col := range requiredCols {
			if !presentSet[col] {
				t.Errorf("table %s: required column %q is absent from the live database", table, col)
			}
		}

		createSQL, err := tableCreateStatement(db, table)
		if err != nil {
			t.Fatalf("read sqlite_master CREATE statement for %s: %v", table, err)
		}
		t.Logf("CAPTURED CREATE statement for %s:\n%s", table, createSQL)
	}

	if t.Failed() {
		// A dirty column diff STOPS here (L-5) — do not proceed to the
		// functional read, which would only produce a confusing secondary
		// failure on top of the real one.
		return
	}

	// Functional exercise: prove the plugin's OWN read functions actually
	// return rows, not just that the columns exist by name.
	ownAci, err := readOwnAci(db)
	if err != nil {
		t.Fatalf("readOwnAci: %v", err)
	}
	if ownAci == "" {
		t.Fatal("readOwnAci: expected a non-empty account identifier from the live database")
	}

	convs, err := readConversations(db, ownAci)
	if err != nil {
		t.Fatalf("readConversations: %v", err)
	}
	if len(convs) == 0 {
		t.Fatal("readConversations: expected at least one conversation from the live database")
	}
	t.Logf("readConversations: %d conversation(s) returned", len(convs))

	// Bounded slice, chosen deterministically (first few in returned
	// order) — readMessages applies no time window by design (D-08), and
	// the real database is hundreds of megabytes.
	const maxProbeConversations = 5
	probeIDs := make([]string, 0, maxProbeConversations)
	for i, c := range convs {
		if i >= maxProbeConversations {
			break
		}
		probeIDs = append(probeIDs, c.ID)
	}

	senderNames := buildSenderNames(convs, ownAci)
	msgs, err := readMessages(db, probeIDs, senderNames)
	if err != nil {
		t.Fatalf("readMessages: %v", err)
	}

	var withAttachments, withReactions int
	for _, m := range msgs {
		if len(m.Attachments) > 0 {
			withAttachments++
		}
		if len(m.Reactions) > 0 {
			withReactions++
		}
	}

	// Aggregate counts only — no body, sender name, filename or
	// identifier of any kind (L-2).
	t.Logf(
		"readMessages: %d probed conversation(s), %d message record(s), %d with attachment(s), %d with reaction(s)",
		len(probeIDs), len(msgs), withAttachments, withReactions,
	)
}

// tableColumns returns the column names PRAGMA table_info(table) reports,
// or an empty slice if the table does not exist.
func tableColumns(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dfltValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// tableCreateStatement returns table's own CREATE TABLE statement as
// captured in sqlite_master — the precedent record L-7 requires.
func tableCreateStatement(db *sql.DB, table string) (string, error) {
	var createSQL string
	err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&createSQL)
	if err != nil {
		return "", err
	}
	return createSQL, nil
}
