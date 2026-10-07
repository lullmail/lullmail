package main

// LUL-D02 server side: a replayed offline mutation carries the offline
// namespace it was queued under (X-Lullmail-Owner). requireAuth compares
// it against the AUTHENTICATED session's identity before any handler,
// idempotency admission, or side effect, so queued work cannot be applied
// to a different signed-in owner even if the browser's cookie changed
// between the client's status confirmation and dispatch. Legacy clients
// send no header and are not enforced.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExpectedOwnerHeaderFencesReplayedMutations(t *testing.T) {
	p := newProductPG(t)
	handler := p.app.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	raw := "raw-owner-session"
	if _, err := p.db.Exec(`INSERT INTO auth_sessions
		(id_hash, user_id, expires_at, user_agent, login_method, auth_epoch, created_at)
		VALUES ($1, $2, now()+interval '1 day', 'test', 'password', 0, now())`,
		tokenHash(raw), p.uid); err != nil {
		t.Fatal(err)
	}
	instID, err := p.app.installationID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	namespace := instID + "/" + p.uid

	mutate := func(header string) int {
		r := httptest.NewRequest(http.MethodPost, "/api/notes/n1", strings.NewReader("{}"))
		if header != "" {
			r.Header.Set("X-Lullmail-Owner", header)
		}
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: raw})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}

	if code := mutate(""); code != http.StatusNoContent {
		t.Fatalf("legacy client without the header=%d", code)
	}
	if code := mutate(namespace); code != http.StatusNoContent {
		t.Fatalf("matching owner namespace=%d", code)
	}
	var email string
	if err := p.db.QueryRow(`SELECT email FROM users WHERE id=$1`, p.uid).Scan(&email); err != nil {
		t.Fatal(err)
	}
	if code := mutate(strings.ToUpper(email)); code != http.StatusNoContent {
		t.Fatalf("legacy email namespace=%d", code)
	}
	if code := mutate(instID + "/00000000-0000-0000-0000-000000000000"); code != http.StatusConflict {
		t.Fatalf("another owner's namespace=%d", code)
	}
	if code := mutate("someone-else@example.test"); code != http.StatusConflict {
		t.Fatalf("another owner's email namespace=%d", code)
	}

	// Display reads are not mutations: a stale header on a GET is ignored.
	r := httptest.NewRequest(http.MethodGet, "/api/buckets/imbox", nil)
	r.Header.Set("X-Lullmail-Owner", "wrong-owner")
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: raw})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("GET carrying a mismatched header=%d", w.Code)
	}
}

// The concrete cross-owner effect the fence prevents: a queued
// /screener/decide is owner-scoped by the SESSION, not by its payload
// (which carries only sender/allow/route). Executed against real SQL so
// the sender-rule upsert is demonstrated, not modeled.
func TestScreenerDecideAppliesToTheAuthenticatedOwner(t *testing.T) {
	p := newProductPG(t)
	r := httptest.NewRequest(http.MethodPost, "/api/screener/decide",
		strings.NewReader(`{"sender":"Sender@Example.Test","allow":true,"route":"feed"}`))
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
	w := httptest.NewRecorder()
	p.app.handleDecide(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("decide=%d %.200s", w.Code, w.Body.String())
	}
	var allowed bool
	var route string
	if err := p.db.QueryRow(`SELECT allowed, route FROM hey_senders WHERE user_id=$1 AND sender_key='sender@example.test'`, p.uid).Scan(&allowed, &route); err != nil {
		t.Fatalf("the decision did not land for the authenticated owner: %v", err)
	}
	if !allowed || route != "feed" {
		t.Fatalf("decision landed wrong: allowed=%v route=%s", allowed, route)
	}
}
