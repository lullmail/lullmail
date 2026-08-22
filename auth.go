package main

// Dev auth v0 (TASKS 1.2): single user, bearer token from the environment.
// Every /api route except the health probes requires it. Passkeys, recovery
// codes and sessions replace this without changing the handler shapes.

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
)

func (a *App) requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.cfg.APIToken == "" {
			writeProblem(w, http.StatusServiceUnavailable, "Auth Unconfigured", "EMAILSOFT_TOKEN is not set; API is locked")
			return
		}
		got := r.Header.Get("Authorization")
		// Constant-time: the token guards every route, so it must not leak
		// by comparison timing either.
		if subtle.ConstantTimeCompare([]byte(got), []byte("Bearer "+a.cfg.APIToken)) != 1 {
			writeProblem(w, http.StatusUnauthorized, "Unauthorized", "bad or missing bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ensureUser bootstraps the single v0 user. Passkey signup replaces this.
func (a *App) ensureUser(ctx context.Context) error {
	if a.cfg.UserEmail == "" {
		return nil
	}
	_, err := a.db.ExecContext(ctx,
		`INSERT INTO users (email) VALUES ($1) ON CONFLICT (email) DO NOTHING`,
		a.cfg.UserEmail)
	return err
}

// userID is the v0 single-user lookup. Multi-user arrives with real auth.
func (a *App) userID(ctx context.Context) (string, error) {
	var id string
	err := a.db.QueryRowContext(ctx, `SELECT id FROM users ORDER BY created_at LIMIT 1`).Scan(&id)
	return id, err
}

func writeProblem(w http.ResponseWriter, status int, title, detail string) {
	// Marshal, never concatenate: provider errors carry quotes and
	// newlines, which would emit malformed JSON to the client.
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"title": title, "detail": detail})
}
