package main

import (
	"encoding/hex"
	"net/http/httptest"
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
