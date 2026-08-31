module github.com/davison/topos-plugins/plugins/signal

go 1.25.0

require github.com/mattn/go-sqlite3 v1.14.49

require (
	github.com/keybase/dbus v0.0.0-20220506165403-5aa21ea2c23a
	github.com/keybase/go-keychain v0.0.1
	golang.org/x/crypto v0.54.0
)

// go.mod replace (Task 1 checkpoint, 04-01-PLAN.md, option-a): the
// libsqlcipher build tag that dynamically links the system's own
// SQLCipher library — the driver strategy the checkpoint authorised over
// the CLAUDE.md-pinned mutecomm/go-sqlcipher/v4 (stale, bundles a
// pre-3.51.3 SQLite core; see 04-RESEARCH.md Pitfall 1) — originates from
// upstream PR github.com/mattn/go-sqlite3#1109, open since November 2022
// and still unmerged. This replace pins the exact fork+commit the
// checkpoint authorised: jgiannuzzi/go-sqlite3's `sqlcipher` branch at
// commit f208443ec79de7edaf1b80276806005a5c0cf340 (verified live against
// the GitHub API during the Task 1 checkpoint, last updated 2026-07-01 —
// the live head of PR #1109 at authorisation time). The fork's own go.mod
// still declares module path github.com/mattn/go-sqlite3 (it has never
// been renamed) — that is exactly why a `replace`, rather than a plain
// `require` of a different module path, is the correct mechanism here.
replace github.com/mattn/go-sqlite3 => github.com/jgiannuzzi/go-sqlite3 v1.14.17-0.20230327162135-f208443ec79d
