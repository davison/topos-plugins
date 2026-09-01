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
//   - 1780: verified 2026-09-01 (davison/topos-plugins#18, the M1 live
//     UAT remedy), against Arch package signal-desktop 8.25.0-1 — a
//     release boundary crossed since the 1760 verification on 8.22.0-1,
//     and the first advance found by guardSchemaVersion firing on the
//     operator's live instance rather than by a scheduled re-check,
//     which is the trigger this comment prescribes. Signal Desktop's two
//     migrations in between are outside the read set by inspection of
//     its ts/sql/migrations: 1770 (add-blocked-at) rewrites the
//     blocked-number/serviceId/group rows of `items` to carry blockedAt,
//     and this plugin reads `items` only for id = 'uuid_id'; 1780
//     (fts-reindex) rebuilds messages_fts, touching no table read here.
//     Verified via the same unchanged tooling against the real database
//     at 1780: every read-set column present across all five tables;
//     readConversations 210 rows; readMessages 279 records across 5
//     probed conversations, 29 with attachments, 22 with reactions.
//
// Raising this constant is a deliberate act, performed only after
// re-running that same introspection (schema_readset.go's declared read
// set plus live_schema_test.go's opt-in live test) against the real
// database at the new version — never bumped speculatively, never in
// response to a single failing sync alone, and never inferred from an
// app version number, since app version and schema version do not move
// in lockstep. The trigger for re-verification is guardSchemaVersion
// firing, not a Signal Desktop upgrade notification.
const highestSupportedSchemaVersion = 1780

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
