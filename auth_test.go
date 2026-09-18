package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type authExecFunc func(context.Context, string, ...any) (sql.Result, error)

func (f authExecFunc) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return f(ctx, query, args...)
}

type authResult struct{}

func (authResult) LastInsertId() (int64, error) { return 0, nil }
func (authResult) RowsAffected() (int64, error) { return 1, nil }

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

func TestClientHostIgnoresForwardedFor(t *testing.T) {
	// Two requests from the same peer with different client-supplied
	// forwarding headers must land in the SAME limiter bucket: otherwise a
	// direct client mints a fresh allowance per request (audit AUTH-01).
	r := httptest.NewRequest("POST", "/api/auth/password", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.1")
	if got := clientHost(r); got != "10.0.0.1" {
		t.Fatalf("clientHost = %q, want the socket peer", got)
	}
	r.Header.Set("X-Forwarded-For", "198.51.100.7")
	if got := clientHost(r); got != "10.0.0.1" {
		t.Fatalf("clientHost followed a rotated header: %q", got)
	}
	// IPv4-mapped IPv6 peers normalize onto their IPv4 form.
	r.RemoteAddr = "[::ffff:192.0.2.4]:999"
	if got := clientHost(r); got != "192.0.2.4" {
		t.Fatalf("mapped v6 = %q", got)
	}
	// An unparseable peer gets a stable, distinct bucket.
	r.RemoteAddr = "garbage"
	if got := clientHost(r); got != "invalid-peer" {
		t.Fatalf("invalid peer = %q", got)
	}
}

func TestAuthAttemptsBoundByTableCeiling(t *testing.T) {
	a := &App{authAttempts: map[string]authAttempt{}}
	for i := 0; i < authAttemptCeil; i++ {
		a.authAttempts[fmt.Sprintf("198.51.100.%d", i%250)+"-"+strconv.Itoa(i)] = authAttempt{Window: time.Now()}
	}
	r := httptest.NewRequest("POST", "/api/auth/recovery", nil)
	r.RemoteAddr = "203.0.113.9:1234" // a fresh key; table is at capacity
	if a.allowAuthAttempt(r) {
		t.Fatal("a fresh limiter key was admitted at table capacity")
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

func TestPersistSessionReturnsTokenOnlyAfterInsert(t *testing.T) {
	a := &App{}
	r := httptest.NewRequest("POST", "/auth/recovery", nil)
	r.Header.Set("User-Agent", strings.Repeat("a", 400))
	var storedHash string
	exec := authExecFunc(func(_ context.Context, query string, args ...any) (sql.Result, error) {
		if !strings.Contains(query, "INSERT INTO auth_sessions") {
			t.Fatalf("unexpected query: %s", query)
		}
		storedHash = args[0].(string)
		if args[1] != "user-id" || args[3] != strings.Repeat("a", 300) || args[4] != loginMethodPasskey {
			t.Fatalf("unexpected session args: %#v", args)
		}
		if args[5] != int64(3) {
			t.Fatalf("session epoch = %v, want 3", args[5])
		}
		return authResult{}, nil
	})
	raw, err := a.persistSession(context.Background(), exec, r, "user-id", "unknown", 3)
	if err != nil {
		t.Fatal(err)
	}
	if raw == "" || storedHash != tokenHash(raw) || storedHash == raw {
		t.Fatal("session token was not returned only as a hash-backed opaque value")
	}

	wantErr := errors.New("insert failed")
	raw, err = a.persistSession(context.Background(), authExecFunc(func(context.Context, string, ...any) (sql.Result, error) {
		return nil, wantErr
	}), r, "user-id", loginMethodRecovery, 0)
	if raw != "" || !errors.Is(err, wantErr) {
		t.Fatalf("failed insert returned raw=%q err=%v", raw, err)
	}
}

func TestLastFactorInvariant(t *testing.T) {
	onlyPass := loginFactors{Password: true}
	if !lastFactor(onlyPass, "password") {
		t.Fatal("sole password was not last")
	}
	if lastFactor(loginFactors{Password: true, Passkeys: 1}, "password") {
		t.Fatal("password with a passkey treated as last")
	}
	if !lastFactor(loginFactors{Passkeys: 1}, "passkey") {
		t.Fatal("sole passkey was not last")
	}
	if lastFactor(loginFactors{Passkeys: 1, Password: true}, "passkey") {
		t.Fatal("last passkey with a password treated as last credential")
	}
	if !lastFactor(loginFactors{TOTP: true}, "totp") {
		t.Fatal("sole totp was not last")
	}
	if lastFactor(loginFactors{TOTP: true, Password: true}, "totp") {
		t.Fatal("totp with a password treated as last")
	}
}

func TestLoginMethodConstantsAndSchemaSync(t *testing.T) {
	for _, m := range []string{loginMethodPasskey, loginMethodPassword, loginMethodRecovery, loginMethodTOTP, loginMethodBootstrap} {
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
		strings.Join([]string{loginMethodPasskey, loginMethodRecovery, loginMethodTOTP, loginMethodBootstrap, loginMethodPassword}, "','") + "'))"
	if !strings.Contains(schemaSQL, want) {
		t.Fatalf("schema.sql login_method CHECK out of sync with loginMethod* constants; want %q", want)
	}
	// Old installs carry the first-release constraint. The drop/re-add pair is
	// what upgrades it in place; pin that both halves name the same stable
	// auto-generated constraint and re-add the current method set.
	drop := "ALTER TABLE auth_sessions DROP CONSTRAINT IF EXISTS auth_sessions_login_method_check"
	if !strings.Contains(schemaSQL, drop) {
		t.Fatal("schema.sql is missing the login_method constraint drop for upgrading old installs")
	}
	add := "ALTER TABLE auth_sessions ADD CONSTRAINT auth_sessions_login_method_check"
	if !strings.Contains(schemaSQL, add) {
		t.Fatal("schema.sql is missing the login_method constraint re-add")
	}
}

// The auth-epoch pair must keep converging on old installs (both columns
// IF NOT EXISTS, both defaulting to 0 so standing sessions stay valid),
// and the session lookup must carry the epoch join (audit AUTH-05
// remainder / AUTH-01).
func TestAuthEpochSchemaAndLookupSync(t *testing.T) {
	for _, want := range []string{
		"ALTER TABLE users ADD COLUMN IF NOT EXISTS auth_epoch bigint NOT NULL DEFAULT 0",
		"ALTER TABLE auth_sessions ADD COLUMN IF NOT EXISTS auth_epoch bigint NOT NULL DEFAULT 0",
	} {
		if !strings.Contains(schemaSQL, want) {
			t.Fatalf("schema.sql is missing the auth-epoch column: %q", want)
		}
	}
}

// The matched step must be the highest acceptable one so replay
// consumption is monotonic: a code valid for two windows claims the later
// step, and no step ever moves backwards (audit AUTH-03).
func TestTOTPMatchedStepIsHighestAndMonotonic(t *testing.T) {
	secret, _ := hex.DecodeString("3132333435363738393031323334353637383930")
	now := time.Unix(59, 0)
	code := totpCode(secret, now)
	step := totpMatchedStep(secret, code, now)
	if step != now.Unix()/30 {
		t.Fatalf("matched step %d, want the current step %d", step, now.Unix()/30)
	}
	// One window later the previous step's code is still inside the -1
	// skew window; it must match at its own (earlier) step, never the
	// current one.
	later := now.Add(30 * time.Second)
	step = totpMatchedStep(secret, code, later)
	if step != now.Unix()/30 {
		t.Fatalf("skew match reported step %d, want the earlier step %d", step, now.Unix()/30)
	}
	if totpMatchedStep(secret, "000000", now) != -1 {
		t.Fatal("a nonmatching code produced a step")
	}
}

// A failed session revocation must keep the cookie: clearing it would remove
// the only token this browser can retry revocation with, while pretending the
// user signed out (audit 4 F13).
func TestLogoutKeepsCookieWhenRevocationFails(t *testing.T) {
	a := &App{
		cfg: &Config{},
		log: discardLogger(),
		db:  openStepDB(t, dbStep{kind: "exec", err: errors.New("database unavailable")}),
	}
	r := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "the-session-token"})
	w := httptest.NewRecorder()
	a.handleLogout(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("failed revocation did not advertise Retry-After")
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge < 0 {
			t.Fatal("failed revocation cleared the session cookie; the browser can no longer retry")
		}
	}
}

// A successful revocation clears the cookie and reports signed-out.
func TestLogoutClearsCookieOnSuccess(t *testing.T) {
	a := &App{cfg: &Config{}, log: discardLogger(), db: openStepDB(t, dbStep{kind: "exec"})}
	r := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "the-session-token"})
	w := httptest.NewRecorder()
	a.handleLogout(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	cleared := false
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("successful logout did not clear the session cookie")
	}
}

// Truncation must never split a multi-byte rune: an 80-byte slice of
// accented or CJK text used to become invalid UTF-8 before storage
// (audit 4 F17).
func TestUTF8PrefixKeepsRunesIntact(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		max   int
		empty bool
	}{
		{"short ascii passes through", "Jane Doe", 80, false},
		{"accented name", strings.Repeat("é", 60), 80, false},
		{"cjk name", strings.Repeat("邮", 60), 80, false},
		{"emoji name", strings.Repeat("📩", 40), 80, false},
		{"zero limit", "anything", 0, true},
	}
	for _, tc := range cases {
		got := utf8Prefix(tc.in, tc.max)
		if got == "" && !tc.empty {
			t.Fatalf("%s: empty result for max=%d", tc.name, tc.max)
		}
		if len(got) > tc.max {
			t.Fatalf("%s: len = %d, want <= %d", tc.name, len(got), tc.max)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("%s: result is invalid UTF-8: %q", tc.name, got)
		}
	}
	// A cut landing mid-rune backs off to the rune boundary; a cut landing
	// exactly on one keeps it.
	if got := utf8Prefix("abcéd", 4); got != "abc" {
		t.Fatalf("mid-rune cut = %q, want %q", got, "abc")
	}
	if got := utf8Prefix("abcéd", 5); got != "abcé" {
		t.Fatalf("boundary cut = %q, want %q", got, "abcé")
	}
}
