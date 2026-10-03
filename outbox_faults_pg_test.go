package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"lullmail/internal/faketransport"
)

const (
	cont = faketransport.Continue
	drop = faketransport.Drop
	rej  = faketransport.Reject
)

// ---- admission: before and after the commit that makes a send durable ----

func TestFaultAdmissionBoundaries(t *testing.T) {
	t.Run("killed before the insert commits", func(t *testing.T) {
		e := newOutboxEnv(t)
		m := marker("kill-before-commit")
		first := e.newProc()
		first.killAt("accept:before-commit")
		if !first.run(func() { first.send(m, m) }) {
			t.Fatal("process was not killed at the boundary")
		}
		// Nothing was acknowledged and nothing is stored.
		if n := e.rows("true"); n != 0 {
			t.Fatalf("%d rows survived an uncommitted insert", n)
		}
		// The client retries its unacknowledged send with the same key.
		second := e.newProc()
		id := mustSend(t, second, m, m)
		e.makeDue()
		second.pass()
		second.pass()
		if v := e.job(id); v.State != "submitted" || e.delivered(m) != 1 {
			t.Fatalf("retry after pre-commit kill: %s delivered=%d", fmtState(v), e.delivered(m))
		}
	})

	t.Run("killed after the commit, before the response", func(t *testing.T) {
		e := newOutboxEnv(t)
		m := marker("kill-after-commit")
		first := e.newProc()
		first.killAt("accept:after-commit")
		if !first.run(func() { first.send(m, m) }) {
			t.Fatal("process was not killed at the boundary")
		}
		// The user never saw an acknowledgment, yet the send is durable.
		if n := e.rows("submission_key=$1 AND state='pending'", m); n != 1 {
			t.Fatalf("committed send not durable: %d rows", n)
		}
		var id string
		e.p.db.QueryRow(`SELECT id::text FROM outbox_jobs WHERE submission_key=$1`, m).Scan(&id)
		second := e.newProc()
		// The client's retry is a replay of the same send, not a new one.
		r := second.send(m, m)
		if r.Code != 200 || !r.Replay || r.ID != id {
			t.Fatalf("retry was not a replay: %+v", r)
		}
		e.makeDue()
		second.pass()
		second.pass()
		third := e.newProc().send(m, m)
		if e.rows("true") != 1 || e.delivered(m) != 1 || third.ID != id || third.Status != "submitted" {
			t.Fatalf("rows=%d delivered=%d replay=%+v", e.rows("true"), e.delivered(m), third)
		}
	})

	t.Run("commit acknowledgment lost", func(t *testing.T) {
		e := newOutboxEnv(t)
		m := marker("ack-lost")
		pr := e.newProc()
		pr.failAt("accept:after-commit", errors.New("connection reset reading COMMIT"))
		r := pr.send(m, m)
		if r.Code != 503 || strings.Contains(r.Body, `"queued"`) {
			t.Fatalf("an unconfirmed commit was acknowledged: %+v", r)
		}
		// It did commit; the same key finds it.
		pr.app.outboxFault = nil
		again := pr.send(m, m)
		if again.Code != 200 || !again.Replay || e.rows("true") != 1 {
			t.Fatalf("retry after lost ack: %+v rows=%d", again, e.rows("true"))
		}
		e.makeDue()
		pr.pass()
		pr.pass()
		if e.delivered(m) != 1 {
			t.Fatalf("delivered %d", e.delivered(m))
		}
	})

	t.Run("pool exhausted at admission", func(t *testing.T) {
		e := newOutboxEnv(t)
		m := marker("pool")
		pr := e.newProc()
		pr.db.SetMaxOpenConns(1)
		held, err := pr.db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
		defer cancel()
		r := pr.sendCtx(ctx, m, m, m)
		if r.Code == 200 || strings.Contains(r.Body, `"queued"`) {
			t.Fatalf("send acknowledged with no database connection: %+v", r)
		}
		if n := e.rows("true"); n != 0 {
			t.Fatalf("%d rows stored while the pool was exhausted", n)
		}
		held.Close()
		if r := pr.send(m, m); r.Code != 200 {
			t.Fatalf("retry with a free pool: %+v", r)
		}
		if e.rows("true") != 1 {
			t.Fatalf("rows=%d", e.rows("true"))
		}
	})
}

// ---- delivery: every SMTP step, the connection failing there ----

func TestFaultSMTPStepOutcomes(t *testing.T) {
	cases := []struct {
		step      string
		action    faketransport.Action
		state     string
		code      string
		delivered int
	}{
		// Before the message body is sent: provably not delivered.
		{"greeting", drop, "failed", "not_submitted", 0},
		{"ehlo", drop, "failed", "not_submitted", 0},
		{"mail", drop, "failed", "not_submitted", 0},
		{"rcpt", drop, "failed", "not_submitted", 0},
		{"rcpt", rej, "failed", "not_submitted", 0},
		{"data", drop, "failed", "not_submitted", 0},
		{"data", rej, "failed", "not_submitted", 0},
		// The body was in flight: the server may or may not have kept it.
		{"body", drop, "ambiguous", "submission_outcome_unknown", 0},
		{"end", drop, "ambiguous", "submission_outcome_unknown", 0},
		// Kept, never acknowledged: delivered AND uncertain. The dangerous case.
		{"committed", drop, "ambiguous", "submission_outcome_unknown", 1},
		// An explicit refusal after the body is a definite no.
		{"end", rej, "failed", "not_submitted", 0},
	}
	for _, tc := range cases {
		name := tc.step + "-" + map[faketransport.Action]string{drop: "drop", rej: "reject"}[tc.action]
		t.Run(name, func(t *testing.T) {
			e := newOutboxEnv(t)
			m := marker(name)
			pr := e.newProc()
			id := mustSend(t, pr, m, m)
			e.makeDue()
			e.smtp.SetHook(faketransport.Script(tc.step, 1, tc.action))
			if pr.pass() {
				t.Fatal("worker died")
			}
			v := e.job(id)
			if v.State != tc.state || v.Code != tc.code || e.delivered(m) != tc.delivered {
				t.Fatalf("%s delivered=%d; want state=%s code=%s delivered=%d", fmtState(v), e.delivered(m), tc.state, tc.code, tc.delivered)
			}
			e.assertRecoverable(pr, id)
			conns := e.smtp.Connections()

			// Neither a restart, nor aged claims, nor the same key again, nor
			// many more passes may ever send it again.
			again := e.settle(4)
			replay := again.send(m, m)
			again.pass()
			if replay.ID != id || !replay.Replay || replay.Status != tc.state {
				t.Fatalf("same-key retry after %s: %+v", tc.state, replay)
			}
			if e.smtp.Connections() != conns || e.delivered(m) != tc.delivered || e.rows("true") != 1 {
				t.Fatalf("an outcome that was not delivered was retried: conns %d->%d delivered=%d rows=%d", conns, e.smtp.Connections(), e.delivered(m), e.rows("true"))
			}
			if v := e.job(id); v.State != tc.state {
				t.Fatalf("state drifted to %s", fmtState(v))
			}
		})
	}
}

// ---- the process dies at each boundary around the provider ----

func TestFaultProcessKilledAroundSubmission(t *testing.T) {
	type killCase struct {
		name      string
		arm       func(e *outboxEnv, pr *proc)
		delivered int
		// state the dead process left behind, and what recovery makes of it
		stranded string
	}
	cases := []killCase{
		{"after claim before any provider traffic", func(e *outboxEnv, pr *proc) { pr.killAt("claim:after") }, 0, "submitting"},
		{"just before the SMTP submission", func(e *outboxEnv, pr *proc) { pr.killAt("deliver:before-submit") }, 0, "submitting"},
		{"mid DATA", func(e *outboxEnv, pr *proc) { e.smtp.SetHook(pr.dropAndKill("body")) }, 0, "submitting"},
		{"after the whole body, before the server keeps it", func(e *outboxEnv, pr *proc) { e.smtp.SetHook(pr.dropAndKill("end")) }, 0, "submitting"},
		{"after the server keeps it, before 250", func(e *outboxEnv, pr *proc) { e.smtp.SetHook(pr.dropAndKill("committed")) }, 1, "submitting"},
		{"after 250 received, before the status write", func(e *outboxEnv, pr *proc) { pr.killAt("deliver:after-acceptance") }, 1, "submitting"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newOutboxEnv(t)
			m := marker(tc.name)
			dying := e.newProc()
			id := mustSend(t, dying, m, m)
			e.makeDue()
			tc.arm(e, dying)
			dying.pass()
			if !dying.dead.Load() {
				t.Fatal("process survived the injected kill")
			}
			// The dead process left an in-flight claim and wrote nothing else.
			v := e.job(id)
			if v.State != tc.stranded || !v.Payload || e.delivered(m) != tc.delivered {
				t.Fatalf("after kill: %s delivered=%d", fmtState(v), e.delivered(m))
			}
			e.smtp.SetHook(nil)

			// A restart too soon must not touch a claim that may still be live.
			soon := e.newProc()
			soon.pass()
			if v := e.job(id); v.State != "submitting" {
				t.Fatalf("a fresh claim was swept immediately: %s", fmtState(v))
			}
			conns := e.smtp.Connections()

			// After the claim timeout the stranded send is uncertain, kept, and
			// never resubmitted: recovery must not resend what may have gone.
			rec := e.settle(5)
			v = e.job(id)
			if v.State != "ambiguous" || v.Code != "interrupted_submission" {
				t.Fatalf("stranded claim: %s", fmtState(v))
			}
			e.assertRecoverable(rec, id)
			if e.delivered(m) != tc.delivered || e.smtp.Connections() != conns {
				t.Fatalf("recovery contacted the provider again: delivered=%d conns %d->%d", e.delivered(m), conns, e.smtp.Connections())
			}
			if r := rec.send(m, m); r.ID != id || r.Status != "ambiguous" || !r.Replay {
				t.Fatalf("same-key retry: %+v", r)
			}
			if e.delivered(m) != tc.delivered {
				t.Fatal("retrying the key delivered again")
			}
		})
	}
}

// A graceful shutdown cancels the worker's context. An attempt already claimed
// is detached from it: it finishes, Sent filing included, within the drain
// grace. Past the grace it is cancelled and ends by the usual rules: failed
// before the body was on the wire, ambiguous once it was.
func TestFaultGracefulShutdownDuringSubmission(t *testing.T) {
	t.Run("stopped right after the claim, finishes", func(t *testing.T) {
		e := newOutboxEnv(t)
		m := marker("shutdown-early")
		pr := e.newProc()
		id := mustSend(t, pr, m, m)
		e.makeDue()
		pr.app.outboxFault = func(p string) error {
			if p == "claim:after" {
				pr.cancel()
			}
			return nil
		}
		pr.pass()
		v := e.job(id)
		if v.State != "submitted" || v.Filing != "filed" || e.delivered(m) != 1 || e.filed(m) != 1 {
			t.Fatalf("%s delivered=%d filed=%d", fmtState(v), e.delivered(m), e.filed(m))
		}
	})
	t.Run("stopped mid DATA, finishes", func(t *testing.T) {
		e := newOutboxEnv(t)
		m := marker("shutdown-mid")
		pr := e.newProc()
		id := mustSend(t, pr, m, m)
		e.makeDue()
		e.smtp.SetHook(func(step string) faketransport.Action {
			if step == "body" {
				pr.cancel() // SIGTERM: the worker context ends, the attempt does not
				time.Sleep(300 * time.Millisecond)
			}
			return cont
		})
		pr.pass()
		v := e.job(id)
		if v.State != "submitted" || v.Filing != "filed" || e.delivered(m) != 1 || e.filed(m) != 1 {
			t.Fatalf("%s delivered=%d filed=%d", fmtState(v), e.delivered(m), e.filed(m))
		}
		e.smtp.SetHook(nil)
		e.settle(3)
		if e.delivered(m) != 1 {
			t.Fatal("duplicated after shutdown")
		}
	})
	t.Run("grace exhausted before the body", func(t *testing.T) {
		e := newOutboxEnv(t)
		m := marker("shutdown-late-early")
		pr := e.newProc()
		pr.app.outboxGrace = 100 * time.Millisecond
		id := mustSend(t, pr, m, m)
		e.makeDue()
		pr.app.outboxFault = func(p string) error {
			if p == "claim:after" {
				pr.cancel()
			}
			return nil
		}
		e.smtp.SetHook(func(step string) faketransport.Action {
			if step == "greeting" {
				time.Sleep(600 * time.Millisecond)
			}
			return cont
		})
		pr.pass()
		v := e.job(id)
		if v.State != "failed" || v.Code != "not_submitted" || e.delivered(m) != 0 || !v.Payload {
			t.Fatalf("%s delivered=%d", fmtState(v), e.delivered(m))
		}
	})
	t.Run("grace exhausted mid DATA", func(t *testing.T) {
		e := newOutboxEnv(t)
		m := marker("shutdown-late-mid")
		pr := e.newProc()
		pr.app.outboxGrace = 100 * time.Millisecond
		id := mustSend(t, pr, m, m)
		e.makeDue()
		e.smtp.SetHook(func(step string) faketransport.Action {
			if step == "body" {
				pr.cancel()
				time.Sleep(600 * time.Millisecond)
			}
			return cont
		})
		pr.pass()
		v := e.job(id)
		// The body was on the wire when the grace ran out: the only honest
		// answer is "unknown", never "not sent" and never "sent".
		if v.State != "ambiguous" || !v.Payload {
			t.Fatalf("%s delivered=%d", fmtState(v), e.delivered(m))
		}
		e.smtp.SetHook(nil)
		e.settle(3)
		if e.delivered(m) > 1 {
			t.Fatal("duplicated after shutdown")
		}
	})
	t.Run("nothing new is claimed once stopping", func(t *testing.T) {
		e := newOutboxEnv(t)
		m := marker("shutdown-unclaimed")
		pr := e.newProc()
		id := mustSend(t, pr, m, m)
		e.makeDue()
		pr.cancel()
		pr.pass()
		if v := e.job(id); v.State != "pending" || e.smtp.Connections() != 0 {
			t.Fatalf("claimed after the stop: %s conns=%d", fmtState(v), e.smtp.Connections())
		}
		// The next process sends it once.
		e.settle(2)
		if v := e.job(id); v.State != "submitted" || e.delivered(m) != 1 {
			t.Fatalf("%s delivered=%d", fmtState(v), e.delivered(m))
		}
	})
}

// ---- Sent-copy filing: SMTP acceptance is already durable ----

func TestFaultSentFilingBoundaries(t *testing.T) {
	cases := []struct {
		step   string
		action faketransport.Action
		stored int
	}{
		{"greeting", drop, 0}, {"capability", drop, 0}, {"login", drop, 0}, {"login", rej, 0},
		{"append", drop, 0}, {"append", rej, 0},
		{"literal", drop, 0}, {"end", drop, 0}, {"end", rej, 0},
		{"committed", drop, 1}, // kept by the server, OK never seen
	}
	for _, tc := range cases {
		name := tc.step + "-" + map[faketransport.Action]string{drop: "drop", rej: "reject"}[tc.action]
		t.Run(name, func(t *testing.T) {
			e := newOutboxEnv(t)
			m := marker("filing-" + name)
			pr := e.newProc()
			id := mustSend(t, pr, m, m)
			e.makeDue()
			e.imap.SetHook(faketransport.Script(tc.step, 1, tc.action))
			pr.pass() // delivers, then files in the same pass
			v := e.job(id)
			if v.State != "submitted" || v.Filing != "ambiguous" || e.delivered(m) != 1 || e.filed(m) != tc.stored {
				t.Fatalf("%s delivered=%d stored=%d", fmtState(v), e.delivered(m), e.filed(m))
			}
			e.assertRecoverable(pr, id)
			conns := e.imap.Connections()
			e.imap.SetHook(nil)
			later := e.settle(4)
			if later.send(m, m).Status != "submitted" {
				t.Fatal("replay lost the accepted outcome")
			}
			if e.delivered(m) != 1 || e.filed(m) != tc.stored || e.imap.Connections() != conns {
				t.Fatalf("an uncertain APPEND was retried: delivered=%d stored=%d conns %d->%d", e.delivered(m), e.filed(m), conns, e.imap.Connections())
			}
		})
	}

	t.Run("success files once and releases the copy", func(t *testing.T) {
		e := newOutboxEnv(t)
		m := marker("filing-ok")
		pr := e.newProc()
		id := mustSend(t, pr, m, m)
		e.makeDue()
		pr.pass()
		pr.pass()
		v := e.job(id)
		if v.State != "submitted" || v.Filing != "filed" || v.Sent || v.Payload || e.filed(m) != 1 || e.delivered(m) != 1 {
			t.Fatalf("%s delivered=%d stored=%d", fmtState(v), e.delivered(m), e.filed(m))
		}
		var bytes int64
		e.p.db.QueryRow(`SELECT payload_bytes FROM outbox_jobs WHERE id::text=$1`, id).Scan(&bytes)
		if bytes != 0 {
			t.Fatalf("filed message still reserves %d bytes", bytes)
		}
	})

	killed := []struct {
		name   string
		arm    func(e *outboxEnv, pr *proc)
		stored int
	}{
		{"after the filing claim, before APPEND", func(e *outboxEnv, pr *proc) { pr.killAt("filing:before-append") }, 0},
		{"mid literal", func(e *outboxEnv, pr *proc) { e.imap.SetHook(pr.dropAndKill("literal")) }, 0},
		{"kept, OK not seen", func(e *outboxEnv, pr *proc) { e.imap.SetHook(pr.dropAndKill("committed")) }, 1},
		{"after APPEND, before the status write", func(e *outboxEnv, pr *proc) { pr.killAt("filing:after-append") }, 1},
	}
	for _, tc := range killed {
		t.Run("killed "+tc.name, func(t *testing.T) {
			e := newOutboxEnv(t)
			m := marker("filing-kill-" + tc.name)
			dying := e.newProc()
			id := mustSend(t, dying, m, m)
			e.makeDue()
			// Deliver with a first process that is healthy, then die while filing.
			// pass() delivers and files back to back, so arm the kill for filing only.
			tc.arm(e, dying)
			dying.pass()
			if !dying.dead.Load() {
				t.Fatal("process survived the injected kill")
			}
			v := e.job(id)
			if v.State != "submitted" || v.Filing != "submitting" || !v.Sent || e.delivered(m) != 1 || e.filed(m) != tc.stored {
				t.Fatalf("after kill: %s delivered=%d stored=%d", fmtState(v), e.delivered(m), e.filed(m))
			}
			e.imap.SetHook(nil)
			conns := e.imap.Connections()
			rec := e.settle(5)
			v = e.job(id)
			if v.State != "submitted" || v.Filing != "ambiguous" || v.Code != "interrupted_filing" {
				t.Fatalf("stranded filing: %s", fmtState(v))
			}
			e.assertRecoverable(rec, id)
			if e.delivered(m) != 1 || e.filed(m) != tc.stored || e.imap.Connections() != conns {
				t.Fatalf("recovery repeated a provider call: delivered=%d stored=%d conns %d->%d", e.delivered(m), e.filed(m), conns, e.imap.Connections())
			}
		})
	}

	t.Run("killed after the status write, before filing starts", func(t *testing.T) {
		e := newOutboxEnv(t)
		m := marker("filing-pending-kill")
		dying := e.newProc()
		id := mustSend(t, dying, m, m)
		e.makeDue()
		dying.killAt("deliver:after-record")
		dying.pass()
		v := e.job(id)
		if v.State != "submitted" || v.Filing != "pending" || !v.Sent || e.delivered(m) != 1 || e.filed(m) != 0 {
			t.Fatalf("%s delivered=%d stored=%d", fmtState(v), e.delivered(m), e.filed(m))
		}
		// Nothing was attempted, so recovery files it, exactly once.
		rec := e.newProc()
		rec.pass()
		rec.pass()
		v = e.job(id)
		if v.Filing != "filed" || e.filed(m) != 1 || e.delivered(m) != 1 {
			t.Fatalf("%s delivered=%d stored=%d", fmtState(v), e.delivered(m), e.filed(m))
		}
	})
}
