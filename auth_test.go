package main

import (
	"encoding/hex"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTOTPValidationWindowAndFormatting(t *testing.T) {
	secret, _ := hex.DecodeString("3132333435363738393031323334353637383930")
	at := time.Unix(59, 0)
	if got := totpCode(secret, at); got != "287082" { // RFC 6238 vector, final six digits.
		t.Fatalf("totp = %q", got)
	}
	if !validTOTP(secret, "287082", at) {
		t.Fatal("current code rejected")
	}
	if !validTOTP(secret, totpCode(secret, at.Add(30*time.Second)), at) {
		t.Fatal("adjacent code rejected")
	}
	if validTOTP(secret, "000000", at) {
		t.Fatal("bad code accepted")
	}
}

func TestAuthRateLimitIsBoundedAndResets(t *testing.T) {
	a := &App{authAttempts: map[string]authAttempt{}}
	r := httptest.NewRequest("POST", "/api/auth/recovery", nil)
	r.RemoteAddr = "192.0.2.4:1234"
	for i := 0; i < 10; i++ {
		if !a.allowAuthAttempt(r) {
			t.Fatalf("attempt %d rejected early", i+1)
		}
	}
	if a.allowAuthAttempt(r) {
		t.Fatal("eleventh attempt accepted")
	}
	a.clearAuthAttempts(r)
	if !a.allowAuthAttempt(r) {
		t.Fatal("successful-login reset did not clear limit")
	}
}

func TestCookieSecurityFollowsPublicOrigin(t *testing.T) {
	a := &App{cfg: &Config{SecureAuth: true}}
	w := httptest.NewRecorder()
	a.setCookie(w, sessionCookie, "opaque", time.Hour)
	cookie := w.Result().Cookies()[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite == 0 {
		t.Fatalf("weak cookie: %#v", cookie)
	}
}

func TestLoginMethodConstantsAndSchemaSync(t *testing.T) {
	for _, m := range []string{loginMethodPasskey, loginMethodRecovery, loginMethodTOTP, loginMethodBootstrap} {
		if got := normalizeLoginMethod(m); got != m {
			t.Fatalf("normalizeLoginMethod(%q) = %q", m, got)
		}
	}
	// NULL scans as "" — pre-migration rows must read as passkey, not nudge.
	for _, m := range []string{"", "unknown", "PASSKEY"} {
		if got := normalizeLoginMethod(m); got != loginMethodPasskey {
			t.Fatalf("normalizeLoginMethod(%q) = %q, want %q", m, got, loginMethodPasskey)
		}
	}
	want := "login_method text CHECK (login_method IN ('" +
		strings.Join([]string{loginMethodPasskey, loginMethodRecovery, loginMethodTOTP, loginMethodBootstrap}, "','") + "'))"
	if !strings.Contains(schemaSQL, want) {
		t.Fatalf("schema.sql login_method CHECK out of sync with loginMethod* constants; want %q", want)
	}
}
