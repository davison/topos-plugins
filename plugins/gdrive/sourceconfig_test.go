// Package main's sourceconfig_test.go covers loadSourceConfig and
// clientCredentials, following plugin_test.go's Test<Fn>_<BehaviorInPlain
// English> naming idiom and plain t.Errorf/t.Fatalf assertions.
package main

import (
	"strings"
	"testing"
)

func staticGetenv(vars map[string]string) func(string) string {
	return func(key string) string {
		return vars[key]
	}
}

func TestLoadSourceConfig_UnsetVariableYieldsEmptyConfigAndNilError(t *testing.T) {
	getenv := staticGetenv(nil)
	cfg, err := loadSourceConfig(getenv)
	if err != nil {
		t.Fatalf("loadSourceConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("loadSourceConfig returned a nil config with a nil error")
	}
	if len(cfg.Extras) != 0 {
		t.Errorf("Extras = %v, want empty", cfg.Extras)
	}
}

func TestLoadSourceConfig_EmptyStringVariableYieldsEmptyConfigAndNilError(t *testing.T) {
	getenv := staticGetenv(map[string]string{sourceConfigEnvVar: ""})
	cfg, err := loadSourceConfig(getenv)
	if err != nil {
		t.Fatalf("loadSourceConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("loadSourceConfig returned a nil config with a nil error")
	}
	if len(cfg.Extras) != 0 {
		t.Errorf("Extras = %v, want empty", cfg.Extras)
	}
}

func TestLoadSourceConfig_MalformedJSONNamesVariableNotPayload(t *testing.T) {
	sentinel := "sentinel-secret-value-should-never-appear-in-error"
	raw := `{"extras": ` + sentinel // deliberately truncated/invalid JSON
	getenv := staticGetenv(map[string]string{sourceConfigEnvVar: raw})

	cfg, err := loadSourceConfig(getenv)
	if err == nil {
		t.Fatalf("loadSourceConfig(%q) = %v, %v; want non-nil error", raw, cfg, err)
	}
	if !strings.Contains(err.Error(), sourceConfigEnvVar) {
		t.Errorf("error %q does not name %s", err.Error(), sourceConfigEnvVar)
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Errorf("error %q contains the raw payload sentinel — must never echo the payload", err.Error())
	}
}

func TestLoadSourceConfig_AbsentExtrasKeyYieldsNilMapAndNilError(t *testing.T) {
	getenv := staticGetenv(map[string]string{sourceConfigEnvVar: `{"base_url": "https://example.lan"}`})
	cfg, err := loadSourceConfig(getenv)
	if err != nil {
		t.Fatalf("loadSourceConfig: %v", err)
	}
	if cfg.Extras != nil {
		t.Errorf("Extras = %v, want nil (extras key absent)", cfg.Extras)
	}
}

func TestClientCredentials_ExtrasValuesWinOverEnvironmentValues(t *testing.T) {
	cfg := &sourceConfig{Extras: map[string]string{
		"client_id":     "extras-id",
		"client_secret": "extras-secret",
	}}
	getenv := staticGetenv(map[string]string{
		"GDRIVE_CLIENT_ID":     "env-id",
		"GDRIVE_CLIENT_SECRET": "env-secret",
	})

	id, secret, err := cfg.clientCredentials(getenv)
	if err != nil {
		t.Fatalf("clientCredentials: %v", err)
	}
	if id != "extras-id" {
		t.Errorf("clientID = %q, want %q (extras must win)", id, "extras-id")
	}
	if secret != "extras-secret" {
		t.Errorf("clientSecret = %q, want %q (extras must win)", secret, "extras-secret")
	}
}

func TestClientCredentials_EnvironmentUsedWhenExtrasEntriesAbsent(t *testing.T) {
	cfg := &sourceConfig{}
	getenv := staticGetenv(map[string]string{
		"GDRIVE_CLIENT_ID":     "env-id",
		"GDRIVE_CLIENT_SECRET": "env-secret",
	})

	id, secret, err := cfg.clientCredentials(getenv)
	if err != nil {
		t.Fatalf("clientCredentials: %v", err)
	}
	if id != "env-id" || secret != "env-secret" {
		t.Errorf("clientCredentials = (%q, %q), want (%q, %q)", id, secret, "env-id", "env-secret")
	}
}

func TestClientCredentials_EnvironmentUsedWhenExtrasEntriesPresentButEmpty(t *testing.T) {
	cfg := &sourceConfig{Extras: map[string]string{
		"client_id":     "",
		"client_secret": "",
	}}
	getenv := staticGetenv(map[string]string{
		"GDRIVE_CLIENT_ID":     "env-id",
		"GDRIVE_CLIENT_SECRET": "env-secret",
	})

	id, secret, err := cfg.clientCredentials(getenv)
	if err != nil {
		t.Fatalf("clientCredentials: %v", err)
	}
	if id != "env-id" || secret != "env-secret" {
		t.Errorf("clientCredentials = (%q, %q), want (%q, %q)", id, secret, "env-id", "env-secret")
	}
}

func TestClientCredentials_WhitespaceOnlyValuesTreatedAsEmpty(t *testing.T) {
	cfg := &sourceConfig{Extras: map[string]string{
		"client_id":     "   ",
		"client_secret": "\t\n",
	}}
	getenv := staticGetenv(map[string]string{
		"GDRIVE_CLIENT_ID":     "env-id",
		"GDRIVE_CLIENT_SECRET": "env-secret",
	})

	id, secret, err := cfg.clientCredentials(getenv)
	if err != nil {
		t.Fatalf("clientCredentials: %v", err)
	}
	if id != "env-id" || secret != "env-secret" {
		t.Errorf("clientCredentials = (%q, %q), want (%q, %q) — whitespace-only extras must be treated as empty", id, secret, "env-id", "env-secret")
	}
}

func TestClientCredentials_MissingValueNamesKeyAndEnvVarNotValues(t *testing.T) {
	cfg := &sourceConfig{}
	getenv := staticGetenv(nil)

	id, secret, err := cfg.clientCredentials(getenv)
	if err == nil {
		t.Fatalf("clientCredentials = (%q, %q), %v; want a non-nil error", id, secret, err)
	}
	if id != "" || secret != "" {
		t.Errorf("clientCredentials returned a partially-resolved pair (%q, %q) with a non-nil error", id, secret)
	}
	msg := err.Error()
	for _, want := range []string{"client_id", "client_secret", "GDRIVE_CLIENT_ID", "GDRIVE_CLIENT_SECRET"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not contain %q", msg, want)
		}
	}
}

// TestFolderID_AcceptsARealisticDriveIDAndTheSuiteFixtureIDs pins WR-01's
// accept side: the exact literal this plugin's own Describe placeholder
// shows an operator, and the hyphenated fixture ids the existing suite
// configures, must both pass validation unchanged.
func TestFolderID_AcceptsARealisticDriveIDAndTheSuiteFixtureIDs(t *testing.T) {
	for _, id := range []string{"1a2B3cD4EfGhIjKlmNoPQRstuVwxYZ", "root-move-out", "root-cascade", "root_1"} {
		cfg := &sourceConfig{Extras: map[string]string{"folder_id": id}}
		got, err := cfg.folderID()
		if err != nil {
			t.Errorf("folderID(%q): unexpected error %v", id, err)
			continue
		}
		if got != id {
			t.Errorf("folderID(%q) = %q, want the value unchanged", id, got)
		}
	}
}

// TestFolderID_RejectsAValueOutsideDriveIDCharacterSet pins WR-01's
// reject side: a value that could carry quoting, whitespace, a path
// separator, or a Drive-query boolean operator into folderwalk.go's
// interpolated query string is rejected at the point it is read, with an
// error naming the extras key and never echoing the rejected value.
func TestFolderID_RejectsAValueOutsideDriveIDCharacterSet(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"single quote", `abc'def`},
		{"space", "abc def"},
		{"drive-query boolean operator payload", `x' in parents or 'y`},
		{"slash", "abc/def"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &sourceConfig{Extras: map[string]string{"folder_id": tc.value}}
			got, err := cfg.folderID()
			if err == nil {
				t.Fatalf("folderID(%q) = %q, nil; want a non-nil error", tc.value, got)
			}
			if !strings.Contains(err.Error(), "folder_id") {
				t.Errorf("error %q does not name the folder_id extras key", err.Error())
			}
			if strings.Contains(err.Error(), tc.value) {
				t.Errorf("error %q echoes the rejected value — must never appear", err.Error())
			}
		})
	}
}

// TestFolderID_EmptyValueErrorTextUnchanged pins the pre-existing
// empty-value behavior: validation is additive, the established
// not-configured error text is untouched.
func TestFolderID_EmptyValueErrorTextUnchanged(t *testing.T) {
	cfg := &sourceConfig{}
	_, err := cfg.folderID()
	if err == nil {
		t.Fatal("folderID on an empty config = nil error, want the not-configured error")
	}
	if got, want := err.Error(), `extras key "folder_id" not configured`; got != want {
		t.Errorf("error = %q, want %q (unchanged)", got, want)
	}
}
