package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestSealBoundRoundTripAndBinding(t *testing.T) {
	cfg := &Config{SecretKey: "binding-key"}
	sealed, err := sealBound(cfg, "payload", "owner-1", "row-1", "private composition")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, "v2:") || strings.Contains(sealed, "private") {
		t.Fatalf("unversioned or plaintext output: %q", sealed)
	}
	if got, err := openBound(cfg, "payload", "owner-1", "row-1", sealed); err != nil || got != "private composition" {
		t.Fatalf("round trip: %q %v", got, err)
	}
	for name, args := range map[string][3]string{
		"another owner":   {"payload", "owner-2", "row-1"},
		"another row":     {"payload", "owner-1", "row-2"},
		"another purpose": {"sent", "owner-1", "row-1"},
	} {
		if got, err := openBound(cfg, args[0], args[1], args[2], sealed); !errors.Is(err, errSealedInvalid) || got != "" {
			t.Errorf("%s: opened=%q err=%v; ciphertext is not bound", name, got, err)
		}
	}
}

func TestSealBoundKeyFailuresAreDistinctAndRecoverable(t *testing.T) {
	sealed, _ := sealBound(&Config{SecretKey: "original"}, "payload", "o", "r", "private")
	if _, err := openBound(&Config{SecretKey: "replacement"}, "payload", "o", "r", sealed); !errors.Is(err, errSealedKeyUnavailable) || errors.Is(err, errSealedInvalid) {
		t.Fatalf("changed key must be reported as a key problem: %v", err)
	}
	if _, err := openBound(&Config{}, "payload", "o", "r", sealed); !errors.Is(err, errNoSecretKey) {
		t.Fatalf("missing key: %v", err)
	}
	if _, err := openBound(nil, "payload", "o", "r", sealed); !errors.Is(err, errNoSecretKey) {
		t.Fatalf("nil config must not panic: %v", err)
	}
	if got, err := openBound(&Config{SecretKey: "original"}, "payload", "o", "r", sealed); err != nil || got != "private" {
		t.Fatalf("restoring the key must recover the data: %q %v", got, err)
	}
	if outboxSealCode(errSealedKeyUnavailable) != "payload_key_unavailable" || outboxSealCode(errNoSecretKey) != "payload_key_unavailable" || outboxSealCode(errSealedInvalid) != "payload_corrupt" {
		t.Fatal("failure codes do not separate the remedies")
	}
}

func TestOpenBoundRefusesTheUnboundFormat(t *testing.T) {
	cfg := &Config{SecretKey: "legacy-key"}
	unbound, err := sealSecret(cfg, "a stored credential")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := openBound(cfg, "sent", "any-owner", "any-row", unbound); !errors.Is(err, errSealedInvalid) || got != "" {
		t.Fatalf("unbound ciphertext opened: %q %v", got, err)
	}
}

func TestOpenBoundRejectsDamageWithoutPanicking(t *testing.T) {
	cfg := &Config{SecretKey: "damage-key"}
	sealed, _ := sealBound(cfg, "payload", "o", "r", "private")
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, "v2:"))
	flip := append([]byte{}, raw...)
	flip[len(flip)-1] ^= 1
	cases := []string{
		"v2:", "v2:!!!notbase64", "v2:" + base64.StdEncoding.EncodeToString(raw[:5]),
		"v2:" + base64.StdEncoding.EncodeToString(raw[:len(raw)-1]),
		"v2:" + base64.StdEncoding.EncodeToString(flip),
		"v2:" + base64.StdEncoding.EncodeToString(append([]byte{9}, raw[1:]...)),
	}
	for i := 0; i < 64; i++ {
		junk := make([]byte, i*3)
		rand.Read(junk)
		cases = append(cases, "v2:"+base64.StdEncoding.EncodeToString(junk), base64.StdEncoding.EncodeToString(junk))
	}
	for _, c := range cases {
		if c == "" {
			continue // empty means "no data", by design
		}
		if got, err := openBound(cfg, "payload", "o", "r", c); err == nil {
			t.Errorf("damaged input %.20q opened as %q", c, got)
		}
	}
}
