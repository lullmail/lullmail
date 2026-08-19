package main

// AES-256-GCM for provider credentials held in the database (app passwords
// until OAuth lands in Phase 1b). Key is derived from SECRET_KEY; nonce is
// random per secret and stored alongside, since nonce reuse under one GCM key
// forfeits authenticity entirely. Same scheme as akiroo's secretbox.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
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
