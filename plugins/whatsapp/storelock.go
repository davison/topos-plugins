package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrStoreInUse is returned by acquireStoreLock when another process
// already holds the exclusive lock for the same data directory — the
// CONTEXT hard requirement's enforcement mechanism: the link-mode
// subprocess (link.go) and a pluginhost-launched serve-mode instance
// (connect.go) must never both hold whatsmeow's own sqlstore open at the
// same time. Named distinctly so both callers can print an actionable,
// specific message rather than a generic "resource busy" error.
var ErrStoreInUse = errors.New("whatsapp: another topos-plugin-whatsapp process already holds this data directory's lock")

// storeLock is an exclusive, per-data-directory advisory lock, held for
// the acquiring process's entire lifetime (both link.go's runLinkCLI and
// connect.go's startBackgroundClient acquire it before touching either of
// this plugin's two owned databases, and hold it until they exit).
type storeLock struct {
	f *os.File
}

// acquireStoreLock creates (idempotently) dir and takes a non-blocking
// exclusive advisory lock on dir/whatsapp.lock via syscall.Flock. Returns
// ErrStoreInUse when the lock is already held by another process; any
// other failure (e.g. the directory itself can't be created) is returned
// wrapped and named.
func acquireStoreLock(dir string) (*storeLock, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("whatsapp: create data directory %s: %w", dir, err)
	}

	path := filepath.Join(dir, "whatsapp.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: open lock file %s: %w", path, err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrStoreInUse
		}
		return nil, fmt.Errorf("whatsapp: acquire lock %s: %w", path, err)
	}

	return &storeLock{f: f}, nil
}

// Release unlocks and closes the lock file. Safe to call once per
// successful acquireStoreLock call; the process holding it is expected to
// call this on its own clean shutdown path (the lock is also released
// implicitly by the OS if the process exits or crashes without calling
// this, since flock locks are process-lifetime-scoped).
func (l *storeLock) Release() error {
	unlockErr := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	closeErr := l.f.Close()
	if unlockErr != nil {
		return fmt.Errorf("whatsapp: release lock: %w", unlockErr)
	}
	return closeErr
}
