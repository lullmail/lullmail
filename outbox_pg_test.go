package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func seedOutboxAccount(t *testing.T, p productPG) {
	t.Helper()
	if _, err := p.db.Exec(`INSERT INTO email_accounts(user_id,mirror_account_id,provider,address,cred_ciphertext) VALUES($1,'outbox-acct','imap','sender@example.test','fixture')`, p.uid); err != nil {
		t.Fatal(err)
	}
}
func seedOutbox(t *testing.T, p productPG, key string) outboxRecord {
	t.Helper()
	payload, _ := json.Marshal(outboxPayload{Request: json.RawMessage(`{"to":"recipient@example.test","text":"synthetic"}`)})
	cipher, err := sealSecret(p.cfg, string(payload))
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := p.app.saveOutbox(context.Background(), p.uid, "outbox-acct", key, "hash", cipher, int64(len(cipher)))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestIntegrationOutboxConcurrentAcceptanceSurvivesRestart(t *testing.T) {
	p := newProductPG(t)
	seedOutboxAccount(t, p)
	const n = 16
	var wg sync.WaitGroup
	ids := make(chan string, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, _, e := p.app.saveOutbox(context.Background(), p.uid, "outbox-acct", "one-key", "hash", "encrypted-fixture", 20)
			if e != nil {
				errs <- e
				return
			}
			ids <- r.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	id := ""
	for got := range ids {
		if id == "" {
			id = got
		}
		if got != id {
			t.Fatal("same key acquired multiple ids")
		}
	}
	fresh := &App{db: p.db, cfg: p.cfg, log: discardLogger()}
	r, replay, err := fresh.saveOutbox(context.Background(), p.uid, "outbox-acct", "one-key", "hash", "not-used", 20)
	if err != nil || !replay || r.ID != id {
		t.Fatalf("restart replay=%+v %v %v", r, replay, err)
	}
	if _, _, err = fresh.saveOutbox(context.Background(), p.uid, "outbox-acct", "one-key", "different", "unused", 1); !errors.Is(err, errOutboxKeyConflict) {
		t.Fatal("key conflict accepted", err)
	}
}

func TestIntegrationOutboxOnlyOneWorkerClaimsAndNeverReclaims(t *testing.T) {
	p := newProductPG(t)
	seedOutboxAccount(t, p)
	row := seedOutbox(t, p, "claim")
	if _, err := p.db.Exec(`UPDATE outbox_jobs SET undo_until=now()-interval '1 second' WHERE id=$1`, row.ID); err != nil {
		t.Fatal(err)
	}
	const n = 8
	var wg sync.WaitGroup
	claims := make(chan outboxAttempt, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, _, _, err := p.app.claimOutbox(context.Background())
			if err == nil {
				claims <- j
			} else if !errors.Is(err, sql.ErrNoRows) {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(claims)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if len(claims) != 1 {
		t.Fatalf("%d workers claimed one job", len(claims))
	}
	if _, err := p.db.Exec(`UPDATE outbox_jobs SET state='ambiguous',started_at=now()-interval '10 minutes' WHERE id=$1`, row.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := p.app.claimOutbox(context.Background()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("ambiguous send was reclaimed: %v", err)
	}
}

func TestIntegrationOutboxUndoAndOwnerBoundaries(t *testing.T) {
	p := newProductPG(t)
	seedOutboxAccount(t, p)
	row := seedOutbox(t, p, "cancel")
	call := func(owner string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("DELETE", "/api/outbox/"+row.ID, nil)
		r.SetPathValue("id", row.ID)
		r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, owner))
		w := httptest.NewRecorder()
		p.app.cancelOutbox(w, r)
		return w
	}
	if w := call("00000000-0000-0000-0000-000000000000"); w.Code != 410 {
		t.Fatalf("other owner cancel=%d", w.Code)
	}
	for i := 0; i < 2; i++ {
		if w := call(p.uid); w.Code != 200 {
			t.Fatalf("cancel/retry=%d %s", w.Code, w.Body.String())
		}
	}
	if _, err := p.db.Exec(`UPDATE outbox_jobs SET undo_until=now()-interval '1 second' WHERE id=$1`, row.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := p.app.claimOutbox(context.Background()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("cancelled send claimed", err)
	}
	if _, err := p.db.Exec(`DELETE FROM email_accounts WHERE user_id=$1`, p.uid); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := p.db.QueryRow(`SELECT COUNT(*) FROM outbox_jobs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("account delete kept payloads: %d %v", count, err)
	}
}

func TestIntegrationOutboxSMTPAcceptanceSeparatesFiling(t *testing.T) {
	p := newProductPG(t)
	seedOutboxAccount(t, p)
	row := seedOutbox(t, p, "smtp")
	if _, err := p.db.Exec(`UPDATE outbox_jobs SET undo_until=now()-interval '1 second' WHERE id=$1`, row.ID); err != nil {
		t.Fatal(err)
	}
	job, _, _, err := p.app.claimOutbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("Message-ID: <synthetic@example.test>\r\n\r\nSynthetic test only")
	if err = p.app.recordOutboxAccepted(job, raw); err != nil {
		t.Fatal(err)
	}
	var state, filing, original, sent string
	if err = p.db.QueryRow(`SELECT state,filing_state,payload_ciphertext,sent_ciphertext FROM outbox_jobs WHERE id=$1`, row.ID).Scan(&state, &filing, &original, &sent); err != nil {
		t.Fatal(err)
	}
	if state != "submitted" || filing != "pending" || original != "" || sent == string(raw) {
		t.Fatalf("outcome=%s filing=%s original=%d sent=%d", state, filing, len(original), len(sent))
	}
	recovered, err := openSecret(p.cfg, sent)
	if err != nil || recovered != string(raw) {
		t.Fatal("Sent copy lost", err)
	}
	if _, _, _, err = p.app.claimOutbox(context.Background()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("submitted mail reclaimed", err)
	}
}

func TestIntegrationOutboxQuotaAndReplayAtCapacity(t *testing.T) {
	p := newProductPG(t)
	seedOutboxAccount(t, p)
	for i := 0; i < sendMaxJobs; i++ {
		seedOutbox(t, p, fmt.Sprint(i))
	}
	if _, _, err := p.app.saveOutbox(context.Background(), p.uid, "outbox-acct", "overflow", "hash", "x", 1); !errors.Is(err, errOutboxCapacity) {
		t.Fatal("quota ignored", err)
	}
	if _, replay, err := p.app.saveOutbox(context.Background(), p.uid, "outbox-acct", "0", "hash", "x", 1); err != nil || !replay {
		t.Fatal("full queue blocked receipt replay", err)
	}
}

func TestIntegrationOutboxExpiredClaimRetainsRecoverablePayload(t *testing.T) {
	p := newProductPG(t)
	seedOutboxAccount(t, p)
	row := seedOutbox(t, p, "interrupted")
	if _, err := p.db.Exec(`UPDATE outbox_jobs SET state='submitting',started_at=$2,undo_until=now()-interval '1 second' WHERE id=$1`, row.ID, time.Now().Add(-2*outboxClaimTimeout)); err != nil {
		t.Fatal(err)
	}
	if err := p.app.processOutbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	var state, payload string
	if err := p.db.QueryRow(`SELECT state,payload_ciphertext FROM outbox_jobs WHERE id=$1`, row.ID).Scan(&state, &payload); err != nil {
		t.Fatal(err)
	}
	if state != "ambiguous" || payload == "" {
		t.Fatalf("lost interrupted job=%s payload=%d", state, len(payload))
	}
}

func TestIntegrationOutboxDiscardSentCopyRetainsReceipt(t *testing.T) {
	p := newProductPG(t)
	seedOutboxAccount(t, p)
	row := seedOutbox(t, p, "discard-sent")
	if _, err := p.db.Exec(`UPDATE outbox_jobs SET state='submitted',filing_state='ambiguous',payload_ciphertext='',sent_ciphertext='encrypted-bytes',payload_bytes=50 WHERE id=$1`, row.ID); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("DELETE", "/api/outbox/"+row.ID+"/payload", nil)
	r.SetPathValue("id", row.ID)
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
	w := httptest.NewRecorder()
	p.app.handleOutboxDiscard(w, r)
	if w.Code != 204 {
		t.Fatalf("discard=%d %s", w.Code, w.Body.String())
	}
	var state, cipher, key string
	var size int64
	if err := p.db.QueryRow(`SELECT state,sent_ciphertext,payload_bytes,submission_key FROM outbox_jobs WHERE id=$1`, row.ID).Scan(&state, &cipher, &size, &key); err != nil {
		t.Fatal(err)
	}
	if state != "submitted" || cipher != "" || size != 0 || key != "discard-sent" {
		t.Fatalf("discard changed receipt or kept bytes: %s %q %d %s", state, cipher, size, key)
	}
}

func TestIntegrationOutboxSentReplacementCannotExceedReservation(t *testing.T) {
	p := newProductPG(t)
	seedOutboxAccount(t, p)
	row := seedOutbox(t, p, "bounded-sent-copy")
	if _, err := p.db.Exec(`UPDATE outbox_jobs SET undo_until=now()-interval '1 second',payload_bytes=1 WHERE id=$1`, row.ID); err != nil {
		t.Fatal(err)
	}
	job, _, _, err := p.app.claimOutbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = p.app.recordOutboxAccepted(job, []byte("Synthetic oversized MIME")); err == nil {
		t.Fatal("Sent copy exceeded its admitted reservation")
	}
	var state, original, sent string
	var reserved int64
	if err = p.db.QueryRow(`SELECT state,payload_ciphertext,sent_ciphertext,payload_bytes FROM outbox_jobs WHERE id=$1`, row.ID).Scan(&state, &original, &sent, &reserved); err != nil {
		t.Fatal(err)
	}
	if state != "submitting" || original == "" || sent != "" || reserved != 1 {
		t.Fatalf("failed replacement changed durable ownership: %s original=%d sent=%d reserved=%d", state, len(original), len(sent), reserved)
	}
}

func TestIntegrationOutboxOwnerDeleteCascadesPrivateCopies(t *testing.T) {
	p := newProductPG(t)
	seedOutboxAccount(t, p)
	seedOutbox(t, p, "owner-delete")
	if _, err := p.db.Exec(`DELETE FROM users WHERE id=$1`, p.uid); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := p.db.QueryRow(`SELECT count(*) FROM outbox_jobs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("owner deletion retained private outbox rows: %d %v", count, err)
	}
}
