// safestorage_linux.go implements the Electron/Chromium os_crypt
// AES-128-CBC unwrap for a config.json encryptedKey blob. The algorithm
// and every constant below are copied verbatim from Chromium's own
// source, read directly from chromium.googlesource.com during this task:
// components/os_crypt/sync/os_crypt_linux.cc (the pre-OSCryptAsync "sync"
// backend, still what ships in the Electron/Chromium versions Signal
// Desktop currently bundles) and
// components/os_crypt/async/browser/freedesktop_secret_key_provider.cc
// (the newer backend, upstream's own migration target) — both declare the
// identical salt/iteration-count/key-length/IV constants, confirmed
// byte-for-byte across the two files. These constants are load-bearing:
// AES-CBC has no integrity check, so a wrong constant here produces
// silent garbage, not an error (04-RESEARCH.md Pitfall 4) — do not
// "simplify" any of them.
package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1" //nolint:gosec // required: this is Chromium's own documented os_crypt KDF, not a choice made here
	"fmt"

	"golang.org/x/crypto/pbkdf2"
)

const (
	// osCryptSalt is the literal PBKDF2 salt Chromium's os_crypt uses on
	// Linux — the string "saltysalt", copied verbatim.
	osCryptSalt = "saltysalt"

	// osCryptIterations is Chromium's own PBKDF2 iteration count for this
	// scheme: deliberately just 1 (the OS keyring, not PBKDF2 iteration
	// count, is what provides the real secrecy margin here).
	osCryptIterations = 1

	// osCryptKeyLenBytes is the derived AES key length: 128 bits.
	osCryptKeyLenBytes = 16

	// osCryptIVSize is the AES-128-CBC block size, and also the length of
	// the fixed IV below.
	osCryptIVSize = 16

	// osCryptIVByte is the single byte value the entire 16-byte IV is
	// filled with — the literal ASCII space character (0x20), never a
	// random value. Chromium hardcodes this; it is not a nonce in the
	// usual sense.
	osCryptIVByte = ' '

	// safeStorageV10Prefix marks a blob encrypted with the fixed
	// basicTextPassword fallback (no OS keyring backend available/used).
	safeStorageV10Prefix = "v10"

	// safeStorageV11Prefix marks a blob encrypted with a password
	// retrieved from an OS keyring backend (Secret Service/KWallet).
	safeStorageV11Prefix = "v11"

	// basicTextPassword is Chromium's own hardcoded fallback password —
	// literally "peanuts" in Chromium's source
	// (os_crypt_linux.cc GetPasswordV10) — used for the v10 case. Rounds
	// through the identical PBKDF2/AES-CBC path as every other backend
	// (04-RESEARCH.md Pitfall 3): only the password source differs.
	basicTextPassword = "peanuts"
)

// decryptSafeStorageBlob strips blob's v10/v11 prefix, derives the
// AES-128 key from masterPassword via PBKDF2-HMAC-SHA1
// (osCryptSalt/osCryptIterations/osCryptKeyLenBytes), decrypts with the
// fixed 16-space IV, and strips PKCS7 padding. Rejects a blob carrying
// neither prefix, a ciphertext whose length is not a multiple of the AES
// block size, and a malformed padding result — the wrong password
// decrypts to plausible-looking garbage rather than a clean failure
// unless padding is checked explicitly (04-RESEARCH.md Pitfall 4).
func decryptSafeStorageBlob(masterPassword string, blob []byte) ([]byte, error) {
	if len(blob) < 3 || (string(blob[:3]) != safeStorageV10Prefix && string(blob[:3]) != safeStorageV11Prefix) {
		return nil, fmt.Errorf("signal: safeStorage blob missing v10/v11 prefix")
	}
	ciphertext := blob[3:]
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("signal: safeStorage ciphertext length %d is not a multiple of the AES block size", len(ciphertext))
	}

	key := deriveOSCryptKey(masterPassword)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("signal: build AES cipher: %w", err)
	}

	iv := bytes.Repeat([]byte{osCryptIVByte}, osCryptIVSize)
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plaintext, ciphertext)

	return pkcs7Unpad(plaintext)
}

// deriveOSCryptKey runs PBKDF2-HMAC-SHA1 over masterPassword using
// Chromium's own os_crypt salt/iteration-count/key-length constants.
func deriveOSCryptKey(masterPassword string) []byte {
	return pbkdf2.Key([]byte(masterPassword), []byte(osCryptSalt), osCryptIterations, osCryptKeyLenBytes, sha1.New)
}

// pkcs7Unpad strips PKCS7 padding from data, rejecting a malformed pad
// loudly rather than returning corrupted plaintext (04-RESEARCH.md
// Pitfall 4 — this is the check that turns "wrong password" into a clean
// error instead of silent garbage).
func pkcs7Unpad(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("signal: safeStorage plaintext is empty, cannot strip PKCS7 padding")
	}
	padLen := int(data[len(data)-1])
	if padLen == 0 || padLen > aes.BlockSize || padLen > len(data) {
		return nil, fmt.Errorf("signal: safeStorage plaintext has invalid PKCS7 padding length %d — likely decrypted with the wrong password", padLen)
	}
	for _, b := range data[len(data)-padLen:] {
		if int(b) != padLen {
			return nil, fmt.Errorf("signal: safeStorage plaintext has malformed PKCS7 padding — likely decrypted with the wrong password")
		}
	}
	return data[:len(data)-padLen], nil
}
