package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveSecretKeyGeneratesAndPersists(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{DataDir: dir}
	if err := resolveSecretKey(cfg); err != nil {
		t.Fatal(err)
	}
	first := cfg.SecretKey
	if len(first) != 64 {
		t.Fatalf("key length = %d, want 64 hex chars", len(first))
	}
	info, err := os.Stat(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("keyfile mode = %o, want 0600", perm)
	}
	cfg2 := &Config{DataDir: dir}
	if err := resolveSecretKey(cfg2); err != nil || cfg2.SecretKey != first {
		t.Fatalf("second load did not reuse the keyfile: %v %q", err, cfg2.SecretKey)
	}
	cfg3 := &Config{DataDir: dir, SecretKey: strings.Repeat("ab", 32)}
	if err := resolveSecretKey(cfg3); err != nil || cfg3.SecretKey == first {
		t.Fatal("env SECRET_KEY must win over the keyfile")
	}
}

func TestSetupTokenLifecycle(t *testing.T) {
	original := setupNow
	defer func() { setupNow = original }()
	base := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()

	setupNow = func() time.Time { return base }
	first, err := loadOrCreateSetupToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first.Token == "" || first.Created.IsZero() {
		t.Fatalf("bad token file: %+v", first)
	}

	// A restart within the window reuses the same token.
	setupNow = func() time.Time { return base.Add(23 * time.Hour) }
	again, err := loadOrCreateSetupToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.Token != first.Token {
		t.Fatal("fresh token was not reused after restart")
	}

	// Past 24h a new one is minted and the old token stops authorizing.
	setupNow = func() time.Time { return base.Add(25 * time.Hour) }
	regenerated, err := loadOrCreateSetupToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	if regenerated.Token == first.Token {
		t.Fatal("expired token was reused")
	}
	app := &App{cfg: &Config{APIToken: first.Token}, setupTokenCreated: first.Created}
	if app.setupTokenValid() {
		t.Fatal("expired setup token still valid")
	}
	app2 := &App{cfg: &Config{APIToken: regenerated.Token}, setupTokenCreated: regenerated.Created}
	if !app2.setupTokenValid() {
		t.Fatal("fresh setup token rejected")
	}

	// Completion retires the file entirely.
	deleteSetupToken(dir)
	if raw, err := os.ReadFile(filepath.Join(dir, "setup-token.json")); err == nil {
		t.Fatalf("token file survived deletion: %s", raw)
	}
	// A corrupt file is replaced, not fatal.
	if err := os.WriteFile(filepath.Join(dir, "setup-token.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh, err := loadOrCreateSetupToken(dir)
	if err != nil || fresh.Token == "" {
		t.Fatalf("corrupt token file not replaced: %v", err)
	}
	var written setupTokenFile
	raw, _ := os.ReadFile(filepath.Join(dir, "setup-token.json"))
	if json.Unmarshal(raw, &written) != nil || written.Token != fresh.Token {
		t.Fatal("replacement token was not persisted")
	}
}

func TestSetupTokenEnvHasNoExpiry(t *testing.T) {
	app := &App{cfg: &Config{APIToken: "from-env"}, setupTokenCreated: time.Time{}}
	if !app.setupTokenValid() {
		t.Fatal("env token must not be subject to the 24h window")
	}
	none := &App{cfg: &Config{}}
	if none.setupTokenValid() {
		t.Fatal("missing token must not authorize")
	}
}

func TestDetectOrigin(t *testing.T) {
	cases := []struct {
		name  string
		build func() *http.Request
		want  string
	}{
		{"plain", func() *http.Request {
			r := httptest.NewRequest("GET", "/api/auth/status", nil)
			r.Host = "mail.example.com"
			return r
		}, "http://mail.example.com"},
		{"host port", func() *http.Request {
			r := httptest.NewRequest("GET", "/", nil)
			r.Host = "192.168.1.4:8080"
			return r
		}, "http://192.168.1.4:8080"},
		{"forwarded proto", func() *http.Request {
			r := httptest.NewRequest("GET", "/", nil)
			r.Host = "mail.example.com"
			r.Header.Set("X-Forwarded-Proto", "https")
			return r
		}, "https://mail.example.com"},
		{"forwarded chain", func() *http.Request {
			r := httptest.NewRequest("GET", "/", nil)
			r.Host = "internal:8080"
			r.Header.Set("X-Forwarded-Proto", "https, http")
			r.Header.Set("X-Forwarded-Host", "mail.example.com, proxy.internal")
			return r
		}, "https://mail.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectOrigin(tc.build()); got != tc.want {
				t.Fatalf("detectOrigin = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestApplyOrigin(t *testing.T) {
	cfg := &Config{}
	if !applyOrigin(cfg, "https://mail.example.com") {
		t.Fatal("valid origin rejected")
	}
	if cfg.PublicURL != "https://mail.example.com" || cfg.RPID != "mail.example.com" || !cfg.SecureAuth {
		t.Fatalf("bad config from origin: %+v", cfg)
	}
	if !applyOrigin(cfg, "http://192.168.1.4:8080/") {
		t.Fatal("origin with port and slash rejected")
	}
	if cfg.RPID != "192.168.1.4" || cfg.SecureAuth {
		t.Fatalf("port not stripped or secure wrong: %+v", cfg)
	}
	if applyOrigin(cfg, "not a url") || applyOrigin(cfg, "") {
		t.Fatal("garbage origin accepted")
	}
}

func TestConstantTimeBearer(t *testing.T) {
	if !constantTimeBearer("Bearer abc", "abc") {
		t.Fatal("matching bearer rejected")
	}
	if constantTimeBearer("Bearer abc", "abd") || constantTimeBearer("abc", "abc") {
		t.Fatal("mismatched bearer accepted")
	}
}
