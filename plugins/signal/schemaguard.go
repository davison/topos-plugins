package main

import (
	"database/sql"
	"fmt"
)

// highestSupportedSchemaVersion is the highest Signal Desktop database
// schema PRAGMA user_version this plugin's read set has been verified
// against. It tracks the newest schema STATE this plugin has looked at
// and proven its read set intact against — not "the newest Signal
// Desktop release this plugin supports". Those are different guarantees:
// this constant can advance even when the installed Signal Desktop
// package version does not, because Signal Desktop migrates its database
// on launch and the schema version observed at one point in time is not
// necessarily the maximum migration that release ever ships.
//
// Provenance, oldest to newest:
//   - 1730: read directly off a real, live db.sqlite running Signal
//     Desktop 8.21.0 (Arch package signal-desktop 8.21.0-1), verified
//     2026-08-03 — NOT carried over from 04-RESEARCH.md's illustrative
//     "1640" snippet, which its own doc comment flagged as never
//     independently confirmed against a real install.
//   - 1740: verified 2026-08-05 (260805-lry quick task), against the
//     SAME installed Arch package signal-desktop 8.21.0-1 — no Signal
//     Desktop upgrade occurred between the two verifications, confirming
//     the schema version advanced within a single app release rather
//     than tracking a release boundary. Verified via
//     plugins/signal/schema_readset.go's declared read set and
//     plugins/signal/live_schema_test.go's TestLiveSchemaReadSet: every
//     read-set column present, and readOwnAci/readConversations/
//     readMessages (covering readAttachments/readReactions) all
//     returned non-zero rows against the real database.
//   - 1760: verified 2026-08-19 (260819-jc1 quick task), against Arch
//     package signal-desktop 8.22.0-1 — a real Signal Desktop release
//     boundary crossed since the 1740 verification (which stayed on
//     8.21.0-1), unlike the 1730->1740 advance. Verified via the same
//     unchanged tooling, schema_readset.go's declared read set and
//     live_schema_test.go's TestLiveSchemaReadSet: every read-set column
//     present across all five tables, and readOwnAci/readConversations/
//     readMessages (covering readAttachments/readReactions) all
//     returned non-zero rows against the real database.
//
// Raising this constant is a deliberate act, performed only after
// re-running that same introspection (schema_readset.go's declared read
// set plus live_schema_test.go's opt-in live test) against the real
// database at the new version — never bumped speculatively, never in
// response to a single failing sync alone, and never inferred from an
// app version number, since app version and schema version do not move
// in lockstep. The trigger for re-verification is guardSchemaVersion
// firing, not a Signal Desktop upgrade notification.
const highestSupportedSchemaVersion = 1760

// guardSchemaVersion reads PRAGMA user_version on db and fails loudly,
// naming both the version found and the highest supported, if it exceeds
// highestSupportedSchemaVersion. Must be called before any query against
// messages/conversations (04-RESEARCH.md Pattern 2).
func guardSchemaVersion(db *sql.DB) error {
	var found int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&found); err != nil {
		return fmt.Errorf("signal: read schema version: %w", err)
	}
	if found > highestSupportedSchemaVersion {
		return fmt.Errorf(
			"signal: unrecognised database schema version %d (this plugin was built against up to %d) — refusing to import, not silently skipping",
			found, highestSupportedSchemaVersion,
		)
	}
	return nil
}
