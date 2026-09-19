package main

// Real-PostgreSQL regressions for the re-authentication ceremony (audit
// AUTH-02/AUTH-06 pass 6) and the durable per-user TOTP budget (audit
// AUTH-06 pass 7).

import (
	"database/sql"
	"encoding/base32"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testPassword = "correct horse battery staple"

// seedPassword installs a standing password credential for the owner.
func seedPassword(t *testing.T, p productPG, password string) {
	t.Helper()
	hash, err := hashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(`INSERT INTO auth_passwords (user_id, hash) VALUES ($1,$2)
		ON CONFLICT (user_id) DO UPDATE SET hash=excluded.hash`, p.uid, hash); err != nil {
		t.Fatal(err)
	}
}

// seedSession inserts a session row and returns its id_hash; age backs
// created_at away from now.
func seedSession(t *testing.T, p productPG, age time.Duration) string {
	t.Helper()
	hash := tokenHash("raw-session-" + time.Now().String())
	if _, err := p.db.Exec(`INSERT INTO auth_sessions
		(id_hash, user_id, expires_at, user_agent, login_method, auth_epoch, created_at)
		VALUES ($1,$2,now()+interval '1 day','test','password',0,now()-$3::interval)`,
		hash, p.uid, age.String()); err != nil {
		t.Fatal(err)
	}
	return hash
}

// Freshness gate: a stale session is refused every credential-enrolling
// mutation, a fresh sign-in passes, a password confirmation reopens the
// window for a stale session, and the stamp itself expires.
func TestIntegrationReauthFreshnessGate(t *testing.T) {
	p := newProductPG(t)
	seedPassword(t, p, testPassword)
	stale := seedSession(t, p, time.Hour)
	fresh := seedSession(t, p, time.Minute)

	// The sweep's side-effect-free members: TOTP enrollment shows the
	// secret, agent-token creation shows a bearer credential. Password set
	// and recovery regeneration only appear on the stale session, where
	// the gate refuses them before any side effect runs.
	totpBegin := func(session string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		p.app.handleTOTPBegin(w, ownerSession(http.MethodPost, "/api/security/totp/begin", "", p.uid, session))
		return w
	}
	agentCreate := func(session string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		p.app.handleAgentTokens(w, ownerSession(http.MethodPost, "/api/security/agent-tokens", `{"name":"t"}`, p.uid, session))
		return w
	}
	passwordSet := func(session string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		p.app.handlePasswordSet(w, ownerSession(http.MethodPost, "/api/security/password", `{"current":"`+testPassword+`","new":"`+testPassword+`"}`, p.uid, session))
		return w
	}
	recoveryRegen := func(session string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		p.app.handleRecoveryRegenerate(w, ownerSession(http.MethodPost, "/api/security/recovery/regenerate", "", p.uid, session))
		return w
	}

	for name, invoke := range map[string]func(string) *httptest.ResponseRecorder{
		"totp begin":          totpBegin,
		"agent token create":  agentCreate,
		"password set":        passwordSet,
		"recovery regenerate": recoveryRegen,
	} {
		if w := invoke(stale); w.Code != http.StatusPreconditionRequired {
			t.Fatalf("%s on a stale session: status = %d, want 428 (%s)", name, w.Code, w.Body.String())
		}
	}
	for name, invoke := range map[string]func(string) *httptest.ResponseRecorder{
		"totp begin":         totpBegin,
		"agent token create": agentCreate,
	} {
		if w := invoke(fresh); w.Code == http.StatusPreconditionRequired {
			t.Fatalf("%s on a fresh sign-in session was gated: %s", name, w.Body.String())
		}
	}

	// A confirmation on the stale session reopens the window for it...
	if w := p.reauthenticate(stale, testPassword); w.Code != http.StatusOK {
		t.Fatalf("reauthenticate status = %d: %s", w.Code, w.Body.String())
	}
	var stamped sql.NullTime
	if err := p.db.QueryRow(`SELECT reauthenticated_at FROM auth_sessions WHERE id_hash=$1`, stale).Scan(&stamped); err != nil {
		t.Fatal(err)
	}
	if !stamped.Valid || time.Since(stamped.Time) > time.Minute {
		t.Fatalf("reauthenticated_at not stamped freshly: %v", stamped)
	}
	for name, invoke := range map[string]func(string) *httptest.ResponseRecorder{
		"totp begin":         totpBegin,
		"agent token create": agentCreate,
	} {
		if w := invoke(stale); w.Code == http.StatusPreconditionRequired {
			t.Fatalf("%s still gated after confirmation: %s", name, w.Body.String())
		}
	}

	// ...for ten minutes.
	if _, err := p.db.Exec(`UPDATE auth_sessions SET reauthenticated_at = now() - interval '11 minutes' WHERE id_hash=$1`, stale); err != nil {
		t.Fatal(err)
	}
	for name, invoke := range map[string]func(string) *httptest.ResponseRecorder{
		"totp begin":          totpBegin,
		"agent token create":  agentCreate,
		"password set":        passwordSet,
		"recovery regenerate": recoveryRegen,
	} {
		if w := invoke(stale); w.Code != http.StatusPreconditionRequired {
			t.Fatalf("%s accepted an expired confirmation: status = %d", name, w.Code)
		}
	}
}

// reauthenticate drives the ceremony endpoint for one session hash.
func (p productPG) reauthenticate(session, password string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := ownerSession(http.MethodPost, "/api/security/reauthenticate", `{"password":"`+password+`"}`, p.uid, session)
	p.app.handleReauthenticate(w, r)
	return w
}

// Wrong confirmations are refused and counted exactly like wrong sign-ins;
// the lockout threshold then protects the ceremony account-wide.
func TestIntegrationReauthenticateWrongPasswordAndLockout(t *testing.T) {
	p := newProductPG(t)
	seedPassword(t, p, testPassword)
	session := seedSession(t, p, time.Hour)

	if w := p.reauthenticate(session, "wrong-password"); w.Code != http.StatusForbidden {
		t.Fatalf("wrong password status = %d: %s", w.Code, w.Body.String())
	}
	var stamped sql.NullTime
	if err := p.db.QueryRow(`SELECT reauthenticated_at FROM auth_sessions WHERE id_hash=$1`, session).Scan(&stamped); err != nil {
		t.Fatal(err)
	}
	if stamped.Valid {
		t.Fatal("a wrong confirmation stamped reauthenticated_at")
	}

	for i := 0; i < passwordLockThreshold-1; i++ {
		if w := p.reauthenticate(session, "wrong-password"); w.Code != http.StatusForbidden {
			t.Fatalf("failure %d status = %d", i+2, w.Code)
		}
	}
	if w := p.reauthenticate(session, testPassword); w.Code != http.StatusTooManyRequests {
		t.Fatalf("correct password after lockout threshold status = %d, want 429", w.Code)
	}
	// The lockout key is the account, not the session: a second session
	// cannot sidestep it by confirming in parallel.
	other := seedSession(t, p, time.Hour)
	if w := p.reauthenticate(other, testPassword); w.Code != http.StatusTooManyRequests {
		t.Fatalf("lockout is not account-wide: status = %d", w.Code)
	}
}

// seedTOTP installs an enabled authenticator with a known secret.
func seedTOTP(t *testing.T, p productPG) []byte {
	t.Helper()
	secret := []byte("lullmail-test-secret")
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
	sealed, err := sealSecret(p.cfg, encoded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(`INSERT INTO auth_totp (user_id, secret_ciphertext, enabled_at, last_used_step)
		VALUES ($1,$2,now(),-1) ON CONFLICT (user_id) DO UPDATE SET secret_ciphertext=excluded.secret_ciphertext, enabled_at=now(), last_used_step=-1`,
		p.uid, sealed); err != nil {
		t.Fatal(err)
	}
	return secret
}

// totpAttempt drives the standalone TOTP login once from a given peer.
func (p productPG) totpAttempt(code, remoteAddr string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/auth/totp", strings.NewReader(`{"code":"`+code+`"}`))
	r.RemoteAddr = remoteAddr
	p.app.handleTOTPLogin(w, r)
	return w
}

// The per-user budget: wrong guesses arriving from many distinct peers —
// each inside its own per-peer allowance — exhaust one shared window, and
// even the correct code is refused until the fixed window rolls over.
func TestIntegrationTOTPPerUserBudgetExhaustion(t *testing.T) {
	p := newProductPG(t)
	secret := seedTOTP(t, p)

	for i := 0; i < maxTOTPWindowAttempts; i++ {
		peer := "198.51.100." + string(rune('0'+i)) + ":9999"
		if w := p.totpAttempt("000000", peer); w.Code != http.StatusUnauthorized {
			t.Fatalf("guess %d from %s status = %d: %s", i+1, peer, w.Code, w.Body.String())
		}
	}
	w := p.totpAttempt(totpCode(secret, time.Now()), "198.51.100.200:9999")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("correct code after budget exhaustion status = %d, want 429: %s", w.Code, w.Body.String())
	}
	if retryAfter := w.Header().Get("Retry-After"); retryAfter == "" {
		t.Fatal("budget exhaustion carries no Retry-After")
	}
	var attempts int
	if err := p.db.QueryRow(`SELECT attempts FROM auth_factor_windows WHERE user_id=$1 AND factor='totp'`, p.uid).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != maxTOTPWindowAttempts {
		t.Fatalf("recorded attempts = %d, want %d", attempts, maxTOTPWindowAttempts)
	}

	// Fixed-window semantics without sleeping five minutes: a spent window
	// in the previous slot does not lock the current one, and the correct
	// code signs in.
	if _, err := p.db.Exec(`UPDATE auth_factor_windows
		SET window_start = window_start - interval '10 minutes'
		WHERE user_id=$1 AND factor='totp'`, p.uid); err != nil {
		t.Fatal(err)
	}
	w = p.totpAttempt(totpCode(secret, time.Now()), "198.51.100.201:9999")
	if w.Code != http.StatusOK {
		t.Fatalf("sign-in after window rollover status = %d: %s", w.Code, w.Body.String())
	}
}

// The budget is per user: another user's exhausted window never leaks into
// this one's sign-in.
func TestIntegrationTOTPBudgetIsPerUser(t *testing.T) {
	p := newProductPG(t)
	seedTOTP(t, p)
	var other string
	if err := p.db.QueryRow(`INSERT INTO users (email, display_name) VALUES ('other@example.com','Other') RETURNING id::text`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(`INSERT INTO auth_factor_windows (user_id, factor, window_start, attempts)
		VALUES ($1,'totp',$2,$3)`,
		other, totpWindowStart(time.Now()), maxTOTPWindowAttempts); err != nil {
		t.Fatal(err)
	}
	// A wrong guess for THIS user still answers plain 401 — the other
	// user's budget does not lock this account.
	if w := p.totpAttempt("000000", "198.51.100.7:9999"); w.Code != http.StatusUnauthorized {
		t.Fatalf("another user's budget leaked: status = %d (%s)", w.Code, w.Body.String())
	}
}
