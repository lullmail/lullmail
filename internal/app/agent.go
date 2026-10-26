package app

// Agent tokens: long-lived Bearer credentials for scripting and AI agents
// (bulk-importing mailboxes from a provider API, automation, an MCP server).
// The browser keeps the passkey; agents get a revocable token that can reach
// accounts and mail but never the auth/security surface — that boundary is
// enforced here, in the router, not left to the token holder's good behavior.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// New tokens carry the lull_ prefix. es_ is the email-soft era prefix and
// stays valid so existing integrations (Akiroo's stored token) survive the
// rename.
const agentTokenPrefix = "lull_"
const legacyAgentTokenPrefix = "es_"

// The four named token scopes (OPS-10). They layer over the path allowlist,
// which stays the outer boundary: a scope can only shrink what a token
// reaches inside the allowlisted surface, never enlarge it.
const (
	scopeMailRead    = "mail:read"
	scopeMailSend    = "mail:send"
	scopeMailAct     = "mail:act"
	scopeAgentManage = "agent:manage"
)

// agentScopes is the closed scope vocabulary, in canonical grant order.
var agentScopes = []string{scopeMailRead, scopeMailSend, scopeMailAct, scopeAgentManage}

// agentRotationGrace is the dual-validity window after a rotation: the
// superseded token keeps authenticating until now()+grace, then dies
// indistinguishably from a revoked one. A variable (not a constant) so
// tests can shrink it instead of sleeping ten minutes.
var agentRotationGrace = 10 * time.Minute

// agentScopeGrants reports whether the granted scopes include one scope.
func agentScopeGrants(granted []string, scope string) bool {
	for _, s := range granted {
		if s == scope {
			return true
		}
	}
	return false
}

// agentScopesValue scans the token row's scopes column for database/sql:
// pgx's stdlib surface delivers text[] as the array literal string. The
// scope vocabulary is closed (no comma, quote, or brace in any name), so
// an exact split is possible without a full array parser.
type agentScopesValue []string

func (v *agentScopesValue) Scan(src any) error {
	var literal string
	switch s := src.(type) {
	case nil:
		literal = "{}"
	case string:
		literal = s
	case []byte:
		literal = string(s)
	default:
		return errors.New("agent scopes column arrived as " + fmt.Sprintf("%T", src))
	}
	literal = strings.TrimSpace(literal)
	if literal == "NULL" {
		literal = "{}"
	}
	if !strings.HasPrefix(literal, "{") || !strings.HasSuffix(literal, "}") {
		return errors.New("agent scopes column is not an array literal: " + literal)
	}
	inner := literal[1 : len(literal)-1]
	if inner == "" {
		*v = (*v)[:0]
		return nil
	}
	*v = strings.Split(inner, ",")
	return nil
}

// normalizeAgentScopes validates a requested scope list against the
// vocabulary and returns it deduplicated in canonical order. An empty
// request means the default: every scope.
func normalizeAgentScopes(requested []string) ([]string, bool) {
	if len(requested) == 0 {
		return append([]string(nil), agentScopes...), true
	}
	var out []string
	for _, want := range requested {
		if !agentScopeGrants(agentScopes, want) {
			return nil, false
		}
	}
	for _, s := range agentScopes {
		if agentScopeGrants(requested, s) {
			out = append(out, s)
		}
	}
	return out, true
}

func isAgentToken(raw string) bool {
	return strings.HasPrefix(raw, agentTokenPrefix) || strings.HasPrefix(raw, legacyAgentTokenPrefix)
}

// agentAPIPrefixes are everything a token may touch. Auth ceremonies,
// passkey/TOTP/session management, and token management itself stay
// session-only: a leaked agent token must not be able to mint siblings or
// lock the owner out.
var agentAllowed = map[string]bool{
	"/accounts":    true, // list/connect
	"/accounts/":   true, // item routes: sync, retention, delete
	"/screener":    true,
	"/screener/":   true, // decide + undecide (the MCP adapter's screener_decide)
	"/counts":      true,
	"/search":      true,
	"/briefing":    true,
	"/board":       true,
	"/board/":      true, // pin, unpin, cards
	"/notes":       true,
	"/notes/":      true, // item update/delete
	"/people":      true,
	"/people/sent": true, // last-sent per correspondent, read-only
	"/recent":      true,
	"/folder":      true,
	"/mailboxes":   true,
	"/buckets/":    true,
	"/threads/":    true,
	"/messages/":   true,
	"/send":        true,
	// Outcomes only. /outbox/{id} serves the decrypted composition, its Sent
	// copy and its destruction, so like the mailbox archive it is session-only.
	"/outbox":          true,
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
	if strings.Contains(path, "..") {
		return false
	}
	// Mailbox zip export is the product-layer equivalent of /mail/: a
	// leaked token must not dump the archive. /personal/export stays
	// allowed (notes/board JSON, not the mail mirror).
	if strings.HasPrefix(path, "/accounts/") && strings.HasSuffix(path, "/export") {
		return false
	}
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

// agentMayUndo reports whether the request is DELETE /outbox/{id}, which only
// cancels a send still inside its undo window. The same path with GET serves
// the decrypted composition and stays session-only.
func agentMayUndo(method, path string) bool {
	id, ok := strings.CutPrefix(path, "/outbox/")
	return method == http.MethodDelete && ok && id != "" && !strings.Contains(id, "/") && !strings.Contains(path, "..")
}

// agentRequiredScope resolves an /api-relative request to the one scope that
// governs it (the OPS-10 route→scope table). Entries that are read and
// mutate on one prefix resolve by method: GET/HEAD are reads, every other
// method is a mutation. The token-management surface resolves to
// agent:manage, but it sits outside agentAllowedPath — the allowlist stays
// the outer boundary, so an agent bearer is fenced off /security entirely
// (403 Forbidden) before any scope, agent:manage included, is consulted.
func agentRequiredScope(method, path string) string {
	if path == "/security/agent-tokens" || strings.HasPrefix(path, "/security/agent-tokens/") {
		return scopeAgentManage
	}
	// Cancel-within-undo is part of the send lifecycle: the scope that may
	// mint a send may also cancel one inside its undo window.
	if agentMayUndo(method, path) {
		return scopeMailSend
	}
	switch path {
	case "/send":
		return scopeMailSend
	case "/classify":
		return scopeMailAct
	}
	if method == http.MethodGet || method == http.MethodHead {
		return scopeMailRead
	}
	return scopeMailAct
}

// agentAuth is one Bearer credential resolved to its row: the owner, the
// granted scopes, and — for a refused token — whether it was refused for
// expiry (the one 401 the API distinguishes from "not recognized").
type agentAuth struct {
	uid     string
	scopes  []string
	expired bool
}

// authenticateAgent resolves an "Authorization: Bearer lull_..." (or legacy
// es_...) token to its owner, updating last_used_at at most once a minute so
// hot polling loops do not turn into write storms. A token past its expiry
// fails with expired set; a superseded token past its rotation grace fails
// like a revoked one — indistinguishable from unknown, on purpose.
func (a *App) authenticateAgent(r *http.Request) (agentAuth, bool) {
	raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !isAgentToken(raw) {
		return agentAuth{}, false
	}
	var auth agentAuth
	var lastUsed sql.NullTime
	var scopes agentScopesValue
	var expires, grace sql.NullTime
	err := a.db.QueryRowContext(r.Context(), `
		SELECT user_id, last_used_at, scopes, expires_at, rotated_grace_until
		FROM agent_tokens WHERE token_hash=$1`, tokenHash(raw)).
		Scan(&auth.uid, &lastUsed, &scopes, &expires, &grace)
	if err != nil {
		return agentAuth{}, false
	}
	auth.scopes = []string(scopes)
	if grace.Valid && grace.Time.Before(time.Now()) {
		return agentAuth{}, false
	}
	if expires.Valid && expires.Time.Before(time.Now()) {
		auth.expired = true
		return auth, false
	}
	if !lastUsed.Valid || time.Since(lastUsed.Time) > time.Minute {
		_, _ = a.db.ExecContext(r.Context(), `UPDATE agent_tokens SET last_used_at=now() WHERE token_hash=$1`, tokenHash(raw))
	}
	return auth, true
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
		auth, ok := a.authenticateAgent(r)
		if !ok {
			if auth.expired {
				writeProblem(w, http.StatusUnauthorized, "Token Expired", "this agent token reached its expiry — mint a replacement")
				return
			}
			writeProblem(w, http.StatusUnauthorized, "Unauthorized", "agent token not recognized")
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api")
		if !agentAllowedPath(path) && !agentMayUndo(r.Method, path) {
			writeProblem(w, http.StatusForbidden, "Forbidden", "agent tokens cannot reach this surface")
			return
		}
		if required := agentRequiredScope(r.Method, path); !agentScopeGrants(auth.scopes, required) {
			writeProblem(w, http.StatusForbidden, "Insufficient Token Scope",
				"this agent token is not granted "+required)
			return
		}
		// The token's owner is the authenticated principal for the scoped
		// handlers — otherwise every agent request silently acts as the
		// installation's first user instead of the user who minted it.
		ctx := context.WithValue(r.Context(), authContextKey{}, auth.uid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *App) mountAgentTokens(mux *http.ServeMux) {
	mux.HandleFunc("GET /security/agent-tokens", a.handleAgentTokens)
	mux.HandleFunc("POST /security/agent-tokens", a.handleAgentTokens)
	mux.HandleFunc("DELETE /security/agent-tokens/{id}", a.handleAgentTokenDelete)
	mux.HandleFunc("POST /security/agent-tokens/{id}/rotate", a.handleAgentTokenRotate)
}

// agentTokenRequest is the mint/rotate body: everything optional. Scopes
// validate against the four; expires_at is RFC 3339 and must be future.
type agentTokenRequest struct {
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	ExpiresAt string   `json:"expires_at"`
}

// parseAgentTokenExpiry validates the optional RFC 3339 expiry. A missing
// value is no expiry; present values must parse and be in the future.
func parseAgentTokenExpiry(raw string) (any, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, true
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, false
	}
	if !t.After(time.Now()) {
		return nil, false
	}
	return t, true
}

func (a *App) handleAgentTokens(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, 500, "Lookup Failed", err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		rows, err := a.db.QueryContext(r.Context(), `
			SELECT id, name, created_at, last_used_at, scopes, expires_at,
			       rotated_to::text, rotated_grace_until
			FROM agent_tokens WHERE user_id=$1 ORDER BY created_at`, uid)
		if err != nil {
			writeProblem(w, 500, "List Failed", err.Error())
			return
		}
		defer rows.Close()
		tokens := []map[string]any{}
		for rows.Next() {
			var id, name string
			var created time.Time
			var scopes agentScopesValue
			var last, expires, grace sql.NullTime
			var rotatedTo sql.NullString
			if err := rows.Scan(&id, &name, &created, &last, &scopes, &expires, &rotatedTo, &grace); err != nil {
				writeProblem(w, 500, "List Failed", err.Error())
				return
			}
			entry := map[string]any{"id": id, "name": name, "created_at": created, "last_used_at": nil}
			if last.Valid {
				entry["last_used_at"] = last.Time
			}
			entry["scopes"] = []string(scopes)
			entry["expires_at"] = nil
			if expires.Valid {
				entry["expires_at"] = expires.Time
			}
			entry["rotated_to"] = nil
			if rotatedTo.Valid {
				entry["rotated_to"] = rotatedTo.String
			}
			entry["rotated_grace_until"] = nil
			if grace.Valid {
				entry["rotated_grace_until"] = grace.Time
			}
			tokens = append(tokens, entry)
		}
		if err := rows.Err(); err != nil {
			writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
			return
		}
		writeJSON(w, tokens)
	case http.MethodPost:
		// Minting shows a raw bearer secret that lives until revoked:
		// recent proof first (audit AUTH-02).
		if !a.requireRecentReauth(w, r) {
			return
		}
		var req agentTokenRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			// A body that does not parse must not fall through to the
			// all-scopes default: a truncated request would silently mint
			// broader trust than the caller asked for.
			writeProblem(w, http.StatusBadRequest, "Invalid Request", "the token request body is not valid JSON")
			return
		}
		scopes, ok := normalizeAgentScopes(req.Scopes)
		if !ok {
			writeProblem(w, http.StatusBadRequest, "Invalid Scopes",
				"scopes must be drawn from "+strings.Join(agentScopes, ", "))
			return
		}
		expiresAt, ok := parseAgentTokenExpiry(req.ExpiresAt)
		if !ok {
			writeProblem(w, http.StatusBadRequest, "Invalid Expiry",
				"expires_at must be an RFC 3339 timestamp in the future")
			return
		}
		name := strings.TrimSpace(req.Name)
		if name == "" {
			name = "Agent"
		}
		name = utf8Prefix(name, 80)
		raw, err := opaqueToken(24)
		if err != nil {
			writeProblem(w, 500, "Token Failed", err.Error())
			return
		}
		raw = agentTokenPrefix + raw
		if _, err := a.db.ExecContext(r.Context(),
			`INSERT INTO agent_tokens (user_id,name,token_hash,scopes,expires_at) VALUES ($1,$2,$3,$4,$5)`,
			uid, name, tokenHash(raw), scopes, expiresAt); err != nil {
			writeProblem(w, 500, "Token Failed", err.Error())
			return
		}
		// The raw value crosses the wire exactly once, like a recovery code.
		writeJSON(w, map[string]any{"ok": true, "token": raw, "name": name, "scopes": scopes, "expires_at": expiresAt})
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

// handleAgentTokenRotate supersedes one token with a freshly minted
// successor (OPS-10): the successor's secret is returned once like a mint,
// the predecessor keeps authenticating for agentRotationGrace, then dies
// indistinguishably from a revoked token. The successor inherits the
// predecessor's name, scopes, and expiry unless the body overrides them.
// One rotation at a time — a superseded token refuses to rotate again.
func (a *App) handleAgentTokenRotate(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, 500, "Lookup Failed", err.Error())
		return
	}
	// Rotation reveals a raw bearer credential, exactly like minting: the
	// freshness gate (audit AUTH-02) applies for the same reason.
	if !a.requireRecentReauth(w, r) {
		return
	}
	var req agentTokenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeProblem(w, http.StatusBadRequest, "Invalid Request", "the rotation request body is not valid JSON")
		return
	}
	var name string
	var predecessorScopes agentScopesValue
	var predecessorExpires sql.NullTime
	err = a.db.QueryRowContext(r.Context(), `
		SELECT name, scopes, expires_at FROM agent_tokens
		WHERE id=$1 AND user_id=$2 AND rotated_to IS NULL AND rotated_grace_until IS NULL`,
		r.PathValue("id"), uid).Scan(&name, &predecessorScopes, &predecessorExpires)
	if errors.Is(err, sql.ErrNoRows) {
		// Also the double-rotation answer: a superseded token (or another
		// user's id, or none at all) has no rotatable row.
		var superseded bool
		if err2 := a.db.QueryRowContext(r.Context(),
			`SELECT EXISTS(SELECT 1 FROM agent_tokens WHERE id=$1 AND user_id=$2 AND (rotated_to IS NOT NULL OR rotated_grace_until IS NOT NULL))`,
			r.PathValue("id"), uid).Scan(&superseded); err2 == nil && superseded {
			writeProblem(w, http.StatusConflict, "Token Superseded",
				"this token was already rotated — rotate its successor instead")
			return
		}
		writeProblem(w, http.StatusNotFound, "Not Found", "no such token")
		return
	}
	if err != nil {
		writeProblem(w, 500, "Rotation Failed", err.Error())
		return
	}

	scopes := []string(predecessorScopes)
	if req.Scopes != nil {
		var ok bool
		if scopes, ok = normalizeAgentScopes(req.Scopes); !ok {
			writeProblem(w, http.StatusBadRequest, "Invalid Scopes",
				"scopes must be drawn from "+strings.Join(agentScopes, ", "))
			return
		}
	}
	var expiresAt any
	if predecessorExpires.Valid {
		expiresAt = predecessorExpires.Time
	}
	if strings.TrimSpace(req.ExpiresAt) != "" {
		var ok bool
		if expiresAt, ok = parseAgentTokenExpiry(req.ExpiresAt); !ok {
			writeProblem(w, http.StatusBadRequest, "Invalid Expiry",
				"expires_at must be an RFC 3339 timestamp in the future")
			return
		}
	}

	raw, err := opaqueToken(24)
	if err != nil {
		writeProblem(w, 500, "Rotation Failed", err.Error())
		return
	}
	raw = agentTokenPrefix + raw
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, 500, "Rotation Failed", err.Error())
		return
	}
	defer tx.Rollback()
	var successor string
	if err := tx.QueryRowContext(r.Context(),
		`INSERT INTO agent_tokens (user_id,name,token_hash,scopes,expires_at) VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		uid, name, tokenHash(raw), scopes, expiresAt).Scan(&successor); err != nil {
		writeProblem(w, 500, "Rotation Failed", err.Error())
		return
	}
	// The NULL guards make a concurrent second rotation land on zero rows
	// and roll back its successor instead of orphaning the linkage.
	res, err := tx.ExecContext(r.Context(), `
		UPDATE agent_tokens SET rotated_to=$2,
			rotated_grace_until=now()+make_interval(secs => $3::int)
		WHERE id=$1 AND user_id=$4 AND rotated_to IS NULL AND rotated_grace_until IS NULL`,
		r.PathValue("id"), successor, agentRotationGrace.Seconds(), uid)
	if err != nil {
		writeProblem(w, 500, "Rotation Failed", err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		writeProblem(w, http.StatusConflict, "Token Superseded",
			"this token was already rotated — rotate its successor instead")
		return
	}
	if err := tx.Commit(); err != nil {
		writeProblem(w, 500, "Rotation Failed", err.Error())
		return
	}
	// The raw value crosses the wire exactly once, like a mint.
	writeJSON(w, map[string]any{"ok": true, "token": raw, "name": name, "scopes": scopes, "expires_at": expiresAt})
}
