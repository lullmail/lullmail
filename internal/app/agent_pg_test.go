package app

// Real-PostgreSQL regressions for OPS-10: agent-token scopes, expiry, and
// rotation. The scope fence is enforced in requireAgent between the path
// allowlist and the handler, so these drive the real middleware against
// real token rows — every deny here is the middleware refusing, not a
// handler shaping its answer.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// agentRouteScopeCases is the exhaustive OPS-10 route→scope table: every
// route the mux mounts under an allowlisted prefix, with the one scope
// that governs it. agentBlocked marks routes the outer path allowlist
// fences agent bearers off entirely — no scope can open them (the
// /security surface and the mailbox archive export).
var agentRouteScopeCases = []struct {
	method, path, scope string
	agentBlocked        bool
}{
	{"GET", "/accounts", scopeMailRead, false},
	{"POST", "/accounts", scopeMailAct, false},
	{"GET", "/accounts/acct1", scopeMailRead, false},
	{"POST", "/accounts/acct1", scopeMailAct, false},
	{"DELETE", "/accounts/acct1", scopeMailAct, false},
	{"GET", "/accounts/acct1/export", "", true},
	{"GET", "/screener", scopeMailRead, false},
	{"POST", "/screener/decide", scopeMailAct, false},
	{"POST", "/screener/undecide", scopeMailAct, false},
	{"GET", "/counts", scopeMailRead, false},
	{"GET", "/search", scopeMailRead, false},
	{"GET", "/briefing", scopeMailRead, false},
	{"GET", "/board", scopeMailRead, false},
	{"POST", "/board/pin", scopeMailAct, false},
	{"POST", "/board/cards", scopeMailAct, false},
	{"POST", "/board/cards/c1/done", scopeMailAct, false},
	{"POST", "/board/unpin", scopeMailAct, false},
	{"GET", "/notes", scopeMailRead, false},
	{"POST", "/notes", scopeMailAct, false},
	{"POST", "/notes/n1", scopeMailAct, false},
	{"DELETE", "/notes/n1", scopeMailAct, false},
	{"GET", "/people", scopeMailRead, false},
	{"GET", "/recent", scopeMailRead, false},
	{"GET", "/folder", scopeMailRead, false},
	{"GET", "/mailboxes", scopeMailRead, false},
	{"GET", "/buckets/imbox", scopeMailRead, false},
	{"GET", "/threads/t1", scopeMailRead, false},
	{"GET", "/messages/m1/body", scopeMailRead, false},
	{"GET", "/messages/m1/eml", scopeMailRead, false},
	{"GET", "/messages/m1/attachment/2", scopeMailRead, false},
	{"POST", "/messages/m1/action", scopeMailAct, false},
	{"POST", "/send", scopeMailSend, false},
	{"GET", "/outbox", scopeMailRead, false},
	// Cancel-within-undo rides the send lifecycle (agentMayUndo).
	{"DELETE", "/outbox/job1", scopeMailSend, false},
	{"POST", "/classify", scopeMailAct, false},
	{"GET", "/personal/export", scopeMailRead, false},
	// Token management resolves to agent:manage, but the allowlist stays
	// the outer boundary: an agent bearer is refused before scopes apply.
	{"GET", "/security/agent-tokens", scopeAgentManage, true},
	{"POST", "/security/agent-tokens", scopeAgentManage, true},
	{"DELETE", "/security/agent-tokens/tok1", scopeAgentManage, true},
	{"POST", "/security/agent-tokens/tok1/rotate", scopeAgentManage, true},
}

// seedAgentToken inserts a token row directly (scopes/expiry explicit),
// returning the raw Bearer value. Enforcement is what these suites prove,
// so minting through the API is reserved for the API-surface tests.
func seedAgentToken(t *testing.T, p productPG, scopes []string, expires any, prefix string) string {
	t.Helper()
	raw, err := opaqueToken(24)
	if err != nil {
		t.Fatal(err)
	}
	raw = prefix + raw
	if _, err := p.db.Exec(`INSERT INTO agent_tokens (user_id,name,token_hash,scopes,expires_at)
		VALUES ($1,'seed',$2,$3,$4)`, p.uid, tokenHash(raw), scopes, expires); err != nil {
		t.Fatalf("seed agent token: %v", err)
	}
	return raw
}

// agentServe drives one /api request through the real requireAgent
// middleware with an agent Bearer; status 200 means the stub handler was
// reached, i.e. authentication, allowlist, and scope all passed.
func (p productPG) agentServe(handler http.Handler, method, target, bearer string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	r.Header.Set("Authorization", "Bearer "+bearer)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

// problemTitle decodes an RFC 7807 body's title.
func problemTitle(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var prob struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &prob); err != nil {
		t.Fatalf("response is not an RFC 7807 problem: %q", w.Body.String())
	}
	return prob.Title
}

// Obligation 1: every allowlisted route × every scope combination,
// allow/deny exactly per the table — and the allowlist-blocked surface
// stays shut for every combination, scopes or none.
func TestIntegrationAgentScopeRouteTable(t *testing.T) {
	p := newProductPG(t)
	handler := p.app.requireAgent(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// All 16 subsets of the four scopes, one seeded token each.
	for mask := 0; mask < 1<<len(agentScopes); mask++ {
		var granted []string
		for i, s := range agentScopes {
			if mask&(1<<i) != 0 {
				granted = append(granted, s)
			}
		}
		if granted == nil {
			granted = []string{}
		}
		bearer := seedAgentToken(t, p, granted, nil, agentTokenPrefix)
		for _, c := range agentRouteScopeCases {
			w := p.agentServe(handler, c.method, "/api"+c.path, bearer)
			if c.agentBlocked {
				// The outer boundary: refused before any scope applies,
				// whatever the grant.
				if w.Code != http.StatusForbidden || problemTitle(t, w) != "Forbidden" {
					t.Fatalf("%s %q with granted=%v: status=%d title=%q, want 403 Forbidden (allowlist)",
						c.method, c.path, granted, w.Code, problemTitle(t, w))
				}
				continue
			}
			if agentScopeGrants(granted, c.scope) {
				if w.Code != http.StatusOK {
					t.Fatalf("%s %q with granted=%v should pass %v: status=%d (%s)",
						c.method, c.path, granted, c.scope, w.Code, w.Body.String())
				}
				continue
			}
			if w.Code != http.StatusForbidden {
				t.Fatalf("%s %q with granted=%v should be denied (needs %v): status=%d",
					c.method, c.path, granted, c.scope, w.Code)
			}
			if title := problemTitle(t, w); title != "Insufficient Token Scope" {
				t.Fatalf("%s %q denial title=%q, want %q", c.method, c.path, title, "Insufficient Token Scope")
			}
		}
	}
}

// mintResponse is the mint/rotate endpoint's JSON answer.
type mintResponse struct {
	OK        bool       `json:"ok"`
	Token     string     `json:"token"`
	Name      string     `json:"name"`
	Scopes    []string   `json:"scopes"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// mintAgentToken drives the real mint endpoint on a fresh session and
// returns the decoded response.
func mintAgentToken(t *testing.T, p productPG, session, body string) (mintResponse, *httptest.ResponseRecorder) {
	t.Helper()
	var out mintResponse
	w := httptest.NewRecorder()
	p.app.handleAgentTokens(w, ownerSession(http.MethodPost, "/api/security/agent-tokens", body, p.uid, session))
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("mint response: %v (%s)", err, w.Body.String())
		}
	}
	return out, w
}

// Obligation 2: minting with reduced scopes yields reduced access — today
// (pre-OPS-10) every token reaches everything the allowlist permits.
func TestIntegrationAgentReducedScopeMint(t *testing.T) {
	p := newProductPG(t)
	seedPassword(t, p, testPassword)
	session := seedSession(t, p, 0)
	handler := p.app.requireAgent(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	out, w := mintAgentToken(t, p, session, `{"name":"reader","scopes":["mail:read"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("reduced-scope mint status=%d (%s)", w.Code, w.Body.String())
	}
	if len(out.Scopes) != 1 || out.Scopes[0] != scopeMailRead {
		t.Fatalf("mint echoed scopes=%v, want [%s]", out.Scopes, scopeMailRead)
	}
	if out.ExpiresAt != nil {
		t.Fatalf("mint without expires_at got %v, want null", out.ExpiresAt)
	}
	if !strings.HasPrefix(out.Token, agentTokenPrefix) {
		t.Fatalf("minted token %q lacks the %s prefix", out.Token, agentTokenPrefix)
	}
	if w := p.agentServe(handler, http.MethodGet, "/api/recent", out.Token); w.Code != http.StatusOK {
		t.Fatalf("mail:read token denied a read: %d (%s)", w.Code, w.Body.String())
	}
	for _, c := range []struct{ method, target string }{
		{http.MethodPost, "/api/send"},
		{http.MethodPost, "/api/classify"},
		{http.MethodPost, "/api/notes"},
		{http.MethodDelete, "/api/outbox/job1"},
	} {
		w := p.agentServe(handler, c.method, c.target, out.Token)
		if w.Code != http.StatusForbidden || problemTitle(t, w) != "Insufficient Token Scope" {
			t.Fatalf("mail:read token allowed %s %q: status=%d (%s)", c.method, c.target, w.Code, w.Body.String())
		}
	}
	// The outer boundary is unchanged: no scope reaches the auth surface.
	if w := p.agentServe(handler, http.MethodGet, "/api/security", out.Token); w.Code != http.StatusForbidden {
		t.Fatalf("reduced token reached /security: %d", w.Code)
	}

	// Defaults are byte-for-byte today's trust: all four scopes, no expiry.
	full, w := mintAgentToken(t, p, session, `{"name":"full"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("default mint status=%d (%s)", w.Code, w.Body.String())
	}
	if len(full.Scopes) != len(agentScopes) {
		t.Fatalf("default mint scopes=%v, want all four", full.Scopes)
	}
	for _, s := range agentScopes {
		if !agentScopeGrants(full.Scopes, s) {
			t.Fatalf("default mint missing %s: %v", s, full.Scopes)
		}
	}
	if w := p.agentServe(handler, http.MethodPost, "/api/send", full.Token); w.Code != http.StatusOK {
		t.Fatalf("full-scope token denied a send: %d (%s)", w.Code, w.Body.String())
	}

	// Validation: unknown scopes and bad/past expiry refuse the mint.
	for _, c := range []struct{ body, title string }{
		{`{"name":"x","scopes":["mail:root"]}`, "Invalid Scopes"},
		{`{"name":"x","scopes":["mail:read","nope"]}`, "Invalid Scopes"},
		{`{"name":"x","expires_at":"not-a-timestamp"}`, "Invalid Expiry"},
		{`{"name":"x","expires_at":"2020-01-01T00:00:00Z"}`, "Invalid Expiry"},
		{`{"scopes":[`, "Invalid Request"},
	} {
		_, w := mintAgentToken(t, p, session, c.body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status=%d, want 400 (%s)", c.body, w.Code, w.Body.String())
		}
		if title := problemTitle(t, w); title != c.title {
			t.Fatalf("body %q: title=%q, want %q", c.body, title, c.title)
		}
	}

	// The list shows scopes, expiry, and rotation state — never a secret.
	w = httptest.NewRecorder()
	p.app.handleAgentTokens(w, ownerSession(http.MethodGet, "/api/security/agent-tokens", "", p.uid, session))
	if w.Code != http.StatusOK {
		t.Fatalf("list status=%d", w.Code)
	}
	var listed []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatalf("list response: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("listed %d tokens, want 2", len(listed))
	}
	for _, entry := range listed {
		if _, leaked := entry["token"]; leaked {
			t.Fatal("token list leaked the raw secret")
		}
		if _, leaked := entry["token_hash"]; leaked {
			t.Fatal("token list leaked the token hash")
		}
		if _, ok := entry["scopes"]; !ok {
			t.Fatalf("token list entry lacks scopes: %v", entry)
		}
		if _, ok := entry["expires_at"]; !ok {
			t.Fatalf("token list entry lacks expires_at: %v", entry)
		}
		if _, ok := entry["rotated_to"]; !ok {
			t.Fatalf("token list entry lacks rotation state: %v", entry)
		}
	}
}

// Obligations 3 and 6: expiry is checked at authentication — future expiry
// works, past expiry is a 401 whose title is distinct from both the
// unknown-token 401 and the scope-denial 403.
func TestIntegrationAgentTokenExpiry(t *testing.T) {
	p := newProductPG(t)
	seedPassword(t, p, testPassword)
	session := seedSession(t, p, 0)
	handler := p.app.requireAgent(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	out, w := mintAgentToken(t, p, session, `{"name":"short","expires_at":"`+expires+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expiring mint status=%d (%s)", w.Code, w.Body.String())
	}
	if out.ExpiresAt == nil || time.Until(*out.ExpiresAt) <= 0 {
		t.Fatalf("expiring mint echoed %v, want a future timestamp", out.ExpiresAt)
	}
	if w := p.agentServe(handler, http.MethodGet, "/api/recent", out.Token); w.Code != http.StatusOK {
		t.Fatalf("future expiry denied: %d (%s)", w.Code, w.Body.String())
	}

	// Time-travel the row past its expiry (no sleeping).
	if _, err := p.db.Exec(`UPDATE agent_tokens SET expires_at=now()-interval '1 minute' WHERE token_hash=$1`,
		tokenHash(out.Token)); err != nil {
		t.Fatal(err)
	}
	w = p.agentServe(handler, http.MethodGet, "/api/recent", out.Token)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expired token status=%d, want 401", w.Code)
	}
	if title := problemTitle(t, w); title != "Token Expired" {
		t.Fatalf("expired title=%q, want %q", title, "Token Expired")
	}
	// Expiry precedes the scope decision: even a full-scope token is 401.
	w = p.agentServe(handler, http.MethodPost, "/api/send", out.Token)
	if w.Code != http.StatusUnauthorized || problemTitle(t, w) != "Token Expired" {
		t.Fatalf("expired token on a mutation: %d %q", w.Code, problemTitle(t, w))
	}

	// The three refusals stay distinguishable: unknown 401, expired 401,
	// scope 403.
	unknown := p.agentServe(handler, http.MethodGet, "/api/recent", "lull_does-not-exist")
	if unknown.Code != http.StatusUnauthorized || problemTitle(t, unknown) != "Unauthorized" {
		t.Fatalf("unknown token: %d %q", unknown.Code, problemTitle(t, unknown))
	}
	reader := seedAgentToken(t, p, []string{scopeMailRead}, nil, agentTokenPrefix)
	denied := p.agentServe(handler, http.MethodPost, "/api/send", reader)
	if denied.Code != http.StatusForbidden || problemTitle(t, denied) != "Insufficient Token Scope" {
		t.Fatalf("scope denial: %d %q", denied.Code, problemTitle(t, denied))
	}
}

// rotateAgentToken drives the real rotate route through a mounted mux (the
// handler reads the {id} path value the router binds).
func rotateAgentToken(t *testing.T, p productPG, session, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	p.app.mountAgentTokens(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, ownerSession(http.MethodPost, "/security/agent-tokens/"+id+"/rotate", body, p.uid, session))
	return w
}

// agentTokenID resolves a raw token to its row id.
func agentTokenID(t *testing.T, p productPG, raw string) string {
	t.Helper()
	var id string
	if err := p.db.QueryRow(`SELECT id::text FROM agent_tokens WHERE token_hash=$1`, tokenHash(raw)).Scan(&id); err != nil {
		t.Fatalf("token id: %v", err)
	}
	return id
}

// Obligation 4: the rotation lifecycle — successor live immediately,
// predecessor live within grace and dead after it (indistinguishable from
// revoked), one rotation at a time, defaults inherit, revoking the
// successor kills only the successor.
func TestIntegrationAgentTokenRotation(t *testing.T) {
	p := newProductPG(t)
	seedPassword(t, p, testPassword)
	session := seedSession(t, p, 0)
	handler := p.app.requireAgent(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// A short, controllable grace: no test sleeps ten minutes.
	originalGrace := agentRotationGrace
	agentRotationGrace = 90 * time.Second
	t.Cleanup(func() { agentRotationGrace = originalGrace })

	mint, w := mintAgentToken(t, p, session, `{"name":"rotating"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("mint status=%d", w.Code)
	}
	predecessor := mint.Token

	// Unknown id and another user's rows are 404, not a rotation.
	if w := rotateAgentToken(t, p, session, "00000000-0000-0000-0000-000000000000", ""); w.Code != http.StatusNotFound {
		t.Fatalf("rotate unknown id: %d (%s)", w.Code, w.Body.String())
	}

	w = rotateAgentToken(t, p, session, agentTokenID(t, p, predecessor), "")
	if w.Code != http.StatusOK {
		t.Fatalf("rotate status=%d (%s)", w.Code, w.Body.String())
	}
	var successorOut struct {
		OK      bool       `json:"ok"`
		Token   string     `json:"token"`
		Name    string     `json:"name"`
		Scopes  []string   `json:"scopes"`
		Expires *time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &successorOut); err != nil {
		t.Fatalf("rotate response: %v (%s)", err, w.Body.String())
	}
	successor := successorOut.Token

	// The predecessor row carries the linkage and the grace window.
	var rotatedTo sql.NullString
	var graceUntil time.Time
	var graceSet bool
	if err := p.db.QueryRow(`SELECT rotated_to::text, rotated_grace_until IS NOT NULL, rotated_grace_until
		FROM agent_tokens WHERE token_hash=$1`, tokenHash(predecessor)).Scan(&rotatedTo, &graceSet, &graceUntil); err != nil {
		t.Fatal(err)
	}
	if !graceSet || rotatedTo.String != agentTokenID(t, p, successor) {
		t.Fatalf("predecessor linkage = %q grace=%v", rotatedTo.String, graceUntil)
	}
	if d := time.Until(graceUntil); d < agentRotationGrace-time.Minute || d > agentRotationGrace+time.Minute {
		t.Fatalf("grace window = %v, want ~%v", d, agentRotationGrace)
	}

	// Successor works immediately; predecessor still works within grace.
	if w := p.agentServe(handler, http.MethodGet, "/api/recent", successor); w.Code != http.StatusOK {
		t.Fatalf("successor denied: %d (%s)", w.Code, w.Body.String())
	}
	if w := p.agentServe(handler, http.MethodGet, "/api/recent", predecessor); w.Code != http.StatusOK {
		t.Fatalf("predecessor denied inside grace: %d (%s)", w.Code, w.Body.String())
	}

	// Double rotation refuses — the predecessor is superseded.
	w = rotateAgentToken(t, p, session, agentTokenID(t, p, predecessor), "")
	if w.Code != http.StatusConflict {
		t.Fatalf("double rotation status=%d, want 409 (%s)", w.Code, w.Body.String())
	}
	if title := problemTitle(t, w); title != "Token Superseded" {
		t.Fatalf("double rotation title=%q", title)
	}

	// Past grace, the predecessor is dead — and indistinguishable from a
	// revoked token (the plain unauthorized 401, not Token Expired).
	if _, err := p.db.Exec(`UPDATE agent_tokens SET rotated_grace_until=now()-interval '1 second' WHERE token_hash=$1`,
		tokenHash(predecessor)); err != nil {
		t.Fatal(err)
	}
	pastGrace := p.agentServe(handler, http.MethodGet, "/api/recent", predecessor)
	if pastGrace.Code != http.StatusUnauthorized {
		t.Fatalf("predecessor past grace status=%d, want 401", pastGrace.Code)
	}
	if title := problemTitle(t, pastGrace); title != "Unauthorized" {
		t.Fatalf("predecessor past grace title=%q, want the revoked-indistinguishable %q", title, "Unauthorized")
	}

	// Defaults inherit: scopes and expiry carry to the successor unless
	// the rotation body overrides them.
	expires := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	scoped, w := mintAgentToken(t, p, session, `{"name":"scoped","scopes":["mail:read","mail:send"],"expires_at":"`+expires+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("scoped mint status=%d", w.Code)
	}
	w = rotateAgentToken(t, p, session, agentTokenID(t, p, scoped.Token), "")
	if w.Code != http.StatusOK {
		t.Fatalf("scoped rotate status=%d (%s)", w.Code, w.Body.String())
	}
	var inherited struct {
		Token   string     `json:"token"`
		Scopes  []string   `json:"scopes"`
		Expires *time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &inherited); err != nil {
		t.Fatalf("scoped rotate response: %v", err)
	}
	if len(inherited.Scopes) != 2 || !agentScopeGrants(inherited.Scopes, scopeMailRead) || !agentScopeGrants(inherited.Scopes, scopeMailSend) {
		t.Fatalf("successor scopes=%v, want the predecessor's [mail:read mail:send]", inherited.Scopes)
	}
	if inherited.Expires == nil || time.Until(*inherited.Expires) < time.Hour {
		t.Fatalf("successor expiry=%v, want the predecessor's ~2h", inherited.Expires)
	}
	// The inherited grant is enforced, not just echoed: no mail:act.
	if w := p.agentServe(handler, http.MethodPost, "/api/classify", inherited.Token); w.Code != http.StatusForbidden {
		t.Fatalf("inherited-scope successor allowed a mutation: %d", w.Code)
	}
	// An explicit override replaces the inherited scopes.
	w = rotateAgentToken(t, p, session, agentTokenID(t, p, inherited.Token), `{"scopes":["mail:act"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("override rotate status=%d (%s)", w.Code, w.Body.String())
	}
	var overridden struct {
		Token  string   `json:"token"`
		Scopes []string `json:"scopes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &overridden); err != nil {
		t.Fatal(err)
	}
	if len(overridden.Scopes) != 1 || overridden.Scopes[0] != scopeMailAct {
		t.Fatalf("overridden scopes=%v, want [mail:act]", overridden.Scopes)
	}

	// Revoking the successor kills only the successor: the predecessor
	// inside its grace keeps working, and stays un-rotatable forever.
	live, w := mintAgentToken(t, p, session, `{"name":"pair"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("pair mint status=%d", w.Code)
	}
	w = rotateAgentToken(t, p, session, agentTokenID(t, p, live.Token), "")
	if w.Code != http.StatusOK {
		t.Fatalf("pair rotate status=%d", w.Code)
	}
	var pair struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &pair); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	p.app.mountAgentTokens(mux)
	dw := httptest.NewRecorder()
	mux.ServeHTTP(dw, ownerSession(http.MethodDelete, "/security/agent-tokens/"+agentTokenID(t, p, pair.Token), "", p.uid, session))
	if dw.Code != http.StatusOK {
		t.Fatalf("revoke successor status=%d (%s)", dw.Code, dw.Body.String())
	}
	if w := p.agentServe(handler, http.MethodGet, "/api/recent", pair.Token); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked successor status=%d, want 401", w.Code)
	}
	if w := p.agentServe(handler, http.MethodGet, "/api/recent", live.Token); w.Code != http.StatusOK {
		t.Fatalf("predecessor died with its successor: %d (%s)", w.Code, w.Body.String())
	}
	// ON DELETE SET NULL clears rotated_to, but the grace tombstone keeps
	// the superseded token from ever rotating again.
	if w := rotateAgentToken(t, p, session, agentTokenID(t, p, live.Token), ""); w.Code != http.StatusConflict {
		t.Fatalf("re-rotating a token whose successor was revoked: %d, want 409", w.Code)
	}
}

// Obligation 5: the legacy proof. A row shaped exactly like pre-v12 (no
// scopes, no expiry — here under the pre-rename es_ prefix) is seeded, the
// real migration runner applies v12 around it, and the token comes out
// full-scope with no expiry, forever.
func TestIntegrationAgentLegacyRowFullScopeAfterMigration(t *testing.T) {
	p := newProductPG(t)

	// Roll the schema back to v11 for agent_tokens only: drop the v12
	// columns and forget the ledger row, then re-run the real runner.
	for _, col := range []string{"rotated_to", "rotated_grace_until", "expires_at", "scopes"} {
		if _, err := p.db.Exec(`ALTER TABLE agent_tokens DROP COLUMN ` + col); err != nil {
			t.Fatalf("drop %s: %v", col, err)
		}
	}
	if _, err := p.db.Exec(`DELETE FROM app_migrations WHERE version=12`); err != nil {
		t.Fatal(err)
	}

	// The pre-v12 INSERT shape: user, name, hash — nothing else exists.
	raw, err := opaqueToken(24)
	if err != nil {
		t.Fatal(err)
	}
	raw = legacyAgentTokenPrefix + raw
	if _, err := p.db.Exec(`INSERT INTO agent_tokens (user_id,name,token_hash) VALUES ($1,'legacy',$2)`,
		p.uid, tokenHash(raw)); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	if err := runProductMigrations(context.Background(), p.db); err != nil {
		t.Fatalf("v12 re-apply: %v", err)
	}

	var scopes agentScopesValue
	var expires, grace sql.NullTime
	if err := p.db.QueryRow(`SELECT scopes, expires_at, rotated_grace_until FROM agent_tokens WHERE token_hash=$1`,
		tokenHash(raw)).Scan(&scopes, &expires, &grace); err != nil {
		t.Fatal(err)
	}
	if len(scopes) != len(agentScopes) {
		t.Fatalf("legacy scopes=%v, want all four", scopes)
	}
	for _, s := range agentScopes {
		if !agentScopeGrants(scopes, s) {
			t.Fatalf("legacy row missing %s: %v", s, scopes)
		}
	}
	if expires.Valid || grace.Valid {
		t.Fatalf("legacy row gained expiry=%v grace=%v, want none", expires.Time, grace.Time)
	}

	// Full-scope, no expiry, forever: reads and mutations both pass, and
	// the old es_ prefix still authenticates.
	handler := p.app.requireAgent(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	if w := p.agentServe(handler, http.MethodGet, "/api/recent", raw); w.Code != http.StatusOK {
		t.Fatalf("legacy token denied a read: %d (%s)", w.Code, w.Body.String())
	}
	if w := p.agentServe(handler, http.MethodPost, "/api/send", raw); w.Code != http.StatusOK {
		t.Fatalf("legacy token denied a send: %d (%s)", w.Code, w.Body.String())
	}
	if w := p.agentServe(handler, http.MethodGet, "/api/security", raw); w.Code != http.StatusForbidden {
		t.Fatalf("legacy token reached the security surface: %d", w.Code)
	}
}
