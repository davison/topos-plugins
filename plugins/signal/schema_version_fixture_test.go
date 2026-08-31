package main

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestSchemaVersionCeiling is the negative control proving
// guardSchemaVersion is not vacuous (04-02-PLAN.md Task 3, ROADMAP
// criterion 5): a fixture database built via byte_identical_test.go's
// buildFixtureDatabase, with PRAGMA user_version set one above the
// ceiling, must fail loudly naming both the version found and the
// ceiling; a fixture at exactly the ceiling and a fixture below it must
// both pass.
func TestSchemaVersionCeiling(t *testing.T) {
	cases := []struct {
		name      string
		version   int
		wantError bool
	}{
		{"above ceiling", highestSupportedSchemaVersion + 1, true},
		{"at ceiling", highestSupportedSchemaVersion, false},
		{"below ceiling", highestSupportedSchemaVersion - 1, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "db.sqlite")
			buildFixtureDatabase(t, dbPath, c.version)

			db, err := openReadOnly(dbPath, fixtureKeyHex)
			if err != nil {
				t.Fatalf("openReadOnly: %v", err)
			}
			defer db.Close()

			err = guardSchemaVersion(db)
			switch {
			case c.wantError && err == nil:
				t.Fatal("expected guardSchemaVersion to fail for a schema version above the ceiling")
			case !c.wantError && err != nil:
				t.Fatalf("expected guardSchemaVersion to pass, got: %v", err)
			}

			if c.wantError {
				foundStr := strconv.Itoa(c.version)
				ceilingStr := strconv.Itoa(highestSupportedSchemaVersion)
				if !strings.Contains(err.Error(), foundStr) {
					t.Errorf("expected the error to name the found version %s, got: %v", foundStr, err)
				}
				if !strings.Contains(err.Error(), ceilingStr) {
					t.Errorf("expected the error to name the ceiling %s, got: %v", ceilingStr, err)
				}
			}
		})
	}
}
