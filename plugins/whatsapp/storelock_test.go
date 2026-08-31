package main

import (
	"errors"
	"testing"
)

// TestStoreLock_SecondAcquireFails proves the CONTEXT hard requirement's
// enforcement mechanism: a second acquireStoreLock against the same data
// directory, while the first is still held, returns ErrStoreInUse rather
// than blocking or silently succeeding.
func TestStoreLock_SecondAcquireFails(t *testing.T) {
	dir := t.TempDir()

	first, err := acquireStoreLock(dir)
	if err != nil {
		t.Fatalf("first acquireStoreLock: %v", err)
	}
	defer first.Release()

	_, err = acquireStoreLock(dir)
	if !errors.Is(err, ErrStoreInUse) {
		t.Fatalf("second acquireStoreLock: want ErrStoreInUse, got %v", err)
	}
}

// TestStoreLock_ReleaseAllowsReacquire proves releasing the first lock lets
// a subsequent acquireStoreLock against the same directory succeed.
func TestStoreLock_ReleaseAllowsReacquire(t *testing.T) {
	dir := t.TempDir()

	first, err := acquireStoreLock(dir)
	if err != nil {
		t.Fatalf("first acquireStoreLock: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	second, err := acquireStoreLock(dir)
	if err != nil {
		t.Fatalf("acquireStoreLock after release: %v", err)
	}
	defer second.Release()
}
