package main

import "testing"

// These two tests pin down duplicate-send windows that remain by design (see
// "Duplicate-send windows that remain" in docs/durable-outbox.md). They assert
// today's behavior so that closing or widening a window is a deliberate change.

// A caller that sends no Idempotency-Key and retries a request whose
// acknowledgment it lost has no identity to replay against.
func TestRemainingWindowKeylessRetryIsANewSend(t *testing.T) {
	e := newOutboxEnv(t)
	pr := e.newProc()
	m := marker("keyless")
	first := pr.send("", m)
	second := pr.send("", m)
	if first.Code != 200 || second.Code != 200 || first.ID == second.ID || second.Replay {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	e.makeDue()
	pr.pass()
	pr.pass()
	pr.pass()
	if e.delivered(m) != 2 {
		t.Fatalf("delivered=%d; the documented keyless window changed", e.delivered(m))
	}
}

// Once a receipt is past the 90-day replay window the same key is a new send.
func TestRemainingWindowKeyAfterReceiptExpiryIsANewSend(t *testing.T) {
	e := newOutboxEnv(t)
	pr := e.newProc()
	m := marker("expired-receipt")
	id := mustSend(t, pr, m, m)
	e.makeDue()
	pr.pass()
	pr.pass()
	if e.delivered(m) != 1 {
		t.Fatalf("setup: delivered=%d", e.delivered(m))
	}
	if r := pr.send(m, m); r.ID != id || !r.Replay {
		t.Fatalf("inside the window the key must replay: %+v", r)
	}
	if _, err := e.p.db.Exec(`UPDATE outbox_jobs SET updated_at=now()-interval '91 days' WHERE id::text=$1`, id); err != nil {
		t.Fatal(err)
	}
	pr.pass() // retention removes the receipt
	again := pr.send(m, m)
	if again.Code != 200 || again.ID == id || again.Replay {
		t.Fatalf("past the window the key is no longer remembered: %+v", again)
	}
}
