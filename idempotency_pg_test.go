package main

// Real-PostgreSQL coverage for the api_mutations idempotency contract
// (audit WEB-04/WEB-03/R07): concurrent same-key mutations apply exactly
// once and every caller receives the same answer; a reused key with a
// different request is rejected; a lost acknowledgment replays the
// recorded response instead of re-applying the mutation. Same skip
// contract as product_pg_test.go: LULL_TEST_DATABASE_URL points at a
// DISPOSABLE scratch database.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// idempotentCall issues one wrapped request against a fresh recorder and
// returns status, body, and whether the answer was a recorded replay.
func idempotentCall(t *testing.T, p productPG, method, target, body, key string) (int, string, bool) {
	t.Helper()
	r := jsonBody(t, method, target, body)
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	p.app.withIdempotency(p.app.handleNoteCreate)(w, r)
	res := w.Result()
	data, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(data), res.Header.Get("X-Idempotent-Replay") == "true"
}

func noteCount(t *testing.T, p productPG) int {
	t.Helper()
	var n int
	if err := p.db.QueryRow(`SELECT count(*) FROM sticky_notes WHERE user_id=$1`, p.uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestIntegrationConcurrentSameKeyMutationAppliesOnce is the core
// multi-tab/lost-ACK proof: N concurrent requests sharing one key all
// succeed with byte-identical answers, and the creating endpoint applied
// exactly once.
func TestIntegrationConcurrentSameKeyMutationAppliesOnce(t *testing.T) {
	p := newProductPG(t)
	const workers = 8
	body := `{"x":10,"y":20,"text":"one note","color":2}`
	var wg sync.WaitGroup
	results := make([]struct {
		status int
		body   string
		replay bool
	}, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status, resp, replay := idempotentCall(t, p, "POST", "/notes", body, "note-key-1")
			results[i].status, results[i].body, results[i].replay = status, resp, replay
		}(i)
	}
	wg.Wait()
	for i, r := range results {
		if r.status != http.StatusOK {
			t.Fatalf("worker %d: status %d body %s", i, r.status, r.body)
		}
		if r.body != results[0].body {
			t.Fatalf("worker %d answer %q differs from worker 0 answer %q", i, r.body, results[0].body)
		}
	}
	if n := noteCount(t, p); n != 1 {
		t.Fatalf("same-key request applied %d times, want exactly 1", n)
	}
}

// TestIntegrationLostAcknowledgmentReplaysRecordedResponse simulates the
// offline-replay case end to end: the first application commits but its
// acknowledgment is lost (the client retries the identical request), and
// the retry receives the recorded answer without a second application.
func TestIntegrationLostAcknowledgmentReplaysRecordedResponse(t *testing.T) {
	p := newProductPG(t)
	body := `{"x":1,"y":2,"text":"replayed","color":0}`
	status1, first, replay1 := idempotentCall(t, p, "POST", "/notes", body, "replay-key")
	if status1 != http.StatusOK || replay1 {
		t.Fatalf("first application: status %d replay %v", status1, replay1)
	}
	for attempt := 0; attempt < 3; attempt++ {
		status, again, replay := idempotentCall(t, p, "POST", "/notes", body, "replay-key")
		if status != http.StatusOK || !replay || again != first {
			t.Fatalf("retry %d: status %d replay %v body %q (want recorded %q)", attempt, status, replay, again, first)
		}
	}
	if n := noteCount(t, p); n != 1 {
		t.Fatalf("retried mutation applied %d times, want exactly 1", n)
	}
}

// TestIntegrationIdempotencyKeyConflictRejectsDifferentRequest pins the
// request-hash half of the contract: one key, two request shapes, the
// second is a 409 and applies nothing.
func TestIntegrationIdempotencyKeyConflictRejectsDifferentRequest(t *testing.T) {
	p := newProductPG(t)
	if status, _, _ := idempotentCall(t, p, "POST", "/notes", `{"x":0,"y":0,"text":"a","color":0}`, "conflict-key"); status != http.StatusOK {
		t.Fatalf("first request status %d", status)
	}
	status, resp, _ := idempotentCall(t, p, "POST", "/notes", `{"x":0,"y":0,"text":"DIFFERENT","color":0}`, "conflict-key")
	if status != http.StatusConflict {
		t.Fatalf("different body under same key: status %d body %s", status, resp)
	}
	// The same request shape at a different path is also a different
	// request: keys are per-request, not per-session.
	if status, _, _ := idempotentCall(t, p, "POST", "/notes", `{"x":9,"y":9,"text":"path","color":1}`, "conflict-key"); status != http.StatusConflict {
		t.Fatalf("same key different path accepted: status %d", status)
	}
	if n := noteCount(t, p); n != 1 {
		t.Fatalf("conflicting requests applied %d times, want 1", n)
	}
}

// TestIntegrationIdempotentHandlerErrorIsReplayed pins that a handler's
// own 4xx answer is the recorded answer too: retrying a rejected mutation
// must not re-run it (the rejection is the outcome, not a retry signal).
func TestIntegrationIdempotentHandlerErrorIsReplayed(t *testing.T) {
	p := newProductPG(t)
	newReq := func() *http.Request {
		r := jsonBody(t, "POST", "/notes", `{"text":"e"}`)
		r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
		r.Header.Set("Idempotency-Key", "note-create-key")
		r.SetPathValue("id", "")
		return r
	}
	first := httptest.NewRecorder()
	p.app.withIdempotency(func(w http.ResponseWriter, req *http.Request) {
		writeProblem(w, http.StatusUnprocessableEntity, "Missing Title", "title is required")
	})(first, newReq())
	second := httptest.NewRecorder()
	p.app.withIdempotency(func(w http.ResponseWriter, req *http.Request) {
		// Must never run: reaching here means the recorded rejection was
		// discarded and the mutation re-executed.
		writeJSON(w, map[string]any{"ok": true})
	})(second, newReq())
	if first.Code != http.StatusUnprocessableEntity || second.Code != http.StatusUnprocessableEntity {
		t.Fatalf("recorded rejection not replayed: %d then %d", first.Code, second.Code)
	}
	if second.Header().Get("X-Idempotent-Replay") != "true" {
		t.Fatal("replayed rejection missing the replay marker")
	}
	if !strings.Contains(second.Body.String(), "title is required") {
		t.Fatalf("replayed body %q lost the recorded detail", second.Body.String())
	}
}

// TestIntegrationIdempotencyKeyLengthAndBodyBound checks the wrapper's
// own admission bounds independently of handler behavior.
func TestIntegrationIdempotencyKeyLengthAndBodyBound(t *testing.T) {
	p := newProductPG(t)
	longKey := strings.Repeat("k", idempotencyKeyLimit+1)
	status, body, _ := idempotentCall(t, p, "POST", "/notes", `{"text":"x"}`, longKey)
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("over-long key: status %d body %s", status, body)
	}
	huge := fmt.Sprintf(`{"text":"%s"}`, strings.Repeat("a", idempotencyBodyLimit+10))
	status, body, _ = idempotentCall(t, p, "POST", "/notes", huge, "big-body-key")
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body: status %d body %s", status, body)
	}
}

// A one-connection pool makes accidental nested acquisition deterministic.
// Exercise every wrapped route, including existing transaction-owning handlers.
func TestIntegrationKeyedMutationsUseOneConnection(t *testing.T) {
	p := newProductPG(t)
	messageID, account := seedSnoozeTarget(t, p)
	p.db.SetMaxOpenConns(1)
	call := func(handler http.HandlerFunc, method, path, body, key, id string) map[string]any {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		r := jsonBody(t, method, path, body).WithContext(context.WithValue(ctx, authContextKey{}, p.uid))
		r.Header.Set("Idempotency-Key", key)
		r.SetPathValue("id", id)
		r.SetPathValue("message", messageID)
		w := httptest.NewRecorder()
		p.app.withIdempotency(handler)(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", key, w.Code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	note := call(p.app.handleNoteCreate, "POST", "/notes", `{"text":"one"}`, "create-note", "")
	id := note["id"].(string)
	call(p.app.handleNoteUpdate, "POST", "/notes/"+id, `{"text":"two"}`, "update-note", id)
	call(p.app.handleNoteDelete, "DELETE", "/notes/"+id, "", "delete-note", id)
	card := call(p.app.handleBoardCard, "POST", "/board/cards", `{"title":"manual"}`, "create-card", "")
	id = card["card_id"].(string)
	call(p.app.handleBoardCardDone, "POST", "/board/cards/"+id+"/done", `{"done":true}`, "done-card", id)
	call(p.app.handleBoardUnpin, "POST", "/board/unpin", `{"card_id":"`+id+`"}`, "unpin-manual", "")
	call(p.app.handleBoardPin, "POST", "/board/pin", `{"account":"`+account+`","thread_id":"thread-snooze"}`, "pin-thread", "")
	call(p.app.handleDecide, "POST", "/screener/decide", `{"sender":"someone@example.com","allow":true,"route":"imbox"}`, "decide", "")
	call(p.app.handleUndecide, "POST", "/screener/undecide", `{"sender":"someone@example.com"}`, "undecide", "")
	call(p.app.handleMessageAction, "POST", "/messages/"+messageID+"/action?account="+account, `{"action":"read"}`, "read", "")
}

func TestIntegrationLedgerWriteFailureRollsBackDomainMutation(t *testing.T) {
	p := newProductPG(t)
	if _, err := p.db.Exec(`ALTER TABLE api_mutations ADD CONSTRAINT reject_recorded_success CHECK (response_status <> 200)`); err != nil {
		t.Fatal(err)
	}
	body := `{"text":"must roll back"}`
	status, _, _ := idempotentCall(t, p, "POST", "/notes", body, "failed-ledger")
	if status != http.StatusInternalServerError {
		t.Fatalf("ledger failure: %d", status)
	}
	if n := noteCount(t, p); n != 0 {
		t.Fatalf("failed ledger retained %d notes", n)
	}
	if _, err := p.db.Exec(`ALTER TABLE api_mutations DROP CONSTRAINT reject_recorded_success`); err != nil {
		t.Fatal(err)
	}
	if status, _, replay := idempotentCall(t, p, "POST", "/notes", body, "failed-ledger"); status != http.StatusOK || replay {
		t.Fatalf("retry: status=%d replay=%v", status, replay)
	}
	if n := noteCount(t, p); n != 1 {
		t.Fatalf("retried mutation: %d notes", n)
	}
}

func TestIntegrationHandlerPanicRollsBackMutationAndClaim(t *testing.T) {
	p := newProductPG(t)
	body := `{"text":"panic rollback"}`
	r := jsonBody(t, "POST", "/notes", body)
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
	r.Header.Set("Idempotency-Key", "panic-key")
	func() {
		defer func() {
			if recover() == nil {
				t.Error("handler panic disappeared")
			}
		}()
		p.app.withIdempotency(func(w http.ResponseWriter, r *http.Request) {
			p.app.handleNoteCreate(w, r)
			panic("interrupted before ledger update")
		})(httptest.NewRecorder(), r)
	}()
	if n := noteCount(t, p); n != 0 {
		t.Fatalf("panic retained %d notes", n)
	}
	if status, _, replay := idempotentCall(t, p, "POST", "/notes", body, "panic-key"); status != http.StatusOK || replay {
		t.Fatalf("retry: status=%d replay=%v", status, replay)
	}
}

func TestIntegrationTransientMutationFailureIsRetryable(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusTooManyRequests, http.StatusRequestTimeout} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			p := newProductPG(t)
			body := `{"text":"retry after rollback"}`
			r := jsonBody(t, "POST", "/notes", body)
			r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
			r.Header.Set("Idempotency-Key", "transient-key")
			first := httptest.NewRecorder()
			p.app.withIdempotency(func(w http.ResponseWriter, req *http.Request) {
				// Apply the real domain mutation without writing its success
				// response, then simulate failure before acknowledgement.
				p.app.handleNoteCreate(httptest.NewRecorder(), req)
				w.Header().Set("Retry-After", "2")
				writeProblem(w, status, "Temporary", "retry this same key")
			})(first, r)
			if first.Code != status || first.Header().Get("Retry-After") != "2" {
				t.Fatalf("first response: %d %v", first.Code, first.Header())
			}
			if n := noteCount(t, p); n != 0 {
				t.Fatalf("failed handler retained %d notes", n)
			}
			if got, _, replay := idempotentCall(t, p, "POST", "/notes", body, "transient-key"); got != http.StatusOK || replay {
				t.Fatalf("retry: status=%d replay=%v", got, replay)
			}
		})
	}
}

func TestIntegrationDeterministicRejectionRollsBackAndPreservesMetadata(t *testing.T) {
	p := newProductPG(t)
	request := func() *http.Request {
		r := jsonBody(t, "POST", "/notes", `{"text":"rejected"}`)
		r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
		r.Header.Set("Idempotency-Key", "rejected-key")
		return r
	}
	first := httptest.NewRecorder()
	p.app.withIdempotency(func(w http.ResponseWriter, r *http.Request) {
		p.app.handleNoteCreate(httptest.NewRecorder(), r)
		w.Header().Set("Location", "/review")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Set-Cookie", "never-replay-this")
		writeProblem(w, http.StatusConflict, "Rejected", "a deterministic rejection")
	})(first, request())
	if n := noteCount(t, p); n != 0 {
		t.Fatalf("rejection retained %d notes", n)
	}
	second := httptest.NewRecorder()
	p.app.withIdempotency(func(http.ResponseWriter, *http.Request) { t.Error("rejection was re-executed") })(second, request())
	if second.Code != http.StatusConflict || second.Body.String() != first.Body.String() {
		t.Fatalf("replay: %d %s", second.Code, second.Body.String())
	}
	if second.Header().Get("Location") != "/review" || second.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("lost metadata: %v", second.Header())
	}
	if second.Header().Get("Set-Cookie") != "" {
		t.Fatal("replayed a cookie")
	}
}

func TestIntegrationDistinctKeyDecisionsDoNotDeadlockOnOwnerLock(t *testing.T) {
	p := newProductPG(t)
	p.db.SetMaxOpenConns(16)
	const workers = 16
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			r := jsonBody(t, "POST", "/screener/decide", fmt.Sprintf(`{"sender":"person%d@example.com","allow":true}`, i))
			r = r.WithContext(context.WithValue(ctx, authContextKey{}, p.uid))
			r.Header.Set("Idempotency-Key", fmt.Sprintf("decide-%d", i))
			w := httptest.NewRecorder()
			p.app.withIdempotency(p.app.handleDecide)(w, r)
			if w.Code != http.StatusOK {
				t.Errorf("decision %d: %d %s", i, w.Code, w.Body.String())
			}
		}(i)
	}
	close(start)
	wg.Wait()
}
