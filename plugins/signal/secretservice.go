// secretservice.go wraps github.com/keybase/go-keychain/secretservice to
// fetch Signal Desktop's safeStorage master password from the freedesktop
// Secret Service (org.freedesktop.secrets) — the D-Bus round-trip
// keyresolve.go's resolveSafeStorageKey dispatches to for every
// Secret-Service-routed safeStorageBackend value (gnome_libsecret,
// kwallet, kwallet5, kwallet6). Uses the encrypted DH-AES session mode
// (AuthenticationDHAES) — never the plain session mode, which would carry
// the secret over the session bus in the clear. The Diffie-Hellman
// handshake itself is never hand-rolled (04-RESEARCH.md "Don't
// Hand-Roll") — go-keychain implements it.
//
// Every error this file returns names the failing D-Bus step and the
// backend, never a secret (plugin contract: never log a credential).
package main

import (
	"fmt"

	dbus "github.com/keybase/dbus"
	"github.com/keybase/go-keychain/secretservice"
)

// secretServiceApplicationAttribute is the Secret Service item attribute
// key Electron's freedesktop-Secret-Service os_crypt key provider
// searches/stores under — the literal string "application", traced
// directly from Chromium's own source during this task
// (kApplicationAttributeKey in both the "sync" and "async" os_crypt
// backends).
const secretServiceApplicationAttribute = "application"

// signalDesktopApplicationName is the "application" attribute VALUE
// Electron stores Signal Desktop's safeStorage entry under. Traced from
// Chromium's own source: the attribute value is
// os_crypt::Config.application_name when the embedder (Electron) sets
// it — Electron sets this from app.getName(), which defaults to
// package.json's "productName" field when present. Signal Desktop's own
// package.json declares "productName": "Signal" (confirmed against
// signalapp/Signal-Desktop's live source during this task) and never
// calls app.setName() to override it, so "Signal" is this plugin's
// best-available, documentation-traced value.
//
// This machine's real Signal Desktop has never migrated to safeStorage
// (04-01-SUMMARY.md: legacy plaintext key shape) — this constant cannot
// be verified against a live install here, and is flagged for
// re-verification the first time this branch runs against a real
// safeStorage-migrated Signal Desktop install.
const signalDesktopApplicationName = "Signal"

// errSecretServiceUnavailable is returned when the session D-Bus itself
// cannot be reached — a real and common state on a desktop environment
// with no keyring daemon running at all.
var errSecretServiceUnavailable = fmt.Errorf("signal: org.freedesktop.secrets is not available on the session D-Bus")

// secretServicePassword fetches Signal Desktop's safeStorage master
// password from the freedesktop Secret Service over an encrypted
// (AuthenticationDHAES) session: open a session, unlock the default
// collection, search it by the application attribute Electron stores the
// entry under, and return the secret. backend is the literal
// safeStorageBackend value from config.json, used only to name which
// backend was declared in this file's error messages — the search itself
// is identical regardless of which Secret-Service-routed backend value is
// passed (keyresolve.go's dispatch, not this file, is what varies by
// backend).
func secretServicePassword(backend string) (string, error) {
	service, err := secretservice.NewService()
	if err != nil {
		return "", fmt.Errorf("%w (backend=%s): %v", errSecretServiceUnavailable, backend, err)
	}

	session, err := service.OpenSession(secretservice.AuthenticationDHAES)
	if err != nil {
		return "", fmt.Errorf("signal: open Secret Service session (backend=%s): %w", backend, err)
	}
	defer service.CloseSession(session)

	if err := service.Unlock([]dbus.ObjectPath{secretservice.DefaultCollection}); err != nil {
		return "", fmt.Errorf("signal: unlock the default Secret Service collection (backend=%s): %w", backend, err)
	}

	items, err := service.SearchCollection(secretservice.DefaultCollection, secretservice.Attributes{
		secretServiceApplicationAttribute: signalDesktopApplicationName,
	})
	if err != nil {
		return "", fmt.Errorf("signal: search Secret Service default collection (backend=%s): %w", backend, err)
	}
	if len(items) == 0 {
		return "", fmt.Errorf("signal: no Secret Service item found for application=%s (backend=%s)", signalDesktopApplicationName, backend)
	}

	secretBytes, err := service.GetSecret(items[0], *session)
	if err != nil {
		return "", fmt.Errorf("signal: retrieve Secret Service secret (backend=%s): %w", backend, err)
	}
	if len(secretBytes) == 0 {
		return "", fmt.Errorf("signal: Secret Service returned an empty secret (backend=%s)", backend)
	}

	return string(secretBytes), nil
}
