package main

// Agent tokens: long-lived Bearer credentials for scripting and AI agents
// (bulk-importing mailboxes from a provider API, automation, an MCP server).
// The browser keeps the passkey; agents get a revocable token that can reach
// accounts and mail but never the auth/security surface — that boundary is
// enforced here, in the router, not left to the token holder's good behavior.

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// New tokens carry the lull_ prefix. es_ is the email-soft era prefix and
// stays valid so existing integrations (Akiroo's stored token) survive the
// rename.
const agentTokenPrefix = "lull_"
const legacyAgentTokenPrefix = "es_"

func isAgentToken(raw string) bool {
	return strings.HasPrefix(raw, agentTokenPrefix) || strings.HasPrefix(raw, legacyAgentTokenPrefix)
}

// agentAPIPrefixes are everything a token may touch. Auth ceremonies,
// passkey/TOTP/session management, and token management itself stay
// session-only: a leaked agent token must not be able to mint siblings or
// lock the owner out.
var agentAllowed = map[string]bool{
	"/accounts":        true, // list/connect
	"/accounts/":       true, // item routes: sync, retention, delete
	"/screener":        true,
	"/screener/":       true, // decide + undecide (the MCP adapter's screener_decide)
	"/counts":          true,
	"/search":          true,
	"/briefing":        true,
	"/board":           true,
	"/notes":           true,
	"/people":          true,
	"/recent":          true,
	"/folder":          true,
	"/mailboxes":       true,
	"/buckets/":        true,
	"/threads/":        true,
	"/messages/":       true,
	"/send":            true,
	"/outbox/":         true,
	"/classify":        true,
	"/personal/export": true,
	// The raw /mail/ engine surface is deliberately NOT agent-reachable:
	// it is account-id keyed with its own semantics, and everything an agent
	// legitimately needs exists as an owned /api route above.
}

// agentAllowedPath reports whether an /api-relative path is inside the agent
// scope. Exact entries match whole segments; trailing-slash entries are
// prefixes.
func agentAllowedPath(path string) bool {
	for prefix := range agentAllowed {
		if strings.HasSuffix(prefix, "/") {
			if strings.HasPrefix(path, prefix) {
				return true
			}
		} else if path == prefix {
			return true
		}
	}
	return false
}

// authenticateAgent resolves an "Authorization: Bearer lull_..." (or legacy
// es_...) token to its owner, updating last_used_at at most once a minute so
// hot polling loops do not turn into write storms.
func (a *App) authenticateAgent(r *http.Request) (string, bool) {
	raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !isAgentToken(raw) {
		return "", false
	}
	var uid string
	var lastUsed sql.NullTime
	err := a.db.QueryRowContext(r.Context(), `SELECT user_id, last_used_at FROM agent_tokens WHERE token_hash=$1`, tokenHash(raw)).Scan(&uid, &lastUsed)
	if err != nil {
		return "", false
	}
	if !lastUsed.Valid || time.Since(lastUsed.Time) > time.Minute {
		_, _ = a.db.ExecContext(r.Context(), `UPDATE agent_tokens SET last_used_at=now() WHERE token_hash=$1`, tokenHash(raw))
	}
	return uid, true
}

// hasAgentBearer reports whether the request carries an lull_/es_-prefixed
// Bearer credential, which routes it through the agent path.
func hasAgentBearer(r *http.Request) bool {
	return isAgentToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
}

// requireAgent wraps requireAuth's tree with the token path. Agent requests
// skip the browser Origin check: no browser, no Origin header, no CSRF
// surface — the Bearer secret is the entire authentication.
func (a *App) requireAgent(next http.Handler) http.Handler {
	inner := a.requireAuth(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hasAgentBearer(r) {
			inner.ServeHTTP(w, r)
			return
		}
		uid, ok := a.authenticateAgent(r)
		if !ok {
			writeProblem(w, http.StatusUnauthorized, "Unauthorized", "agent token not recognized")
			return
		}
		if !agentAllowedPath(strings.TrimPrefix(r.URL.Path, "/api")) {
			writeProblem(w, http.StatusForbidden, "Forbidden", "agent tokens cannot reach this surface")
			return
		}
		// The token's owner is the authenticated principal for the scoped
		// handlers — otherwise every agent request silently acts as the
		// installation's first user instead of the user who minted it.
		ctx := contextWithAgent(r.Context(), uid)
		ctx = context.WithValue(ctx, authContextKey{}, uid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *App) mountAgentTokens(mux *http.ServeMux) {
	mux.HandleFunc("GET /security/agent-tokens", a.handleAgentTokens)
	mux.HandleFunc("POST /security/agent-tokens", a.handleAgentTokens)
	mux.HandleFunc("DELETE /security/agent-tokens/{id}", a.handleAgentTokenDelete)
}

func (a *App) handleAgentTokens(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, 500, "Lookup Failed", err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		rows, err := a.db.QueryContext(r.Context(), `SELECT id,name,created_at,last_used_at FROM agent_tokens WHERE user_id=$1 ORDER BY created_at`, uid)
		if err != nil {
			writeProblem(w, 500, "List Failed", err.Error())
			return
		}
		defer rows.Close()
		tokens := []map[string]any{}
		for rows.Next() {
			var id, name string
			var created time.Time
			var last sql.NullTime
			if err := rows.Scan(&id, &name, &created, &last); err != nil {
				writeProblem(w, 500, "List Failed", err.Error())
				return
			}
			entry := map[string]any{"id": id, "name": name, "created_at": created, "last_used_at": nil}
			if last.Valid {
				entry["last_used_at"] = last.Time
			}
			tokens = append(tokens, entry)
		}
		writeJSON(w, tokens)
	case http.MethodPost:
		var req struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req)
		name := strings.TrimSpace(req.Name)
		if name == "" {
			name = "Agent"
		}
		if len(name) > 80 {
			name = name[:80]
		}
		raw, err := opaqueToken(24)
		if err != nil {
			writeProblem(w, 500, "Token Failed", err.Error())
			return
		}
		raw = agentTokenPrefix + raw
		if _, err := a.db.ExecContext(r.Context(), `INSERT INTO agent_tokens (user_id,name,token_hash) VALUES ($1,$2,$3)`, uid, name, tokenHash(raw)); err != nil {
			writeProblem(w, 500, "Token Failed", err.Error())
			return
		}
		// The raw value crosses the wire exactly once, like a recovery code.
		writeJSON(w, map[string]any{"ok": true, "token": raw, "name": name})
	}
}

func (a *App) handleAgentTokenDelete(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, 500, "Lookup Failed", err.Error())
		return
	}
	if _, err := a.db.ExecContext(r.Context(), `DELETE FROM agent_tokens WHERE id=$1 AND user_id=$2`, r.PathValue("id"), uid); err != nil {
		writeProblem(w, 500, "Revoke Failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// constantTimeAgentTokenCompare exists so tests can pin the prefix decision.
func constantTimeAgentTokenCompare(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
