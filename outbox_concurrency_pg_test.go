package main

import (
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"lullmail/internal/faketransport"
)

// Many clients, two processes, one key: one row, one delivery, one answer.
func TestConcurrentSameKeySubmitsOnce(t *testing.T) {
	e := newOutboxEnv(t)
	m := marker("same-key")
	procs := []*proc{e.newProc(), e.newProc()}
	const n = 24
	var wg sync.WaitGroup
	results := make([]sendResult, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i] = procs[i%2].send(m, m)
		}(i)
	}
	close(start)
	wg.Wait()
	id := ""
	for i, r := range results {
		if r.Code != 200 || r.ID == "" {
			t.Fatalf("request %d: %+v", i, r)
		}
		if id == "" {
			id = r.ID
		}
		if r.ID != id {
			t.Fatalf("one key produced two sends: %s and %s", id, r.ID)
		}
	}
	if e.rows("true") != 1 {
		t.Fatalf("rows=%d", e.rows("true"))
	}
	e.makeDue()
	var workers sync.WaitGroup
	for _, pr := range procs {
		workers.Add(1)
		go func(pr *proc) {
			defer workers.Done()
			for i := 0; i < 3; i++ {
				pr.pass()
			}
		}(pr)
	}
	workers.Wait()
	if e.delivered(m) != 1 || e.filed(m) != 1 {
		t.Fatalf("delivered=%d filed=%d", e.delivered(m), e.filed(m))
	}
}

// The undo boundary: cancellation and the worker's claim race for the same
// instant. Exactly one wins, and the user is told the truth either way.
func TestConcurrentCancelRacesClaimAtUndoBoundary(t *testing.T) {
	e := newOutboxEnv(t)
	worker := e.newProc()
	api := e.newProc()
	rng := rand.New(rand.NewSource(7))
	cancelled, sent := 0, 0
	const rounds = 40
	for i := 0; i < rounds; i++ {
		m := marker(fmt.Sprintf("boundary-%d", i))
		id := mustSend(t, api, m, m)
		// The undo deadline falls a few milliseconds ahead, jittered so the
		// two sides land on either side of it across rounds.
		lead := time.Duration(15+rng.Intn(25)) * time.Millisecond
		// Cancel lands between 10 ms before and 10 ms after the deadline.
		cancelAt := lead + time.Duration(rng.Intn(21)-10)*time.Millisecond
		if _, err := e.p.db.Exec(`UPDATE outbox_jobs SET undo_until=now()+make_interval(secs=>$2) WHERE id::text=$1`, id, lead.Seconds()); err != nil {
			t.Fatal(err)
		}
		var code int
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			time.Sleep(cancelAt)
			code = api.cancelSend(id)
		}()
		go func() {
			defer wg.Done()
			deadline := time.Now().Add(lead + 300*time.Millisecond)
			for time.Now().Before(deadline) {
				worker.pass()
				if v := e.job(id); v.State != "pending" {
					return
				}
				time.Sleep(2 * time.Millisecond)
			}
		}()
		wg.Wait()
		worker.pass()
		v := e.job(id)
		switch code {
		case http.StatusOK:
			cancelled++
			if v.State != "cancelled" || e.delivered(m) != 0 {
				t.Fatalf("round %d: told cancelled but %s delivered=%d", i, fmtState(v), e.delivered(m))
			}
		case http.StatusGone:
			sent++
			if v.State == "cancelled" || v.State == "pending" || e.delivered(m) != 1 {
				t.Fatalf("round %d: told too late but %s delivered=%d", i, fmtState(v), e.delivered(m))
			}
		default:
			t.Fatalf("round %d: cancel answered %d", i, code)
		}
		// Whatever happened, it stays that way.
		worker.pass()
		worker.pass()
		if e.delivered(m) > 1 || (code == http.StatusOK && e.delivered(m) != 0) {
			t.Fatalf("round %d: later passes changed the outcome (delivered=%d)", i, e.delivered(m))
		}
	}
	t.Logf("cancel won %d, claim won %d of %d", cancelled, sent, rounds)
}

// Several processes polling the same queue: each send delivered once, filed once.
func TestConcurrentWorkersNeverShareAClaim(t *testing.T) {
	e := newOutboxEnv(t)
	api := e.newProc()
	const jobs = sendMaxJobs
	markers := make([]string, jobs)
	for i := range markers {
		markers[i] = marker(fmt.Sprintf("contend-%d", i))
		mustSend(t, api, markers[i], markers[i])
	}
	e.makeDue()
	workers := make([]*proc, 4)
	for i := range workers {
		workers[i] = e.newProc()
	}
	var wg sync.WaitGroup
	for _, w := range workers {
		wg.Add(1)
		go func(w *proc) {
			defer wg.Done()
			for i := 0; i < jobs+2; i++ {
				w.pass()
			}
		}(w)
	}
	wg.Wait()
	for _, m := range markers {
		if e.delivered(m) != 1 || e.filed(m) != 1 {
			t.Fatalf("%s delivered=%d filed=%d", m, e.delivered(m), e.filed(m))
		}
	}
	if n := e.rows("state='submitted' AND filing_state='filed'"); n != jobs {
		t.Fatalf("%d of %d settled", n, jobs)
	}
}

// A late success from the attempt that was swept to ambiguous is still
// recorded; a stranger's success is not.
func TestAttemptOwnershipOfLateOutcomes(t *testing.T) {
	e := newOutboxEnv(t)
	pr := e.newProc()
	m := marker("late")
	id := mustSend(t, pr, m, m)
	e.makeDue()
	job, _, _, err := pr.app.claimOutbox(pr.ctx)
	if err != nil || job.ID != id {
		t.Fatal(err)
	}
	e.ageClaims()
	pr.app.processOutbox(pr.ctx) // sweeps this claim to ambiguous
	if v := e.job(id); v.State != "ambiguous" {
		t.Fatalf("%s", fmtState(v))
	}
	stranger := job
	stranger.Token = "00000000-0000-0000-0000-000000000000"
	if err := pr.app.recordOutboxAccepted(stranger, []byte("Subject: x\r\n\r\ny")); err == nil {
		t.Fatal("an outcome from a different attempt was recorded")
	}
	if v := e.job(id); v.State != "ambiguous" || !v.Payload {
		t.Fatalf("stranger changed the row: %s", fmtState(v))
	}
	if err := pr.app.recordOutboxAccepted(job, []byte("Subject: late\r\n\r\ny")); err != nil {
		t.Fatalf("the claiming attempt's late proof of acceptance was refused: %v", err)
	}
	if v := e.job(id); v.State != "submitted" || v.Filing != "pending" || v.Payload || !v.Sent {
		t.Fatalf("%s", fmtState(v))
	}
	// A terminal row cannot be pulled back by any later write.
	if err := pr.app.recordOutboxAccepted(job, []byte("again")); err == nil {
		t.Fatal("a settled send accepted a second outcome")
	}
}

// Removing a saved copy must not race the work that still needs it.
func TestDiscardRacesFilingAndClaims(t *testing.T) {
	e := newOutboxEnv(t)
	pr := e.newProc()
	m := marker("discard")
	id := mustSend(t, pr, m, m)
	if code := pr.discard(id); code != http.StatusConflict {
		t.Fatalf("discard of a pending send=%d", code)
	}
	e.makeDue()
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	e.imap.SetHook(func(step string) faketransport.Action {
		if step == "literal" {
			once.Do(func() { close(started) })
			<-release
		}
		return cont
	})
	done := make(chan struct{})
	go func() { pr.pass(); close(done) }()
	<-started
	// Filing is mid-APPEND: the saved copy is still in use.
	if code := pr.discard(id); code != http.StatusConflict {
		t.Fatalf("discard during filing=%d", code)
	}
	close(release)
	<-done
	if v := e.job(id); v.Filing != "filed" {
		t.Fatalf("%s", fmtState(v))
	}
	if code := pr.discard(id); code != http.StatusNoContent {
		t.Fatalf("discard after filing=%d", code)
	}
	if pr.send(m, m).Status != "submitted" {
		t.Fatal("discard removed the idempotent receipt")
	}
}

// ---- teardown: account deletion and logout racing every stage ----

func (pr *proc) deleteAccount() int {
	r := httptest.NewRequest(http.MethodDelete, "/api/accounts/"+pr.env.publicID, nil)
	r = r.WithContext(contextWithOwner(r.Context(), pr.env.p.uid))
	w := httptest.NewRecorder()
	pr.app.deleteAccount(w, r, pr.env.publicID)
	return w.Code
}

func TestTeardownRacesAdmissionSubmissionAndFiling(t *testing.T) {
	t.Run("admission", func(t *testing.T) {
		e := newOutboxEnv(t)
		pr := e.newProc()
		var accepted, refused atomic.Int32
		var wg sync.WaitGroup
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				time.Sleep(time.Duration(i) * 3 * time.Millisecond)
				r := pr.send(fmt.Sprintf("adm-%d", i), marker("teardown-admission"))
				if r.Code == 200 {
					accepted.Add(1)
				} else {
					refused.Add(1)
					if r.ID != "" {
						t.Errorf("a refused send carried a queued id: %+v", r)
					}
				}
			}(i)
		}
		time.Sleep(15 * time.Millisecond)
		if code := pr.deleteAccount(); code != 200 {
			t.Fatalf("deleteAccount=%d", code)
		}
		wg.Wait()
		// Every send that raced the deletion either was refused or died with
		// the account; none is left behind, none reaches the provider later.
		if n := e.rows("true"); n != 0 {
			t.Fatalf("%d outbox rows outlived their account (accepted=%d refused=%d)", n, accepted.Load(), refused.Load())
		}
		e.makeDue()
		pr.pass()
		if e.smtp.CountContaining("teardown-admission") != 0 {
			t.Fatal("mail was submitted for a deleted account")
		}
	})

	t.Run("submission", func(t *testing.T) {
		e := newOutboxEnv(t)
		pr := e.newProc()
		m := marker("teardown-submission")
		mustSend(t, pr, m, m)
		e.makeDue()
		inFlight := make(chan struct{})
		var once sync.Once
		e.smtp.SetHook(func(step string) faketransport.Action {
			if step == "body" {
				once.Do(func() { close(inFlight) })
				time.Sleep(400 * time.Millisecond)
			}
			return cont
		})
		done := make(chan struct{})
		go func() { pr.pass(); close(done) }()
		<-inFlight
		deleted := make(chan int, 1)
		go func() { deleted <- pr.deleteAccount() }()
		select {
		case code := <-deleted:
			if code != 200 {
				t.Fatalf("deleteAccount=%d", code)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("account deletion hung behind an in-flight send")
		}
		<-done
		if n := e.rows("true"); n != 0 {
			t.Fatalf("rows=%d", n)
		}
		if e.delivered(m) > 1 {
			t.Fatalf("delivered %d", e.delivered(m))
		}
		// Nothing further may ever be sent for the deleted account.
		before := e.smtp.Connections()
		pr.pass()
		pr.pass()
		if e.smtp.Connections() != before {
			t.Fatal("a worker contacted the provider after the account was deleted")
		}
	})

	t.Run("filing", func(t *testing.T) {
		e := newOutboxEnv(t)
		pr := e.newProc()
		m := marker("teardown-filing")
		mustSend(t, pr, m, m)
		e.makeDue()
		inFlight := make(chan struct{})
		var once sync.Once
		e.imap.SetHook(func(step string) faketransport.Action {
			if step == "literal" {
				once.Do(func() { close(inFlight) })
				time.Sleep(400 * time.Millisecond)
			}
			return cont
		})
		done := make(chan struct{})
		go func() { pr.pass(); close(done) }()
		<-inFlight
		deleted := make(chan int, 1)
		go func() { deleted <- pr.deleteAccount() }()
		select {
		case code := <-deleted:
			if code != 200 {
				t.Fatalf("deleteAccount=%d", code)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("account deletion hung behind an in-flight Sent filing")
		}
		<-done
		if e.delivered(m) != 1 || e.rows("true") != 0 {
			t.Fatalf("delivered=%d rows=%d", e.delivered(m), e.rows("true"))
		}
		before := e.imap.Connections()
		pr.pass()
		if e.imap.Connections() != before {
			t.Fatal("filing continued for a deleted account")
		}
	})

	t.Run("logout with a pending send", func(t *testing.T) {
		e := newOutboxEnv(t)
		pr := e.newProc()
		m := marker("logout")
		id := mustSend(t, pr, m, m)
		// The session ends while the undo window is still open.
		if _, err := e.p.db.Exec(`INSERT INTO auth_sessions(id_hash,user_id,expires_at) VALUES($1,$2,now()+interval '1 day')`, tokenHash("session-token"), e.p.uid); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "session-token"})
		if w := httptest.NewRecorder(); func() int { pr.app.handleLogout(w, r); return w.Code }() != 200 {
			t.Fatal("logout failed")
		}
		var sessions int
		e.p.db.QueryRow(`SELECT count(*) FROM auth_sessions`).Scan(&sessions)
		if sessions != 0 {
			t.Fatal("the session survived logout")
		}
		// Accepted work belongs to the owner, not the browser session.
		e.makeDue()
		pr.pass()
		pr.pass()
		if v := e.job(id); v.State != "submitted" || e.delivered(m) != 1 {
			t.Fatalf("%s delivered=%d", fmtState(v), e.delivered(m))
		}
	})
}
