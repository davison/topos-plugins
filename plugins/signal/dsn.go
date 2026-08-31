package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

// openReadOnly opens Signal Desktop's db.sqlite strictly read-only via a
// mode=ro URI DSN, using rawHexKey (already resolved by resolveKey) as
// the SQLCipher raw key, and performs one trivial read (PRAGMA
// user_version) before returning, so a wrong key surfaces here as a
// clear error rather than a confusing failure deep inside a later query
// (04-RESEARCH.md Pitfall 4 — AES/SQLCipher decryption with the wrong
// key does not error, it silently produces garbage).
//
// Deliberately NOT adding &immutable=1: Signal Desktop is a live
// concurrent writer (journal_mode=WAL) whenever it's running —
// immutable=1 tells SQLite the file will never change and disables its
// own change-detection/locking, which risks stale or torn reads exactly
// in the "Signal Desktop running at the same time" case this phase's
// own success criteria name explicitly.
//
// DSN parameter names: this driver (github.com/mattn/go-sqlite3's
// SQLiteDriver.Open, as vendored by the Task 1-authorised
// jgiannuzzi/go-sqlite3 fork — see go.mod's replace directive) accepts
// "_key=X" and "_cipher_page_size=X", NOT "_pragma_key"/
// "_pragma_cipher_page_size". This diverges from 04-RESEARCH.md's
// illustrative DSN snippet, which was written against a different
// driver's (mutecomm/go-sqlcipher's) DSN convention before Task 1
// selected the current one; the parameter names below were confirmed
// directly against this machine's real, live db.sqlite during this
// task's own schema-introspection step (04-01-SUMMARY.md records this
// deviation).
//
// The key value MUST use the SQLCipher raw-key hex-literal form
// (x'<hex>'), never a bare hex string: SQLCipher treats an unquoted
// string as a passphrase and runs it through its own key-derivation
// function, which silently derives the WRONG key from an already-raw key
// like Signal's — this is exactly the "decrypts to garbage instead of
// erroring" failure mode 04-RESEARCH.md Pitfall 4 warns about, confirmed
// hands-on: the unquoted form failed with "file is not a database" while
// the x'...' form opened correctly against the same real key.
func openReadOnly(dbPath, rawHexKey string) (*sql.DB, error) {
	dsn := buildReadOnlyDSN(dbPath, rawHexKey)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("signal: open %s read-only: %w", dbPath, err)
	}
	db.SetMaxOpenConns(1)

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		db.Close()
		return nil, fmt.Errorf("signal: verify key by reading schema version: %w", err)
	}

	// The SQLite version floor (04-RESEARCH.md assumption A2, ROADMAP.md's
	// spike note): runs immediately after the trivial key-proving read
	// above and before the caller's schema-version guard
	// (plugin.go's openGuarded calls guardSchemaVersion only after this
	// function returns) — a distro whose packaged SQLCipher lags must
	// refuse to run rather than silently reintroduce the WAL-reset
	// corruption exposure this floor exists to close.
	if err := checkSQLiteVersionFloor(db); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

// buildReadOnlyDSN constructs the mode=ro URI DSN openReadOnly uses —
// factored out so dsn_test.go can assert on its exact shape without
// needing a real database file. See openReadOnly's doc comment for the
// DSN parameter-naming and raw-key-literal rationale this mirrors.
func buildReadOnlyDSN(dbPath, rawHexKey string) string {
	return fmt.Sprintf(
		"file:%s?mode=ro&_key=x'%s'&_cipher_page_size=4096",
		dbPath, rawHexKey,
	)
}

// minSQLiteVersion is the floor this plugin refuses to run below: SQLite
// 3.51.3 (2026-03-13) fixed a critical WAL-reset database-corruption bug
// [sqlite.org/releaselog/3_51_3.html] — ROADMAP.md's mandatory spike note
// pins this floor by name, and this plugin reads a live, actively-written
// WAL database (Signal Desktop sets journal_mode=WAL) every time it runs.
// Below this floor, whatever links the driver at build time silently
// reintroduces the exact exposure the spike existed to close.
var minSQLiteVersion = [3]int{3, 51, 3}

// errSQLiteVersionBelowFloor is the named, wrapped sentinel
// checkSQLiteVersionFloor returns — callers can errors.Is against it.
var errSQLiteVersionBelowFloor = errors.New("signal: linked SQLite version is below the WAL-reset-corruption-fix floor")

// checkSQLiteVersionFloor reads sqlite_version() off the already-open
// connection db and fails loudly, naming the version found and the
// floor required, if it is below minSQLiteVersion.
func checkSQLiteVersionFloor(db *sql.DB) error {
	var versionStr string
	if err := db.QueryRow(`SELECT sqlite_version()`).Scan(&versionStr); err != nil {
		return fmt.Errorf("signal: read linked SQLite version: %w", err)
	}
	found, err := parseSQLiteVersion(versionStr)
	if err != nil {
		return fmt.Errorf("signal: parse linked SQLite version %q: %w", versionStr, err)
	}
	if compareSQLiteVersions(found, minSQLiteVersion) < 0 {
		return fmt.Errorf(
			"%w: found %s, require at least %d.%d.%d (fixes a WAL-reset database-corruption bug; this plugin reads a live WAL database)",
			errSQLiteVersionBelowFloor, versionStr, minSQLiteVersion[0], minSQLiteVersion[1], minSQLiteVersion[2],
		)
	}
	return nil
}

// parseSQLiteVersion parses a "MAJOR.MINOR.PATCH"-shaped sqlite_version()
// result (SQLite's own version string format) into its three integer
// components.
func parseSQLiteVersion(v string) ([3]int, error) {
	var out [3]int
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return out, fmt.Errorf("expected MAJOR.MINOR.PATCH, got %q", v)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, fmt.Errorf("non-numeric version component %q: %w", p, err)
		}
		out[i] = n
	}
	return out, nil
}

// compareSQLiteVersions returns -1/0/1 for a<b, a==b, a>b, comparing
// MAJOR then MINOR then PATCH in order.
func compareSQLiteVersions(a, b [3]int) int {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}
