// Package main's token_test.go covers token.go's save/load round trip, its
// atomic-0600 write discipline, and its path-resolution fallback, plus
// oauthconfig.go's scope pinning. Follows plugin_test.go's own idiom:
// Test<Thing>_<BehaviorInPlainEnglish> names, plain t.Errorf/t.Fatalf
// assertions, no assertion library, t.TempDir()/t.Setenv isolation.
package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func envLookup(env map[string]string) func(string) string {
	return func(key string) string {
		return env[key]
	}
}

func TestSaveToken_RoundTripsEveryField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", tokenFileName)

	want := &oauth2.Token{
		AccessToken:  "access-value",
		TokenType:    "Bearer",
		RefreshToken: "refresh-value",
		Expiry:       time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC),
	}

	if err := saveToken(path, want); err != nil {
		t.Fatalf("saveToken: %v", err)
	}

	got, err := loadToken(path)
	if err != nil {
		t.Fatalf("loadToken: %v", err)
	}

	if got.AccessToken != want.AccessToken {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, want.AccessToken)
	}
	if got.TokenType != want.TokenType {
		t.Errorf("TokenType = %q, want %q", got.TokenType, want.TokenType)
	}
	if got.RefreshToken != want.RefreshToken {
		t.Errorf("RefreshToken = %q, want %q", got.RefreshToken, want.RefreshToken)
	}
	if !got.Expiry.Equal(want.Expiry) {
		t.Errorf("Expiry = %v, want %v", got.Expiry, want.Expiry)
	}
}

func TestSaveToken_WritesMode0600OnFreshFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, tokenFileName)

	if err := saveToken(path, &oauth2.Token{RefreshToken: "r"}); err != nil {
		t.Fatalf("saveToken: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %v, want 0600", perm)
	}
}

func TestSaveToken_ReplacingAnExistingWiderModeFileStaysAt0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, tokenFileName)

	// Pre-create the file at a wider mode, simulating a token file that
	// somehow ended up world/group readable before this code ever ran.
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("seed WriteFile: %v", err)
	}

	if err := saveToken(path, &oauth2.Token{RefreshToken: "r"}); err != nil {
		t.Fatalf("saveToken (replace): %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode after replace = %v, want 0600 (atomic replace must never inherit a wider mode)", perm)
	}
}

func TestSaveToken_ParentDirectoryModeIs0700(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "nested", "deeper")
	path := filepath.Join(nested, tokenFileName)

	if err := saveToken(path, &oauth2.Token{RefreshToken: "r"}); err != nil {
		t.Fatalf("saveToken: %v", err)
	}

	info, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("Stat(%s): %v", nested, err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("parent dir mode = %v, want 0700", perm)
	}
}

func TestSaveToken_LeavesNoOrphanedTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, tokenFileName)

	if err := saveToken(path, &oauth2.Token{RefreshToken: "r"}); err != nil {
		t.Fatalf("saveToken: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("directory contains %d entries after save, want 1 (no orphaned temp file): %v", len(entries), names)
	}
	if entries[0].Name() != tokenFileName {
		t.Errorf("directory entry = %q, want %q", entries[0].Name(), tokenFileName)
	}
}

func TestLoadToken_AbsentFileSatisfiesErrorsIsNotExist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, tokenFileName)

	_, err := loadToken(path)
	if err == nil {
		t.Fatal("loadToken on absent file: got nil error, want non-nil")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("loadToken error = %v, want errors.Is(err, fs.ErrNotExist)", err)
	}
}

func TestLoadToken_ZeroByteFileReturnsNamedError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, tokenFileName)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("seed WriteFile: %v", err)
	}

	_, err := loadToken(path)
	if err == nil {
		t.Fatal("loadToken on zero-byte file: got nil error, want non-nil")
	}
	if got := err.Error(); !contains(got, path) {
		t.Errorf("error %q does not name the path %q", got, path)
	}
}

func TestLoadToken_MalformedJSONReturnsNamedError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, tokenFileName)
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("seed WriteFile: %v", err)
	}

	_, err := loadToken(path)
	if err == nil {
		t.Fatal("loadToken on malformed JSON: got nil error, want non-nil")
	}
	if got := err.Error(); !contains(got, path) {
		t.Errorf("error %q does not name the path %q", got, path)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

func TestDataDir_PrefersXDGDataHomeWhenSet(t *testing.T) {
	getenv := envLookup(map[string]string{
		"XDG_DATA_HOME": "/xdg/data",
		"HOME":          "/home/operator",
	})

	got, err := dataDir(getenv)
	if err != nil {
		t.Fatalf("dataDir: %v", err)
	}
	want := filepath.Join("/xdg/data", dataDirName)
	if got != want {
		t.Errorf("dataDir = %q, want %q", got, want)
	}
}

func TestDataDir_FallsBackToHomeLocalShareWhenXDGDataHomeEmpty(t *testing.T) {
	getenv := envLookup(map[string]string{
		"XDG_DATA_HOME": "",
		"HOME":          "/home/operator",
	})

	got, err := dataDir(getenv)
	if err != nil {
		t.Fatalf("dataDir: %v", err)
	}
	want := filepath.Join("/home/operator", ".local", "share", dataDirName)
	if got != want {
		t.Errorf("dataDir = %q, want %q", got, want)
	}
}

func TestNewOAuthConfig_ScopesHasExactlyOneReadOnlyElement(t *testing.T) {
	conf := newOAuthConfig("id", "secret", "http://127.0.0.1:1234")
	if len(conf.Scopes) != 1 {
		t.Fatalf("len(Scopes) = %d, want 1", len(conf.Scopes))
	}
	if conf.Scopes[0] != "https://www.googleapis.com/auth/drive.readonly" {
		t.Errorf("Scopes[0] = %q, want the read-only Drive scope", conf.Scopes[0])
	}
}

func TestNewOAuthConfig_MutatingReturnedScopesDoesNotAffectLaterConfigs(t *testing.T) {
	first := newOAuthConfig("id", "secret", "http://127.0.0.1:1234")
	first.Scopes[0] = "https://www.googleapis.com/auth/drive"

	second := newOAuthConfig("id", "secret", "http://127.0.0.1:1234")
	if second.Scopes[0] != "https://www.googleapis.com/auth/drive.readonly" {
		t.Errorf("second config's Scopes[0] = %q, want the read-only scope (mutation of a previous call's slice must not leak)", second.Scopes[0])
	}
}
