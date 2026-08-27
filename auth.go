package main

// Recovery-first authentication. Passkeys are the primary factor; opaque,
// hashed server-side sessions keep WebAuthn out of the request hot path.
// Printable one-use recovery codes and optional TOTP prevent a lost device
// from turning a self-hosted mailbox into a permanent lockout.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

const (
	sessionCookie    = "es_session"
	ceremonyCookie   = "es_ceremony"
	sessionLifetime  = 30 * 24 * time.Hour
	ceremonyLifetime = 5 * time.Minute
)

// How a session was created; the values are pinned by the auth_sessions
// login_method CHECK constraint in schema.sql. The dashboard uses them to
// nudge fallback sessions toward adding a passkey on the current device.
const (
	loginMethodPasskey   = "passkey"
	loginMethodRecovery  = "recovery"
	loginMethodTOTP      = "totp"
	loginMethodBootstrap = "bootstrap"
)

// normalizeLoginMethod maps a stored login_method onto the constants above.
// Rows from before the column existed read as NULL; the nudge only targets
// fallback logins, so unknown values read as passkey.
func normalizeLoginMethod(method string) string {
	switch method {
	case loginMethodRecovery, loginMethodTOTP, loginMethodBootstrap:
		return method
	default:
		return loginMethodPasskey
	}
}

type authContextKey struct{}
type sessionContextKey struct{}
type agentContextKey struct{}

// contextWithAgent stamps an agent-token identity into a request context.
func contextWithAgent(ctx context.Context, uid string) context.Context {
	return context.WithValue(ctx, agentContextKey{}, uid)
}

type authAttempt struct {
	Window time.Time
	Count  int
}

func (a *App) allowAuthAttempt(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if host == "" {
		host = "unknown"
	}
	a.authMu.Lock()
	defer a.authMu.Unlock()
	now := time.Now()
	// The map is keyed by client host and nothing else prunes it; a sweep on
	// a size threshold keeps a many-source internet from growing it forever.
	if len(a.authAttempts) > 512 {
		for h, at := range a.authAttempts {
			if now.Sub(at.Window) > 5*time.Minute {
				delete(a.authAttempts, h)
			}
		}
	}
	attempt := a.authAttempts[host]
	if attempt.Window.IsZero() || now.Sub(attempt.Window) > 5*time.Minute {
		attempt = authAttempt{Window: now}
	}
	attempt.Count++
	a.authAttempts[host] = attempt
	return attempt.Count <= 10
}

func (a *App) clearAuthAttempts(r *http.Request) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a.authMu.Lock()
	delete(a.authAttempts, host)
	a.authMu.Unlock()
}

type webUser struct {
	ID          string
	Handle      []byte
	Email       string
	DisplayName string
	Credentials []webauthn.Credential
}

func (u *webUser) WebAuthnID() []byte   { return u.Handle }
func (u *webUser) WebAuthnName() string { return u.Email }
func (u *webUser) WebAuthnDisplayName() string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Email
}
func (u *webUser) WebAuthnCredentials() []webauthn.Credential { return u.Credentials }

func newWebAuthn(cfg *Config) (*webauthn.WebAuthn, error) {
	if cfg.RPID == "" || cfg.PublicURL == "" {
		return nil, errors.New("PUBLIC_URL must be an absolute browser origin")
	}
	return webauthn.New(&webauthn.Config{
		RPDisplayName: "email-soft",
		RPID:          cfg.RPID,
		RPOrigins:     []string{cfg.PublicURL},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		},
	})
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := io.ReadFull(rand.Reader, b)
	return b, err
}

func opaqueToken(n int) (string, error) {
	b, err := randomBytes(n)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func tokenHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (a *App) setCookie(w http.ResponseWriter, name, value string, lifetime time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", HttpOnly: true,
		Secure: a.cfg.SecureAuth, SameSite: http.SameSiteLaxMode,
		MaxAge: int(lifetime.Seconds()), Expires: time.Now().Add(lifetime),
	})
}

func (a *App) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/", HttpOnly: true,
		Secure: a.cfg.SecureAuth, SameSite: http.SameSiteLaxMode,
		MaxAge: -1, Expires: time.Unix(1, 0),
	})
}

func (a *App) authenticateRequest(r *http.Request) (string, string, error) {
	if cookie, err := r.Cookie(sessionCookie); err == nil && cookie.Value != "" {
		hash := tokenHash(cookie.Value)
		var uid string
		err := a.db.QueryRowContext(r.Context(), `
			UPDATE auth_sessions SET last_seen_at = now()
			WHERE id_hash = $1 AND expires_at > now()
			RETURNING user_id`, hash).Scan(&uid)
		if err == nil {
			return uid, hash, nil
		}
		if err != sql.ErrNoRows {
			return "", "", err
		}
	}

	// EMAILSOFT_TOKEN is an installation/bootstrap secret, not a permanent
	// parallel login. It stops opening product data after the first passkey,
	// and a generated token additionally expires 24h after it was minted.
	var credentials int
	if err := a.db.QueryRowContext(r.Context(), `SELECT count(*) FROM auth_credentials`).Scan(&credentials); err != nil {
		return "", "", err
	}
	if credentials == 0 && a.setupTokenValid() {
		got := r.Header.Get("Authorization")
		if constantTimeBearer(got, a.cfg.APIToken) {
			uid, err := a.firstUserID(r.Context())
			return uid, "bootstrap", err
		}
	}
	return "", "", sql.ErrNoRows
}

func (a *App) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, session, err := a.authenticateRequest(r)
		if err != nil {
			// Only "no credential matched" is an auth failure; a database
			// outage mislabeled as 401 sends clients hunting the wrong bug.
			if !errors.Is(err, sql.ErrNoRows) {
				writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
				return
			}
			writeProblem(w, http.StatusUnauthorized, "Unauthorized", "sign in to continue")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if origin := r.Header.Get("Origin"); origin != "" && origin != a.cfg.PublicURL {
				writeProblem(w, http.StatusForbidden, "Origin Rejected", "request origin does not match PUBLIC_URL")
				return
			}
		}
		ctx := context.WithValue(r.Context(), authContextKey{}, uid)
		ctx = context.WithValue(ctx, sessionContextKey{}, session)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *App) mountAuth(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/status", a.handleAuthStatus)
	mux.HandleFunc("POST /auth/bootstrap/begin", a.handleBootstrapBegin)
	mux.HandleFunc("POST /auth/bootstrap/finish", a.handleBootstrapFinish)
	mux.HandleFunc("POST /auth/login/begin", a.handleLoginBegin)
	mux.HandleFunc("POST /auth/login/finish", a.handleLoginFinish)
	mux.HandleFunc("POST /auth/recovery", a.handleRecoveryLogin)
	mux.HandleFunc("POST /auth/totp", a.handleTOTPLogin)
	mux.HandleFunc("POST /auth/logout", a.handleLogout)
}

func (a *App) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	var count int
	if err := a.db.QueryRowContext(r.Context(), `SELECT count(*) FROM auth_credentials`).Scan(&count); err != nil {
		writeProblem(w, 500, "Status Failed", err.Error())
		return
	}
	uid, session, err := a.authenticateRequest(r)
	authenticated := err == nil
	email := ""
	via := ""
	if authenticated {
		_ = a.db.QueryRowContext(r.Context(), `SELECT email FROM users WHERE id=$1`, uid).Scan(&email)
		via = a.sessionLoginMethod(r.Context(), session)
	}
	status := map[string]any{
		"configured": count > 0, "authenticated": authenticated, "email": email,
		"bootstrap_available": count == 0 && a.setupTokenValid(),
		"passkey_supported":   true,
	}
	if via != "" {
		status["via"] = via
	}
	if count == 0 && a.cfg.RPID == "" {
		// First-run setup with no pinned origin: show the wizard where it
		// thinks it is, so a wrong proxy header is visible before a passkey
		// gets bound to it.
		status["detected_origin"] = detectOrigin(r)
	}
	writeJSON(w, status)
}

func (a *App) bootstrapAuthorized(r *http.Request) bool {
	if !a.setupTokenValid() {
		return false
	}
	return constantTimeBearer(r.Header.Get("Authorization"), a.cfg.APIToken)
}

func (a *App) handleBootstrapBegin(w http.ResponseWriter, r *http.Request) {
	if !a.bootstrapAuthorized(r) {
		writeProblem(w, 401, "Unauthorized", "the one-time setup token is required")
		return
	}
	var count int
	if err := a.db.QueryRowContext(r.Context(), `SELECT count(*) FROM auth_credentials`).Scan(&count); err != nil || count != 0 {
		writeProblem(w, 409, "Already Configured", "a passkey already protects this installation")
		return
	}
	// No origin pinned yet (PUBLIC_URL unset, fresh install): adopt the one
	// this setup request actually arrived on. Nothing is secret before the
	// first passkey exists; the completing request's origin gets stored.
	if a.cfg.RPID == "" {
		if !a.setOriginForSetup(detectOrigin(r)) {
			writeProblem(w, 422, "Origin Unavailable", "the browser origin could not be detected — set PUBLIC_URL on the server")
			return
		}
	}
	var req struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req)
	if _, err := a.firstUserID(r.Context()); err == sql.ErrNoRows {
		name := strings.TrimSpace(req.Name)
		email := strings.TrimSpace(req.Email)
		if email == "" && name == "" {
			email = a.cfg.UserEmail
		}
		if email == "" && name != "" {
			email = ownerEmailFromName(name)
		}
		if email == "" {
			writeProblem(w, 422, "Name Missing", "enter your name so email-soft knows whose mail this is")
			return
		}
		a.cfg.UserEmail = email
		if err := a.ensureUser(r.Context(), name); err != nil {
			writeProblem(w, 500, "Setup Failed", err.Error())
			return
		}
	} else if name := strings.TrimSpace(req.Name); name != "" {
		_ = a.setOwnerName(r.Context(), name)
	}
	uid, err := a.firstUserID(r.Context())
	if err != nil {
		writeProblem(w, 422, "Owner Missing", "enter your name to finish setup")
		return
	}
	a.beginRegistration(w, r, uid, "bootstrap")
}

func (a *App) handleBootstrapFinish(w http.ResponseWriter, r *http.Request) {
	if !a.bootstrapAuthorized(r) {
		writeProblem(w, 401, "Unauthorized", "the one-time setup token is required")
		return
	}
	uid, err := a.finishRegistration(w, r, "bootstrap")
	if err != nil {
		return
	}
	// Setup is complete: pin the origin this ceremony used (unless PUBLIC_URL
	// was set explicitly) and retire the one-time token for good.
	if !a.cfg.PublicURLSet && a.cfg.RPID != "" {
		if err := storeSetting(a.db, "public_url", a.cfg.PublicURL); err != nil {
			writeProblem(w, 500, "Setup Failed", "could not persist the site origin: "+err.Error())
			return
		}
	}
	if a.cfg.DataDir != "" && !a.tokenFromEnv {
		deleteSetupToken(a.cfg.DataDir)
	}
	a.setupTokenCreated = time.Time{}
	codes, err := a.replaceRecoveryCodes(r.Context(), uid)
	if err != nil {
		writeProblem(w, 500, "Recovery Setup Failed", err.Error())
		return
	}
	if err := a.createSession(w, r, uid, loginMethodBootstrap); err != nil {
		writeProblem(w, 500, "Session Failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "recovery_codes": codes})
}

func (a *App) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	uid, _ := a.userID(r.Context())
	a.beginRegistration(w, r, uid, "register")
}

func (a *App) beginRegistration(w http.ResponseWriter, r *http.Request, uid, kind string) {
	user, err := a.loadWebUser(r.Context(), uid)
	if err != nil {
		writeProblem(w, 500, "Passkey Setup Failed", err.Error())
		return
	}
	exclusions := make([]protocol.CredentialDescriptor, 0, len(user.Credentials))
	for _, c := range user.Credentials {
		exclusions = append(exclusions, c.Descriptor())
	}
	creation, session, err := a.webAuthn().BeginRegistration(user,
		webauthn.WithExclusions(exclusions),
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		}),
	)
	if err != nil {
		writeProblem(w, 500, "Passkey Setup Failed", err.Error())
		return
	}
	if err := a.storeCeremony(w, r.Context(), uid, kind, session); err != nil {
		writeProblem(w, 500, "Passkey Setup Failed", err.Error())
		return
	}
	writeJSON(w, creation)
}

func (a *App) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	uid, err := a.finishRegistration(w, r, "register")
	if err != nil {
		return
	}
	writeJSON(w, map[string]any{"ok": true, "user_id": uid})
}

func (a *App) finishRegistration(w http.ResponseWriter, r *http.Request, kind string) (string, error) {
	uid, session, err := a.takeCeremony(r, kind)
	if err != nil {
		writeProblem(w, 400, "Passkey Expired", "start the passkey step again")
		return "", err
	}
	user, err := a.loadWebUser(r.Context(), uid)
	if err != nil {
		writeProblem(w, 500, "Passkey Failed", err.Error())
		return "", err
	}
	credential, err := a.webAuthn().FinishRegistration(user, *session, r)
	if err != nil {
		writeProblem(w, 400, "Passkey Rejected", "the browser response could not be verified")
		return "", err
	}
	encoded, err := json.Marshal(credential)
	if err != nil {
		writeProblem(w, 500, "Passkey Failed", err.Error())
		return "", err
	}
	sealed, err := sealSecret(a.cfg, string(encoded))
	if err != nil {
		writeProblem(w, 500, "Passkey Failed", err.Error())
		return "", err
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = "Passkey"
	}
	if len(name) > 80 {
		name = name[:80]
	}
	id := base64.RawURLEncoding.EncodeToString(credential.ID)
	_, err = a.db.ExecContext(r.Context(), `INSERT INTO auth_credentials
		(id,user_id,name,credential_ciphertext) VALUES ($1,$2,$3,$4)`, id, uid, name, sealed)
	if err != nil {
		writeProblem(w, 409, "Passkey Exists", "this passkey is already registered")
		return "", err
	}
	a.clearCookie(w, ceremonyCookie)
	return uid, nil
}

func (a *App) handleLoginBegin(w http.ResponseWriter, r *http.Request) {
	// Ceremonies are rows; an unthrottled begin lets strangers grow the
	// table (the purge tick bounds it, but the tap still gets shut).
	if !a.allowAuthAttempt(r) {
		writeProblem(w, 429, "Too Many Attempts", "wait five minutes before trying again")
		return
	}
	var count int
	_ = a.db.QueryRowContext(r.Context(), `SELECT count(*) FROM auth_credentials`).Scan(&count)
	if count == 0 {
		writeProblem(w, 409, "Setup Required", "create the first passkey with the setup token")
		return
	}
	assertion, session, err := a.webAuthn().BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		writeProblem(w, 500, "Sign In Failed", err.Error())
		return
	}
	if err := a.storeCeremony(w, r.Context(), "", "login", session); err != nil {
		writeProblem(w, 500, "Sign In Failed", err.Error())
		return
	}
	writeJSON(w, assertion)
}

func (a *App) handleLoginFinish(w http.ResponseWriter, r *http.Request) {
	if !a.allowAuthAttempt(r) {
		writeProblem(w, 429, "Too Many Attempts", "wait five minutes before trying again")
		return
	}
	_, session, err := a.takeCeremony(r, "login")
	if err != nil {
		writeProblem(w, 400, "Sign In Expired", "start sign-in again")
		return
	}
	userAny, credential, err := a.wa.FinishPasskeyLogin(func(rawID, handle []byte) (webauthn.User, error) {
		user, err := a.loadWebUserByHandle(r.Context(), handle)
		if err != nil {
			return nil, err
		}
		wanted := base64.RawURLEncoding.EncodeToString(rawID)
		for _, c := range user.Credentials {
			if base64.RawURLEncoding.EncodeToString(c.ID) == wanted {
				return user, nil
			}
		}
		return nil, sql.ErrNoRows
	}, *session, r)
	if err != nil {
		writeProblem(w, 401, "Sign In Failed", "that passkey could not be verified")
		return
	}
	user := userAny.(*webUser)
	if err := a.saveUsedCredential(r.Context(), user.ID, credential); err != nil {
		writeProblem(w, 500, "Sign In Failed", err.Error())
		return
	}
	if err := a.createSession(w, r, user.ID, loginMethodPasskey); err != nil {
		writeProblem(w, 500, "Session Failed", err.Error())
		return
	}
	a.clearAuthAttempts(r)
	a.clearCookie(w, ceremonyCookie)
	writeJSON(w, map[string]any{"ok": true, "email": user.Email})
}

func (a *App) storeCeremony(w http.ResponseWriter, ctx context.Context, uid, kind string, session *webauthn.SessionData) error {
	raw, err := opaqueToken(32)
	if err != nil {
		return err
	}
	data, err := json.Marshal(session)
	if err != nil {
		return err
	}
	var nullable any
	if uid != "" {
		nullable = uid
	}
	_, err = a.db.ExecContext(ctx, `INSERT INTO auth_challenges
		(id_hash,user_id,kind,session_json,expires_at) VALUES ($1,$2,$3,$4,$5)`,
		tokenHash(raw), nullable, kind, string(data), time.Now().Add(ceremonyLifetime))
	if err == nil {
		a.setCookie(w, ceremonyCookie, raw, ceremonyLifetime)
	}
	return err
}

func (a *App) takeCeremony(r *http.Request, kind string) (string, *webauthn.SessionData, error) {
	cookie, err := r.Cookie(ceremonyCookie)
	if err != nil {
		return "", nil, err
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()
	var uid sql.NullString
	var raw string
	err = tx.QueryRowContext(r.Context(), `DELETE FROM auth_challenges
		WHERE id_hash=$1 AND kind=$2 AND expires_at>now() RETURNING user_id,session_json`,
		tokenHash(cookie.Value), kind).Scan(&uid, &raw)
	if err != nil {
		return "", nil, err
	}
	if err = tx.Commit(); err != nil {
		return "", nil, err
	}
	var session webauthn.SessionData
	if err := json.Unmarshal([]byte(raw), &session); err != nil {
		return "", nil, err
	}
	return uid.String, &session, nil
}

func (a *App) createSession(w http.ResponseWriter, r *http.Request, uid, method string) error {
	raw, err := opaqueToken(32)
	if err != nil {
		return err
	}
	ua := r.UserAgent()
	if len(ua) > 300 {
		ua = ua[:300]
	}
	_, err = a.db.ExecContext(r.Context(), `INSERT INTO auth_sessions
		(id_hash,user_id,expires_at,user_agent,login_method) VALUES ($1,$2,$3,$4,$5)`,
		tokenHash(raw), uid, time.Now().Add(sessionLifetime), ua, normalizeLoginMethod(method))
	if err == nil {
		a.setCookie(w, sessionCookie, raw, sessionLifetime)
	}
	return err
}

// sessionLoginMethod reports how the session backing a request was created.
// The bootstrap token path has no session row; it is its own method. A row
// that vanished mid-request (logout race) reports "".
func (a *App) sessionLoginMethod(ctx context.Context, session string) string {
	if session == "" {
		return ""
	}
	if session == "bootstrap" {
		return loginMethodBootstrap
	}
	var method sql.NullString
	if a.db.QueryRowContext(ctx, `SELECT login_method FROM auth_sessions WHERE id_hash=$1`, session).Scan(&method) != nil {
		return ""
	}
	return normalizeLoginMethod(method.String)
}

func (a *App) loadWebUser(ctx context.Context, uid string) (*webUser, error) {
	var u webUser
	err := a.db.QueryRowContext(ctx, `SELECT id,webauthn_handle,email,display_name FROM users WHERE id=$1`, uid).
		Scan(&u.ID, &u.Handle, &u.Email, &u.DisplayName)
	if err != nil {
		return nil, err
	}
	if len(u.Handle) == 0 {
		u.Handle, err = randomBytes(32)
		if err != nil {
			return nil, err
		}
		if _, err = a.db.ExecContext(ctx, `UPDATE users SET webauthn_handle=$1 WHERE id=$2`, u.Handle, uid); err != nil {
			return nil, err
		}
	}
	rows, err := a.db.QueryContext(ctx, `SELECT credential_ciphertext FROM auth_credentials WHERE user_id=$1 ORDER BY created_at`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sealed string
		if err := rows.Scan(&sealed); err != nil {
			return nil, err
		}
		plain, err := openSecret(a.cfg, sealed)
		if err != nil {
			return nil, err
		}
		var c webauthn.Credential
		if err := json.Unmarshal([]byte(plain), &c); err != nil {
			return nil, err
		}
		u.Credentials = append(u.Credentials, c)
	}
	return &u, rows.Err()
}

func (a *App) loadWebUserByHandle(ctx context.Context, handle []byte) (*webUser, error) {
	var uid string
	if err := a.db.QueryRowContext(ctx, `SELECT id FROM users WHERE webauthn_handle=$1`, handle).Scan(&uid); err != nil {
		return nil, err
	}
	return a.loadWebUser(ctx, uid)
}

func (a *App) saveUsedCredential(ctx context.Context, uid string, credential *webauthn.Credential) error {
	data, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	sealed, err := sealSecret(a.cfg, string(data))
	if err != nil {
		return err
	}
	id := base64.RawURLEncoding.EncodeToString(credential.ID)
	result, err := a.db.ExecContext(ctx, `UPDATE auth_credentials SET credential_ciphertext=$1,last_used_at=now() WHERE id=$2 AND user_id=$3`, sealed, id, uid)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

// revokeOtherSessions signs out every session except the one making the
// request. Credential events (passkey removed, recovery codes replaced) are
// the owner saying "something may be compromised" — stale sessions must not
// outlive that.
func (a *App) revokeOtherSessions(r *http.Request, uid string) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		_, _ = a.db.ExecContext(r.Context(),
			`DELETE FROM auth_sessions WHERE user_id=$1 AND id_hash<>$2`, uid, tokenHash(cookie.Value))
	}
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		_, _ = a.db.ExecContext(r.Context(), `DELETE FROM auth_sessions WHERE id_hash=$1`, tokenHash(cookie.Value))
	}
	a.clearCookie(w, sessionCookie)
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) replaceRecoveryCodes(ctx context.Context, uid string) ([]string, error) {
	codes := make([]string, 10)
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_recovery_codes WHERE user_id=$1`, uid); err != nil {
		return nil, err
	}
	for i := range codes {
		b, err := randomBytes(10)
		if err != nil {
			return nil, err
		}
		compact := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
		codes[i] = compact[:4] + "-" + compact[4:8] + "-" + compact[8:12] + "-" + compact[12:]
		if _, err := tx.ExecContext(ctx, `INSERT INTO auth_recovery_codes(user_id,code_hash) VALUES ($1,$2)`, uid, a.recoveryDigest(codes[i])); err != nil {
			return nil, err
		}
	}
	return codes, tx.Commit()
}

func (a *App) recoveryDigest(code string) string {
	normal := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(code))
	h := hmac.New(sha256.New, []byte(a.cfg.SecretKey))
	h.Write([]byte(normal))
	return hex.EncodeToString(h.Sum(nil))
}

func (a *App) recoveryUser(ctx context.Context, email string) (string, error) {
	var uid string
	if strings.TrimSpace(email) != "" {
		return uid, a.db.QueryRowContext(ctx, `SELECT id FROM users WHERE lower(email)=lower($1)`, strings.TrimSpace(email)).Scan(&uid)
	}
	return uid, a.db.QueryRowContext(ctx, `SELECT id FROM users ORDER BY created_at LIMIT 1`).Scan(&uid)
}

func (a *App) handleRecoveryLogin(w http.ResponseWriter, r *http.Request) {
	if !a.allowAuthAttempt(r) {
		writeProblem(w, 429, "Too Many Attempts", "wait five minutes before trying again")
		return
	}
	var req struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req) != nil {
		writeProblem(w, 400, "Bad Request", "enter a recovery code")
		return
	}
	uid, err := a.recoveryUser(r.Context(), req.Email)
	if err != nil {
		writeProblem(w, 401, "Recovery Failed", "that recovery code is not valid")
		return
	}
	result, err := a.db.ExecContext(r.Context(), `UPDATE auth_recovery_codes SET used_at=now()
		WHERE user_id=$1 AND code_hash=$2 AND used_at IS NULL`, uid, a.recoveryDigest(req.Code))
	if err != nil {
		writeProblem(w, 500, "Recovery Failed", err.Error())
		return
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		writeProblem(w, 401, "Recovery Failed", "that recovery code is not valid or was already used")
		return
	}
	if err := a.createSession(w, r, uid, loginMethodRecovery); err != nil {
		writeProblem(w, 500, "Session Failed", err.Error())
		return
	}
	a.clearAuthAttempts(r)
	writeJSON(w, map[string]any{"ok": true})
}

func totpCode(secret []byte, at time.Time) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(at.Unix()/30))
	h := hmac.New(sha1.New, secret)
	h.Write(counter[:])
	sum := h.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[off])&0x7f)<<24 | uint32(sum[off+1])<<16 | uint32(sum[off+2])<<8 | uint32(sum[off+3])
	return fmt.Sprintf("%06d", value%1_000_000)
}

func validTOTP(secret []byte, value string, now time.Time) bool {
	value = strings.TrimSpace(value)
	valid := 0
	for step := -1; step <= 1; step++ {
		valid |= subtle.ConstantTimeCompare([]byte(totpCode(secret, now.Add(time.Duration(step)*30*time.Second))), []byte(value))
	}
	return valid == 1
}

func (a *App) handleTOTPLogin(w http.ResponseWriter, r *http.Request) {
	if !a.allowAuthAttempt(r) {
		writeProblem(w, 429, "Too Many Attempts", "wait five minutes before trying again")
		return
	}
	var req struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req) != nil {
		writeProblem(w, 400, "Bad Request", "enter an authenticator code")
		return
	}
	uid, err := a.recoveryUser(r.Context(), req.Email)
	if err != nil {
		writeProblem(w, 401, "Sign In Failed", "invalid authenticator code")
		return
	}
	var sealed string
	err = a.db.QueryRowContext(r.Context(), `SELECT secret_ciphertext FROM auth_totp WHERE user_id=$1 AND enabled_at IS NOT NULL`, uid).Scan(&sealed)
	if err != nil {
		writeProblem(w, 401, "Sign In Failed", "invalid authenticator code")
		return
	}
	plain, err := openSecret(a.cfg, sealed)
	if err != nil {
		writeProblem(w, 500, "Sign In Failed", err.Error())
		return
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(plain)
	if err != nil || !validTOTP(secret, req.Code, time.Now()) {
		writeProblem(w, 401, "Sign In Failed", "invalid authenticator code")
		return
	}
	if err := a.createSession(w, r, uid, loginMethodTOTP); err != nil {
		writeProblem(w, 500, "Session Failed", err.Error())
		return
	}
	a.clearAuthAttempts(r)
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) handleSecurity(w http.ResponseWriter, r *http.Request) {
	uid, _ := a.userID(r.Context())
	rows, err := a.db.QueryContext(r.Context(), `SELECT id,name,created_at,last_used_at FROM auth_credentials WHERE user_id=$1 ORDER BY created_at`, uid)
	if err != nil {
		writeProblem(w, 500, "Security Failed", err.Error())
		return
	}
	defer rows.Close()
	passkeys := []map[string]any{}
	for rows.Next() {
		var id, name string
		var created time.Time
		var used sql.NullTime
		if err := rows.Scan(&id, &name, &created, &used); err != nil {
			writeProblem(w, 500, "Security Failed", err.Error())
			return
		}
		var last any
		if used.Valid {
			last = used.Time
		}
		passkeys = append(passkeys, map[string]any{"id": id, "name": name, "created_at": created, "last_used_at": last})
	}
	var totp bool
	_ = a.db.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM auth_totp WHERE user_id=$1 AND enabled_at IS NOT NULL)`, uid).Scan(&totp)
	var recovery int
	_ = a.db.QueryRowContext(r.Context(), `SELECT count(*) FROM auth_recovery_codes WHERE user_id=$1 AND used_at IS NULL`, uid).Scan(&recovery)
	var email string
	_ = a.db.QueryRowContext(r.Context(), `SELECT email FROM users WHERE id=$1`, uid).Scan(&email)
	writeJSON(w, map[string]any{"email": email, "passkeys": passkeys, "totp_enabled": totp, "recovery_codes_remaining": recovery})
}

func (a *App) handlePasskeyDelete(w http.ResponseWriter, r *http.Request) {
	uid, _ := a.userID(r.Context())
	var count int
	_ = a.db.QueryRowContext(r.Context(), `SELECT count(*) FROM auth_credentials WHERE user_id=$1`, uid).Scan(&count)
	if count <= 1 {
		writeProblem(w, 409, "Last Passkey", "add another passkey before removing this one")
		return
	}
	result, err := a.db.ExecContext(r.Context(), `DELETE FROM auth_credentials WHERE id=$1 AND user_id=$2`, r.PathValue("id"), uid)
	if err != nil {
		writeProblem(w, 500, "Delete Failed", err.Error())
		return
	}
	if n, _ := result.RowsAffected(); n != 1 {
		writeProblem(w, 404, "Not Found", "no such passkey")
		return
	}
	a.revokeOtherSessions(r, uid)
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) handleRecoveryRegenerate(w http.ResponseWriter, r *http.Request) {
	uid, _ := a.userID(r.Context())
	codes, err := a.replaceRecoveryCodes(r.Context(), uid)
	if err != nil {
		writeProblem(w, 500, "Recovery Failed", err.Error())
		return
	}
	a.revokeOtherSessions(r, uid)
	writeJSON(w, map[string]any{"recovery_codes": codes})
}

func (a *App) handleTOTPBegin(w http.ResponseWriter, r *http.Request) {
	uid, _ := a.userID(r.Context())
	var enabled bool
	_ = a.db.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM auth_totp WHERE user_id=$1 AND enabled_at IS NOT NULL)`, uid).Scan(&enabled)
	if enabled {
		writeProblem(w, 409, "TOTP Already Enabled", "disable the existing authenticator before replacing it")
		return
	}
	secret, err := randomBytes(20)
	if err != nil {
		writeProblem(w, 500, "TOTP Failed", err.Error())
		return
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
	sealed, err := sealSecret(a.cfg, encoded)
	if err != nil {
		writeProblem(w, 500, "TOTP Failed", err.Error())
		return
	}
	_, err = a.db.ExecContext(r.Context(), `INSERT INTO auth_totp(user_id,secret_ciphertext,enabled_at) VALUES ($1,$2,NULL)
		ON CONFLICT(user_id) DO UPDATE SET secret_ciphertext=excluded.secret_ciphertext,enabled_at=NULL`, uid, sealed)
	if err != nil {
		writeProblem(w, 500, "TOTP Failed", err.Error())
		return
	}
	var email string
	_ = a.db.QueryRowContext(r.Context(), `SELECT email FROM users WHERE id=$1`, uid).Scan(&email)
	uri := "otpauth://totp/" + url.PathEscape("email-soft:"+email) + "?secret=" + encoded + "&issuer=email-soft&algorithm=SHA1&digits=6&period=30"
	writeJSON(w, map[string]any{"secret": encoded, "uri": uri})
}

func (a *App) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	uid, _ := a.userID(r.Context())
	var req struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	var sealed string
	err := a.db.QueryRowContext(r.Context(), `SELECT secret_ciphertext FROM auth_totp WHERE user_id=$1`, uid).Scan(&sealed)
	if err != nil {
		writeProblem(w, 409, "TOTP Missing", "start setup again")
		return
	}
	plain, err := openSecret(a.cfg, sealed)
	if err != nil {
		writeProblem(w, 500, "TOTP Failed", err.Error())
		return
	}
	secret, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(plain)
	if !validTOTP(secret, req.Code, time.Now()) {
		writeProblem(w, 422, "Code Rejected", "the six-digit code did not match")
		return
	}
	_, err = a.db.ExecContext(r.Context(), `UPDATE auth_totp SET enabled_at=now() WHERE user_id=$1`, uid)
	if err != nil {
		writeProblem(w, 500, "TOTP Failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) handleTOTPDelete(w http.ResponseWriter, r *http.Request) {
	uid, _ := a.userID(r.Context())
	_, err := a.db.ExecContext(r.Context(), `DELETE FROM auth_totp WHERE user_id=$1`, uid)
	if err != nil {
		writeProblem(w, 500, "TOTP Failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) handleSessions(w http.ResponseWriter, r *http.Request) {
	uid, _ := a.userID(r.Context())
	if r.Method == http.MethodDelete {
		id := r.PathValue("id")
		current, _ := r.Context().Value(sessionContextKey{}).(string)
		result, err := a.db.ExecContext(r.Context(), `DELETE FROM auth_sessions WHERE id_hash=$1 AND user_id=$2`, id, uid)
		if err != nil {
			writeProblem(w, 500, "Delete Failed", err.Error())
			return
		}
		if n, _ := result.RowsAffected(); n != 1 {
			writeProblem(w, 404, "Not Found", "no such session")
			return
		}
		if id == current {
			a.clearCookie(w, sessionCookie)
		}
		writeJSON(w, map[string]any{"ok": true, "current": id == current})
		return
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT id_hash,created_at,last_seen_at,expires_at,user_agent FROM auth_sessions WHERE user_id=$1 AND expires_at>now() ORDER BY last_seen_at DESC`, uid)
	if err != nil {
		writeProblem(w, 500, "Sessions Failed", err.Error())
		return
	}
	defer rows.Close()
	current, _ := r.Context().Value(sessionContextKey{}).(string)
	out := []map[string]any{}
	for rows.Next() {
		var id, ua string
		var created, seen, expires time.Time
		if rows.Scan(&id, &created, &seen, &expires, &ua) != nil {
			continue
		}
		out = append(out, map[string]any{"id": id, "created_at": created, "last_seen_at": seen, "expires_at": expires, "user_agent": ua, "current": id == current})
	}
	writeJSON(w, out)
}

func (a *App) handleFullAccountDelete(w http.ResponseWriter, r *http.Request) {
	uid, _ := a.userID(r.Context())
	session, _ := r.Context().Value(sessionContextKey{}).(string)
	var recent bool
	if session != "" && session != "bootstrap" {
		_ = a.db.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM auth_sessions WHERE id_hash=$1 AND user_id=$2 AND created_at>now()-interval '10 minutes')`, session, uid).Scan(&recent)
	}
	if !recent {
		writeProblem(w, http.StatusPreconditionRequired, "Fresh Sign-In Required", "sign out and sign back in before deleting the account")
		return
	}
	var req struct {
		Confirmation string `json:"confirmation"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req) != nil {
		writeProblem(w, 400, "Confirmation Required", "enter the account email address")
		return
	}
	var email string
	if a.db.QueryRowContext(r.Context(), `SELECT email FROM users WHERE id=$1`, uid).Scan(&email) != nil || !strings.EqualFold(strings.TrimSpace(req.Confirmation), email) {
		writeProblem(w, 422, "Confirmation Mismatch", "type the account email address exactly")
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, 500, "Delete Failed", err.Error())
		return
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(r.Context(), `SELECT mirror_account_id FROM email_accounts WHERE user_id=$1`, uid)
	if err != nil {
		writeProblem(w, 500, "Delete Failed", err.Error())
		return
	}
	var mirrors []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			mirrors = append(mirrors, id)
		}
	}
	rows.Close()
	for _, mirror := range mirrors {
		queries := []string{
			`DELETE FROM mail_message_mailboxes WHERE account_id=$1`, `DELETE FROM mail_bodies WHERE account_id=$1`,
			`DELETE FROM mail_messages WHERE account_id=$1`, `DELETE FROM mail_mailboxes WHERE account_id=$1`,
			`DELETE FROM mail_sync_state WHERE account_id=$1`, `DELETE FROM mail_accounts WHERE id=$1`,
		}
		for _, q := range queries {
			if _, err = tx.ExecContext(r.Context(), q, mirror); err != nil {
				writeProblem(w, 500, "Delete Failed", err.Error())
				return
			}
		}
	}
	if _, err = tx.ExecContext(r.Context(), `DELETE FROM users WHERE id=$1`, uid); err != nil {
		writeProblem(w, 500, "Delete Failed", err.Error())
		return
	}
	if err = tx.Commit(); err != nil {
		writeProblem(w, 500, "Delete Failed", err.Error())
		return
	}
	a.clearCookie(w, sessionCookie)
	writeJSON(w, map[string]any{"deleted": true})
}

// ensureUser bootstraps an installation owner, but never an authentication
// secret. The setup token is still required to register the first passkey.
// email is a stable internal identifier (owners now supply a name; real
// addresses come from connected accounts); name becomes display_name, which
// is what the browser shows in the passkey prompt.
func (a *App) ensureUser(ctx context.Context, name string) error {
	if a.cfg.UserEmail == "" {
		return nil
	}
	handle, err := randomBytes(32)
	if err != nil {
		return err
	}
	_, err = a.db.ExecContext(ctx, `INSERT INTO users (email,webauthn_handle,display_name) VALUES ($1,$2,$3)
		ON CONFLICT (email) DO UPDATE SET webauthn_handle=COALESCE(users.webauthn_handle,excluded.webauthn_handle)`,
		a.cfg.UserEmail, handle, name)
	return err
}

// setOwnerName updates the display name of the single installation owner.
func (a *App) setOwnerName(ctx context.Context, name string) error {
	uid, err := a.firstUserID(ctx)
	if err != nil {
		return err
	}
	_, err = a.db.ExecContext(ctx, `UPDATE users SET display_name=$1 WHERE id=$2`, name, uid)
	return err
}

// ownerEmailFromName builds a stable internal identifier from the owner's
// name. It never appears in mail flows; whose-turn analysis reads the user's
// real addresses from connected accounts instead.
func ownerEmailFromName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0:
			b.WriteRune('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "owner"
	}
	if len(slug) > 40 {
		slug = slug[:40]
	}
	return slug + "@owner.local"
}

func (a *App) firstUserID(ctx context.Context) (string, error) {
	var id string
	err := a.db.QueryRowContext(ctx, `SELECT id FROM users ORDER BY created_at LIMIT 1`).Scan(&id)
	return id, err
}

// Handlers use the authenticated owner. Background jobs deliberately fall
// back to the installation's first user because they have no request context.
func (a *App) userID(ctx context.Context) (string, error) {
	if id, ok := ctx.Value(authContextKey{}).(string); ok && id != "" {
		return id, nil
	}
	return a.firstUserID(ctx)
}

func writeProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"title": title, "detail": detail})
}
