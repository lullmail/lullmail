package main

// Fair admission, a non-blocking worker, graceful drain, the submission-key
// lookup and the account-deletion guard, against real PostgreSQL and the
// loopback provider fakes.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neutron-build/neutron/mail"
	"lullmail/internal/faketransport"
)

// addSMTPAccount connects a second account of the same owner, with its own
// SMTP server and a Sent mailbox on the shared IMAP fake.
func (e *outboxEnv) addSMTPAccount(uid, mirror string) (publicID string, smtp *faketransport.SMTP) {
	e.t.Helper()
	smtp, err := faketransport.NewSMTP()
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(smtp.Close)
	cred, err := sealSecret(e.p.cfg, "app-password")
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.p.db.QueryRow(`INSERT INTO email_accounts(user_id,mirror_account_id,provider,address,username,host,port,smtp_host,smtp_port,cred_ciphertext,sync_enabled)
 VALUES($1,$2,'imap',$3,'','127.0.0.1',$4,'127.0.0.1',$5,$6,false) RETURNING id::text`, uid, mirror, mirror+"@example.test", e.imap.Port(), smtp.Port(), cred).Scan(&publicID); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.p.db.Exec(`INSERT INTO mail_mailboxes(account_id,id,name,role,native) VALUES($1,'Sent','Sent','sent','Sent')`, mirror); err != nil {
		e.t.Fatal(err)
	}
	return publicID, smtp
}

// sendFrom submits through the real /send handler as owner uid from a
// chosen account.
func (pr *proc) sendFrom(uid, account, key, subject string) sendResult {
	body, _ := json.Marshal(map[string]any{"to": "friend@example.test", "subject": subject, "text": subject, "account_id": account})
	r := httptest.NewRequest(http.MethodPost, "/api/send", bytes.NewReader(body)).WithContext(contextWithOwner(context.Background(), uid))
	r.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	pr.app.handleSend(w, r)
	res := sendResult{Code: w.Code, Body: w.Body.String()}
	var parsed struct {
		Queued string `json:"queued"`
		Status string `json:"status"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &parsed)
	res.ID, res.Status = parsed.Queued, parsed.Status
	return res
}

// runWorker starts the production worker loop on its own context.
func (pr *proc) runWorker() (stop func(), exited <-chan struct{}) {
	ctx, cancel := context.WithCancel(pr.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		pr.app.runOutboxWorker(ctx)
	}()
	pr.env.t.Cleanup(func() {
		cancel()
		<-done
	})
	return cancel, done
}

func (e *outboxEnv) waitFor(what string, limit time.Duration, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(limit)
	for !cond() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// stallAt holds every session of one server at step until release is closed,
// and records how many of its submissions were ever in flight at once.
type stall struct {
	release   chan struct{}
	reached   chan struct{}
	once      sync.Once
	opened    sync.Once
	active    atomic.Int32
	maxActive atomic.Int32
}

// open releases every held and future session.
func (s *stall) open() { s.opened.Do(func() { close(s.release) }) }

func newStall(smtp *faketransport.SMTP, step string) *stall {
	s := &stall{release: make(chan struct{}), reached: make(chan struct{})}
	smtp.SetHook(func(name string) faketransport.Action {
		switch name {
		case "mail":
			n := s.active.Add(1)
			for {
				old := s.maxActive.Load()
				if n <= old || s.maxActive.CompareAndSwap(old, n) {
					break
				}
			}
		case "done":
			s.active.Add(-1)
		}
		if name == step {
			s.once.Do(func() { close(s.reached) })
			<-s.release
		}
		return faketransport.Continue
	})
	return s
}

func TestOutboxWorkerStalledAccountDoesNotDelayAnother(t *testing.T) {
	e := newOutboxEnv(t)
	otherID, otherSMTP := e.addSMTPAccount(e.p.uid, "harness-other")
	pr := e.newProc()
	slow := newStall(e.smtp, "end")
	defer slow.open()

	first := marker("stalled-first")
	second := marker("stalled-second")
	fast := marker("unaffected")
	firstID := mustSend(t, pr, first, first)
	e.makeDue()
	pr.runWorker()
	<-slow.reached

	// The stalled account's next send waits behind it; the other account's
	// send goes straight through.
	secondID := mustSend(t, pr, second, second)
	fastSend := pr.sendFrom(e.p.uid, otherID, fast, fast)
	if fastSend.Code != 200 {
		t.Fatalf("send=%d %s", fastSend.Code, fastSend.Body)
	}
	e.makeDue()
	e.waitFor("the other account's send", 10*time.Second, func() bool {
		v := e.job(fastSend.ID)
		return v.State == "submitted" && v.Filing == "filed"
	})
	if otherSMTP.CountContaining(fast) != 1 {
		t.Fatal("the other account's message was not delivered once")
	}
	if v := e.job(secondID); v.State != "pending" {
		t.Fatalf("a second submission started on the stalled account: %s", fmtState(v))
	}

	// The interrupted-claim sweep keeps running while the provider stalls.
	if _, err := e.p.db.Exec(`INSERT INTO outbox_jobs(id,user_id,account_id,submission_key,request_hash,state,payload_ciphertext,payload_bytes,undo_until,attempt_id,started_at)
 VALUES(gen_random_uuid(),$1,'harness-other','orphan','h','submitting','x',1,now(),gen_random_uuid(),now()-interval '10 minutes')`, e.p.uid); err != nil {
		t.Fatal(err)
	}
	e.waitFor("the sweep", 5*time.Second, func() bool {
		return e.rows(`submission_key='orphan' AND state='ambiguous' AND error_code='interrupted_submission'`) == 1
	})
	if v := e.job(firstID); v.State != "submitting" {
		t.Fatalf("the stalled attempt was disturbed: %s", fmtState(v))
	}

	slow.open()
	e.waitFor("the stalled account to drain", 10*time.Second, func() bool {
		return e.job(firstID).Filing == "filed" && e.job(secondID).Filing == "filed"
	})
	if e.delivered(first) != 1 || e.delivered(second) != 1 {
		t.Fatalf("delivered first=%d second=%d", e.delivered(first), e.delivered(second))
	}
	if n := slow.maxActive.Load(); n != 1 {
		t.Fatalf("the stalled account had %d submissions in flight at once", n)
	}
	// Order within the account is kept.
	got := e.smtp.Delivered()
	if len(got) != 2 || !strings.Contains(got[0], first) || !strings.Contains(got[1], second) {
		t.Fatalf("account order lost: %d messages", len(got))
	}
}

func TestOutboxWorkerConcurrencyIsBounded(t *testing.T) {
	e := newOutboxEnv(t)
	pr := e.newProc()
	// Five accounts with a stalling provider each: four of one owner (its
	// whole share) and one of another owner.
	other, _ := secondOwner(t, e.p, "bound-owner@example.test")
	ids := []string{}
	for i, name := range []string{"bound-a", "bound-b", "bound-c", "bound-d", "bound-e"} {
		uid := e.p.uid
		if i == 4 {
			uid = other
		}
		public, smtp := e.addSMTPAccount(uid, name)
		st := newStall(smtp, "end")
		defer st.open()
		m := marker(name)
		r := pr.sendFrom(uid, public, m, m)
		if r.Code != 200 {
			t.Fatalf("send %d=%d %s", i, r.Code, r.Body)
		}
		ids = append(ids, r.ID)
	}
	e.makeDue()
	pr.runWorker()
	e.waitFor("the worker slots to fill", 10*time.Second, func() bool { return e.rows(`state='submitting'`) == outboxWorkers })
	time.Sleep(1500 * time.Millisecond)
	if n, waiting := e.rows(`state='submitting'`), e.rows(`state='pending'`); n != outboxWorkers || waiting != 1 {
		t.Fatalf("%d attempts in flight and %d waiting, want %d and 1", n, waiting, outboxWorkers)
	}
}

// The production loop on a graceful stop: it claims nothing more, lets the
// attempt in flight finish, files its Sent copy, and only then returns.
func TestOutboxWorkerDrainsInFlightOnStop(t *testing.T) {
	t.Run("finishes within the grace", func(t *testing.T) {
		e := newOutboxEnv(t)
		pr := e.newProc()
		hold := newStall(e.smtp, "end")
		defer hold.open()
		m := marker("drain")
		later := marker("drain-later")
		id := mustSend(t, pr, m, m)
		e.makeDue()
		stop, exited := pr.runWorker()
		<-hold.reached
		laterID := mustSend(t, pr, later, later)
		e.makeDue()
		stop()
		time.Sleep(300 * time.Millisecond)
		select {
		case <-exited:
			t.Fatal("the worker returned with an attempt in flight")
		default:
		}
		hold.open()
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			t.Fatal("the worker did not return after its attempt finished")
		}
		if v := e.job(id); v.State != "submitted" || v.Filing != "filed" || e.delivered(m) != 1 || e.filed(m) != 1 {
			t.Fatalf("%s delivered=%d filed=%d", fmtState(v), e.delivered(m), e.filed(m))
		}
		if v := e.job(laterID); v.State != "pending" {
			t.Fatalf("claimed after the stop: %s", fmtState(v))
		}
	})
	t.Run("cancelled at the grace", func(t *testing.T) {
		e := newOutboxEnv(t)
		pr := e.newProc()
		pr.app.outboxGrace = 300 * time.Millisecond
		hold := newStall(e.smtp, "end")
		defer hold.open()
		m := marker("drain-late")
		id := mustSend(t, pr, m, m)
		e.makeDue()
		stop, exited := pr.runWorker()
		<-hold.reached
		stopped := time.Now()
		stop()
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			t.Fatal("the worker outlived its grace")
		}
		if took := time.Since(stopped); took > 6*time.Second {
			t.Fatalf("the drain took %s", took)
		}
		if v := e.job(id); v.State != "ambiguous" || !v.Payload {
			t.Fatalf("%s", fmtState(v))
		}
	})
}

func TestOutboxLookupBySubmissionKey(t *testing.T) {
	e := newOutboxEnv(t)
	pr := e.newProc()
	m := marker("lookup")
	id := mustSend(t, pr, m, "Secret subject for lookup")
	other, _ := secondOwner(t, e.p, "lookup-other@example.test")
	lookup := func(uid, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/outbox?key="+key, nil)
		r = r.WithContext(contextWithOwner(r.Context(), uid))
		w := httptest.NewRecorder()
		pr.app.handleOutbox(w, r)
		return w
	}
	w := lookup(e.p.uid, m)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("lookup=%d %s", w.Code, w.Body.String())
	}
	var rec map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["id"] != id || rec["status"] != "pending" || rec["account_id"] != e.publicID || rec["recoverable"] != true {
		t.Fatalf("lookup record: %v", rec)
	}
	for _, field := range []string{"request", "payload", "submission_key", "subject"} {
		if _, ok := rec[field]; ok {
			t.Fatalf("lookup exposed %q", field)
		}
	}
	if strings.Contains(w.Body.String(), "Secret subject") || strings.Contains(w.Body.String(), "friend@example.test") {
		t.Fatal("lookup leaked the composition")
	}
	if w := lookup(other, m); w.Code != 404 {
		t.Fatalf("another owner's key resolved: %d", w.Code)
	}
	if w := lookup(e.p.uid, "never-used"); w.Code != 404 {
		t.Fatalf("unknown key=%d", w.Code)
	}
	if w := lookup(e.p.uid, ""); w.Code != 404 {
		t.Fatalf("empty key=%d", w.Code)
	}
	// The list form is unchanged.
	r := httptest.NewRequest(http.MethodGet, "/api/outbox", nil)
	r = r.WithContext(contextWithOwner(r.Context(), e.p.uid))
	lw := httptest.NewRecorder()
	pr.app.handleOutbox(lw, r)
	if lw.Code != 200 || !strings.HasPrefix(strings.TrimSpace(lw.Body.String()), "[") {
		t.Fatalf("list=%d %s", lw.Code, lw.Body.String())
	}
	// Agents reach it only as the outcome list's query form.
	if !agentAllowedPath("/outbox") || agentAllowedPath("/outbox/"+id) {
		t.Fatal("agent scope of the outbox changed")
	}
}

func TestAccountDeletionGuardsOutbox(t *testing.T) {
	t.Run("refused while a send is waiting", func(t *testing.T) {
		e := newOutboxEnv(t)
		pr := e.newProc()
		m := marker("guard-pending")
		id := mustSend(t, pr, m, m)
		mustSend(t, pr, m+"-2", m+"-2")
		r := httptest.NewRequest(http.MethodDelete, "/api/accounts/"+e.publicID, nil)
		r = r.WithContext(contextWithOwner(r.Context(), e.p.uid))
		w := httptest.NewRecorder()
		pr.app.deleteAccount(w, r, e.publicID)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "2 sends are still waiting") {
			t.Fatalf("delete=%d %s", w.Code, w.Body.String())
		}
		if v := e.job(id); v.State != "pending" {
			t.Fatalf("%s", fmtState(v))
		}
		// The refusal did not seal the account: its send still goes out.
		e.makeDue()
		pr.pass()
		if v := e.job(id); v.State != "submitted" || e.delivered(m) != 1 {
			t.Fatalf("%s delivered=%d", fmtState(v), e.delivered(m))
		}
	})
	t.Run("reports retained outcomes it deletes", func(t *testing.T) {
		e := newOutboxEnv(t)
		pr := e.newProc()
		for _, key := range []string{"guard-a", "guard-b", "guard-c"} {
			mustSend(t, pr, key, key)
		}
		if _, err := e.p.db.Exec(`UPDATE outbox_jobs SET state=CASE submission_key WHEN 'guard-a' THEN 'failed' WHEN 'guard-b' THEN 'ambiguous' ELSE 'cancelled' END`); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodDelete, "/api/accounts/"+e.publicID, nil)
		r = r.WithContext(contextWithOwner(r.Context(), e.p.uid))
		w := httptest.NewRecorder()
		pr.app.deleteAccount(w, r, e.publicID)
		var body struct {
			Deleted string `json:"deleted"`
			Outbox  int    `json:"outbox_deleted"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != 200 || body.Outbox != 3 || e.rows("true") != 0 {
			t.Fatalf("delete=%d %s rows=%d", w.Code, w.Body.String(), e.rows("true"))
		}
	})
	t.Run("a send to a vanished account is not retryable", func(t *testing.T) {
		e := newOutboxEnv(t)
		pr := e.newProc()
		// The account disappears between validation and the insert.
		out := &mail.Outgoing{From: mail.Address{Email: "sender@example.test"}, To: []mail.Address{{Email: "friend@example.test"}}, Subject: "x", Text: "x"}
		raw := []byte(`{"to":"friend@example.test","subject":"x","text":"x"}`)
		r := httptest.NewRequest(http.MethodPost, "/api/send", bytes.NewReader(raw))
		r = r.WithContext(contextWithOwner(r.Context(), e.p.uid))
		r.Header.Set("Idempotency-Key", "gone-key")
		w := httptest.NewRecorder()
		pr.app.acceptOutbox(w, r, e.p.uid, "no-such-account", "", out, raw)
		if w.Code != 404 || strings.Contains(w.Body.String(), "retry with the same submission key") || !strings.Contains(w.Body.String(), "not queued") {
			t.Fatalf("answer=%d %s", w.Code, w.Body.String())
		}
		if e.rows("true") != 0 {
			t.Fatal("a row was stored for a vanished account")
		}
	})
}
