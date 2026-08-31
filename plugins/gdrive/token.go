// Package main's token.go resolves this plugin's XDG-style private-state
// directory (CONTRACT-GAPS.md GAP-01/GAP-08) and persists/loads the OAuth
// refresh token that lives there. Every path-resolving function here takes
// an injected getenv func(string) string parameter rather than calling
// os.Getenv directly — the same testability shape contract/mock/readiness.go
// already established in this repository — so tests can override HOME/
// XDG_DATA_HOME without os.Setenv fragility. Production callers pass
// os.Getenv.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/oauth2"
)

const (
	// dataDirName is this plugin's own subdirectory name under the XDG
	// data root — never the topos host's own plugins-external path, which
	// is a completely different, host-owned directory (02-RESEARCH.md
	// Pattern 3).
	dataDirName = "topos-plugin-gdrive"

	// tokenFileName is the JSON file the refresh token is persisted to,
	// inside dataDir().
	tokenFileName = "token.json"
)

// dataDir resolves this plugin's private-state directory: $XDG_DATA_HOME
// when that variable is non-empty, else $HOME/.local/share, falling back to
// os.UserHomeDir() when HOME itself is empty (CONTRACT-GAPS.md GAP-01,
// GAP-08). This function performs no I/O beyond the getenv/UserHomeDir
// reads — it must not create the directory; saveToken is the only function
// in this file that touches the filesystem.
func dataDir(getenv func(string) string) (string, error) {
	if v := getenv("XDG_DATA_HOME"); v != "" {
		return filepath.Join(v, dataDirName), nil
	}
	home := getenv("HOME")
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve data directory: HOME not set and os.UserHomeDir failed: %w", err)
		}
	}
	return filepath.Join(home, ".local", "share", dataDirName), nil
}

// tokenPath resolves the full path to this plugin's persisted token file:
// dataDir() joined with tokenFileName. Creates nothing.
func tokenPath(getenv func(string) string) (string, error) {
	dir, err := dataDir(getenv)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, tokenFileName), nil
}

// saveToken persists tok as indented JSON at path, atomically. The parent
// directory is created 0700; the token itself is written to a fresh 0600
// file in the same directory (os.O_EXCL guarantees a fresh file, never an
// existing one silently reused) and renamed into place — the mode is
// supplied at creation time in one call, never applied as a second,
// separate permission-widening step afterward, which would leave a window
// at the umask-derived default mode (02-RESEARCH.md Anti-Patterns). The
// rename is what makes an interrupted or concurrent write safe: any reader
// observes either the complete previous token or the complete new one,
// never a truncated one.
func saveToken(path string, tok *oauth2.Token) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create token directory %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal token: %w", err)
	}

	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create temp token file %s: %w", tmp, err)
	}

	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write temp token file %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("sync temp token file %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close temp token file %s: %w", tmp, err)
	}

	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename temp token file %s to %s: %w", tmp, path, err)
	}
	return nil
}

// errTokenFileMalformed names a token file that exists but cannot be
// parsed — present but empty, or present but not valid JSON. Distinct from
// the fs.ErrNotExist case so callers can distinguish "never authorized" from
// "authorized once, but the file is now corrupt."
var errTokenFileMalformed = errors.New("token file malformed")

// loadToken reads and unmarshals the token at path. A caller can test for
// "no token has ever been saved" with errors.Is(err, fs.ErrNotExist); any
// other error (including a present-but-empty or present-but-invalid file)
// names the path, never the file's contents, per this repository's secret-
// hygiene discipline (contract/plugin-contract.md's Logging section).
func loadToken(path string) (*oauth2.Token, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("token file %s: %w", path, err)
		}
		return nil, fmt.Errorf("read token file %s: %w", path, err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("token file %s: %w: file is empty", path, errTokenFileMalformed)
	}
	var tok oauth2.Token
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, fmt.Errorf("token file %s: %w: %w", path, errTokenFileMalformed, err)
	}
	return &tok, nil
}

// persistingTokenSource wraps an oauth2.TokenSource so that a refresh which
// rotates the refresh token is written back to the token file rather than
// discarded. oauth2.Config.TokenSource refreshes only in memory (its own
// doc explicitly scopes itself to in-memory refresh — persistence is left
// to the caller by design, per 02-RESEARCH.md Pattern 2 and Pitfall 2); a
// rotated refresh token that is never persisted leaves the next process
// start holding a superseded credential.
type persistingTokenSource struct {
	src  oauth2.TokenSource
	path string

	mu   sync.Mutex
	last string // last-seen AccessToken, to avoid redundant writes
}

// newPersistingTokenSource builds a persistingTokenSource wrapping src,
// persisting through saveToken at path on every access-token change.
func newPersistingTokenSource(src oauth2.TokenSource, path string) oauth2.TokenSource {
	return &persistingTokenSource{src: src, path: path}
}

// Token delegates to the wrapped source. On error it returns that error
// unchanged — a later phase maps it to a named health state. On success,
// when the access token has changed since the last call, it persists the
// refreshed token before returning: a saveToken failure is returned as a
// wrapped error, not swallowed, since silently continuing with an
// unpersisted rotation is exactly the failure mode this wrapper exists to
// prevent (threat T-02-12). When the refreshed token's refresh token is
// empty, the previously persisted refresh token is carried forward rather
// than overwritten with an empty field — Google does not always reissue a
// refresh token on every refresh grant. The returned token is a copy, not
// the live pointer, matching the defensive-copy convention plugin.go's
// Describe already established for its match vocabulary.
func (p *persistingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := p.src.Token()
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if tok.AccessToken != p.last {
		toSave := *tok
		if toSave.RefreshToken == "" {
			if prev, loadErr := loadToken(p.path); loadErr == nil && prev.RefreshToken != "" {
				toSave.RefreshToken = prev.RefreshToken
			}
		}
		if err := saveToken(p.path, &toSave); err != nil {
			return nil, fmt.Errorf("persist refreshed token: %w", err)
		}
		p.last = tok.AccessToken
	}

	out := *tok
	return &out, nil
}
