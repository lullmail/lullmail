package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/neutron-build/neutron/mail"
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
	p.app.acceptOutbox(w, httptest.NewRequest("POST", "/api/send", nil), p.uid, "outbox-acct", "", "", &emptyOutgoing, []byte(`{"text":"x"}`))
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

// The Sent-copy filing claim carries the same schema fence as the
// submission claim (LUL-B02): the worker checks the ledger once per
// sweep, and a newer migration can commit between that check and this
// claim — an unfenced filing claim would let an old build APPEND a saved
// copy a newer build owns, both from dispatch and from the
// submission-completion path that claims by row id.
func TestIntegrationOutboxFencedWorkerCannotClaimSentFiling(t *testing.T) {
	p := newProductPG(t)
	seedOutboxAccount(t, p)
	if _, err := p.db.Exec(`INSERT INTO mail_mailboxes(account_id,id,name,role,native) VALUES('outbox-acct','Sent','Sent','sent','Sent')`); err != nil {
		t.Fatal(err)
	}
	submitted := seedOutbox(t, p, "filing")
	sent, err := sealBound(p.cfg, "sent", p.uid, submitted.ID, "Subject: fenced\r\n\r\nbody\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(`UPDATE outbox_jobs SET state='submitted',filing_state='pending',sent_ciphertext=$2,payload_ciphertext='',payload_bytes=octet_length($2) WHERE id=$1`, submitted.ID, sent); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(`INSERT INTO app_migrations(version,name,checksum,applied_at) VALUES($1,'a newer build',x'00'::text,now())`, outboxSupportedSchema()+1); err != nil {
		t.Fatal(err)
	}
	filingState := func() string {
		var state, filing string
		if err := p.db.QueryRow(`SELECT state,filing_state FROM outbox_jobs WHERE id=$1`, submitted.ID).Scan(&state, &filing); err != nil {
			t.Fatal(err)
		}
		return state + "/" + filing
	}

	// The oldest-waiting filing claim refuses and leaves the copy pending
	// for the capable worker.
	if _, err := p.app.claimSentCopy(context.Background(), "", []string{}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("a fenced build claimed a Sent filing: %v", err)
	}
	if got := filingState(); got != "submitted/pending" {
		t.Fatalf("refused filing claim changed the row: %s", got)
	}
	// The specific-row claim the submission-completion path makes is
	// fenced by the same predicate.
	if _, err := p.app.claimSentCopy(context.Background(), submitted.ID, []string{}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("a fenced build claimed its own Sent filing: %v", err)
	}
	if got := filingState(); got != "submitted/pending" {
		t.Fatalf("refused row claim changed the row: %s", got)
	}

	// Interleave: this pass's initial sweep already ran when the newer
	// migration committed; the submission claim is fenced, and dispatch
	// must not fall through to the filing claim and touch the provider.
	dials := 0
	p.app.dial = func(context.Context, mail.AccountID, mail.Credential) (mail.Adapter, func(), error) {
		dials++
		return nil, nil, errors.New("a fenced build must not dial a provider")
	}
	if err := p.app.processOutbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if dials != 0 {
		t.Fatalf("fenced worker reached the provider for a Sent filing: %d dials", dials)
	}
	if got := filingState(); got != "submitted/pending" {
		t.Fatalf("fenced worker disturbed the waiting filing: %s", got)
	}

	// When the ledger no longer holds a newer build, exactly one filing
	// claim succeeds and the next refuses.
	if _, err := p.db.Exec(`DELETE FROM app_migrations WHERE version>$1`, outboxSupportedSchema()); err != nil {
		t.Fatal(err)
	}
	if _, err := p.app.claimSentCopy(context.Background(), "", []string{}); err != nil {
		t.Fatalf("unfenced build could not claim the filing: %v", err)
	}
	if _, err := p.app.claimSentCopy(context.Background(), "", []string{}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("a second filing claim succeeded for a row already being filed")
	}
}
