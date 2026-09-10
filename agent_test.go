package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentScopeFence(t *testing.T) {
	allowed := []string{
		"/accounts", "/accounts/x/sync", "/screener",
		"/screener/decide", "/screener/undecide", "/counts", "/briefing",
		"/threads/t1", "/messages/m1/action", "/send", "/buckets/imbox",
		"/notes", "/notes/x", "/board", "/board/pin", "/people", "/personal/export",
	}
	for _, path := range allowed {
		if !agentAllowedPath(path) {
			t.Fatalf("agentAllowedPath(%q) = false, want true", path)
		}
	}
	blocked := []string{
		"/auth/status", "/auth/password", "/security", "/security/passkeys/begin",
		"/security/agent-tokens", "/security/sessions", "/security/totp",
		"/security/password", "/account", "/push", "/oauth/gmail/start",
		"/board/../security", "/accounts/x/export",
		"/mail/v1/accounts", "/mail/v1/accounts/a/search",
	}
	for _, path := range blocked {
		if agentAllowedPath(path) {
			t.Fatalf("agentAllowedPath(%q) = true, want false", path)
		}
	}
}

func TestAgentTokenEntersThroughPrefix(t *testing.T) {
	// Agent-prefixed Bearers (lull_ and the pre-rename es_) take the agent
	// path; anything else (sessions, the bootstrap token) falls through to
	// the session handler.
	for _, prefix := range []string{"lull_", "es_"} {
		req := httptest.NewRequest(http.MethodGet, "/api/accounts", nil)
		req.Header.Set("Authorization", "Bearer "+prefix+"something")
		if !hasAgentBearer(req) {
			t.Fatalf("%s bearer not detected", prefix)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/accounts", nil)
	req.Header.Set("Authorization", "Bearer qxDNXub8-setup-token")
	if hasAgentBearer(req) {
		t.Fatal("non-agent bearer took the agent path")
	}
	req.Header.Del("Authorization")
	if hasAgentBearer(req) {
		t.Fatal("missing bearer took the agent path")
	}
}

var _ = http.StatusOK
