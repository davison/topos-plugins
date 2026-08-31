package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"strings"
	"testing"
)

// encryptSafeStorageBlobForTest is this test file's own encrypt-side
// mirror of decryptSafeStorageBlob — production code never needs to
// encrypt (it only ever reads Signal Desktop's own already-encrypted
// config.json fields), so this exists solely to build known-good v10/v11
// fixture blobs below, using the identical constants and code paths under
// test.
func encryptSafeStorageBlobForTest(t *testing.T, password, prefix string, plaintext []byte) []byte {
	t.Helper()

	key := deriveOSCryptKey(password)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("build AES cipher: %v", err)
	}
	iv := bytes.Repeat([]byte{osCryptIVByte}, osCryptIVSize)
	padded := pkcs7PadForTest(plaintext, aes.BlockSize)
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, padded)

	return append([]byte(prefix), ciphertext...)
}

func pkcs7PadForTest(data []byte, blockSize int) []byte {
	padLen := blockSize - len(data)%blockSize
	padding := bytes.Repeat([]byte{byte(padLen)}, padLen)
	return append(append([]byte{}, data...), padding...)
}

func TestSafeStorage_V10RoundTrip(t *testing.T) {
	blob := encryptSafeStorageBlobForTest(t, basicTextPassword, safeStorageV10Prefix, []byte("hello-v10-plaintext"))
	got, err := decryptSafeStorageBlob(basicTextPassword, blob)
	if err != nil {
		t.Fatalf("decryptSafeStorageBlob: %v", err)
	}
	if string(got) != "hello-v10-plaintext" {
		t.Errorf("got %q, want %q", got, "hello-v10-plaintext")
	}
}

func TestSafeStorage_V11RoundTrip(t *testing.T) {
	blob := encryptSafeStorageBlobForTest(t, "keyring-secret-password", safeStorageV11Prefix, []byte("hello-v11-plaintext"))
	got, err := decryptSafeStorageBlob("keyring-secret-password", blob)
	if err != nil {
		t.Fatalf("decryptSafeStorageBlob: %v", err)
	}
	if string(got) != "hello-v11-plaintext" {
		t.Errorf("got %q, want %q", got, "hello-v11-plaintext")
	}
}

func TestSafeStorage_MissingPrefixRejected(t *testing.T) {
	if _, err := decryptSafeStorageBlob("peanuts", []byte("xx-not-a-valid-prefix-16bytes!!")); err == nil {
		t.Fatal("expected an error for a blob missing the v10/v11 prefix")
	}
}

func TestSafeStorage_NonBlockMultipleCiphertextRejected(t *testing.T) {
	blob := append([]byte(safeStorageV10Prefix), []byte("short")...) // 5 bytes, not a multiple of 16
	if _, err := decryptSafeStorageBlob("peanuts", blob); err == nil {
		t.Fatal("expected an error for a ciphertext whose length is not a multiple of the AES block size")
	}
}

func TestSafeStorage_WrongPasswordRejectedByPaddingCheck(t *testing.T) {
	blob := encryptSafeStorageBlobForTest(t, "correct-password", safeStorageV11Prefix, []byte("some plaintext data, deliberately not block-aligned"))
	if _, err := decryptSafeStorageBlob("wrong-password", blob); err == nil {
		t.Fatal("expected the wrong password to be rejected by the PKCS7 padding check (Pitfall 4), got nil error")
	}
}

func TestSafeStorage_ConstantsAreChromiumsOwn(t *testing.T) {
	if osCryptSalt != "saltysalt" {
		t.Errorf("osCryptSalt = %q, want the literal Chromium salt %q", osCryptSalt, "saltysalt")
	}
	if osCryptIterations != 1 {
		t.Errorf("osCryptIterations = %d, want 1", osCryptIterations)
	}
	if osCryptKeyLenBytes != 16 {
		t.Errorf("osCryptKeyLenBytes = %d, want 16 (128-bit AES key)", osCryptKeyLenBytes)
	}
	if osCryptIVSize != 16 {
		t.Errorf("osCryptIVSize = %d, want 16", osCryptIVSize)
	}
	if osCryptIVByte != ' ' {
		t.Errorf("osCryptIVByte = %q, want a literal space character", string(osCryptIVByte))
	}
	if basicTextPassword != "peanuts" {
		t.Errorf("basicTextPassword = %q, want Chromium's literal fallback password %q", basicTextPassword, "peanuts")
	}
}

func TestSafeStorage_ErrorsNeverContainSecretMaterial(t *testing.T) {
	const secretPassword = "super-secret-test-marker-password"
	blob := encryptSafeStorageBlobForTest(t, secretPassword, safeStorageV11Prefix, []byte("plaintext"))

	_, err := decryptSafeStorageBlob("a-completely-different-password", blob)
	if err == nil {
		t.Fatal("expected an error for the wrong password")
	}
	if strings.Contains(err.Error(), secretPassword) {
		t.Errorf("error message leaked the correct password: %v", err)
	}
}
