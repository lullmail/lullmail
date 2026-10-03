package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func readOutboxState(t *testing.T, p productPG, id string) (state, code, payload string) {
	t.Helper()
	if err := p.db.QueryRow(`SELECT state,error_code,payload_ciphertext FROM outbox_jobs WHERE id=$1`, id).Scan(&state, &code, &payload); err != nil {
		t.Fatal(err)
	}
	return
}

func dueNow(t *testing.T, p productPG, id string) {
	t.Helper()
	if _, err := p.db.Exec(`UPDATE outbox_jobs SET undo_until=now()-interval '1 second' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
}

func detail(p productPG, app *App, id string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/api/outbox/"+id, nil)
	r.SetPathValue("id", id)
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
	w := httptest.NewRecorder()
	app.handleOutboxDetail(w, r)
	return w
}

// A changed or missing SECRET_KEY must surface as a visible, recoverable
// failure: nothing is sent, the ciphertext is kept, restoring the key brings
// the composition back, and nothing ever retries it automatically.
func TestIntegrationOutboxChangedKeyFailsClosedAndRecovers(t *testing.T) {
	for name, wrong := range map[string]*Config{"changed key": {SecretKey: "a-different-secret-key-value-123456"}, "missing key": {}} {
		t.Run(name, func(t *testing.T) {
			p := newProductPG(t)
			seedOutboxAccount(t, p)
			row := seedOutbox(t, p, "key-change")
			dueNow(t, p, row.ID)
			restarted := &App{db: p.db, cfg: wrong, log: discardLogger()}
			if err := restarted.processOutbox(context.Background()); err != nil {
				t.Fatal(err)
			}
			state, code, payload := readOutboxState(t, p, row.ID)
			if state != "failed" || code != "payload_key_unavailable" || payload == "" {
				t.Fatalf("state=%s code=%s payload kept=%v", state, code, payload != "")
			}
			if w := detail(p, restarted, row.ID); w.Code != 503 || !strings.Contains(w.Body.String(), "restore the key") {
				t.Fatalf("detail under the wrong key: %d %s", w.Code, w.Body.String())
			}
			// Further passes never pick the failed row up again.
			for i := 0; i < 3; i++ {
				if err := restarted.processOutbox(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if state, _, _ = readOutboxState(t, p, row.ID); state != "failed" {
				t.Fatalf("failed row was re-run: %s", state)
			}
			// Restoring the original key makes the saved composition readable.
			if w := detail(p, p.app, row.ID); w.Code != 200 || !strings.Contains(w.Body.String(), "synthetic") {
				t.Fatalf("detail after restoring the key: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

// A ciphertext moved between rows must not decrypt: the worker fails it
// closed instead of sending another message's content.
func TestIntegrationOutboxCiphertextIsBoundToItsRow(t *testing.T) {
	p := newProductPG(t)
	seedOutboxAccount(t, p)
	a := seedOutbox(t, p, "row-a")
	b := seedOutbox(t, p, "row-b")
	if _, err := p.db.Exec(`UPDATE outbox_jobs SET payload_ciphertext=(SELECT payload_ciphertext FROM outbox_jobs WHERE id=$1) WHERE id=$2`, a.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	dueNow(t, p, b.ID)
	if err := p.app.processOutbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state, code, _ := readOutboxState(t, p, b.ID); state != "failed" || code != "payload_corrupt" {
		t.Fatalf("swapped ciphertext: state=%s code=%s", state, code)
	}
	if w := detail(p, p.app, b.ID); w.Code != 500 {
		t.Fatalf("swapped ciphertext was served: %d %s", w.Code, w.Body.String())
	}
}
