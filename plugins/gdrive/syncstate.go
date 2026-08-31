// Package main's syncstate.go persists this plugin's own private
// folder-membership bookkeeping — the id-keyed tree of Drive files/folders
// under the configured root, plus the changes.list starting page token —
// in the plugin-owned XDG data directory GAP-01/GAP-02 already
// established. Mirrors token.go's atomic tmp+O_EXCL+fsync+rename+0600
// write sequence and its malformed-vs-not-found load-error distinction
// exactly (this repository's own established convention, not reinvented
// here).
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// syncStateFileName is the JSON file this plugin's sync bookkeeping is
// persisted to, inside dataDir() alongside token.go's token.json.
const syncStateFileName = "syncstate.json"

// driveNode is one Drive file or folder's persisted identity/structure
// metadata — never its content, preview, or rendition bytes (this plan's
// standing privacy prohibition: the sync store holds identity and
// structure only). Folders are stored here too (the ancestor-chain
// membership walk needs them) but are excluded from Match's returned item
// set by match.go via MimeType == folderMimeType (GAP-10).
type driveNode struct {
	Name         string `json:"name"`
	MimeType     string `json:"mimeType"`
	ParentID     string `json:"parentId"`
	ModifiedTime string `json:"modifiedTime"`
	WebViewLink  string `json:"webViewLink"`

	// Size is Drive's own internal representation size (blobs and Google
	// Workspace editor files), populated identically by folderwalk.go's
	// walkFolder and changepoll.go's applyChange so the full-walk and
	// delta-apply paths can never disagree about a node's shape. It is NOT
	// a predictor of any export's byte count (04-RESEARCH.md Pitfall 4) —
	// it exists solely to support fetchcontent.go's pre-flight
	// maxFetchBytes cap on a regular file's CONTENT_VARIANT_FULL fetch,
	// never for export-ceiling detection.
	Size int64 `json:"size"`
}

// syncState is this plugin's whole persisted sync-bookkeeping shape: the
// configured root's own id and name (RootName is the GAP-09 Option A
// value that alone matches everything synced by this instance), the
// current changes.list starting page token, and the full id-keyed tree
// under that root.
type syncState struct {
	RootID      string                `json:"rootId"`
	RootName    string                `json:"rootName"`
	ChangeToken string                `json:"changeToken"`
	Tree        map[string]*driveNode `json:"tree"`
}

// syncStatePath resolves the full path to this plugin's persisted
// sync-state file: dataDir() (token.go's existing XDG resolver, taken
// as-is — never re-derived) joined with syncStateFileName. Creates
// nothing.
func syncStatePath(getenv func(string) string) (string, error) {
	dir, err := dataDir(getenv)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, syncStateFileName), nil
}

// saveSyncState persists st as indented JSON at path, atomically —
// byte-for-byte the same MkdirAll 0700 -> O_EXCL 0600 tmp file -> Write ->
// Sync -> Close -> Rename sequence token.go's saveToken already
// establishes, so any reader observes either the complete previous state
// or the complete new one, never a partial write, and no orphaned .tmp
// file survives any failure branch.
func saveSyncState(path string, st *syncState) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create sync state directory %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal sync state: %w", err)
	}

	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create temp sync state file %s: %w", tmp, err)
	}

	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write temp sync state file %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("sync temp sync state file %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close temp sync state file %s: %w", tmp, err)
	}

	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename temp sync state file %s to %s: %w", tmp, path, err)
	}
	return nil
}

// errSyncStateMalformed names a sync-state file that exists but cannot be
// trusted — present but empty, present but not valid JSON, or present and
// valid JSON but missing a field a real persisted state must always carry
// (a nil Tree map or an empty RootID, neither of which a successful
// saveSyncState call ever produces). Distinct from fs.ErrNotExist so
// callers can distinguish "never synced" (full first-run walk) from
// "synced before, state now corrupt" (also a full first-run walk, but a
// different, loggable cause).
var errSyncStateMalformed = errors.New("sync state file malformed")

// loadSyncState reads and unmarshals the sync state at path. A caller can
// test for "never synced" with errors.Is(err, fs.ErrNotExist); any other
// error (including a present-but-empty, present-but-invalid, or
// present-but-incomplete file) names the path and failure class only,
// never the file's contents — this file can carry the operator's private
// folder/file inventory.
func loadSyncState(path string) (*syncState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("sync state file %s: %w", path, err)
		}
		return nil, fmt.Errorf("read sync state file %s: %w", path, err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("sync state file %s: %w: file is empty", path, errSyncStateMalformed)
	}
	var st syncState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("sync state file %s: %w: %w", path, errSyncStateMalformed, err)
	}
	if st.Tree == nil || st.RootID == "" {
		return nil, fmt.Errorf("sync state file %s: %w: missing tree or root id", path, errSyncStateMalformed)
	}
	return &st, nil
}
