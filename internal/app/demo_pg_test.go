package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// In demo mode every visitor is the made-up owner: no cookie, no token. The
// status endpoint says so, the setup wizard is never offered, and the auth
// wrapper refuses everything off the allowlist before a handler runs.
func TestIntegrationDemoModeNeedsNoSignInAndRefusesWhatLeavesTheShowroom(t *testing.T) {
	p := newProductPG(t)
	p.cfg.Demo = true
	p.cfg.APIToken = "a-setup-token-that-must-not-matter"

	w := httptest.NewRecorder()
	p.app.handleAuthStatus(w, jsonBody(t, http.MethodGet, "/api/auth/status", ""))
	var status map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}
	if status["demo"] != true || status["authenticated"] != true || status["configured"] != true || status["bootstrap_available"] != false {
		t.Fatalf("demo status = %v", status)
	}

	reached := false
	guarded := p.app.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		if uid, err := p.app.userID(r.Context()); err != nil || uid != p.uid {
			t.Errorf("handler saw owner %q (%v), want the demo owner", uid, err)
		}
	}))
	call := func(method, path string) int {
		reached = false
		rec := httptest.NewRecorder()
		guarded.ServeHTTP(rec, jsonBody(t, method, path, "{}"))
		if rec.Code == http.StatusForbidden && !strings.Contains(rec.Body.String(), "demo") {
			t.Errorf("%s %s refused without saying why: %s", method, path, rec.Body.String())
		}
		return rec.Code
	}
	if code := call(http.MethodGet, "/buckets/imbox"); code != http.StatusOK || !reached {
		t.Errorf("reading the Inbox: %d reached=%v", code, reached)
	}
	if code := call(http.MethodPost, "/messages/m-1/action"); code != http.StatusOK || !reached {
		t.Errorf("filing a thread: %d reached=%v", code, reached)
	}
	for _, refused := range [][2]string{
		{http.MethodPost, "/send"}, {http.MethodPost, "/accounts"}, {http.MethodPost, "/notes"},
		{http.MethodPost, "/security/password"}, {http.MethodDelete, "/account"}, {http.MethodGet, "/accounts/a/export"},
	} {
		if code := call(refused[0], refused[1]); code != http.StatusForbidden || reached {
			t.Errorf("%s %s: %d reached=%v, want 403 before the handler", refused[0], refused[1], code, reached)
		}
	}

	// And outside demo mode none of this applies: no credential, no entry.
	p.cfg.Demo = false
	if code := call(http.MethodGet, "/buckets/imbox"); code != http.StatusUnauthorized {
		t.Errorf("without demo mode an anonymous read answered %d, want 401", code)
	}
}
