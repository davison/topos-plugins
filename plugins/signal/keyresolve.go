package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// signalConfig is the shape of Signal Desktop's own config.json that this
// plugin needs to resolve the SQLCipher decryption key — Signal Desktop's
// own schema, not this project's. Exactly one of Key or
// EncryptedKey+SafeStorageBackend is present on any real install
// (04-RESEARCH.md Pattern 1); the field names below match the file
// verbatim, confirmed by direct inspection of a real, live config.json
// during this task's own schema-introspection step (field NAMES only —
// this plugin never logs a value read from this struct).
type signalConfig struct {
	Key                string `json:"key,omitempty"`
	EncryptedKey       string `json:"encryptedKey,omitempty"`
	SafeStorageBackend string `json:"safeStorageBackend,omitempty"`
}

// errSafeStorageBackendMismatch is the distinct, named error a caller
// sees when a safeStorage-resolved key fails to actually open the
// database (04-RESEARCH.md Pitfall 4: AES-CBC has no integrity check, so
// a wrong keyring secret decrypts to plausible-looking garbage rather
// than erroring here — the real proof is the read-only open plugin.go's
// openGuarded performs immediately afterward). Kept distinct from a
// generic SQLite "file is not a database" error so the message names the
// real cause instead of misleading the user into thinking their message
// store is corrupt.
var errSafeStorageBackendMismatch = errors.New("signal: safeStorage backend did not yield a usable SQLCipher key")

// expectedRawKeyHexLen is the exact length of a Signal Desktop SQLCipher
// key once resolved: Signal Desktop's own generateSQLKey()
// (randomBytes(32).toString('hex')) always produces a 64-character hex
// string, whether stored directly under the legacy "key" field or
// unwrapped from "encryptedKey". A safeStorage-resolved value of any
// other length is refused immediately, before ever attempting to open
// the database with it.
const expectedRawKeyHexLen = 64

// safeStoragePasswordFunc resolves a Secret-Service-routed keyring
// backend's master password — a package-level function variable so
// keyresolve_test.go can substitute a fake implementation and verify
// dispatch/routing without ever touching a real D-Bus session (this
// plugin's tests never require D-Bus or a keyring daemon to be present).
var safeStoragePasswordFunc = secretServicePassword

// readSignalConfig reads and parses configPath (Signal Desktop's own
// config.json). Never logs configPath's contents.
func readSignalConfig(configPath string) (signalConfig, error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return signalConfig{}, fmt.Errorf("signal: read %s: %w", configPath, err)
	}
	var cfg signalConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return signalConfig{}, fmt.Errorf("signal: parse %s: %w", configPath, err)
	}
	return cfg, nil
}

// resolveKey branches on which of cfg.Key / cfg.EncryptedKey is present
// (04-RESEARCH.md Pattern 1 — "dual-shape key resolution, branch on field
// presence, never assume"). Neither present, or both present, is the same
// fail-loud case — reported by field PRESENCE only, never by value.
func resolveKey(cfg signalConfig) (rawHexKey string, err error) {
	hasKey := cfg.Key != ""
	hasEncrypted := cfg.EncryptedKey != ""

	switch {
	case hasKey && !hasEncrypted:
		// Legacy, unmigrated install — the key IS the raw hex SQLCipher
		// key already, no unwrap needed. Confirmed the live/current shape
		// on this machine's real Signal Desktop install (04-RESEARCH.md
		// finding 2).
		return cfg.Key, nil
	case hasEncrypted && !hasKey:
		return resolveSafeStorageKey(cfg.EncryptedKey, cfg.SafeStorageBackend)
	default:
		return "", fmt.Errorf("signal: config.json has an unrecognized key shape (key present=%v, encryptedKey present=%v) — refusing to guess", hasKey, hasEncrypted)
	}
}

// resolveSafeStorageKey hex-decodes encryptedKeyHex (Signal Desktop's own
// config.json encoding: getSQLKey in Signal Desktop's app/main.main.ts
// does `Buffer.from(modernKeyValue, 'hex')`, confirmed against
// signalapp/Signal-Desktop's live source during this task — not the
// base64 encoding 04-RESEARCH.md's illustrative snippet assumed),
// resolves the master password for the declared backend, unwraps the
// blob, and returns the plaintext directly as the SQLCipher hex key
// string: Signal Desktop's own getSQLKey never re-encodes it after
// decrypting, so the unwrapped plaintext IS the same 64-character hex
// string the legacy "key" field carries directly.
//
// Dispatches strictly on the literal backend string — never on
// $XDG_CURRENT_DESKTOP or any desktop-environment detection this plugin
// might perform itself: 04-RESEARCH.md confirmed this machine's own
// desktop environment (river, a tiling WM outside Electron's safeStorage
// allowlist) still has a working org.freedesktop.secrets via a
// manually-started gnome-keyring-daemon, so DE detection is provably
// wrong here — the ONLY correct signal is the value Electron itself
// already wrote to config.json.
func resolveSafeStorageKey(encryptedKeyHex, backend string) (string, error) {
	blob, err := hex.DecodeString(encryptedKeyHex)
	if err != nil {
		return "", fmt.Errorf("signal: config.json encryptedKey is not valid hex: %w", err)
	}

	var password string
	switch backend {
	case "basic_text":
		// Chromium's own hardcoded v10 fallback password — never touches
		// D-Bus (04-RESEARCH.md Pitfall 3), rounds through the identical
		// AES-128-CBC/PBKDF2 path as every other backend.
		password = basicTextPassword
	case "gnome_libsecret", "kwallet", "kwallet5", "kwallet6":
		password, err = safeStoragePasswordFunc(backend)
		if err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("signal: config.json declares an unrecognised safeStorageBackend %q — refusing to guess a keyring backend", backend)
	}

	plaintext, err := decryptSafeStorageBlob(password, blob)
	if err != nil {
		return "", fmt.Errorf("%w (declared backend=%s): %v", errSafeStorageBackendMismatch, backend, err)
	}
	if len(plaintext) != expectedRawKeyHexLen {
		return "", fmt.Errorf(
			"%w (declared backend=%s): decrypted to %d characters, expected %d",
			errSafeStorageBackendMismatch, backend, len(plaintext), expectedRawKeyHexLen,
		)
	}

	return string(plaintext), nil
}
