package main

// Re-authentication ceremony (audit AUTH-02 pass 7 / AUTH-06 pass 6) and
// the durable per-user TOTP guessing budget (audit AUTH-06 pass 7).
//
// A standing session used to be enough to enroll durable replacement
// credentials. Now the credential-enrolling mutations demand proof that is
// at most ten minutes old: a fresh sign-in (the same rule full account
// deletion already applies) or a password confirmation that stamps
// reauthenticated_at on the current session. Passkey ADD/REMOVE also sits
// behind the gate (audit 5 AUTH-01): user verification on a newly
// registered authenticator proves control of that NEW authenticator, not
// of an existing account credential — an old stolen session must not be
// able to enroll its own passkey. The bootstrap ceremony stays outside:
// it is authorized by the setup token and retires with the first
// credential.

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
	var proofEpoch int64
	err := a.db.QueryRowContext(r.Context(), `
		SELECT p.hash, u.auth_epoch FROM auth_passwords p
		JOIN users u ON u.id = p.user_id WHERE p.user_id=$1`, uid).
		Scan(&encoded, &proofEpoch)
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
	// Stamp the confirming session only after re-checking the credential
	// under the user-row lock: the hash and epoch were captured BEFORE the
	// (slow) KDF, and a concurrent password rotation that carried this
	// session into the new epoch could otherwise let an OLD password's
	// proof buy freshness in the new one (audit 5 AUTH-03).
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, 500, "Confirmation Failed", err.Error())
		return
	}
	defer tx.Rollback()
	var currentEpoch int64
	var currentHash string
	if err := tx.QueryRowContext(r.Context(), `
		SELECT u.auth_epoch, p.hash FROM users u
		JOIN auth_passwords p ON p.user_id = u.id
		WHERE u.id = $1 FOR UPDATE OF u`, uid).Scan(&currentEpoch, &currentHash); err != nil {
		writeProblem(w, 500, "Confirmation Failed", err.Error())
		return
	}
	if currentEpoch != proofEpoch || currentHash != encoded {
		writeProblem(w, http.StatusConflict, "Credentials Changed",
			"the password changed during confirmation — confirm again with the current password")
		return
	}
	result, err := tx.ExecContext(r.Context(), `
		UPDATE auth_sessions SET reauthenticated_at = now()
		WHERE id_hash=$1 AND user_id=$2 AND auth_epoch=$3 AND expires_at > now()`,
		session, uid, proofEpoch)
	if err != nil {
		writeProblem(w, 500, "Confirmation Failed", err.Error())
		return
	}
	if n, _ := result.RowsAffected(); n != 1 {
		writeProblem(w, http.StatusConflict, "Session Ended",
			"this session is no longer active — sign in again")
		return
	}
	if err := tx.Commit(); err != nil {
		writeProblem(w, 500, "Confirmation Failed", err.Error())
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

// retryAfterWindow reports how long until the current fixed window rolls
// over, floored at zero.
func retryAfterWindow(window time.Time) time.Duration {
	remaining := time.Until(window.Add(totpBudgetWindow))
	if remaining < 0 {
		remaining = 0
	}
	return remaining
}

// reserveTOTPAttempt atomically charges one attempt against the user's
// shared fixed window BEFORE any verification work runs (audit 5
// AUTH-02): the conditional upsert is the whole admission decision — the
// insert either wins the slot (first attempt) or the update fires only
// while the stored count is still below the cap, so concurrent guessers
// can never all observe an available budget and proceed. Every admitted
// attempt is counted, successful or not; a database failure fails closed
// (503, no verification) rather than admitting an uncounted guess.
func (a *App) reserveTOTPAttempt(ctx context.Context, uid string) (bool, time.Duration, error) {
	window := totpWindowStart(time.Now())
	var attempts int
	err := a.db.QueryRowContext(ctx, `
		INSERT INTO auth_factor_windows (user_id, factor, window_start, attempts)
		VALUES ($1, 'totp', $2, 1)
		ON CONFLICT (user_id, factor, window_start) DO UPDATE
		  SET attempts = auth_factor_windows.attempts + 1
		  WHERE auth_factor_windows.attempts < $3
		RETURNING attempts`, uid, window, maxTOTPWindowAttempts).Scan(&attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return false, retryAfterWindow(window), nil
	}
	if err != nil {
		return false, 0, err
	}
	return true, 0, nil
}
