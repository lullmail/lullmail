package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"testing"
)

// A newer build that has migrated the database owns outbox work. This build
// must stop admitting and claiming at once, without being restarted.
func TestIntegrationOutboxOlderBuildIsFencedByNewerSchema(t *testing.T) {
	p := newProductPG(t)
	seedOutboxAccount(t, p)
	due := seedOutbox(t, p, "due")
	dueNow(t, p, due.ID)
	stale := seedOutbox(t, p, "stale-claim")
	if _, err := p.db.Exec(`UPDATE outbox_jobs SET state='submitting',started_at=now()-interval '10 minutes' WHERE id=$1`, stale.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := p.db.Exec(`INSERT INTO app_migrations(version,name,checksum,applied_at) VALUES($1,'a newer build',x'00'::text,now())`, outboxSupportedSchema()+1); err != nil {
		t.Fatal(err)
	}

	if _, _, err := p.app.saveOutbox(context.Background(), p.uid, "outbox-acct", "late", "h", "11111111-1111-1111-1111-111111111111", "x", 1); !errors.Is(err, errOutboxFenced) {
		t.Fatalf("a fenced build admitted work: %v", err)
	}
	var rows int
	p.db.QueryRow(`SELECT count(*) FROM outbox_jobs WHERE submission_key='late'`).Scan(&rows)
	if rows != 0 {
		t.Fatal("a fenced build left a row behind")
	}
	// A retry of an already-accepted key is read-only and still answered.
	if _, replay, err := p.app.saveOutbox(context.Background(), p.uid, "outbox-acct", "due", "hash", "22222222-2222-2222-2222-222222222222", "x", 1); err != nil || !replay {
		t.Fatalf("replay while fenced: replay=%v err=%v", replay, err)
	}
	if _, _, _, err := p.app.claimOutbox(context.Background()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("a fenced build claimed work: %v", err)
	}
	if err := p.app.processOutbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := readOutboxState(t, p, due.ID); state != "pending" {
		t.Fatalf("fenced build changed a due job: %s", state)
	}
	if state, _, _ := readOutboxState(t, p, stale.ID); state != "submitting" {
		t.Fatalf("fenced build swept a claim it does not own: %s", state)
	}
	w := httptest.NewRecorder()
	p.app.acceptOutbox(w, httptest.NewRequest("POST", "/api/send", nil), p.uid, "outbox-acct", "", &emptyOutgoing, []byte(`{"text":"x"}`))
	if w.Code != 503 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("fenced acceptance answered %d, want a retryable 503", w.Code)
	}

	// When the ledger no longer holds a newer build, ownership returns.
	if _, err := p.db.Exec(`DELETE FROM app_migrations WHERE version>$1`, outboxSupportedSchema()); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := p.app.claimOutbox(context.Background()); err != nil {
		t.Fatalf("unfenced build could not claim: %v", err)
	}
}
