package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// writeFixtureConfigDir builds a configDir/sql/db.sqlite (via
// buildFixtureDatabase) and configDir/config.json carrying the legacy
// plaintext "key" field pointing at fixtureKeyHex — this file's "healthy
// install" starting point, reused by the schema-ceiling and healthy
// cases below.
func writeFixtureConfigDir(t *testing.T, userVersion int) string {
	t.Helper()
	configDir := t.TempDir()
	sqlDir := filepath.Join(configDir, "sql")
	if err := os.MkdirAll(sqlDir, 0o755); err != nil {
		t.Fatalf("mkdir fixture sql dir: %v", err)
	}
	buildFixtureDatabase(t, filepath.Join(sqlDir, "db.sqlite"), userVersion)

	configJSON := fmt.Sprintf(`{"key":%q}`, fixtureKeyHex)
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configJSON), 0o600); err != nil {
		t.Fatalf("write fixture config.json: %v", err)
	}
	return configDir
}

// TestHealth_MissingDatabase: a config directory whose sql/db.sqlite does
// not exist at all — Reachable:false, LastError naming the missing
// database path, Go error nil.
func TestHealth_MissingDatabase(t *testing.T) {
	configDir := t.TempDir() // deliberately empty: no sql/db.sqlite, no config.json

	plugin := NewSourcePlugin(configDir)
	resp, err := plugin.Health(context.Background(), &toposv1.HealthRequest{})
	if err != nil {
		t.Fatalf("Health must never return a gRPC error, got: %v", err)
	}
	if resp.GetReachable() {
		t.Fatal("expected Reachable=false for a missing database")
	}
	if !strings.Contains(resp.GetLastError(), configDir) {
		t.Errorf("expected LastError to name the missing database path, got: %q", resp.GetLastError())
	}
	if !strings.Contains(resp.GetLastError(), "not found") {
		t.Errorf("expected LastError to say the database was not found, got: %q", resp.GetLastError())
	}
}

// TestHealth_KeyResolutionFailure: the database file exists, but
// config.json declares an unrecognised safeStorageBackend — a
// key-resolution failure that never reaches openReadOnly at all.
// Reachable:false, LastError naming the resolution path attempted and
// the declared backend, Go error nil.
func TestHealth_KeyResolutionFailure(t *testing.T) {
	configDir := t.TempDir()
	sqlDir := filepath.Join(configDir, "sql")
	if err := os.MkdirAll(sqlDir, 0o755); err != nil {
		t.Fatalf("mkdir fixture sql dir: %v", err)
	}
	buildFixtureDatabase(t, filepath.Join(sqlDir, "db.sqlite"), highestSupportedSchemaVersion)

	const declaredBackend = "totally-unknown-backend"
	configJSON := fmt.Sprintf(`{"encryptedKey":"aabbcc","safeStorageBackend":%q}`, declaredBackend)
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configJSON), 0o600); err != nil {
		t.Fatalf("write fixture config.json: %v", err)
	}

	plugin := NewSourcePlugin(configDir)
	resp, err := plugin.Health(context.Background(), &toposv1.HealthRequest{})
	if err != nil {
		t.Fatalf("Health must never return a gRPC error, got: %v", err)
	}
	if resp.GetReachable() {
		t.Fatal("expected Reachable=false for a key-resolution failure")
	}
	if !strings.Contains(resp.GetLastError(), "key resolution failed") {
		t.Errorf("expected LastError to name key resolution as the failing step, got: %q", resp.GetLastError())
	}
	if !strings.Contains(resp.GetLastError(), declaredBackend) {
		t.Errorf("expected LastError to name the declared backend %q, got: %q", declaredBackend, resp.GetLastError())
	}
}

// TestHealth_SchemaVersionCeiling: a fixture whose PRAGMA user_version
// exceeds the ceiling. Reachable:false, LastError naming both versions,
// Go error nil.
func TestHealth_SchemaVersionCeiling(t *testing.T) {
	configDir := writeFixtureConfigDir(t, highestSupportedSchemaVersion+1)

	plugin := NewSourcePlugin(configDir)
	resp, err := plugin.Health(context.Background(), &toposv1.HealthRequest{})
	if err != nil {
		t.Fatalf("Health must never return a gRPC error, got: %v", err)
	}
	if resp.GetReachable() {
		t.Fatal("expected Reachable=false for a schema version above the ceiling")
	}
	wantFound := fmt.Sprintf("%d", highestSupportedSchemaVersion+1)
	wantCeiling := fmt.Sprintf("%d", highestSupportedSchemaVersion)
	if !strings.Contains(resp.GetLastError(), wantFound) {
		t.Errorf("expected LastError to name the found version %s, got: %q", wantFound, resp.GetLastError())
	}
	if !strings.Contains(resp.GetLastError(), wantCeiling) {
		t.Errorf("expected LastError to name the ceiling %s, got: %q", wantCeiling, resp.GetLastError())
	}
}

// TestHealth_Healthy: a good fixture returns Reachable:true.
func TestHealth_Healthy(t *testing.T) {
	configDir := writeFixtureConfigDir(t, highestSupportedSchemaVersion)

	plugin := NewSourcePlugin(configDir)
	resp, err := plugin.Health(context.Background(), &toposv1.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !resp.GetReachable() {
		t.Fatalf("expected Reachable=true for a healthy fixture, got LastError=%q", resp.GetLastError())
	}
}

// TestHealth_NeverLeaksSecretMaterial: no LastError string produced by
// any of the failure cases above contains a key or a message body.
func TestHealth_NeverLeaksSecretMaterial(t *testing.T) {
	const fixtureMessageBody = "let's book the van" // one of buildFixtureDatabase's own fixture message bodies

	configDir := t.TempDir()
	sqlDir := filepath.Join(configDir, "sql")
	if err := os.MkdirAll(sqlDir, 0o755); err != nil {
		t.Fatalf("mkdir fixture sql dir: %v", err)
	}
	buildFixtureDatabase(t, filepath.Join(sqlDir, "db.sqlite"), highestSupportedSchemaVersion)

	configJSON := `{"encryptedKey":"aabbcc","safeStorageBackend":"totally-unknown-backend"}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configJSON), 0o600); err != nil {
		t.Fatalf("write fixture config.json: %v", err)
	}

	plugin := NewSourcePlugin(configDir)
	resp, err := plugin.Health(context.Background(), &toposv1.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if strings.Contains(resp.GetLastError(), fixtureKeyHex) {
		t.Errorf("LastError leaked the fixture key: %q", resp.GetLastError())
	}
	if strings.Contains(resp.GetLastError(), fixtureMessageBody) {
		t.Errorf("LastError leaked a fixture message body: %q", resp.GetLastError())
	}
}
