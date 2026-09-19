package main

// Re-authentication ceremony (audit AUTH-02 pass 7 / AUTH-06 pass 6) and
// the durable per-user TOTP guessing budget (audit AUTH-06 pass 7).
//
// A standing session used to be enough to enroll durable replacement
// credentials. Now the credential-enrolling mutations demand proof that is
// at most ten minutes old: a fresh sign-in (the same rule full account
// deletion already applies) or a password confirmation that stamps
// reauthenticated_at on the current session. Passkey ceremonies verify the
// user intrinsically (UserVerification required) and stay outside the gate.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// reauthFreshness is how long a confirmation or sign-in satisfies the gate.
const reauthFreshness = 10 * time.Minute

// The durable TOTP budget: one fixed five-minute window per user, shared by
// every peer. The per-peer limiter stays in front; this is the distributed
// backstop, so its bound matches the peer allowance — ten wrong guesses
// from anywhere exhaust the window, and the fixed deadline never extends.
const (
	totpBudgetWindow      = 5 * time.Minute
	maxTOTPWindowAttempts = 10
)

// handleReauthenticate is the ceremony's password-confirm endpoint: verify
// the standing password (same KDF admission, failure counting, and lockout
// as sign-in), then stamp reauthenticated_at on the session that asked.
func (a *App) handleReauthenticate(w http.ResponseWriter, r *http.Request) {
	if !a.allowAuthAttempt(r) {
		writeProblem(w, 429, "Too Many Attempts", "wait five minutes before trying again")
		return
	}
	uid, _ := a.userID(r.Context())
	session, _ := r.Context().Value(sessionContextKey{}).(string)
	var req struct {
		Password string `json:"password"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req) != nil {
		writeProblem(w, 400, "Bad Request", "enter your password")
		return
	}
	if session == "" || session == "bootstrap" {
		writeProblem(w, http.StatusConflict, "No Session To Confirm",
			"sign in first — the setup token needs no confirmation")
		return
	}
	lockKey := "uid:" + uid
	if remaining := a.passwordLockRemaining(lockKey); remaining > 0 {
		writePasswordLocked(w, remaining)
		return
	}
	var encoded string
	err := a.db.QueryRowContext(r.Context(), `SELECT hash FROM auth_passwords WHERE user_id=$1`, uid).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		burnPasswordVerify(req.Password)
		writeProblem(w, http.StatusConflict, "No Password Set",
			"this account has no password — sign out and sign back in to confirm, or set a password first")
		return
	}
	if err != nil {
		writeProblem(w, 500, "Confirmation Failed", err.Error())
		return
	}
	release, busyErr := acquirePasswordWork()
	if busyErr != nil {
		writeAuthBusy(w)
		return
	}
	ok, verr := verifyPassword(encoded, req.Password)
	release()
	if verr != nil {
		writeProblem(w, 500, "Confirmation Failed", verr.Error())
		return
	}
	if !ok {
		a.recordPasswordFailure(lockKey)
		writeProblem(w, 403, "Password Incorrect", "enter your current password to confirm")
		return
	}
	a.clearPasswordFailures(lockKey)
	// Stamp the confirming session only, epoch-matched so a session that a
	// concurrent credential change retired cannot buy freshness.
	result, err := a.db.ExecContext(r.Context(), `
		UPDATE auth_sessions SET reauthenticated_at = now()
		WHERE id_hash=$1 AND user_id=$2
		  AND auth_epoch = (SELECT auth_epoch FROM users WHERE users.id = auth_sessions.user_id)
		  AND expires_at > now()`, session, uid)
	if err != nil {
		writeProblem(w, 500, "Confirmation Failed", err.Error())
		return
	}
	if n, _ := result.RowsAffected(); n != 1 {
		writeProblem(w, http.StatusConflict, "Session Ended",
			"this session is no longer active — sign in again")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// requireRecentReauth is the freshness gate for credential-enrolling
// mutations: the session itself must be new, or the owner must have
// confirmed the password on it, within reauthFreshness. The bootstrap token
// passes — it exists only before the first credential and retires with it.
func (a *App) requireRecentReauth(w http.ResponseWriter, r *http.Request) bool {
	uid, _ := a.userID(r.Context())
	session, _ := r.Context().Value(sessionContextKey{}).(string)
	if session == "bootstrap" {
		return true
	}
	if session == "" {
		writeProblem(w, http.StatusPreconditionRequired, "Fresh Confirmation Required",
			"confirm your password to continue — or sign out and sign back in")
		return false
	}
	var fresh bool
	err := a.db.QueryRowContext(r.Context(), `
		SELECT GREATEST(s.created_at, COALESCE(s.reauthenticated_at, s.created_at)) > now() - make_interval(secs => $3)
		FROM auth_sessions s JOIN users u ON u.id = s.user_id
		WHERE s.id_hash=$1 AND s.user_id=$2 AND s.auth_epoch = u.auth_epoch AND s.expires_at > now()`,
		session, uid, int(reauthFreshness.Seconds())).Scan(&fresh)
	if err != nil || !fresh {
		writeProblem(w, http.StatusPreconditionRequired, "Fresh Confirmation Required",
			"confirm your password to continue — or sign out and sign back in")
		return false
	}
	return true
}

// totpWindowStart is the server-computed fixed window boundary; it is never
// taken from the client.
func totpWindowStart(now time.Time) time.Time {
	return now.UTC().Truncate(totpBudgetWindow)
}

// totpBudgetExhausted reports whether the user's shared TOTP budget for the
// current fixed window is spent, and how long until the window rolls over.
func (a *App) totpBudgetExhausted(ctx context.Context, uid string) (bool, time.Duration, error) {
	window := totpWindowStart(time.Now())
	var attempts int
	err := a.db.QueryRowContext(ctx,
		`SELECT attempts FROM auth_factor_windows WHERE user_id=$1 AND factor='totp' AND window_start=$2`,
		uid, window).Scan(&attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	if attempts < maxTOTPWindowAttempts {
		return false, 0, nil
	}
	remaining := time.Until(window.Add(totpBudgetWindow))
	if remaining < 0 {
		remaining = 0
	}
	return true, remaining, nil
}

// recordTOTPFailure counts one wrong standalone-TOTP guess into the user's
// shared fixed window. The upsert is the whole concurrency story: racing
// guessers each add exactly one, and the count is the truth.
func (a *App) recordTOTPFailure(ctx context.Context, uid string) {
	window := totpWindowStart(time.Now())
	if _, err := a.db.ExecContext(ctx, `
		INSERT INTO auth_factor_windows (user_id, factor, window_start, attempts)
		VALUES ($1, 'totp', $2, 1)
		ON CONFLICT (user_id, factor, window_start)
		DO UPDATE SET attempts = auth_factor_windows.attempts + 1`,
		uid, window); err != nil {
		a.log.Error("totp budget record failed", "err", err)
	}
}
