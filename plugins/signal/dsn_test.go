package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildReadOnlyDSNShape asserts on the constructed DSN's exact shape
// (04-02-PLAN.md Task 1): mode=ro is present, the key is embedded as a
// hex pragma literal, and immutable= is never present (dsn.go's doc
// comment on openReadOnly explains why: Signal Desktop is a live
// concurrent WAL writer, and immutable=1 would disable SQLite's own
// change-detection/locking in exactly the case success criterion 3 names).
func TestBuildReadOnlyDSNShape(t *testing.T) {
	dsn := buildReadOnlyDSN("/tmp/example/db.sqlite", "deadbeef")

	if !strings.Contains(dsn, "mode=ro") {
		t.Errorf("expected the DSN to contain mode=ro, got %q", dsn)
	}
	if !strings.Contains(dsn, "_key=x'deadbeef'") {
		t.Errorf("expected the DSN to embed the key as a hex pragma literal, got %q", dsn)
	}
	if strings.Contains(dsn, "immutable=") {
		t.Errorf("expected the DSN to never contain immutable=, got %q", dsn)
	}
}

// TestSQLiteVersionFloor proves the running, linked SQLite library
// (whatever the build tag dynamically links) actually satisfies
// minSQLiteVersion — the floor is meaningless if it's never checked
// against the real linked library.
func TestSQLiteVersionFloor(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "db.sqlite")
	buildFixtureDatabase(t, dbPath, highestSupportedSchemaVersion)

	db, err := openReadOnly(dbPath, fixtureKeyHex)
	if err != nil {
		t.Fatalf("openReadOnly: %v", err)
	}
	defer db.Close()

	if err := checkSQLiteVersionFloor(db); err != nil {
		t.Fatalf("expected the linked SQLite library to satisfy the floor, got: %v", err)
	}
}

// TestSQLiteVersionFloor_ComparisonLogic exercises
// parseSQLiteVersion/compareSQLiteVersions directly against a table of
// version strings, independent of whatever the real linked library
// happens to report.
func TestSQLiteVersionFloor_ComparisonLogic(t *testing.T) {
	cases := []struct {
		version     string
		belowFloor bool
	}{
		{"3.51.3", false},
		{"3.51.4", false},
		{"3.52.0", false},
		{"4.0.0", false},
		{"3.51.2", true},
		{"3.50.9", true},
		{"2.99.99", true},
	}

	for _, c := range cases {
		found, err := parseSQLiteVersion(c.version)
		if err != nil {
			t.Fatalf("parseSQLiteVersion(%q): %v", c.version, err)
		}
		got := compareSQLiteVersions(found, minSQLiteVersion) < 0
		if got != c.belowFloor {
			t.Errorf("version %q: belowFloor=%v, want %v", c.version, got, c.belowFloor)
		}
	}
}
