package main

// Password-primary authentication. Opaque hashed server-side sessions keep
// WebAuthn out of the request hot path. Passkeys, TOTP and one-use recovery
// codes are opt-in; recovery codes are an escape hatch, not a standing
// login factor.

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
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/neutron-build/neutron/mail"
)

const (
	sessionCookie    = "es_session"
	ceremonyCookie   = "es_ceremony"
	sessionLifetime  = 30 * 24 * time.Hour
	ceremonyLifetime = 5 * time.Minute
)

// How a session was created; the values are pinned by the auth_sessions
// login_method CHECK constraint in schema.sql. The dashboard nudges only
// recovery-code sessions toward adding a standing credential.
const (
	loginMethodPasskey   = "passkey"
	loginMethodPassword  = "password"
	loginMethodRecovery  = "recovery"
	loginMethodTOTP      = "totp"
	loginMethodBootstrap = "bootstrap"
)

// normalizeLoginMethod maps a stored login_method onto the constants above.
// Rows from before the column existed read as NULL; the nudge only targets
// fallback logins, so unknown values read as passkey.
func normalizeLoginMethod(method string) string {
	switch method {
	case loginMethodPassword, loginMethodRecovery, loginMethodTOTP, loginMethodBootstrap:
		return method
	default:
		return loginMethodPasskey
	}
}

type authContextKey struct{}
type sessionContextKey struct{}

type authAttempt struct {
	Window time.Time
	Count  int
}

// clientHost is the rate-limit identity for public auth endpoints.
//
// It deliberately ignores X-Forwarded-For and every other client-supplied
// forwarding header (audit AUTH-01): without an explicitly trusted proxy
// configuration, the first forwarded value is chosen by whoever is
// connecting, so a direct client could mint a fresh limiter bucket per
// request and defeat every host-keyed throttle. Fail closed instead —
// deployments behind an ingress share that one peer bucket, which the
// ingress's own rate limiting is responsible for complementing.
func clientHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return "invalid-peer"
	}
	return ip.Unmap().String()
}

// authAttemptCeil bounds the limiter table itself. Fresh keys beyond it are
// rejected rather than admitted: the sweep only removes expired entries, so
// a flood of distinct live peers must not grow the map without limit.
const authAttemptCeil = 1024

func (a *App) allowAuthAttempt(r *http.Request) bool {
	host := clientHost(r)
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
	attempt, seen := a.authAttempts[host]
	if !seen && len(a.authAttempts) >= authAttemptCeil {
		return false
	}
	if !seen || attempt.Window.IsZero() || now.Sub(attempt.Window) > 5*time.Minute {
		attempt = authAttempt{Window: now}
	}
	attempt.Count++
	a.authAttempts[host] = attempt
	return attempt.Count <= 10
}

func (a *App) clearAuthAttempts(r *http.Request) {
	host := clientHost(r)
	a.authMu.Lock()
	delete(a.authAttempts, host)
	a.authMu.Unlock()
}

type webUser struct {
	ID          string
	Handle      []byte
	Email       string
	DisplayName string
	// authEpoch snapshots users.auth_epoch atomically with this load's
	// credential read, so passkey login can reject a mint that raced a
	// credential change (AUTH-01).
	authEpoch   int64
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
		RPDisplayName: "Lull Mail",
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
		// Validation and the last-seen touch are separate concerns: an
		// authenticated read should not write the same session row on
		// every request (row contention and write amplification for a
		// busy browser), and a failed touch must not fabricate a logout.
		// The touch only fires when the row is more than a minute stale,
		// so a revoked session is still refused immediately by the SELECT
		// (audit 3 OPS-02).
		//
		// Both lookups require the session's auth epoch to equal the
		// owner's current epoch: a credential change advances the epoch
		// in its own transaction, so any session minted from a
		// pre-change verification stops authenticating the moment the
		// change commits — including one whose INSERT raced the change's
		// revocation DELETE (audit AUTH-05 remainder / AUTH-01).
		err := a.db.QueryRowContext(r.Context(), `
			UPDATE auth_sessions SET last_seen_at = now()
			WHERE id_hash = $1 AND expires_at > now() AND last_seen_at < now() - interval '1 minute'
			  AND auth_epoch = (SELECT auth_epoch FROM users WHERE users.id = auth_sessions.user_id)
			RETURNING user_id`, hash).Scan(&uid)
		if err == nil {
			return uid, hash, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", "", err
		}
		err = a.db.QueryRowContext(r.Context(), `
			SELECT auth_sessions.user_id FROM auth_sessions
			WHERE id_hash = $1 AND expires_at > now()
			  AND auth_epoch = (SELECT auth_epoch FROM users WHERE users.id = auth_sessions.user_id)`, hash).Scan(&uid)
		if err == nil {
			return uid, hash, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", "", err
		}
	}

	// LULL_TOKEN is an installation/bootstrap secret, not a permanent
	// parallel login. It stops opening product data after the first
	// credential (passkey or password), and a generated token additionally
	// expires 24h after it was minted.
	configured, err := a.ownerConfigured(r.Context())
	if err != nil {
		return "", "", err
	}
	if !configured && a.setupTokenValid() {
		got := r.Header.Get("Authorization")
		if constantTimeBearer(got, a.cfg.APIToken) {
			uid, err := a.firstUserID(r.Context())
			return uid, "bootstrap", err
		}
	}
	return "", "", sql.ErrNoRows
}

// originAllowed compares a request Origin against the pinned PUBLIC_URL.
// Loopback variants are equivalent in the browser's eyes only as names —
// http://localhost and http://127.0.0.1 are distinct origins — but a dev
// install reached at both is the same person both times, so they are
// accepted interchangeably (same scheme and port required).
func originAllowed(origin, pinned string) bool {
	if origin == pinned {
		return true
	}
	o, err1 := url.Parse(origin)
	p, err2 := url.Parse(pinned)
	if err1 != nil || err2 != nil {
		return false
	}
	if o.Scheme != p.Scheme || o.Port() != p.Port() {
		return false
	}
	lo, lp := isLoopbackHost(o.Hostname()), isLoopbackHost(p.Hostname())
	return lo && lp
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
			if origin := r.Header.Get("Origin"); origin != "" && !originAllowed(origin, a.cfg.PublicURL) {
				writeProblem(w, http.StatusForbidden, "Origin Rejected",
					"this install is pinned to "+a.cfg.PublicURL+" — you are browsing from "+origin)
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
	mux.HandleFunc("POST /auth/bootstrap/password", a.handleBootstrapPassword)
	mux.HandleFunc("POST /auth/login/begin", a.handleLoginBegin)
	mux.HandleFunc("POST /auth/login/finish", a.handleLoginFinish)
	mux.HandleFunc("POST /auth/password", a.handlePasswordLogin)
	mux.HandleFunc("POST /auth/recovery", a.handleRecoveryLogin)
	mux.HandleFunc("POST /auth/totp", a.handleTOTPLogin)
	mux.HandleFunc("POST /auth/logout", a.handleLogout)
}

// publicExposure reports whether the pinned browser origin is reachable from
// outside private space. Loopback, LAN (RFC 1918), and a Tailnet (CGNAT
// 100.64/10 addresses or *.ts.net names) read as private; everything else —
// public DNS names included — reads as exposed. The dashboard turns this into
// a warning; nothing anywhere enforces on it.
func publicExposure(publicURL string) bool {
	if publicURL == "" {
		return false
	}
	u, err := url.Parse(publicURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || strings.HasSuffix(host, ".ts.net") {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return true // a name that is not loopback or Tailnet: assume public
	}
	if ip.IsLoopback() || ip.IsPrivate() || isCGNAT(ip) {
		return false
	}
	return true
}

func isCGNAT(ip net.IP) bool {
	b := ip.To4()
	return b != nil && b[0] == 100 && b[1] >= 64 && b[1] <= 127
}

func (a *App) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	configured, err := a.ownerConfigured(r.Context())
	if err != nil {
		writeProblem(w, 500, "Status Failed", err.Error())
		return
	}
	uid, session, err := a.authenticateRequest(r)
	// A database failure is not a sign-out: answering 200 with
	// authenticated=false here flips a healthy browser session to the
	// login gate over a transient outage (audit 3 AUTH-05). Retryable 503
	// instead; the dashboard keeps its last known state.
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		a.log.Error("auth status lookup failed", "err", err)
		writeProblem(w, http.StatusServiceUnavailable, "Status Unavailable",
			"sign-in status could not be checked — try again shortly")
		return
	}
	authenticated := err == nil
	email := ""
	via := ""
	if authenticated {
		if err := a.db.QueryRowContext(r.Context(), `SELECT email FROM users WHERE id=$1`, uid).Scan(&email); err != nil && !errors.Is(err, sql.ErrNoRows) {
			a.log.Error("auth status owner lookup failed", "err", err)
			writeProblem(w, http.StatusServiceUnavailable, "Status Unavailable",
				"sign-in status could not be checked — try again shortly")
			return
		}
		via = a.sessionLoginMethod(r.Context(), session)
	}
	status := map[string]any{
		"configured": configured, "authenticated": authenticated, "email": email,
		"bootstrap_available": !configured && a.setupTokenValid(),
		"passkey_supported":   true,
		// Warn-only exposure signal for the dashboard (D10). Classified here,
		// once per boot, against the pinned origin — never enforced.
		"exposed": publicExposure(a.cfg.PublicURL),
	}
	if via != "" {
		status["via"] = via
	}
	if !configured && a.cfg.RPID == "" {
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

// ownerConfigured is true once any sign-in credential exists — a passkey,
// a password, or enabled TOTP. First-run setup and the bootstrap token
// both key off this, not passkeys alone, so a password-only install is
// a finished install.
func (a *App) ownerConfigured(ctx context.Context) (bool, error) {
	return ownerConfiguredDB(ctx, a.db)
}

// ownerConfiguredDB is ownerConfigured against a transaction or pool, so
// first-run completion can re-check under the user lock it already holds.
// Two ceremonies that both passed token authorization must not both
// install credentials (audit AUTH-07).
func ownerConfiguredDB(ctx context.Context, q queryRower) (bool, error) {
	var ok bool
	err := q.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM auth_credentials)
		    OR EXISTS(SELECT 1 FROM auth_passwords)
		    OR EXISTS(SELECT 1 FROM auth_totp WHERE enabled_at IS NOT NULL)`).Scan(&ok)
	return ok, err
}

func (a *App) handleBootstrapBegin(w http.ResponseWriter, r *http.Request) {
	if !a.bootstrapAuthorized(r) {
		writeProblem(w, 401, "Unauthorized", "the one-time setup token is required")
		return
	}
	if configured, err := a.ownerConfigured(r.Context()); err != nil || configured {
		writeProblem(w, 409, "Already Configured", "this installation already has a sign-in method")
		return
	}
	// No origin pinned yet (PUBLIC_URL unset, fresh install): adopt the one
	// this setup request actually arrived on. Nothing is secret before the
	// first credential exists; the completing request's origin gets stored.
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
			writeProblem(w, 422, "Name Missing", "enter your name so Lull Mail knows whose mail this is")
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

// bootstrapAdvisoryKey serializes first-run completion installation-wide.
// The per-user row lock cannot do this: two concurrent ceremonies with
// different names create DIFFERENT owner rows and lock different rows, so
// the global ownerConfiguredDB recheck alone cannot stop both from
// installing a first credential (audit 3 AUTH-03). A transaction-scoped
// advisory lock is released at commit/rollback, so a crashed ceremony
// never wedges setup. 0x6C756C6C is "lull" in ASCII.
const bootstrapAdvisoryKey = 0x6C756C6C

func lockBootstrapInstallation(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, bootstrapAdvisoryKey)
	return err
}

func (a *App) handleBootstrapFinish(w http.ResponseWriter, r *http.Request) {
	if !a.bootstrapAuthorized(r) {
		writeProblem(w, 401, "Unauthorized", "the one-time setup token is required")
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, 500, "Setup Failed", err.Error())
		return
	}
	defer tx.Rollback()
	// Installation-wide serialization BEFORE the configured check: two
	// in-flight ceremonies must not both observe an unconfigured install
	// (audit 3 AUTH-03).
	if err := lockBootstrapInstallation(r.Context(), tx); err != nil {
		writeProblem(w, 500, "Setup Failed", err.Error())
		return
	}
	uid, err := a.finishRegistration(w, r, "bootstrap", tx)
	if err != nil {
		return
	}
	// The first credential, detected origin, and recovery path become visible
	// together. A failed setup therefore remains a bootstrap-able installation.
	if !a.cfg.PublicURLSet && a.cfg.RPID != "" {
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO app_settings (key,value) VALUES ('public_url',$1)
			ON CONFLICT (key) DO UPDATE SET value=excluded.value`, a.cfg.PublicURL); err != nil {
			writeProblem(w, 500, "Setup Failed", "could not persist the site origin: "+err.Error())
			return
		}
	}
	codes, err := a.replaceRecoveryCodesTx(r.Context(), tx, uid)
	if err != nil {
		writeProblem(w, 500, "Recovery Setup Failed", err.Error())
		return
	}
	// The mint runs inside the installation-wide lock taken above, so the
	// epoch read here is current by construction.
	epoch, err := lockAuthUser(r.Context(), tx, uid)
	if err != nil {
		writeProblem(w, 500, "Session Failed", err.Error())
		return
	}
	rawSession, err := a.persistSession(r.Context(), tx, r, uid, loginMethodBootstrap, epoch)
	if err != nil {
		writeProblem(w, 500, "Session Failed", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		writeProblem(w, 500, "Setup Failed", err.Error())
		return
	}
	a.setCookie(w, sessionCookie, rawSession, sessionLifetime)
	a.retireSetupToken()
	writeJSON(w, map[string]any{"ok": true, "recovery_codes": codes})
}

// handleBootstrapPassword is first-run setup without WebAuthn: name +
// password, recovery codes, session. The setup token still gates it. A
// passkey can be added later from Security; it is no longer required to
// finish installing.
func (a *App) handleBootstrapPassword(w http.ResponseWriter, r *http.Request) {
	if !a.bootstrapAuthorized(r) {
		writeProblem(w, 401, "Unauthorized", "the one-time setup token is required")
		return
	}
	if configured, err := a.ownerConfigured(r.Context()); err != nil || configured {
		writeProblem(w, 409, "Already Configured", "this installation already has a sign-in method")
		return
	}
	if a.cfg.RPID == "" {
		if !a.setOriginForSetup(detectOrigin(r)) {
			writeProblem(w, 422, "Origin Unavailable", "the browser origin could not be detected — set PUBLIC_URL on the server")
			return
		}
	}
	var req struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req) != nil {
		writeProblem(w, 400, "Bad Request", "enter your name and a password")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeProblem(w, 422, "Name Missing", "enter your name so Lull Mail knows whose mail this is")
		return
	}
	if !validPasswordLength(req.Password) {
		writeProblem(w, 422, "Password Too Short", "use at least 8 characters")
		return
	}
	uid, err := a.firstUserID(r.Context())
	if errors.Is(err, sql.ErrNoRows) {
		a.cfg.UserEmail = ownerEmailFromName(name)
		if err := a.ensureUser(r.Context(), name); err != nil {
			writeProblem(w, 500, "Setup Failed", err.Error())
			return
		}
		uid, err = a.firstUserID(r.Context())
	} else if err == nil {
		_ = a.setOwnerName(r.Context(), name)
	}
	if err != nil {
		writeProblem(w, 500, "Setup Failed", err.Error())
		return
	}
	hashRelease, busyErr := acquirePasswordWork()
	if busyErr != nil {
		writeAuthBusy(w)
		return
	}
	hash, err := hashPassword(req.Password)
	hashRelease()
	if err != nil {
		writeProblem(w, 500, "Setup Failed", err.Error())
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, 500, "Setup Failed", err.Error())
		return
	}
	defer tx.Rollback()
	// Same installation-wide serialization as the passkey finish path: a
	// password ceremony and a passkey ceremony racing must not both
	// install a first credential (audit 3 AUTH-03).
	if err := lockBootstrapInstallation(r.Context(), tx); err != nil {
		writeProblem(w, 500, "Setup Failed", err.Error())
		return
	}
	epoch, err := lockAuthUser(r.Context(), tx, uid)
	if err != nil {
		writeProblem(w, 500, "Setup Failed", err.Error())
		return
	}
	// The pre-transaction check raced any competing ceremony; the final
	// word is under the lock, in the same transaction as the insert
	// (audit AUTH-07).
	if configured, err := ownerConfiguredDB(r.Context(), tx); err != nil || configured {
		writeProblem(w, 409, "Already Configured", "this installation already finished setup")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO auth_passwords (user_id,hash,updated_at) VALUES ($1,$2,now())`, uid, hash); err != nil {
		writeProblem(w, 500, "Setup Failed", err.Error())
		return
	}
	if !a.cfg.PublicURLSet && a.cfg.RPID != "" {
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO app_settings (key,value) VALUES ('public_url',$1)
			ON CONFLICT (key) DO UPDATE SET value=excluded.value`, a.cfg.PublicURL); err != nil {
			writeProblem(w, 500, "Setup Failed", "could not persist the site origin: "+err.Error())
			return
		}
	}
	codes, err := a.replaceRecoveryCodesTx(r.Context(), tx, uid)
	if err != nil {
		writeProblem(w, 500, "Recovery Setup Failed", err.Error())
		return
	}
	// First credential: the epoch is current by construction under the
	// installation-wide lock.
	rawSession, err := a.persistSession(r.Context(), tx, r, uid, loginMethodPassword, epoch)
	if err != nil {
		writeProblem(w, 500, "Session Failed", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		writeProblem(w, 500, "Setup Failed", err.Error())
		return
	}
	a.setCookie(w, sessionCookie, rawSession, sessionLifetime)
	a.retireSetupToken()
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
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, 500, "Passkey Failed", err.Error())
		return
	}
	defer tx.Rollback()
	uid, err := a.finishRegistration(w, r, "register", tx)
	if err != nil {
		return
	}
	if err := tx.Commit(); err != nil {
		writeProblem(w, 500, "Passkey Failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "user_id": uid})
}

func (a *App) finishRegistration(w http.ResponseWriter, r *http.Request, kind string, tx *sql.Tx) (string, error) {
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
	name = utf8Prefix(name, 80)
	id := base64.RawURLEncoding.EncodeToString(credential.ID)
	if _, err := lockAuthUser(r.Context(), tx, uid); err != nil {
		writeProblem(w, 409, "Passkey Failed", "the account is being deleted")
		return "", err
	}
	// First-run completion re-checks installation state under the lock it
	// just took, BEFORE inserting: two bootstrap ceremonies that both held
	// tokens must not both install a first credential (audit AUTH-07).
	if kind == "bootstrap" {
		if configured, err := ownerConfiguredDB(r.Context(), tx); err != nil || configured {
			writeProblem(w, 409, "Already Configured", "this installation already finished setup")
			return "", sql.ErrNoRows
		}
	}
	_, err = tx.ExecContext(r.Context(), `INSERT INTO auth_credentials
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
		if configured, _ := a.ownerConfigured(r.Context()); configured {
			writeProblem(w, 409, "No Passkey", "this account has no passkey — sign in with a password")
		} else {
			writeProblem(w, 409, "Setup Required", "create the first password or passkey with the setup token")
		}
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
	userAny, credential, err := a.webAuthn().FinishPasskeyLogin(func(rawID, handle []byte) (webauthn.User, error) {
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
	// user.authEpoch was read atomically with the credentials the
	// assertion was verified against, so the mint's epoch re-check under
	// the users row lock rejects a session if any credential changed in
	// between (AUTH-01: passkey verification and session creation are
	// separate steps).
	if err := a.createSession(w, r, user.ID, loginMethodPasskey, user.authEpoch); err != nil {
		if errors.Is(err, errAuthEpochAdvanced) {
			writeProblem(w, 401, "Sign In Failed", "credentials changed during sign-in — try again")
			return
		}
		writeProblem(w, 500, "Session Failed", err.Error())
		return
	}
	a.clearAuthAttempts(r)
	a.clearCookie(w, ceremonyCookie)
	writeJSON(w, map[string]any{"ok": true, "email": user.Email})
}

// handlePasswordLogin is the default face of sign-in: email + password for a
// session. Every failure that is not a lockout answers with the SAME 401
// problem — unknown email, no password set, wrong password — so the endpoint
// cannot be used to enumerate accounts. Five failures lock the account for
// fifteen minutes (password.go), answered as 429 with Retry-After.
func (a *App) handlePasswordLogin(w http.ResponseWriter, r *http.Request) {
	if !a.allowAuthAttempt(r) {
		writeProblem(w, 429, "Too Many Attempts", "wait five minutes before trying again")
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req) != nil ||
		strings.TrimSpace(req.Email) == "" || req.Password == "" {
		writeProblem(w, 400, "Bad Request", "enter your email and password")
		return
	}
	// Lock keys are resolved, not typed: "uid:<id>" once the ident resolves,
	// the shared unknown-user bucket otherwise. Aliases of one account (email
	// vs display name) share a single allowance, and unknown idents cannot
	// grow attempt state without bound (audit AUTH-02).
	lockKey := unknownPasswordLockKey
	uid, err := a.recoveryUser(r.Context(), req.Email)
	if err == nil {
		lockKey = "uid:" + uid
	}
	if remaining := a.passwordLockRemaining(lockKey); remaining > 0 {
		writePasswordLocked(w, remaining)
		return
	}
	// Same 401 for unknown ident, no password, and wrong password. Misses
	// still run argon2 against a dummy hash so the wall-clock cost matches
	// a real verify.
	rejected := func() {
		burnPasswordVerify(req.Password)
		a.recordPasswordFailure(lockKey)
		writeProblem(w, 401, "Sign In Failed", "invalid email or password")
	}
	if err != nil {
		rejected()
		return
	}
	// The hash and the owner's auth epoch are read as one statement: this
	// is the verification snapshot. The mint re-checks the epoch under the
	// users row lock, so a password change that commits between verify and
	// session insert cannot leave a usable session behind (AUTH-05
	// remainder / AUTH-01).
	var encoded string
	var verifyEpoch int64
	err = a.db.QueryRowContext(r.Context(),
		`SELECT p.hash, u.auth_epoch FROM auth_passwords p JOIN users u ON u.id=p.user_id WHERE p.user_id=$1`, uid).
		Scan(&encoded, &verifyEpoch)
	if err == sql.ErrNoRows {
		rejected()
		return
	}
	if err != nil {
		writeProblem(w, 500, "Sign In Failed", err.Error())
		return
	}
	release, err := acquirePasswordWork()
	if err != nil {
		writeAuthBusy(w)
		return
	}
	ok, verr := verifyPassword(encoded, req.Password)
	release()
	if verr != nil {
		writeProblem(w, 500, "Sign In Failed", verr.Error())
		return
	}
	if !ok {
		a.recordPasswordFailure(lockKey)
		writeProblem(w, 401, "Sign In Failed", "invalid email or password")
		return
	}
	a.clearPasswordFailures(lockKey)
	if err := a.createSession(w, r, uid, loginMethodPassword, verifyEpoch); err != nil {
		if errors.Is(err, errAuthEpochAdvanced) {
			writeProblem(w, 401, "Sign In Failed", "credentials changed during sign-in — try again")
			return
		}
		writeProblem(w, 500, "Session Failed", err.Error())
		return
	}
	a.clearAuthAttempts(r)
	writeJSON(w, map[string]any{"ok": true})
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

func (a *App) createSession(w http.ResponseWriter, r *http.Request, uid, method string, verifyEpoch int64) error {
	raw, err := a.mintSession(r.Context(), r, uid, method, verifyEpoch)
	if err == nil {
		a.setCookie(w, sessionCookie, raw, sessionLifetime)
	}
	return err
}

// errAuthEpochAdvanced marks a sign-in whose credential verification
// predates a committed credential change: the session is refused rather
// than minted against stale proof (audit AUTH-05 remainder / AUTH-01).
var errAuthEpochAdvanced = errors.New("credentials changed during sign-in")

// mintSession inserts a session only when the owner's auth epoch still
// matches the epoch observed when the credential was verified. The users
// row lock serializes minting with credential changes (which take the same
// lock before advancing the epoch), so a verification that predates a
// change cannot produce a usable session even if it raced the change's
// revocation DELETE mid-flight.
func (a *App) mintSession(ctx context.Context, r *http.Request, uid, method string, verifyEpoch int64) (string, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	epoch, err := lockAuthUser(ctx, tx, uid)
	if err != nil {
		return "", err
	}
	if epoch != verifyEpoch {
		return "", errAuthEpochAdvanced
	}
	raw, err := a.persistSession(ctx, tx, r, uid, method, epoch)
	if err != nil {
		return "", err
	}
	return raw, tx.Commit()
}

type sqlExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (a *App) persistSession(ctx context.Context, db sqlExecer, r *http.Request, uid, method string, epoch int64) (string, error) {
	raw, err := opaqueToken(32)
	if err != nil {
		return "", err
	}
	ua := utf8Prefix(r.UserAgent(), 300)
	_, err = db.ExecContext(ctx, `INSERT INTO auth_sessions
		(id_hash,user_id,expires_at,user_agent,login_method,auth_epoch) VALUES ($1,$2,$3,$4,$5,$6)`,
		tokenHash(raw), uid, time.Now().Add(sessionLifetime), ua, normalizeLoginMethod(method), epoch)
	if err != nil {
		return "", err
	}
	return raw, nil
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
	err := a.db.QueryRowContext(ctx, `SELECT id,webauthn_handle,email,display_name,auth_epoch FROM users WHERE id=$1`, uid).
		Scan(&u.ID, &u.Handle, &u.Email, &u.DisplayName, &u.authEpoch)
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

// revokeOtherSessionsTx signs out every session except the one making the
// request, inside the caller's credential-change transaction. Credential
// events (passkey removed, password replaced, recovery codes regenerated)
// are the owner saying "something may be compromised" — stale sessions must
// not outlive that, and a revocation that cannot be confirmed must fail the
// change rather than silently leave sessions usable (audit AUTH-05).
//
// The epoch advance is what closes the interleaving the in-transaction
// DELETE could not (AUTH-05 remainder / AUTH-01): a login that verified an
// old credential and is paused before its session INSERT would land that
// row after this transaction committed, surviving the DELETE. Advancing
// users.auth_epoch makes the changed-session-lookup join reject that
// session anyway — no mint from a pre-change verification stays usable.
// The changing request's own session is re-stamped onto the new epoch so
// the owner stays signed in on the device making the change.
func revokeOtherSessionsTx(ctx context.Context, tx *sql.Tx, r *http.Request, uid string) error {
	var epoch int64
	if err := tx.QueryRowContext(ctx,
		`UPDATE users SET auth_epoch = auth_epoch + 1 WHERE id=$1 RETURNING auth_epoch`, uid,
	).Scan(&epoch); err != nil {
		return err
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		// No current session to preserve (bootstrap-token request):
		// everything goes, and the epoch advance covers stragglers.
		_, err := tx.ExecContext(ctx, `DELETE FROM auth_sessions WHERE user_id=$1`, uid)
		return err
	}
	current := tokenHash(cookie.Value)
	if _, err := tx.ExecContext(ctx,
		`UPDATE auth_sessions SET auth_epoch=$1 WHERE user_id=$2 AND id_hash=$3`, epoch, uid, current); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`DELETE FROM auth_sessions WHERE user_id=$1 AND id_hash<>$2`, uid, current)
	return err
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Revocation failure must leave the cookie in place: clearing it here
	// would destroy the only token this browser can retry revocation with,
	// while another copy of the session stays valid server-side. The user
	// is told sign-out failed and can try again (audit 4 F13).
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		if _, err := a.db.ExecContext(r.Context(), `DELETE FROM auth_sessions WHERE id_hash=$1`, tokenHash(cookie.Value)); err != nil {
			a.log.Error("logout revocation failed", "err", err)
			w.Header().Set("Retry-After", "2")
			writeProblem(w, http.StatusServiceUnavailable, "Logout Failed",
				"the session could not be revoked — you are not signed out; try again")
			return
		}
	}
	a.clearCookie(w, sessionCookie)
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) replaceRecoveryCodes(ctx context.Context, r *http.Request, uid string) ([]string, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := lockAuthUser(ctx, tx, uid); err != nil {
		return nil, err
	}
	codes, err := a.replaceRecoveryCodesTx(ctx, tx, uid)
	if err != nil {
		return nil, err
	}
	// Fresh recovery codes retire the old ones; other sessions do not
	// survive the rotation either (audit AUTH-05).
	if err := revokeOtherSessionsTx(ctx, tx, r, uid); err != nil {
		return nil, err
	}
	return codes, tx.Commit()
}

func (a *App) replaceRecoveryCodesTx(ctx context.Context, tx *sql.Tx, uid string) ([]string, error) {
	codes := make([]string, 10)
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
	return codes, nil
}

func (a *App) recoveryDigest(code string) string {
	normal := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(code))
	h := hmac.New(sha256.New, []byte(a.cfg.SecretKey))
	h.Write([]byte(normal))
	return hex.EncodeToString(h.Sum(nil))
}

func (a *App) recoveryUser(ctx context.Context, email string) (string, error) {
	var uid string
	if ident := strings.TrimSpace(email); ident != "" {
		return uid, a.db.QueryRowContext(ctx, `
			SELECT id FROM users WHERE lower(email)=lower($1) OR lower(display_name)=lower($1)
			ORDER BY created_at LIMIT 1`, ident).Scan(&uid)
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
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, 500, "Recovery Failed", err.Error())
		return
	}
	defer tx.Rollback()
	epoch, err := lockAuthUser(r.Context(), tx, uid)
	if err != nil {
		writeProblem(w, 401, "Recovery Failed", "that recovery code is not valid")
		return
	}
	result, err := tx.ExecContext(r.Context(), `UPDATE auth_recovery_codes SET used_at=now()
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
	rawSession, err := a.persistSession(r.Context(), tx, r, uid, loginMethodRecovery, epoch)
	if err != nil {
		writeProblem(w, 500, "Session Failed", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		writeProblem(w, 500, "Session Failed", err.Error())
		return
	}
	a.setCookie(w, sessionCookie, rawSession, sessionLifetime)
	a.clearAuthAttempts(r)
	writeJSON(w, map[string]any{"ok": true})
}

func totpCodeAt(secret []byte, step int64) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	h := hmac.New(sha1.New, secret)
	h.Write(counter[:])
	sum := h.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[off])&0x7f)<<24 | uint32(sum[off+1])<<16 | uint32(sum[off+2])<<8 | uint32(sum[off+3])
	return fmt.Sprintf("%06d", value%1_000_000)
}

func totpCode(secret []byte, at time.Time) string {
	return totpCodeAt(secret, at.Unix()/30)
}

// totpMatchedStep returns the highest time step whose code matches within
// the ±1 window, or -1. Returning the step (not just a boolean) is what
// lets login consume the accepted code exactly once (audit AUTH-03).
func totpMatchedStep(secret []byte, value string, now time.Time) int64 {
	value = strings.TrimSpace(value)
	best := int64(-1)
	nowStep := now.Unix() / 30
	for _, delta := range []int64{-1, 0, 1} {
		step := nowStep + delta
		if subtle.ConstantTimeCompare([]byte(totpCodeAt(secret, step)), []byte(value)) == 1 && step > best {
			best = step
		}
	}
	return best
}

func validTOTP(secret []byte, value string, now time.Time) bool {
	return totpMatchedStep(secret, value, now) >= 0
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
	// The secret and the owner's auth epoch are read as one statement:
	// the verification snapshot. AUTH-01's finding was that this read sat
	// before the step-claim lock with nothing connecting the two — a
	// TOTP change committing in between still left the claim+mint able to
	// session-ize a code from the retired secret. The epoch re-check
	// under the claim lock closes that.
	var sealed string
	var verifyEpoch int64
	err = a.db.QueryRowContext(r.Context(),
		`SELECT t.secret_ciphertext, u.auth_epoch FROM auth_totp t JOIN users u ON u.id=t.user_id
		 WHERE t.user_id=$1 AND t.enabled_at IS NOT NULL`, uid).Scan(&sealed, &verifyEpoch)
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
	if err != nil {
		writeProblem(w, 500, "Sign In Failed", "stored authenticator secret is unreadable")
		return
	}
	step := totpMatchedStep(secret, req.Code, time.Now())
	if step < 0 {
		writeProblem(w, 401, "Sign In Failed", "invalid authenticator code")
		return
	}
	// Consume the accepted step atomically with session creation: the
	// UPDATE claims it (last_used_step advances monotonically) and returns
	// no row for a replay or a disabled factor, so no session is minted for
	// a code that already signed someone in (audit AUTH-03).
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, 500, "Sign In Failed", err.Error())
		return
	}
	defer tx.Rollback()
	epoch, err := lockAuthUser(r.Context(), tx, uid)
	if err != nil {
		writeProblem(w, 500, "Sign In Failed", err.Error())
		return
	}
	if epoch != verifyEpoch {
		writeProblem(w, 401, "Sign In Failed", "credentials changed during sign-in — try again")
		return
	}
	var claimed string
	err = tx.QueryRowContext(r.Context(), `
		UPDATE auth_totp SET last_used_step = $2
		WHERE user_id = $1 AND enabled_at IS NOT NULL AND last_used_step < $2
		RETURNING user_id`, uid, step).Scan(&claimed)
	if err != nil {
		writeProblem(w, 401, "Sign In Failed", "invalid authenticator code")
		return
	}
	rawSession, err := a.persistSession(r.Context(), tx, r, uid, loginMethodTOTP, epoch)
	if err != nil {
		writeProblem(w, 500, "Session Failed", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		writeProblem(w, 500, "Session Failed", err.Error())
		return
	}
	a.setCookie(w, sessionCookie, rawSession, sessionLifetime)
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
	if err := rows.Err(); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
	}
	// Each field is a real lookup, not a default: swallowing errors here
	// reports a healthy password/TOTP/recovery set as absent or zero
	// (audit 3 AUTH-05).
	var totp bool
	if err := a.db.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM auth_totp WHERE user_id=$1 AND enabled_at IS NOT NULL)`, uid).Scan(&totp); err != nil {
		a.log.Error("security status (totp) failed", "err", err)
		writeProblem(w, http.StatusServiceUnavailable, "Security Unavailable", "could not read security settings — try again shortly")
		return
	}
	var passwordSet bool
	if err := a.db.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM auth_passwords WHERE user_id=$1)`, uid).Scan(&passwordSet); err != nil {
		a.log.Error("security status (password) failed", "err", err)
		writeProblem(w, http.StatusServiceUnavailable, "Security Unavailable", "could not read security settings — try again shortly")
		return
	}
	var recovery int
	if err := a.db.QueryRowContext(r.Context(), `SELECT count(*) FROM auth_recovery_codes WHERE user_id=$1 AND used_at IS NULL`, uid).Scan(&recovery); err != nil {
		a.log.Error("security status (recovery) failed", "err", err)
		writeProblem(w, http.StatusServiceUnavailable, "Security Unavailable", "could not read security settings — try again shortly")
		return
	}
	var email string
	if err := a.db.QueryRowContext(r.Context(), `SELECT email FROM users WHERE id=$1`, uid).Scan(&email); err != nil {
		a.log.Error("security status (email) failed", "err", err)
		writeProblem(w, http.StatusServiceUnavailable, "Security Unavailable", "could not read security settings — try again shortly")
		return
	}
	writeJSON(w, map[string]any{"email": email, "passkeys": passkeys, "totp_enabled": totp, "password_set": passwordSet, "recovery_codes_remaining": recovery})
}

// Auth-material mutations lock the owner row first. PostgreSQL holds this
// lock through commit, serializing passkey changes with full account
// deletion. The lock also returns the row's auth epoch, so session minting
// can compare it against the epoch observed at credential verification
// (audit AUTH-05 remainder / AUTH-01).
func lockAuthUser(ctx context.Context, tx *sql.Tx, uid string) (int64, error) {
	var epoch int64
	err := tx.QueryRowContext(ctx, `SELECT auth_epoch FROM users WHERE id=$1 FOR UPDATE`, uid).Scan(&epoch)
	return epoch, err
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// loginFactors are the standing ways in. Recovery codes are omitted on
// purpose — D10: they never count as a login credential.
type loginFactors struct {
	Passkeys int
	Password bool
	TOTP     bool
}

func loadLoginFactors(ctx context.Context, q queryRower, uid string) (loginFactors, error) {
	var f loginFactors
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM auth_credentials WHERE user_id=$1`, uid).Scan(&f.Passkeys); err != nil {
		return f, err
	}
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM auth_passwords WHERE user_id=$1)`, uid).Scan(&f.Password); err != nil {
		return f, err
	}
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM auth_totp WHERE user_id=$1 AND enabled_at IS NOT NULL)`, uid).Scan(&f.TOTP)
	return f, err
}

// lastFactor reports whether removing this kind would leave the account
// with no standing sign-in method.
func lastFactor(f loginFactors, removing string) bool {
	switch removing {
	case "password":
		return f.Passkeys == 0 && !f.TOTP
	case "totp":
		return f.Passkeys == 0 && !f.Password
	case "passkey":
		return f.Passkeys <= 1 && !f.Password && !f.TOTP
	default:
		return false
	}
}

func writePasswordLocked(w http.ResponseWriter, remaining time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(remaining.Seconds())+1))
	writeProblem(w, 429, "Too Many Attempts",
		"too many failed attempts — try again in "+(time.Duration(int64(remaining.Seconds())+1)*time.Second).String())
}

// unknownPasswordLockKey is the shared failure bucket for identifiers that
// resolve to no account: bounded storage, identical lock responses.
const unknownPasswordLockKey = "unknown-user"

// writeAuthBusy answers a KDF admission rejection: retryable, and distinct
// from a wrong-password result.
func writeAuthBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	writeProblem(w, 429, "Busy", "too many sign-ins at once — try again in a moment")
}

func (a *App) handlePasskeyDelete(w http.ResponseWriter, r *http.Request) {
	uid, _ := a.userID(r.Context())
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, 500, "Delete Failed", err.Error())
		return
	}
	defer tx.Rollback()
	if _, err := lockAuthUser(r.Context(), tx, uid); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, 404, "Not Found", "no such account")
		} else {
			writeProblem(w, 500, "Delete Failed", err.Error())
		}
		return
	}
	factors, err := loadLoginFactors(r.Context(), tx, uid)
	if err != nil {
		writeProblem(w, 500, "Delete Failed", err.Error())
		return
	}
	if lastFactor(factors, "passkey") {
		writeProblem(w, 409, "Last Credential", "add a password or an authenticator before removing the last passkey")
		return
	}
	result, err := tx.ExecContext(r.Context(), `DELETE FROM auth_credentials WHERE id=$1 AND user_id=$2`, r.PathValue("id"), uid)
	if err != nil {
		writeProblem(w, 500, "Delete Failed", err.Error())
		return
	}
	if n, _ := result.RowsAffected(); n != 1 {
		writeProblem(w, 404, "Not Found", "no such passkey")
		return
	}
	if err := revokeOtherSessionsTx(r.Context(), tx, r, uid); err != nil {
		writeProblem(w, 500, "Delete Failed", "could not revoke other sessions: "+err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		writeProblem(w, 500, "Delete Failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) handleRecoveryRegenerate(w http.ResponseWriter, r *http.Request) {
	uid, _ := a.userID(r.Context())
	codes, err := a.replaceRecoveryCodes(r.Context(), r, uid)
	if err != nil {
		writeProblem(w, 500, "Recovery Failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"recovery_codes": codes})
}

func (a *App) handleTOTPBegin(w http.ResponseWriter, r *http.Request) {
	uid, _ := a.userID(r.Context())
	// Serialized with confirm against the owner row, and the upsert only
	// replaces a still-pending enrollment: a begin that raced a confirm
	// must not overwrite a factor the user just enabled (audit AUTH-04).
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, 500, "TOTP Failed", err.Error())
		return
	}
	defer tx.Rollback()
	if _, err := lockAuthUser(r.Context(), tx, uid); err != nil {
		writeProblem(w, 500, "TOTP Failed", err.Error())
		return
	}
	var enabled bool
	if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM auth_totp WHERE user_id=$1 AND enabled_at IS NOT NULL)`, uid).Scan(&enabled); err != nil {
		writeProblem(w, 500, "TOTP Failed", err.Error())
		return
	}
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
	result, err := tx.ExecContext(r.Context(), `INSERT INTO auth_totp(user_id,secret_ciphertext,enabled_at) VALUES ($1,$2,NULL)
		ON CONFLICT(user_id) DO UPDATE SET secret_ciphertext=excluded.secret_ciphertext,enabled_at=NULL
		WHERE auth_totp.enabled_at IS NULL`, uid, sealed)
	if err != nil {
		writeProblem(w, 500, "TOTP Failed", err.Error())
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		writeProblem(w, 409, "TOTP Already Enabled", "an authenticator was enabled while this setup started — try again")
		return
	}
	if err := tx.Commit(); err != nil {
		writeProblem(w, 500, "TOTP Failed", err.Error())
		return
	}
	var email string
	_ = a.db.QueryRowContext(r.Context(), `SELECT email FROM users WHERE id=$1`, uid).Scan(&email)
	uri := "otpauth://totp/" + url.PathEscape("lullmail:"+email) + "?secret=" + encoded + "&issuer=lullmail&algorithm=SHA1&digits=6&period=30"
	writeJSON(w, map[string]any{"secret": encoded, "uri": uri})
}

func (a *App) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	uid, _ := a.userID(r.Context())
	var req struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, 500, "TOTP Failed", err.Error())
		return
	}
	defer tx.Rollback()
	if _, err := lockAuthUser(r.Context(), tx, uid); err != nil {
		writeProblem(w, 500, "TOTP Failed", err.Error())
		return
	}
	var sealed string
	err = tx.QueryRowContext(r.Context(), `SELECT secret_ciphertext FROM auth_totp WHERE user_id=$1`, uid).Scan(&sealed)
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
	step := totpMatchedStep(secret, req.Code, time.Now())
	if step < 0 {
		writeProblem(w, 422, "Code Rejected", "the six-digit code did not match")
		return
	}
	// Enabling is conditional on the enrollment still being pending, and
	// consumes the confirming step so that same code cannot immediately
	// sign in again (audit AUTH-03/04).
	result, err := tx.ExecContext(r.Context(), `
		UPDATE auth_totp SET enabled_at=now(), last_used_step=$2
		WHERE user_id=$1 AND enabled_at IS NULL`, uid, step)
	if err != nil {
		writeProblem(w, 500, "TOTP Failed", err.Error())
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		writeProblem(w, 409, "TOTP Already Enabled", "an authenticator is already enabled")
		return
	}
	if err := tx.Commit(); err != nil {
		writeProblem(w, 500, "TOTP Failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) handleTOTPDelete(w http.ResponseWriter, r *http.Request) {
	uid, _ := a.userID(r.Context())
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, 500, "TOTP Failed", err.Error())
		return
	}
	defer tx.Rollback()
	if _, err := lockAuthUser(r.Context(), tx, uid); err != nil {
		writeProblem(w, 500, "TOTP Failed", err.Error())
		return
	}
	factors, err := loadLoginFactors(r.Context(), tx, uid)
	if err != nil {
		writeProblem(w, 500, "TOTP Failed", err.Error())
		return
	}
	if lastFactor(factors, "totp") {
		writeProblem(w, 409, "Last Credential", "add a password or a passkey before removing the authenticator")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM auth_totp WHERE user_id=$1`, uid); err != nil {
		writeProblem(w, 500, "TOTP Failed", err.Error())
		return
	}
	// Removing a factor is a credential change: other sessions do not
	// survive it (audit AUTH-05).
	if err := revokeOtherSessionsTx(r.Context(), tx, r, uid); err != nil {
		writeProblem(w, 500, "TOTP Failed", "could not revoke other sessions: "+err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		writeProblem(w, 500, "TOTP Failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handlePasswordSet is the session-gated enrollment/change surface, mirroring
// handleTOTPBegin's shape. `current` is required whenever a password already
// exists — a stolen session must not be able to silently replace the
// credential it is riding on. Agent tokens never get here: the router fence
// (agent.go) scopes them away from /security before this handler runs.
func (a *App) handlePasswordSet(w http.ResponseWriter, r *http.Request) {
	if !a.allowAuthAttempt(r) {
		writeProblem(w, 429, "Too Many Attempts", "wait five minutes before trying again")
		return
	}
	uid, _ := a.userID(r.Context())
	lockKey := "uid:" + uid
	if remaining := a.passwordLockRemaining(lockKey); remaining > 0 {
		writePasswordLocked(w, remaining)
		return
	}
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req) != nil {
		writeProblem(w, 400, "Bad Request", "enter the new password")
		return
	}
	if !validPasswordLength(req.New) {
		writeProblem(w, 422, "Password Too Short", "use at least 8 characters")
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, 500, "Password Failed", err.Error())
		return
	}
	defer tx.Rollback()
	if _, err := lockAuthUser(r.Context(), tx, uid); err != nil {
		writeProblem(w, 500, "Password Failed", err.Error())
		return
	}
	var encoded string
	err = tx.QueryRowContext(r.Context(), `SELECT hash FROM auth_passwords WHERE user_id=$1`, uid).Scan(&encoded)
	had := false
	switch {
	case err == nil:
		had = true
		release, busyErr := acquirePasswordWork()
		if busyErr != nil {
			writeAuthBusy(w)
			return
		}
		ok, verr := verifyPassword(encoded, req.Current)
		release()
		if verr != nil {
			writeProblem(w, 500, "Password Failed", verr.Error())
			return
		}
		if !ok {
			a.recordPasswordFailure(lockKey)
			writeProblem(w, 403, "Current Password Incorrect", "enter the current password to replace it")
			return
		}
	case errors.Is(err, sql.ErrNoRows):
		// First enrollment: no current password to prove.
	default:
		writeProblem(w, 500, "Password Failed", err.Error())
		return
	}
	hashRelease, busyErr := acquirePasswordWork()
	if busyErr != nil {
		writeAuthBusy(w)
		return
	}
	hash, err := hashPassword(req.New)
	hashRelease()
	if err != nil {
		writeProblem(w, 500, "Password Failed", err.Error())
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO auth_passwords (user_id,hash,updated_at)
		VALUES ($1,$2,now()) ON CONFLICT (user_id) DO UPDATE SET hash=excluded.hash, updated_at=now()`, uid, hash); err != nil {
		writeProblem(w, 500, "Password Failed", err.Error())
		return
	}
	if had {
		// Replacing a password revokes every other session, inside the
		// same transaction as the change (audit AUTH-05).
		if err := revokeOtherSessionsTx(r.Context(), tx, r, uid); err != nil {
			writeProblem(w, 500, "Password Failed", "could not revoke other sessions: "+err.Error())
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeProblem(w, 500, "Password Failed", err.Error())
		return
	}
	a.clearPasswordFailures(lockKey)
	writeJSON(w, map[string]any{"ok": true})
}

// handlePasswordDelete removes the password — unless it is the last way in.
// Recovery codes never count: they are one-use escape hatches, not a
// credential an owner can sign in with tomorrow.
func (a *App) handlePasswordDelete(w http.ResponseWriter, r *http.Request) {
	uid, _ := a.userID(r.Context())
	var req struct {
		Current string `json:"current"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req)
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, 500, "Password Failed", err.Error())
		return
	}
	defer tx.Rollback()
	if _, err := lockAuthUser(r.Context(), tx, uid); err != nil {
		writeProblem(w, 500, "Password Failed", err.Error())
		return
	}
	var encoded string
	err = tx.QueryRowContext(r.Context(), `SELECT hash FROM auth_passwords WHERE user_id=$1`, uid).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		writeProblem(w, 404, "No Password", "no password is set on this account")
		return
	}
	if err != nil {
		writeProblem(w, 500, "Password Failed", err.Error())
		return
	}
	// Deleting the password is a credential change; it gets the same
	// failure counting as changing one (audit AUTH-02).
	lockKey := "uid:" + uid
	if remaining := a.passwordLockRemaining(lockKey); remaining > 0 {
		writePasswordLocked(w, remaining)
		return
	}
	release, busyErr := acquirePasswordWork()
	if busyErr != nil {
		writeAuthBusy(w)
		return
	}
	ok, verr := verifyPassword(encoded, req.Current)
	release()
	if verr != nil {
		writeProblem(w, 500, "Password Failed", verr.Error())
		return
	}
	if !ok {
		a.recordPasswordFailure(lockKey)
		writeProblem(w, 403, "Current Password Incorrect", "enter the current password to remove it")
		return
	}
	factors, err := loadLoginFactors(r.Context(), tx, uid)
	if err != nil {
		writeProblem(w, 500, "Password Failed", err.Error())
		return
	}
	if lastFactor(factors, "password") {
		writeProblem(w, 409, "Last Credential",
			"add a passkey or an authenticator before removing the password")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM auth_passwords WHERE user_id=$1`, uid); err != nil {
		writeProblem(w, 500, "Password Failed", err.Error())
		return
	}
	if err := revokeOtherSessionsTx(r.Context(), tx, r, uid); err != nil {
		writeProblem(w, 500, "Password Failed", "could not revoke other sessions: "+err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		writeProblem(w, 500, "Password Failed", err.Error())
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
	// Epoch-matched sessions only: a row left behind by a raced mint is
	// dead server-side and must not be listed as a live device.
	rows, err := a.db.QueryContext(r.Context(), `
		SELECT s.id_hash,s.created_at,s.last_seen_at,s.expires_at,s.user_agent
		FROM auth_sessions s JOIN users u ON u.id = s.user_id
		WHERE s.user_id=$1 AND s.expires_at>now() AND s.auth_epoch = u.auth_epoch
		ORDER BY s.last_seen_at DESC`, uid)
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
	if err := rows.Err(); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
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
	finishFullDelete := a.beginFullAccountDeletion()
	committed := false
	var mirrors []mail.AccountID
	defer func() { finishFullDelete(mirrors, committed) }()
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, 500, "Delete Failed", err.Error())
		return
	}
	defer tx.Rollback()
	if _, err := lockAuthUser(r.Context(), tx, uid); err != nil {
		writeProblem(w, 500, "Delete Failed", err.Error())
		return
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT mirror_account_id FROM email_accounts WHERE user_id=$1`, uid)
	if err != nil {
		writeProblem(w, 500, "Delete Failed", err.Error())
		return
	}
	for rows.Next() {
		var id string
		// A skipped row here is a mailbox that survives "delete everything".
		// Refusing is the only safe answer: a failed delete can be retried, a
		// silently partial one leaves mail on disk the owner believes is gone.
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			writeProblem(w, 500, "Delete Failed", err.Error())
			return
		}
		mirrors = append(mirrors, mail.AccountID(id))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeProblem(w, 500, "Delete Failed", err.Error())
		return
	}
	rows.Close()
	for _, mirror := range mirrors {
		queries := []string{
			`DELETE FROM mail_message_mailboxes WHERE account_id=$1`, `DELETE FROM mail_bodies WHERE account_id=$1`,
			`DELETE FROM mail_messages WHERE account_id=$1`, `DELETE FROM mail_mailboxes WHERE account_id=$1`,
			`DELETE FROM mail_sync_state WHERE account_id=$1`, `DELETE FROM mail_accounts WHERE id=$1`,
		}
		for _, q := range queries {
			if _, err = tx.ExecContext(r.Context(), q, string(mirror)); err != nil {
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
	committed = true
	a.clearCookie(w, sessionCookie)
	writeJSON(w, map[string]any{"deleted": true})
}

// ensureUser bootstraps an installation owner, but never an authentication
// secret. The setup token is still required to register the first credential.
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

// utf8Prefix truncates to at most maxBytes without splitting a multi-byte
// rune. Byte slicing a name or user agent can produce invalid UTF-8 that
// later fails MIME formatting or storage (audit 4 F17); opaque tokens and
// hashes must never pass through here.
func utf8Prefix(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
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

// writeLookupProblem classifies a row-lookup error honestly: a genuinely
// absent row is a 404, while an infrastructure failure is a retryable 503.
// Reporting database outages as missing resources misleads users, hides
// failures from monitoring, and teaches offline clients to treat a transient
// outage as a permanent rejection (audit 4 F23). Call sites log the
// underlying error before delegating here.
func writeLookupProblem(w http.ResponseWriter, err error, noun string) {
	if errors.Is(err, sql.ErrNoRows) {
		writeProblem(w, http.StatusNotFound, "Not Found", "no such "+noun)
		return
	}
	w.Header().Set("Retry-After", "2")
	writeProblem(w, http.StatusServiceUnavailable, "Temporarily Unavailable",
		"the operation could not be completed — retry without discarding your changes")
}
