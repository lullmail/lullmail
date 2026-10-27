package app

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// An install served from an IP address (a LAN or Tailscale address) cannot
// use passkeys — WebAuthn forbids IP relying-party IDs — but must still
// start, finish first-run setup and sign in with a password.
func TestIPOriginRunsWithPasskeysOff(t *testing.T) {
	for _, origin := range []string{"http://127.0.0.1:8080", "http://100.64.0.7:8080", "http://[::1]:8080"} {
		t.Run(origin, func(t *testing.T) {
			cfg := &Config{SecretKey: "test", APIToken: "test"}
			if !applyOrigin(cfg, origin) {
				t.Fatalf("origin %s not applied", origin)
			}
			if _, err := newWebAuthn(cfg); !errors.Is(err, errPasskeysNeedDomain) {
				t.Fatalf("newWebAuthn = %v; want errPasskeysNeedDomain", err)
			}

			a := &App{cfg: &Config{SecretKey: "test", APIToken: "test"}}
			if !a.setOriginForSetup(origin) {
				t.Fatal("first-run setup refused an IP origin")
			}
			if a.config().PublicURL != origin || a.webAuthn() != nil {
				t.Fatalf("origin %q pinned with webauthn %v", a.config().PublicURL, a.webAuthn())
			}
		})
	}
	a := &App{cfg: &Config{SecretKey: "test", APIToken: "test"}}
	if !a.setOriginForSetup("https://mail.example.test") || a.webAuthn() == nil {
		t.Fatal("a domain origin must still enable passkeys")
	}
}

func TestPasskeyCeremonyWithoutWebAuthnIsAConflict(t *testing.T) {
	w := httptest.NewRecorder()
	writePasskeysUnavailable(w)
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d; want 409", w.Code)
	}
}
