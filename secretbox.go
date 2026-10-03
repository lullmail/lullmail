package main

// AES-256-GCM for provider credentials held in the database (app passwords
// until OAuth lands in Phase 1b). Key is derived from SECRET_KEY; nonce is
// random per secret and stored alongside, since nonce reuse under one GCM key
// forfeits authenticity entirely. Same scheme as akiroo's secretbox.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

var errNoSecretKey = errors.New("SECRET_KEY is not set: cannot store mail credentials")

func sealSecret(cfg *Config, plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	if cfg.SecretKey == "" {
		return "", errNoSecretKey
	}
	sum := sha256.Sum256([]byte(cfg.SecretKey))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plaintext), nil)), nil
}

func openSecret(cfg *Config, encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	if cfg.SecretKey == "" {
		return "", errNoSecretKey
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(cfg.SecretKey))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("secretbox: ciphertext shorter than a nonce")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// Bound sealing (durable outbox). Outbox compositions are the user's own
// unsent mail, so unlike a credential they are bound to the row that owns
// them and carry the id of the key that sealed them:
//
//	"v2:" + base64( 0x02 | keyID(4) | nonce(12) | AES-256-GCM(plaintext) )
//
// The GCM additional data is the purpose, owner and row id, so a ciphertext
// copied or restored into another row, another owner or the other column
// (composition vs. Sent copy) fails authentication instead of decrypting.
// keyID is a fingerprint of SECRET_KEY, never the key: when the key is lost
// or changed the failure is identified as exactly that, and recoverable by
// restoring the key, rather than looking like corruption.
//
// Strings without the "v2:" prefix are the unversioned, unbound format of
// sealSecret and are still read, without a binding check, so rows written
// before this format existed stay recoverable.
const sealedV2Prefix = "v2:"
const sealedV2Format byte = 2
const sealedKeyIDLen = 4

var (
	// errSealedKeyUnavailable: the sealing key is missing, or is not the key
	// the data was sealed under. Restoring the original SECRET_KEY recovers it.
	errSealedKeyUnavailable = errors.New("sealed data cannot be opened with the current SECRET_KEY; restore the key it was sealed with")
	// errSealedInvalid: the key is right but the data is damaged, truncated,
	// or bound to a different owner, row or purpose.
	errSealedInvalid = errors.New("sealed data failed authentication for this owner and row")
)

func sealKeyID(cfg *Config) [sealedKeyIDLen]byte {
	sum := sha256.Sum256([]byte("lullmail/key-id\x00" + cfg.SecretKey))
	var id [sealedKeyIDLen]byte
	copy(id[:], sum[:sealedKeyIDLen])
	return id
}

func sealAAD(purpose, owner, row string) []byte {
	return []byte("lullmail/sealed/v2\x00" + purpose + "\x00" + owner + "\x00" + row)
}

func sealGCM(cfg *Config) (cipher.AEAD, error) {
	sum := sha256.Sum256([]byte(cfg.SecretKey))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// sealBound encrypts plaintext for one (purpose, owner, row).
func sealBound(cfg *Config, purpose, owner, row, plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	if cfg == nil || cfg.SecretKey == "" {
		return "", errNoSecretKey
	}
	gcm, err := sealGCM(cfg)
	if err != nil {
		return "", err
	}
	id := sealKeyID(cfg)
	head := append([]byte{sealedV2Format}, id[:]...)
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	out := append(head, nonce...)
	out = gcm.Seal(out, nonce, []byte(plaintext), sealAAD(purpose, owner, row))
	return sealedV2Prefix + base64.StdEncoding.EncodeToString(out), nil
}

// openBound decrypts sealBound output. Errors
// satisfy errors.Is against errNoSecretKey, errSealedKeyUnavailable or
// errSealedInvalid, so callers can tell "restore the key" from "damaged".
func openBound(cfg *Config, purpose, owner, row, encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	if cfg == nil || cfg.SecretKey == "" {
		return "", errNoSecretKey
	}
	if !strings.HasPrefix(encoded, sealedV2Prefix) {
		// Only sealBound output is accepted. The unversioned format carries no
		// binding, so a credential ciphertext copied into a row would open.
		return "", fmt.Errorf("%w: not a bound ciphertext", errSealedInvalid)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, sealedV2Prefix))
	if err != nil {
		return "", fmt.Errorf("%w: %v", errSealedInvalid, err)
	}
	gcm, err := sealGCM(cfg)
	if err != nil {
		return "", err
	}
	if len(raw) < 1+sealedKeyIDLen+gcm.NonceSize()+gcm.Overhead() || raw[0] != sealedV2Format {
		return "", fmt.Errorf("%w: unrecognized sealed format", errSealedInvalid)
	}
	want := sealKeyID(cfg)
	if !hmac.Equal(raw[1:1+sealedKeyIDLen], want[:]) {
		return "", errSealedKeyUnavailable
	}
	body := raw[1+sealedKeyIDLen:]
	plain, err := gcm.Open(nil, body[:gcm.NonceSize()], body[gcm.NonceSize():], sealAAD(purpose, owner, row))
	if err != nil {
		return "", errSealedInvalid
	}
	return string(plain), nil
}
