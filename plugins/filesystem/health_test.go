package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// health_test.go pins 12-03-PLAN.md Task 2's honest-degradation contract
// (12-CONTEXT.md Claude's Discretion): a readable root — empty or not —
// is reachable, and a missing or unreadable root is unreachable with the
// OS error as the reported cause. An empty folder and a missing mount
// must never collapse into the same observable outcome (T-12-16).

func TestHealth_ReadableRootReportsReachableWithNoError(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, root, "invoice.pdf", []byte("x"))

	p := NewSourcePlugin(root, nil, false)
	resp, err := p.Health(t.Context(), &toposv1.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !resp.GetReachable() {
		t.Fatalf("expected reachable, got unreachable: %s", resp.GetLastError())
	}
	if resp.GetLastError() != "" {
		t.Errorf("expected no last_error, got %q", resp.GetLastError())
	}
}

func TestHealth_EmptyButReadableFolderReportsReachable(t *testing.T) {
	root := t.TempDir() // empty by construction

	p := NewSourcePlugin(root, nil, false)
	resp, err := p.Health(t.Context(), &toposv1.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !resp.GetReachable() {
		t.Fatalf("expected an empty-but-readable folder to be reachable, got unreachable: %s", resp.GetLastError())
	}
}

func TestHealth_NonExistentRootReportsUnreachableWithOSErrorCause(t *testing.T) {
	root := filepath.Join(t.TempDir(), "does-not-exist")

	p := NewSourcePlugin(root, nil, false)
	resp, err := p.Health(t.Context(), &toposv1.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if resp.GetReachable() {
		t.Fatal("expected unreachable for a non-existent root, got reachable")
	}
	if resp.GetLastError() == "" {
		t.Error("expected a non-empty last_error naming the OS cause")
	}
}

func TestHealth_UnreadableRootReportsUnreachableWithOSErrorCause(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-bit semantics differ on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root user bypasses permission checks")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o000); err != nil {
		t.Fatalf("chmod root unreadable: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) }) // let TempDir's own cleanup remove it

	p := NewSourcePlugin(root, nil, false)
	resp, err := p.Health(t.Context(), &toposv1.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if resp.GetReachable() {
		t.Fatal("expected an unreadable (permission-denied) root to be unreachable, got reachable")
	}
	if resp.GetLastError() == "" {
		t.Error("expected a non-empty last_error naming the OS cause")
	}
}

// TestHealth_EmptyFolderAndMissingMountAreDistinguishable is the headline
// proof this file exists for (T-12-16): an empty, readable folder and a
// missing mount must never collapse into the same observable outcome.
func TestHealth_EmptyFolderAndMissingMountAreDistinguishable(t *testing.T) {
	emptyRoot := t.TempDir()
	missingRoot := filepath.Join(t.TempDir(), "gone")

	emptyResp, err := NewSourcePlugin(emptyRoot, nil, false).Health(t.Context(), &toposv1.HealthRequest{})
	if err != nil {
		t.Fatalf("Health(empty): %v", err)
	}
	missingResp, err := NewSourcePlugin(missingRoot, nil, false).Health(t.Context(), &toposv1.HealthRequest{})
	if err != nil {
		t.Fatalf("Health(missing): %v", err)
	}

	if !emptyResp.GetReachable() {
		t.Error("expected the empty folder to be reachable")
	}
	if missingResp.GetReachable() {
		t.Error("expected the missing mount to be unreachable")
	}
}
