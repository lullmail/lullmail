package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentScopeFence(t *testing.T) {
	allowed := []string{
		"/accounts", "/accounts/x/sync", "/mail/whatever", "/screener",
		"/screener/decide", "/screener/undecide", "/counts", "/briefing",
		"/threads/t1", "/messages/m1/action", "/send", "/buckets/imbox",
		"/notes", "/people", "/personal/export",
	}
	for _, path := range allowed {
		if !agentAllowedPath(path) {
			t.Fatalf("agentAllowedPath(%q) = false, want true", path)
		}
	}
	blocked := []string{
		"/auth/status", "/security", "/security/passkeys/begin",
		"/security/agent-tokens", "/security/sessions", "/security/totp",
		"/account", "/push", "/oauth/gmail/start", "/board/../security",
	}
	for _, path := range blocked {
		if agentAllowedPath(path) {
			t.Fatalf("agentAllowedPath(%q) = true, want false", path)
		}
	}
}

func TestAgentTokenEntersThroughPrefix(t *testing.T) {
	// Only es_-prefixed Bearer values take the agent path; anything else
	// (sessions, the bootstrap token) falls through to the session handler.
	req := httptest.NewRequest(http.MethodGet, "/api/accounts", nil)
	req.Header.Set("Authorization", "Bearer es_something")
	if !hasAgentBearer(req) {
		t.Fatal("es_ bearer not detected")
	}
	req.Header.Set("Authorization", "Bearer qxDNXub8-setup-token")
	if hasAgentBearer(req) {
		t.Fatal("non-agent bearer took the agent path")
	}
	req.Header.Del("Authorization")
	if hasAgentBearer(req) {
		t.Fatal("missing bearer took the agent path")
	}
}

func TestConstantTimeAgentTokenCompare(t *testing.T) {
	if !constantTimeAgentTokenCompare("es_a", "es_a") {
		t.Fatal("equal tokens did not match")
	}
	if constantTimeAgentTokenCompare("es_a", "es_b") {
		t.Fatal("different tokens matched")
	}
}

var _ = http.StatusOK
