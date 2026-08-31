# plugins/signal test fixtures

This directory intentionally holds no committed binary fixture database.

Every SQLCipher fixture this package's tests need is built at test time,
directly from the same driver the plugin itself links
(`buildFixtureDatabase` in `byte_identical_test.go`): a fresh, empty
database is created in `t.TempDir()`, a `conversations`/`messages`/`items`
schema matching the real Signal Desktop database's column shape
(04-01-SUMMARY.md) is created, a handful of rows are inserted, and
`PRAGMA user_version` is set to whatever value the calling test needs.

Building fixtures this way, rather than committing a pre-built encrypted
`.sqlite` file, means:

- No encrypted blob and no key material of any kind is ever committed to
  this repository — the fixture key (`fixtureKeyHex`) is a fixed, public,
  non-secret test constant, never a real Signal Desktop key.
- The fixture schema stays trivially easy to keep in sync with the real
  database's column shape as this plugin evolves, since it is plain Go
  code (`CREATE TABLE` statements) rather than an opaque binary blob.
- `schema_version_fixture_test.go` (Task 3) can build a fixture at any
  chosen `PRAGMA user_version` — above, at, or below the plugin's ceiling
  — without needing a separate committed fixture per case.

The real, live `~/.config/Signal/sql/db.sqlite` is never copied anywhere
by any test in this package — see `byte_identical_test.go`'s
`TestLiveDatabaseByteIdentical` (opt-in via `WEBSPACES_SIGNAL_LIVE_IT=1`)
for the one test that touches it, strictly read-only.
