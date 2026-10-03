package main

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"

	"lullmail/internal/faketransport"
)

// A seeded randomized walk over the same fault space as the targeted cases:
// one admission fault, one SMTP fault, one filing fault per message, a
// restart, and recovery. After each message the invariants must hold, and a
// second recovery must change nothing (no hidden automatic resend).
//
//	LULL_CHAOS_SEEDS=1,2,3  LULL_CHAOS_ROUNDS=20
func TestChaosCrashRecoveryInvariants(t *testing.T) {
	seeds := []int64{11, 12, 13}
	if s := os.Getenv("LULL_CHAOS_SEEDS"); s != "" {
		seeds = nil
		for _, f := range strings.Split(s, ",") {
			if strings.TrimSpace(f) == "" {
				continue
			}
			n, err := strconv.ParseInt(strings.TrimSpace(f), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			seeds = append(seeds, n)
		}
	}
	rounds := 20
	if s := os.Getenv("LULL_CHAOS_ROUNDS"); s != "" {
		rounds, _ = strconv.Atoi(s)
	}
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			e := newOutboxEnv(t)
			rng := rand.New(rand.NewSource(seed))
			for i := 0; i < rounds; i++ {
				chaosRound(t, e, rng, fmt.Sprintf("c%d-%d", seed, i))
			}
		})
	}
}

type snapshot struct {
	state, filing string
	delivered     int
	stored        int
}

func chaosRound(t *testing.T, e *outboxEnv, rng *rand.Rand, name string) {
	t.Helper()
	m := marker(name)
	var plan []string
	defer func() {
		if t.Failed() {
			t.Logf("plan for %s: %s", name, strings.Join(plan, "; "))
		}
	}()

	// 1. Admission, possibly interrupted; the client retries until told.
	first := e.newProc()
	var id string
	switch rng.Intn(5) {
	case 0:
		plan = append(plan, "admission: kill before commit")
		first.killAt("accept:before-commit")
		first.run(func() { first.send(m, m) })
	case 1:
		plan = append(plan, "admission: kill after commit")
		first.killAt("accept:after-commit")
		first.run(func() { first.send(m, m) })
	case 2:
		plan = append(plan, "admission: lost commit acknowledgment")
		first.failAt("accept:after-commit", fmt.Errorf("connection reset"))
		if r := first.send(m, m); r.Code == 200 {
			t.Fatalf("%s: unconfirmed commit acknowledged", name)
		}
		first.app.outboxFault = nil
	default:
		plan = append(plan, "admission: clean")
	}
	retry := e.newProc()
	r := retry.send(m, m)
	if r.Code != 200 || r.ID == "" {
		t.Fatalf("%s: told nothing after retry: %+v", name, r)
	}
	id = r.ID
	if e.rows("submission_key=$1", m) != 1 {
		t.Fatalf("%s: %d rows for one key", name, e.rows("submission_key=$1", m))
	}
	e.makeDue()

	// 2. A worker with one SMTP-side and one filing-side fault.
	worker := e.newProc()
	smtpSteps := []string{"greeting", "ehlo", "mail", "rcpt", "data", "body", "end", "committed"}
	switch rng.Intn(6) {
	case 0:
		plan = append(plan, "smtp: clean")
	case 1, 2:
		step := smtpSteps[rng.Intn(len(smtpSteps))]
		action := drop
		if rng.Intn(3) == 0 && step != "greeting" && step != "mail" && step != "body" && step != "committed" {
			action = rej
		}
		plan = append(plan, fmt.Sprintf("smtp: %s at %s", map[faketransport.Action]string{drop: "drop", rej: "reject"}[action], step))
		e.smtp.SetHook(faketransport.Script(step, 1, action))
	case 3:
		step := []string{"body", "end", "committed"}[rng.Intn(3)]
		plan = append(plan, "smtp: process killed at "+step)
		e.smtp.SetHook(worker.dropAndKill(step))
	case 4:
		point := []string{"claim:after", "deliver:before-submit", "deliver:after-acceptance", "deliver:after-record"}[rng.Intn(4)]
		plan = append(plan, "process killed at "+point)
		worker.killAt(point)
	case 5:
		plan = append(plan, "smtp: process cancelled mid DATA")
		e.smtp.SetHook(func(step string) faketransport.Action {
			if step == "body" {
				worker.cancel()
			}
			return cont
		})
	}
	if rng.Intn(3) == 0 && !worker.dead.Load() {
		switch rng.Intn(3) {
		case 0:
			step := []string{"greeting", "capability", "login", "append", "literal", "end", "committed"}[rng.Intn(7)]
			plan = append(plan, "imap: drop at "+step)
			e.imap.SetHook(faketransport.Script(step, 1, drop))
		case 1:
			point := []string{"filing:before-append", "filing:after-append"}[rng.Intn(2)]
			plan = append(plan, "process killed at "+point)
			if worker.app.outboxFault == nil {
				worker.killAt(point)
			}
		case 2:
			step := []string{"literal", "committed"}[rng.Intn(2)]
			plan = append(plan, "imap: process killed at "+step)
			e.imap.SetHook(worker.dropAndKill(step))
		}
	}
	for i := 0; i < 2; i++ {
		worker.pass()
	}
	e.smtp.SetHook(nil)
	e.imap.SetHook(nil)

	// 3. Restart: the claim timeout elapses, recovery runs, then runs again.
	check := func(stage string) snapshot {
		v := e.job(id)
		s := snapshot{v.State, v.Filing, e.delivered(m), e.filed(m)}
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("%s (%s): %s — %s delivered=%d stored=%d", name, stage, fmt.Sprintf(format, args...), fmtState(v), s.delivered, s.stored)
		}
		if !v.Exists {
			fail("an acknowledged send is gone")
		}
		if s.delivered > 1 {
			fail("delivered %d times without a user action", s.delivered)
		}
		if s.stored > 1 {
			fail("Sent copy appended %d times", s.stored)
		}
		switch v.State {
		case "submitted":
			if s.delivered != 1 {
				fail("recorded as submitted but the server never kept it")
			}
			if v.Payload {
				fail("a submitted message still holds its composition")
			}
			if v.Filing == "filed" && s.stored != 1 {
				fail("recorded as filed but nothing was appended")
			}
		case "failed":
			if s.delivered != 0 {
				fail("recorded as failed (not sent) but it was delivered")
			}
			e.assertRecoverable(retry, id)
		case "ambiguous":
			e.assertRecoverable(retry, id)
		case "pending", "submitting", "cancelled":
			if stage != "after-crash" || v.State == "cancelled" {
				fail("not settled")
			}
		}
		if v.State != "submitted" && s.delivered == 1 && v.State != "ambiguous" && v.State != "submitting" {
			fail("delivered but recorded as %s", v.State)
		}
		if v.Filing == "ambiguous" || v.Filing == "submitting" {
			if v.State != "submitted" {
				fail("filing uncertainty on an unsubmitted send")
			}
		}
		return s
	}
	check("after-crash")
	recovery := e.settle(4)
	settled := check("recovered")
	// The user retrying their send never creates a second one.
	if again := recovery.send(m, m); again.ID != id || !again.Replay {
		t.Fatalf("%s: retry after recovery: %+v", name, again)
	}
	conns, imapConns := e.smtp.Connections(), e.imap.Connections()
	final := e.settle(4)
	_ = final
	if after := check("second-recovery"); after != settled {
		t.Fatalf("%s: a second recovery changed the outcome: %+v -> %+v", name, settled, after)
	}
	// Only filing that was never attempted may still talk to a provider.
	if e.smtp.Connections() != conns {
		t.Fatalf("%s: recovery reconnected to SMTP", name)
	}
	if settled.filing != "pending" && e.imap.Connections() != imapConns {
		t.Fatalf("%s: recovery reconnected to IMAP after filing was %s", name, settled.filing)
	}
}
