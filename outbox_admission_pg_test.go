package main

// LUL-D10 regressions: account deletion's initial-count/seal pair must be
// serialized against concurrent outbox send admission. The documented race
// had four steps — deletion counts zero active rows, a concurrent send
// commits pending and is acknowledged, the seal turns the worker's later
// claim into not_submitted, and the final count (now seeing a terminal
// row) deletes the account and cascades the accepted composition away. The
// per-account admission gate closes the window: an acceptance that owns
// the gate finishes its commit and is counted (the deletion refuses
// before cancelling anything), and a deletion that owns it first seals
// before a waiting send can enter, so that send is refused without ever
// being acknowledged.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// One send against a specific account (the harness default targets
// e.publicID; this targets another mailbox).
func (pr *proc) sendAccount(key, subject, accountID string) sendResult {
	body, _ := json.Marshal(map[string]any{"to": "friend@example.test", "subject": subject, "text": subject, "account_id": accountID})
	r := httptest.NewRequest(http.MethodPost, "/api/send", bytes.NewReader(body))
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, pr.env.p.uid))
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	pr.app.handleSend(w, r)
	res := sendResult{Code: w.Code, Body: w.Body.String()}
	var parsed struct {
		Queued string `json:"queued"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &parsed)
	res.ID = parsed.Queued
	return res
}

// The erasure schedule itself. Baseline reaches the accepted-send branch
// and fails it; the admission gate makes that branch unreachable, and the
// refusal branch's contract is asserted instead.
func TestDisconnectCannotEraseASendAcceptedDuringItsWindow(t *testing.T) {
	e := newOutboxEnv(t)
	pr := e.newProc()
	m := marker("admission-window")

	counted := make(chan struct{})
	sealed := make(chan struct{})
	resumeCount, resumeSeal := make(chan struct{}), make(chan struct{})
	var countOnce, sealOnce sync.Once
	pr.app.outboxFault = func(point string) error {
		switch point {
		case "delete:after-count":
			countOnce.Do(func() { close(counted) })
			<-resumeCount
		case "delete:after-seal":
			sealOnce.Do(func() { close(sealed) })
			<-resumeSeal
		}
		return nil
	}
	deleted := make(chan int, 1)
	go func() { deleted <- pr.deleteAccount() }()
	<-counted

	sent := make(chan sendResult, 1)
	go func() {
		r := pr.send(m, m)
		sent <- r
	}()

	select {
	case accepted := <-sent:
		// Baseline-only: the send was acknowledged while deletion sat
		// between its first count and its seal. Let the undo window
		// lapse, let the seal cancel the composition before any provider
		// contact (the worker's claim under a sealed account records
		// not_submitted), and require the final count to still refuse:
		// a send the user was told is accepted is never erased unsent.
		if accepted.Code != http.StatusOK {
			t.Fatalf("send inside the window failed: %d %.200s", accepted.Code, accepted.Body)
		}
		e.makeDue()
		close(resumeCount)
		<-sealed
		if pr.pass() {
			t.Fatal("worker died")
		}
		close(resumeSeal)
		code := <-deleted
		switch code {
		case http.StatusConflict:
			if v := e.job(accepted.ID); !v.Exists || v.State == "pending" || v.State == "submitting" {
				// The row may legitimately have failed not_submitted
				// under the seal; it must still exist for recovery.
				t.Fatalf("refused deletion did not retain the accepted send: %+v", v)
			}
			t.Fatalf("deletion refused only after sealing and cancelling an accepted send (code %d)", code)
		case http.StatusOK:
			if e.delivered(m) == 0 {
				t.Fatalf("an accepted send was erased unsent: deletion=%d rows=%d delivered=%d",
					code, e.rows("true"), e.delivered(m))
			}
		default:
			t.Fatalf("deletion=%d", code)
		}

	case <-time.After(5 * time.Second):
		// Patched: the deletion owns the admission gate, so the send
		// cannot enter until the deletion finishes. Let it finish; the
		// send must then be refused without an acknowledgment, a
		// persisted row, or any provider traffic.
		close(resumeCount)
		close(resumeSeal)
		if code := <-deleted; code != http.StatusOK {
			t.Fatalf("uninterrupted deletion=%d", code)
		}
		r := <-sent
		if r.Code == http.StatusOK || r.ID != "" {
			t.Fatalf("a send that could not be admitted was acknowledged: %+v", r)
		}
		if n := e.rows("true"); n != 0 {
			t.Fatalf("%d outbox rows outlived their account", n)
		}
		if e.delivered(m) != 0 {
			t.Fatal("mail was submitted for a deleted account")
		}
	}
}

// A deletion that arrives while an acceptance is already inside the gate
// must observe the row and refuse — without first sealing (cancelling) the
// account work of the send it is refusing to delete.
func TestDisconnectRefusalNeverCancelsTheSendItProtects(t *testing.T) {
	e := newOutboxEnv(t)
	pr := e.newProc()
	m := marker("refusal-protects")

	parked := make(chan struct{})
	release := make(chan struct{})
	sealed := make(chan struct{})
	var parkOnce, sealOnce sync.Once
	pr.app.outboxFault = func(point string) error {
		switch point {
		case "accept:before-commit":
			parkOnce.Do(func() { close(parked) })
			<-release
		case "delete:sealed":
			sealOnce.Do(func() { close(sealed) })
		}
		return nil
	}
	sent := make(chan sendResult, 1)
	go func() { sent <- pr.send(m, m) }()
	<-parked // the send holds the admission gate and its account lease, before its commit

	deleted := make(chan int, 1)
	go func() { deleted <- pr.deleteAccount() }()
	select {
	case <-sealed:
		// Baseline: the deletion ran its count (zero — nothing committed
		// yet) and sealed the account underneath the admitted send.
		close(release)
		code := <-deleted
		t.Fatalf("deletion sealed the account under an admitted send before answering %d", code)
	case <-time.After(2 * time.Second):
	}

	// Patched: the deletion is parked at the admission gate. The send
	// commits and is acknowledged; the deletion then counts the active
	// row and refuses without sealing anything.
	close(release)
	r := <-sent
	if r.Code != http.StatusOK {
		t.Fatalf("admitted send failed: %d %.200s", r.Code, r.Body)
	}
	code := <-deleted
	if code != http.StatusConflict {
		t.Fatalf("deletion racing an admitted send=%d, want %d", code, http.StatusConflict)
	}
	if v := e.job(r.ID); !v.Exists || v.State != "pending" {
		t.Fatalf("the protected send did not survive the refusal: %+v", v)
	}
	var accounts int
	if err := e.p.db.QueryRow(`SELECT count(*) FROM email_accounts`).Scan(&accounts); err != nil {
		t.Fatal(err)
	}
	if accounts != 1 {
		t.Fatal("the refused deletion removed the account")
	}
	// Undo window lapses; the protected send still submits normally.
	e.makeDue()
	if pr.pass() {
		t.Fatal("worker died")
	}
	if e.delivered(m) != 1 {
		t.Fatalf("the protected send was not submitted: delivered=%d", e.delivered(m))
	}
	if code := pr.deleteAccount(); code != http.StatusOK {
		t.Fatalf("deletion after the send settled=%d", code)
	}
}

// The gate is per-account: disconnecting one mailbox must not hold up
// admission for another.
func TestAdmissionGateIsPerAccount(t *testing.T) {
	e := newOutboxEnv(t)
	pr := e.newProc()
	m := marker("gate-per-account")

	cred, err := sealSecret(e.p.cfg, "app-password")
	if err != nil {
		t.Fatal(err)
	}
	var publicB string
	if err := e.p.db.QueryRow(`INSERT INTO email_accounts(user_id,mirror_account_id,provider,address,username,host,port,smtp_host,smtp_port,cred_ciphertext,sync_enabled)
		VALUES($1,'harness-acct-b','imap','b@example.test','','127.0.0.1',$2,'127.0.0.1',$3,$4,false) RETURNING id::text`,
		e.p.uid, e.imap.Port(), e.smtp.Port(), cred).Scan(&publicB); err != nil {
		t.Fatal(err)
	}
	if _, err := e.p.db.Exec(`INSERT INTO mail_mailboxes(account_id,id,name,role,native) VALUES('harness-acct-b','Sent','Sent','sent','Sent')`); err != nil {
		t.Fatal(err)
	}

	counted := make(chan struct{})
	resume := make(chan struct{})
	var once sync.Once
	pr.app.outboxFault = func(point string) error {
		if point == "delete:after-count" {
			once.Do(func() { close(counted) })
			<-resume
		}
		return nil
	}
	deleted := make(chan int, 1)
	go func() { deleted <- pr.deleteAccount() }() // disconnecting harness-acct
	<-counted

	// Admission for the other mailbox answers while the first account's
	// deletion is parked inside its window.
	r := pr.sendAccount(m, m, publicB)
	if r.Code != http.StatusOK {
		t.Fatalf("unrelated mailbox queued behind another account's deletion: %d %.200s", r.Code, r.Body)
	}
	close(resume)
	if code := <-deleted; code != http.StatusOK {
		t.Fatalf("deletion of the idle account=%d", code)
	}
	e.makeDue()
	if pr.pass() {
		t.Fatal("worker died")
	}
	if e.delivered(m) != 1 {
		t.Fatalf("the unrelated send was not submitted: delivered=%d", e.delivered(m))
	}
}

// Full-owner deletion (which deliberately cascades accepted-but-unsent
// work away with everything else) must not deadlock against the admission
// gate on the same process.
func TestFullOwnerDeleteWithAcceptedSendCompletes(t *testing.T) {
	e := newOutboxEnv(t)
	pr := e.newProc()
	m := marker("owner-delete")
	if id := mustSend(t, pr, m, m); id == "" {
		t.Fatal("send was not accepted")
	}
	e.makeDue()
	session := seedSession(t, e.p, time.Minute)
	r := jsonBody(t, http.MethodDelete, "/api/security/account", `{"confirmation":"owner@example.com"}`)
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, e.p.uid))
	r = r.WithContext(context.WithValue(r.Context(), sessionContextKey{}, session))
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		pr.app.handleFullAccountDelete(w, r)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("full owner deletion deadlocked behind the admission gate")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("full owner delete=%d %.200s", w.Code, w.Body.String())
	}
	if n := e.rows("true"); n != 0 {
		t.Fatalf("%d outbox rows outlived their owner", n)
	}
}
