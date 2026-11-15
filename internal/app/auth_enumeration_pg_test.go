package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The README promises that a wrong name and a wrong secret are
// indistinguishable. These tests pin the three response paths the
// 2026-11-14 review found telling them apart (see AUDIT_OPEN.md,
// "Independent review round (2026-11-14)").

func problemOf(t *testing.T, w *httptest.ResponseRecorder) (int, string, string) {
	t.Helper()
	var body struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("not a problem document: %s", w.Body.String())
	}
	return w.Code, body.Title, body.Detail
}

// A recovery attempt against a name that does not exist reads exactly like
// one against the owner with a wrong code.
func TestIntegrationRecoveryLoginDoesNotRevealWhetherTheNameExists(t *testing.T) {
	p := newProductPG(t)
	attempt := func(ident string) (int, string, string) {
		w := httptest.NewRecorder()
		p.app.handleRecoveryLogin(w, jsonBody(t, http.MethodPost, "/api/auth/recovery",
			`{"email":"`+ident+`","code":"not-a-real-code"}`))
		return problemOf(t, w)
	}
	knownCode, knownTitle, knownDetail := attempt("owner@example.com")
	ghostCode, ghostTitle, ghostDetail := attempt("nobody@example.com")
	if knownCode != http.StatusUnauthorized {
		t.Fatalf("wrong code for the owner answered %d", knownCode)
	}
	if knownCode != ghostCode || knownTitle != ghostTitle || knownDetail != ghostDetail {
		t.Fatalf("the two answers differ:\n  owner:   %d %q %q\n  unknown: %d %q %q",
			knownCode, knownTitle, knownDetail, ghostCode, ghostTitle, ghostDetail)
	}
}

// With every argon2 slot taken, a real name cannot verify and answers 429
// "Busy". An unknown name used to skip its dummy verify and answer 401 —
// so four concurrent requests told a flooder which names exist.
func TestIntegrationPasswordLoginAnswersBusyForKnownAndUnknownNamesAlike(t *testing.T) {
	p := newProductPG(t)
	seedPassword(t, p, testPassword)

	var held []func()
	for {
		release, err := acquirePasswordWork()
		if err != nil {
			break
		}
		held = append(held, release)
	}
	t.Cleanup(func() {
		for _, release := range held {
			release()
		}
	})

	attempt := func(ident string) (int, string, string, string) {
		w := httptest.NewRecorder()
		p.app.handlePasswordLogin(w, jsonBody(t, http.MethodPost, "/api/auth/password",
			`{"email":"`+ident+`","password":"wrong-password"}`))
		code, title, detail := problemOf(t, w)
		return code, title, detail, w.Header().Get("Retry-After")
	}
	kc, kt, kd, kr := attempt("owner@example.com")
	gc, gt, gd, gr := attempt("nobody@example.com")
	if kc != http.StatusTooManyRequests || kt != "Busy" {
		t.Fatalf("a saturated verify for the owner answered %d %q", kc, kt)
	}
	if kc != gc || kt != gt || kd != gd || kr != gr {
		t.Fatalf("the two answers differ:\n  owner:   %d %q %q retry=%q\n  unknown: %d %q %q retry=%q", kc, kt, kd, kr, gc, gt, gd, gr)
	}
	// Neither request did any verification work, so neither is a failed
	// attempt: a flood must not also lock the owner out.
	if n := len(p.app.pwFails); n != 0 {
		t.Fatalf("a Busy answer recorded %d failed attempt(s)", n)
	}
}

// The first-run setup routes sit behind the per-host limiter: ten wrong
// setup tokens from one host and the eleventh request is refused before the
// token is even compared.
func TestIntegrationSetupTokenGuessesAreThrottled(t *testing.T) {
	p := newProductPG(t)
	p.cfg.APIToken = "the-real-setup-token"

	post := func(token string) int {
		r := jsonBody(t, http.MethodPost, "/api/auth/bootstrap/password", `{"name":"Owner","password":"`+testPassword+`"}`)
		r.RemoteAddr = "198.51.100.7:4000"
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		p.app.handleBootstrapPassword(w, r)
		return w.Code
	}
	for i := 1; i <= 10; i++ {
		if code := post("guess"); code != http.StatusUnauthorized {
			t.Fatalf("wrong token %d answered %d, want 401", i, code)
		}
	}
	if code := post("guess"); code != http.StatusTooManyRequests {
		t.Fatalf("the eleventh guess answered %d, want 429", code)
	}
	// Past the limit even the right token waits out the window.
	if code := post("the-real-setup-token"); code != http.StatusTooManyRequests {
		t.Fatalf("the right token after ten misses answered %d, want 429", code)
	}
}

// On every other route the setup token is checked in authenticateRequest.
// Misses are charged against the same limiter; hits are not, because the
// setup screens make many requests with the right token.
func TestIntegrationWrongSetupBearerIsChargedAndTheRightOneIsNot(t *testing.T) {
	p := newProductPG(t)
	p.cfg.APIToken = "the-real-setup-token"

	auth := func(host, token string) error {
		r := jsonBody(t, http.MethodGet, "/api/accounts", "")
		r.RemoteAddr = host + ":4000"
		r.Header.Set("Authorization", "Bearer "+token)
		_, _, err := p.app.authenticateRequest(r)
		return err
	}
	for i := 1; i <= 25; i++ {
		if err := auth("198.51.100.8", "the-real-setup-token"); err != nil {
			t.Fatalf("right token, request %d: %v", i, err)
		}
	}
	for i := 1; i <= 10; i++ {
		if err := auth("198.51.100.9", "guess"); err == nil {
			t.Fatalf("wrong token %d authenticated", i)
		}
	}
	if err := auth("198.51.100.9", "the-real-setup-token"); err == nil {
		t.Fatal("a host that spent its ten guesses still authenticated with the right token")
	}
	// Another host is unaffected.
	if err := auth("198.51.100.8", "the-real-setup-token"); err != nil {
		t.Fatalf("an unrelated host was throttled: %v", err)
	}
}
