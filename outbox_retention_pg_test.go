package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"testing"
)

// secondOwner adds an independent owner with one connected account.
func secondOwner(t *testing.T, p productPG, email string) (uid, account string) {
	t.Helper()
	if err := p.db.QueryRow(`INSERT INTO users (email,display_name) VALUES ($1,'Other') RETURNING id`, email).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	account = "acct-" + email
	if _, err := p.db.Exec(`INSERT INTO email_accounts(user_id,mirror_account_id,provider,address,cred_ciphertext) VALUES($1,$2,'imap',$3,'fixture')`, uid, account, email); err != nil {
		t.Fatal(err)
	}
	return uid, account
}

// bulkReceipts inserts n settled, payload-free receipts of the given age.
func bulkReceipts(t *testing.T, p productPG, uid, account, prefix, state, age string, n int) {
	t.Helper()
	if _, err := p.db.Exec(`INSERT INTO outbox_jobs(id,user_id,account_id,submission_key,request_hash,state,payload_ciphertext,payload_bytes,undo_until,created_at,updated_at,filing_state)
 SELECT gen_random_uuid(),$1,$2,$3||g,'h',$4,'',0,now(),now()-$5::interval,now()-$5::interval,'filed' FROM generate_series(1,$6) g`,
		uid, account, prefix, state, age, n); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationOutboxReceiptsOfOneOwnerNeverBlockAnother(t *testing.T) {
	p := newProductPG(t)
	seedOutboxAccount(t, p)
	other, otherAcct := secondOwner(t, p, "heavy@example.test")
	bulkReceipts(t, p, other, otherAcct, "heavy-", "submitted", "1 day", outboxOwnerReceiptLimit)
	// The heavy owner is refused with the receipt-specific error...
	_, _, err := p.app.saveOutbox(context.Background(), other, otherAcct, "one-more", "h", uuid.NewString(), "x", 1)
	if !errors.Is(err, errOutboxReceiptLimit) {
		t.Fatalf("heavy owner at the cap: %v", err)
	}
	// ...but an unrelated owner is not affected at all.
	if _, _, err = p.app.saveOutbox(context.Background(), p.uid, "outbox-acct", "healthy", "h", uuid.NewString(), "x", 1); err != nil {
		t.Fatalf("healthy owner wedged by another owner's receipts: %v", err)
	}
}

func TestIntegrationOutboxExpiredReceiptsAreReclaimedAtAdmission(t *testing.T) {
	p := newProductPG(t)
	seedOutboxAccount(t, p)
	// Half the cap is past the receipt window, half is still inside it.
	bulkReceipts(t, p, p.uid, "outbox-acct", "old-", "submitted", "91 days", outboxOwnerReceiptLimit/2)
	bulkReceipts(t, p, p.uid, "outbox-acct", "new-", "submitted", "1 day", outboxOwnerReceiptLimit-outboxOwnerReceiptLimit/2)
	if _, _, err := p.app.saveOutbox(context.Background(), p.uid, "outbox-acct", "fresh", "h", uuid.NewString(), "x", 1); err != nil {
		t.Fatalf("send refused although expired receipts could be reclaimed: %v", err)
	}
	var old, recent int
	p.db.QueryRow(`SELECT count(*) FROM outbox_jobs WHERE submission_key LIKE 'old-%'`).Scan(&old)
	p.db.QueryRow(`SELECT count(*) FROM outbox_jobs WHERE submission_key LIKE 'new-%'`).Scan(&recent)
	if old >= outboxOwnerReceiptLimit/2 || recent != outboxOwnerReceiptLimit-outboxOwnerReceiptLimit/2 {
		t.Fatalf("expired=%d recent=%d: pruning removed receipts inside the replay window or none at all", old, recent)
	}
	// A cap filled entirely with in-window receipts refuses, yet still replays.
	q := newProductPG(t)
	seedOutboxAccount(t, q)
	bulkReceipts(t, q, q.uid, "outbox-acct", "live-", "submitted", "1 day", outboxOwnerReceiptLimit)
	if _, _, err := q.app.saveOutbox(context.Background(), q.uid, "outbox-acct", "fresh", "h", uuid.NewString(), "x", 1); !errors.Is(err, errOutboxReceiptLimit) {
		t.Fatalf("in-window receipts were evicted or ignored: %v", err)
	}
	if _, replay, err := q.app.saveOutbox(context.Background(), q.uid, "outbox-acct", "live-7", "h", uuid.NewString(), "x", 1); err != nil || !replay {
		t.Fatalf("a retained receipt must still replay at the cap: replay=%v err=%v", replay, err)
	}
}

func TestIntegrationOutboxPayloadRetiresBeforeReceipt(t *testing.T) {
	p := newProductPG(t)
	seedOutboxAccount(t, p)
	mk := func(key, state, filing, age string, withPayload bool) string {
		row := seedOutbox(t, p, key)
		payload := "''"
		bytes := 0
		if withPayload {
			payload, bytes = "'sealed'", 6
		}
		if _, err := p.db.Exec(fmt.Sprintf(`UPDATE outbox_jobs SET state=$2,filing_state=$3,payload_ciphertext=%s,payload_bytes=%d,sent_ciphertext='',updated_at=now()-$4::interval WHERE id=$1`, payload, bytes), row.ID, state, filing, age); err != nil {
			t.Fatal(err)
		}
		return row.ID
	}
	staleFailed := mk("stale-failed", "failed", "not_started", "31 days", true)
	freshAmbiguous := mk("fresh-ambiguous", "ambiguous", "not_started", "5 days", true)
	oldReceipt := mk("old-receipt", "submitted", "filed", "91 days", false)
	midReceipt := mk("mid-receipt", "submitted", "filed", "60 days", false)
	oldFiling := mk("old-filing", "submitted", "submitting", "200 days", false)
	oldPending := mk("old-pending", "pending", "not_started", "200 days", true)
	if err := p.app.processOutbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	state := func(id string) (found bool, payload string) {
		err := p.db.QueryRow(`SELECT payload_ciphertext FROM outbox_jobs WHERE id=$1`, id).Scan(&payload)
		return err == nil, payload
	}
	if ok, payload := state(staleFailed); !ok || payload != "" {
		t.Fatalf("31-day-old payload must be cleared but its receipt kept: found=%v payload=%q", ok, payload)
	}
	if _, replay, err := p.app.saveOutbox(context.Background(), p.uid, "outbox-acct", "stale-failed", "hash", uuid.NewString(), "x", 1); err != nil || !replay {
		t.Fatalf("receipt of a cleared payload must still replay: replay=%v err=%v", replay, err)
	}
	if ok, payload := state(freshAmbiguous); !ok || payload == "" {
		t.Fatal("a recent ambiguous composition lost its payload")
	}
	if ok, _ := state(oldReceipt); ok {
		t.Fatal("receipt older than the replay window was kept")
	}
	if ok, _ := state(midReceipt); !ok {
		t.Fatal("receipt inside the replay window was removed with its payload window")
	}
	if ok, _ := state(oldFiling); !ok {
		t.Fatal("a row with Sent filing in flight was expired")
	}
	if ok, payload := state(oldPending); !ok || payload == "" {
		t.Fatal("pending work was expired")
	}
	var bytes int64
	p.db.QueryRow(`SELECT payload_bytes FROM outbox_jobs WHERE id=$1`, staleFailed).Scan(&bytes)
	if bytes != 0 {
		t.Fatalf("cleared payload still holds %d reserved bytes", bytes)
	}
}

func TestIntegrationOutboxOwnerByteShareAndRelease(t *testing.T) {
	p := newProductPG(t)
	seedOutboxAccount(t, p)
	other, otherAcct := secondOwner(t, p, "bytes@example.test")
	// One owner fills its share with a stopped composition.
	if _, _, err := p.app.saveOutbox(context.Background(), p.uid, "outbox-acct", "big", "h", uuid.NewString(), "x", outboxOwnerMaxBytes); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.app.saveOutbox(context.Background(), p.uid, "outbox-acct", "more", "h", uuid.NewString(), "x", 1); !errors.Is(err, errOutboxCapacity) {
		t.Fatalf("owner exceeded its share: %v", err)
	}
	if _, _, err := p.app.saveOutbox(context.Background(), other, otherAcct, "elsewhere", "h", uuid.NewString(), "x", 1<<20); err != nil {
		t.Fatalf("one owner's share starved another: %v", err)
	}
	// Cancelling releases the send reservation down to the stored ciphertext.
	if _, err := p.db.Exec(`UPDATE outbox_jobs SET state='cancelled',payload_ciphertext='stored',payload_bytes=octet_length('stored') WHERE submission_key='big'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.app.saveOutbox(context.Background(), p.uid, "outbox-acct", "after", "h", uuid.NewString(), "x", 1<<20); err != nil {
		t.Fatalf("released bytes were not reusable: %v", err)
	}
}
