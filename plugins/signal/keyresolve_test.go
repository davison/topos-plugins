package main

import (
	"encoding/hex"
	"strings"
	"testing"
)

// testRawKeyHex is a fixed, 64-character (32-byte) hex string shaped
// exactly like a real Signal Desktop SQLCipher key — never a real key —
// used as the plaintext safeStorage unwrap fixtures below decrypt to.
// Built via strings.Repeat rather than a hand-counted literal so its
// length is exactly expectedRawKeyHexLen by construction.
var testRawKeyHex = strings.Repeat("ab", expectedRawKeyHexLen/2)

func TestResolveKey_LegacyKeyOnly(t *testing.T) {
	got, err := resolveKey(signalConfig{Key: "deadbeef"})
	if err != nil {
		t.Fatalf("resolveKey: %v", err)
	}
	if got != "deadbeef" {
		t.Errorf("got %q, want %q", got, "deadbeef")
	}
}

func TestResolveKey_NeitherFieldPresent(t *testing.T) {
	_, err := resolveKey(signalConfig{})
	if err == nil {
		t.Fatal("expected an error when neither key nor encryptedKey is present")
	}
	if !strings.Contains(err.Error(), "key present=false") || !strings.Contains(err.Error(), "encryptedKey present=false") {
		t.Errorf("expected the error to name both fields by presence, got: %v", err)
	}
}

func TestResolveKey_BothFieldsPresent(t *testing.T) {
	_, err := resolveKey(signalConfig{Key: "deadbeef", EncryptedKey: "aabbcc"})
	if err == nil {
		t.Fatal("expected an error when both key and encryptedKey are present")
	}
	if !strings.Contains(err.Error(), "key present=true") || !strings.Contains(err.Error(), "encryptedKey present=true") {
		t.Errorf("expected the error to name both fields by presence, got: %v", err)
	}
}

func TestResolveKey_BasicTextNeverTouchesDBus(t *testing.T) {
	original := safeStoragePasswordFunc
	defer func() { safeStoragePasswordFunc = original }()
	safeStoragePasswordFunc = func(backend string) (string, error) {
		t.Fatalf("basic_text must never call the Secret Service fetcher (backend=%s)", backend)
		return "", nil
	}

	blob := encryptSafeStorageBlobForTest(t, basicTextPassword, safeStorageV10Prefix, []byte(testRawKeyHex))
	cfg := signalConfig{EncryptedKey: hex.EncodeToString(blob), SafeStorageBackend: "basic_text"}

	got, err := resolveKey(cfg)
	if err != nil {
		t.Fatalf("resolveKey: %v", err)
	}
	if got != testRawKeyHex {
		t.Errorf("got %q, want %q", got, testRawKeyHex)
	}
}

func TestResolveKey_RoutesToSecretServiceForKeyringBackends(t *testing.T) {
	original := safeStoragePasswordFunc
	defer func() { safeStoragePasswordFunc = original }()

	for _, backend := range []string{"gnome_libsecret", "kwallet", "kwallet5", "kwallet6"} {
		t.Run(backend, func(t *testing.T) {
			const fakePassword = "fake-keyring-secret"
			var calledWithBackend string
			safeStoragePasswordFunc = func(b string) (string, error) {
				calledWithBackend = b
				return fakePassword, nil
			}

			blob := encryptSafeStorageBlobForTest(t, fakePassword, safeStorageV11Prefix, []byte(testRawKeyHex))
			cfg := signalConfig{EncryptedKey: hex.EncodeToString(blob), SafeStorageBackend: backend}

			got, err := resolveKey(cfg)
			if err != nil {
				t.Fatalf("resolveKey: %v", err)
			}
			if got != testRawKeyHex {
				t.Errorf("got %q, want %q", got, testRawKeyHex)
			}
			if calledWithBackend != backend {
				t.Errorf("expected the Secret Service fetcher to be called with backend=%s, got %q", backend, calledWithBackend)
			}
		})
	}
}

func TestResolveKey_UnrecognisedBackend(t *testing.T) {
	cases := []string{"unknown", "", "totally-made-up-backend"}
	for _, backend := range cases {
		t.Run(backend, func(t *testing.T) {
			cfg := signalConfig{EncryptedKey: "aabbcc", SafeStorageBackend: backend}
			_, err := resolveKey(cfg)
			if err == nil {
				t.Fatalf("expected an error for safeStorageBackend %q", backend)
			}
			if backend != "" && !strings.Contains(err.Error(), backend) {
				t.Errorf("expected the error to name the unrecognised value %q, got: %v", backend, err)
			}
		})
	}
}

func TestResolveKey_ErrorsNeverContainSecretMaterial(t *testing.T) {
	const secretMarker = "super-secret-marker-value"

	original := safeStoragePasswordFunc
	defer func() { safeStoragePasswordFunc = original }()
	safeStoragePasswordFunc = func(backend string) (string, error) { return secretMarker, nil }

	// Encrypted with a DIFFERENT password than the fetcher above returns,
	// so decryption fails — any accidental leak of the resolved secret
	// would surface it in the returned error.
	blob := encryptSafeStorageBlobForTest(t, "the-real-password", safeStorageV11Prefix, []byte(testRawKeyHex))
	cfg := signalConfig{EncryptedKey: hex.EncodeToString(blob), SafeStorageBackend: "gnome_libsecret"}

	_, err := resolveKey(cfg)
	if err == nil {
		t.Fatal("expected an error when the fetched password does not decrypt the blob")
	}
	if strings.Contains(err.Error(), secretMarker) {
		t.Errorf("error message leaked the resolved secret: %v", err)
	}
	if strings.Contains(err.Error(), testRawKeyHex) {
		t.Errorf("error message leaked the resolved key: %v", err)
	}
}
