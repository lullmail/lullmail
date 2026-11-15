package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The demo is an allowlist. This table is the contract: a route added to
// the product is refused in the demo until someone decides it is harmless.
func TestDemoAllowsOnlyTheShowroomActions(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/briefing", true},
		{http.MethodGet, "/buckets/imbox", true},
		{http.MethodGet, "/thread", true},
		{http.MethodGet, "/accounts", true},
		{http.MethodPost, "/messages/m-1/action", true},
		{http.MethodPost, "/screener/decide", true},
		{http.MethodPost, "/screener/undecide", true},
		{http.MethodPost, "/board/pin", true},
		{http.MethodPost, "/board/unpin", true},
		{http.MethodPost, "/board/cards/c-1/done", true},
		{http.MethodPost, "/api/messages/m-1/action", true},

		// Leaves the database, or costs real work.
		{http.MethodPost, "/send", false},
		{http.MethodPost, "/messages/m-1/trash", false},
		{http.MethodPost, "/accounts", false},
		{http.MethodPost, "/accounts/a-1", false},
		{http.MethodDelete, "/accounts/a-1", false},
		{http.MethodGet, "/accounts/a-1/export", false},
		{http.MethodGet, "/export", false},
		{http.MethodPost, "/oauth/google/start", false},
		{http.MethodPost, "/push", false},
		// Credentials and tokens.
		{http.MethodPost, "/security/password", false},
		{http.MethodPost, "/security/agent-tokens", false},
		{http.MethodDelete, "/account", false},
		// A visitor's own words, shown to the next visitor.
		{http.MethodPost, "/notes", false},
		{http.MethodPost, "/notes/n-1", false},
		{http.MethodDelete, "/notes/n-1", false},
		{http.MethodPost, "/board/cards", false},
		// Changes the product for everyone until the next reset.
		{http.MethodPost, "/prefs", false},
		{http.MethodPut, "/messages/m-1/action", false},
	}
	for _, c := range cases {
		if got := demoAllows(httptest.NewRequest(c.method, c.path, nil)); got != c.want {
			t.Errorf("%s %s: allowed=%v, want %v", c.method, c.path, got, c.want)
		}
	}
}
