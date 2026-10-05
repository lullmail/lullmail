package main

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neutron-build/neutron/mail"
)

func TestHTMLFallbackText(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			name: "paragraphs and line breaks become newlines",
			in:   "<p>Hello there.</p><p>Second line.<br/>Third.</p>",
			want: "Hello there.\n\nSecond line.\nThird.",
		},
		{
			name: "entities decode and tags vanish",
			in:   "<div>Fish &amp; <b>chips</b></div>",
			want: "Fish & chips",
		},
		{
			name: "list items keep their bullets",
			in:   "<ul><li>one</li><li>two</li></ul>",
			want: "- one\n- two",
		},
		{
			name: "case-insensitive markup, content case preserved",
			in:   "<P>Keep MY Case</P><BR>After the break",
			want: "Keep MY Case\n\nAfter the break",
		},
		{
			name: "attributes inside tags are dropped",
			in:   `<a href="https://x.test/r?u=1">Read &quot;this&quot;</a>`,
			want: `Read "this"`,
		},
		{
			name: "whitespace is trimmed",
			in:   "   <p>  padded  </p>   ",
			want: "padded",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := htmlFallbackText(tc.in); got != tc.want {
				t.Errorf("htmlFallbackText(%q)\n got  %q\n want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestOutboundRecipients(t *testing.T) {
	for _, tc := range []struct{ name, list string }{
		{"header injection", "person@example.com\r\nBcc: victim@example.com"},
		{"not an address", "not an address"},
		{"one bad spoils the list", "person@example.com, garbage"},
	} {
		if _, ok := outboundRecipients(tc.list); ok {
			t.Errorf("outboundRecipients(%q) accepted", tc.list)
		}
	}
	if got, ok := outboundRecipients(""); !ok || len(got) != 0 {
		t.Errorf("empty list = %+v, %v; want empty ok", got, ok)
	}
	got, ok := outboundRecipients("Person <person@example.com>, other@example.com")
	if !ok || len(got) != 2 || got[0].Name != "Person" || got[0].Email != "person@example.com" || got[1].Email != "other@example.com" {
		t.Fatalf("valid list = %+v, %v", got, ok)
	}
}

func TestUndoSendOnlyCancelsPendingDelivery(t *testing.T) {
	for _, tc := range []struct {
		name       string
		state      sendState
		wantStatus int
		wantCancel bool
	}{
		{"pending", sendPending, http.StatusOK, true},
		{"delivering", sendDelivering, http.StatusGone, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			q := newSendQueue()
			q.sends["send-1"] = &pendingSend{cancel: cancel, state: tc.state}
			a := &App{sendq: q}
			r := httptest.NewRequest(http.MethodDelete, "/api/send/send-1", nil)
			r.SetPathValue("id", "send-1")
			w := httptest.NewRecorder()

			a.handleUndoSend(w, r)

			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tc.wantStatus)
			}
			select {
			case <-ctx.Done():
				if !tc.wantCancel {
					t.Fatal("delivery context was cancelled after delivery started")
				}
			default:
				if tc.wantCancel {
					t.Fatal("pending delivery context was not cancelled")
				}
			}
			cancel()
		})
	}
}

func ptr(s string) *string { return &s }

func TestDecodeAttachments(t *testing.T) {
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

	if atts, problem := decodeAttachments(nil, 25<<20); atts != nil || problem != "" {
		t.Fatalf("empty set = %v, %q; want nil, ok", atts, problem)
	}
	atts, problem := decodeAttachments([]sendAttachmentRequest{
		{Filename: "a.txt", ContentType: "text/plain", DataB64: ptr(b64("hello"))},
		{Filename: "b.bin", DataB64: ptr(b64("xyz"))},
	}, 25<<20)
	if problem != "" || len(atts) != 2 || string(atts[0].Data) != "hello" || atts[1].ContentType != "" {
		t.Fatalf("valid set = %+v, %q", atts, problem)
	}

	for _, tc := range []struct {
		name string
		reqs []sendAttachmentRequest
		want string
	}{
		{"bad base64", []sendAttachmentRequest{{Filename: "a", DataB64: ptr("!!!")}}, "not valid base64"},
		{"missing data", []sendAttachmentRequest{{Filename: "a"}}, "no data"},
	} {
		if _, problem := decodeAttachments(tc.reqs, 25<<20); problem == "" || !strings.Contains(problem, tc.want) {
			t.Errorf("%s: problem = %q, want it to mention %q", tc.name, problem, tc.want)
		}
	}

	big := base64.StdEncoding.EncodeToString(make([]byte, 15<<20+1))
	if _, problem := decodeAttachments([]sendAttachmentRequest{{Filename: "big", DataB64: ptr(big)}}, 25<<20); !strings.Contains(problem, "15 MiB") {
		t.Errorf("oversized file: problem = %q", problem)
	}
}

// The wire cap must let every attachment set that satisfies the decoded
// limits (25 MiB total, 15 MiB per file) through the HTTP handler, and
// refuse anything past it as too large rather than malformed JSON.
func TestHandleSendEnvelopeCoversAdvertisedAttachmentTotals(t *testing.T) {
	cfg := &Config{SecretKey: "0123456789abcdef0123456789abcdef"}
	sendApp := func() *App {
		return &App{
			cfg:   cfg,
			log:   discardLogger(),
			sendq: newSendQueue(),
			db: func() *sql.DB {
				db, _ := openOutboxSteps(t, nil,
					dbStep{kind: "query", rows: &testRows{
						columns: []string{"mirror_account_id", "provider"},
						values:  [][]driver.Value{{"mirror-1", "imap"}},
					}},
					dbStep{kind: "query", rows: &testRows{
						columns: []string{"provider", "address"},
						values:  [][]driver.Value{{"imap", "owner@example.com"}},
					}},
					dbStep{kind: "query", rows: &testRows{
						columns: []string{"address", "display_name"},
						values:  [][]driver.Value{{"owner@example.com", "Owner"}},
					}},
					dbStep{kind: "exec"}, outboxEmpty(), outboxSchema(0), outboxUsage(0, 0, 0), dbStep{kind: "exec"},
				)
				return db
			}(),
		}
	}
	file := func(size int) sendAttachmentRequest {
		return sendAttachmentRequest{Filename: "f.bin", DataB64: ptr(base64.StdEncoding.EncodeToString(make([]byte, size)))}
	}
	post := func(atts ...sendAttachmentRequest) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"to": "dest@example.com", "subject": "files", "text": "hi", "attachments": atts})
		r := httptest.NewRequest(http.MethodPost, "/api/send", bytes.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, "owner-1"))
		w := httptest.NewRecorder()
		sendApp().handleSend(w, r)
		return w
	}

	// Two 12 MiB files: legal under the decoded limits, ~32 MiB on the wire.
	if w := post(file(12<<20), file(12<<20)); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"queued"`) {
		t.Fatalf("two 12 MiB files: status = %d body = %s", w.Code, w.Body.String())
	}
	// Exactly the 25 MiB decoded total still fits the envelope.
	if w := post(file(13<<20), file(12<<20)); w.Code != http.StatusOK {
		t.Fatalf("exact total boundary: status = %d body = %s", w.Code, w.Body.String())
	}
	// One decoded byte over the total is the attachment-specific rejection.
	if w := post(file(13<<20), file(12<<20+1)); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "25 MiB total") {
		t.Fatalf("total over boundary: status = %d body = %s", w.Code, w.Body.String())
	}
	// Past the wire cap the reader refuses with 413, not a JSON complaint.
	if w := post(file(15<<20), file(15<<20), file(6<<20)); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over wire cap: status = %d body = %s", w.Code, w.Body.String())
	}
}

// Graph attachment limits must still be checked BEFORE queue acceptance
// with the exact delivery-time validator, so the two can never drift
// (audit SEND-06). Per-file oversize is no longer a refusal — delivery
// streams those through upload sessions — but the 25 MB total still
// rejects the set up front.
func TestHandleSendRejectsGraphOversizeAttachmentsPreQueue(t *testing.T) {
	small := mail.Attachment{Filename: "f.bin", Data: make([]byte, 3<<20)}
	oversize := mail.Attachment{Filename: "big.bin", Data: make([]byte, 3<<20+1)}
	if msg := validateTransportAttachments("graph", []mail.Attachment{oversize}); msg != "" {
		t.Fatalf("upload-session deliverable file refused pre-queue: %q", msg)
	}
	if msg := validateTransportAttachments("imap", []mail.Attachment{oversize}); msg != "" {
		t.Fatalf("graph limits applied to another provider: %q", msg)
	}
	overTotal := make([]mail.Attachment, 0, 9)
	for range 9 {
		overTotal = append(overTotal, small)
	}
	if msg := validateTransportAttachments("graph", overTotal); msg == "" || !strings.Contains(msg, "25 MB") {
		t.Fatalf("over-total graph set: %q", msg)
	}
}

// The delivery lease must fence account deletion: once deletion begins,
// an admitted send holds it and a new send fails admission instead of
// submitting for a mailbox that is going away (audit 3 SEND-02).
func TestGuardDeliveryFencesAccountDeletion(t *testing.T) {
	a := &App{
		log:           discardLogger(),
		accountStates: map[mail.AccountID]*accountLifecycle{},
	}
	acct := mail.AccountID("mirror-1")

	delivered := make(chan struct{})
	cancelled := make(chan struct{})
	cleanup := make(chan struct{})
	guarded := a.guardDelivery(acct, func(ctx context.Context, _ *mail.Outgoing) error {
		close(delivered)
		<-ctx.Done()
		close(cancelled)
		<-cleanup // cancellation must still wait for transport cleanup
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = guarded(ctx, &mail.Outgoing{}) }()
	<-delivered

	// Deletion cannot COMPLETE while the admitted delivery holds the lease...
	deletionDone := make(chan struct{})
	go func() {
		finish, ok := a.beginAccountDeletion(context.Background(), acct)
		if !ok {
			t.Error("deletion reported in-progress twice")
			return
		}
		finish(true) // committed: stays tombstoned
		close(deletionDone)
	}()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		cancel()
		close(cleanup)
		t.Fatal("account deletion did not cancel the admitted delivery")
	}
	select {
	case <-deletionDone:
		cancel()
		close(cleanup)
		t.Fatal("deletion completed while a delivery held the account lease")
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	close(cleanup)
	<-deletionDone

	// ...and after deletion began, new sends fail admission.
	if err := guarded(context.Background(), &mail.Outgoing{}); err == nil || !strings.Contains(err.Error(), "being deleted") {
		t.Fatalf("post-deletion delivery admitted: %v", err)
	}
}

// The aggregate budget rejects beyond maxJobs and releases exactly once
// (audit 3 SEND-03).
func TestSendBudgetAdmission(t *testing.T) {
	var b sendBudget
	var releases []func()
	for i := 0; i < sendMaxJobs; i++ {
		release, err := b.acquire(1)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	if _, err := b.acquire(1); !errors.Is(err, errSendCapacity) {
		t.Fatalf("job limit not enforced: %v", err)
	}
	for _, release := range releases {
		release()
		release() // double release must not double-count
	}
	if b.jobs != 0 || b.bytes != 0 {
		t.Fatalf("budget leaked: jobs=%d bytes=%d", b.jobs, b.bytes)
	}
	if _, err := b.acquire(sendMaxBytes + 1); !errors.Is(err, errSendCapacity) {
		t.Fatalf("byte limit not enforced: %v", err)
	}
}

// A database failure while resolving the send account must be a retryable
// 503, not the 412 "connect an account first" that sends the user chasing a
// setup problem which does not exist (audit 4 F23).
func TestHandleSendAccountLookupFailureIsRetryable(t *testing.T) {
	a := &App{
		cfg:   &Config{SecretKey: "0123456789abcdef0123456789abcdef"},
		log:   discardLogger(),
		sendq: newSendQueue(),
		db:    openStepDB(t, dbStep{kind: "query", err: errors.New("database unavailable")}),
	}
	body, _ := json.Marshal(map[string]any{"to": "dest@example.com", "subject": "s", "text": "hi"})
	r := httptest.NewRequest(http.MethodPost, "/api/send", bytes.NewReader(body))
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, "owner-1"))
	w := httptest.NewRecorder()
	a.handleSend(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("retryable lookup failure did not advertise Retry-After")
	}
}

// A JMAP account's API token is not an SMTP submission credential; the send
// must fail synchronously before the queue accepts it, not asynchronously
// after the undo window has closed (audit 4 F07).
func TestHandleSendRejectsJMAPWithoutSubmissionTransport(t *testing.T) {
	for _, fresh := range []bool{true, false} {
		t.Run(map[bool]string{true: "fresh send", false: "reply"}[fresh], func(t *testing.T) {
			steps := []dbStep{{
				kind: "query",
				rows: &testRows{columns: []string{"mirror_account_id", "provider"}, values: [][]driver.Value{{"mirror-1", "jmap"}}},
			}}
			payload := map[string]any{"to": "dest@example.com", "subject": "s", "text": "hi"}
			if !fresh {
				payload["account_id"] = "mirror-1"
				payload["reply_to_message_id"] = "msg-1"
				steps = append(steps,
					dbStep{kind: "query", rows: &testRows{
						columns: []string{"account_id", "provider"},
						values:  [][]driver.Value{{"mirror-1", "jmap"}},
					}},
				)
			}
			a := &App{
				cfg:   &Config{SecretKey: "0123456789abcdef0123456789abcdef"},
				log:   discardLogger(),
				sendq: newSendQueue(),
				db:    openStepDB(t, steps...),
			}
			body, _ := json.Marshal(payload)
			r := httptest.NewRequest(http.MethodPost, "/api/send", bytes.NewReader(body))
			r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, "owner-1"))
			w := httptest.NewRecorder()
			a.handleSend(w, r)
			if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "no verified send transport") {
				t.Fatalf("status = %d, want 422 naming the missing transport; body = %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "\"queued\"") {
				t.Fatal("a JMAP send was queued without a submission transport")
			}
		})
	}
}

// The admission weight must count the retained header fields, not only the
// bodies: a huge subject used to weigh exactly as much as a tiny one
// (audit 4 F14).
func TestOutgoingWeightCountsRetainedHeaderFields(t *testing.T) {
	small := &mail.Outgoing{Subject: "s", Text: "body"}
	large := &mail.Outgoing{Subject: strings.Repeat("a", 1<<20), Text: "body"}
	if outgoingWeight(small) >= outgoingWeight(large) {
		t.Fatalf("subject bytes do not count: small = %d, large = %d", outgoingWeight(small), outgoingWeight(large))
	}
	namey := &mail.Outgoing{
		Text: "body",
		To:   []mail.Address{{Name: strings.Repeat("n", 4096), Email: "dest@example.com"}},
	}
	plain := &mail.Outgoing{
		Text: "body",
		To:   []mail.Address{{Email: "dest@example.com"}},
	}
	if outgoingWeight(namey) <= outgoingWeight(plain) {
		t.Fatalf("display names do not count: namey = %d, plain = %d", outgoingWeight(namey), outgoingWeight(plain))
	}
	if outgoingWeight(plain) <= sendOverhead+int64(len("body")) {
		t.Fatalf("recipient addresses do not count: plain = %d", outgoingWeight(plain))
	}
}

// A present-but-empty data_base64 is a valid zero-byte attachment; only an
// OMITTED field is missing data (audit 5 SEND-03).
func TestDecodeAttachmentsAcceptsZeroByteAttachment(t *testing.T) {
	empty := ""
	atts, problem := decodeAttachments([]sendAttachmentRequest{
		{Filename: "empty.txt", ContentType: "text/plain", DataB64: &empty},
	}, 25<<20)
	if problem != "" || len(atts) != 1 || len(atts[0].Data) != 0 || atts[0].Filename != "empty.txt" {
		t.Fatalf("zero-byte attachment = %+v, problem = %q; want one empty attachment", atts, problem)
	}
	if _, problem := decodeAttachments([]sendAttachmentRequest{{Filename: "none"}}, 25<<20); problem == "" || !strings.Contains(problem, "no data") {
		t.Fatalf("omitted data_base64: problem = %q, want the missing-data rejection", problem)
	}
}

// A retried send acceptance under the same Idempotency-Key re-receives the
// SAME queued id without a second submission; a different body under the
// key is a 409 (audit 5 SEND-02). The step DB proves the retry never
// reaches the database: only one acceptance's queries are staged.
func TestHandleSendIdempotentAcceptance(t *testing.T) {
	body := []byte(`{"to":"dest@example.com","subject":"again","text":"hello"}`)
	hash := mutationRequestHash("POST", "/api/send", "", body)
	steps := []dbStep{outboxEmpty(),
		{kind: "query", rows: &testRows{columns: []string{"mirror", "provider"}, values: [][]driver.Value{{"mirror-1", "imap"}}}},
		{kind: "query", rows: &testRows{columns: []string{"provider", "address"}, values: [][]driver.Value{{"imap", "owner@example.com"}}}},
		{kind: "query", rows: &testRows{columns: []string{"address", "name"}, values: [][]driver.Value{{"owner@example.com", "Owner"}}}},
	}
	steps = append(steps, outboxAdmissionSteps()...)
	db, script := openOutboxSteps(t, nil, steps...)
	a := &App{cfg: &Config{SecretKey: "test"}, db: db, log: discardLogger()}
	post := func(body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/send", bytes.NewReader(body))
		r.Header.Set("Idempotency-Key", "key")
		r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, "owner-1"))
		w := httptest.NewRecorder()
		a.handleSend(w, r)
		return w
	}
	first := post(body)
	if first.Code != 200 {
		t.Fatalf("first=%d %s", first.Code, first.Body.String())
	}
	var queued struct {
		Queued string `json:"queued"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &queued); err != nil {
		t.Fatal(err)
	}
	script.steps = append(script.steps, outboxStored(queued.Queued, "pending", hash), outboxStored(queued.Queued, "pending", hash))
	again := post(body)
	if again.Code != 200 || !strings.Contains(again.Body.String(), queued.Queued) {
		t.Fatalf("retry=%d %s", again.Code, again.Body.String())
	}
	conflict := post([]byte(`{"to":"dest@example.com","subject":"different","text":"hello"}`))
	if conflict.Code != 409 {
		t.Fatalf("conflict=%d %s", conflict.Code, conflict.Body.String())
	}
}

// The enqueue boundary is the linearization point: even callers that all
// passed handleSend's optimistic lookup must share one worker and budget slot.
func TestConcurrentEnqueueClaimsSendKeyOnce(t *testing.T) {
	a := &App{sendq: newSendQueue(), log: discardLogger()}
	t.Cleanup(func() { a.stopBackground(time.Second) })
	const count = 32
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, count)
	var delivered atomic.Int32
	for i := 0; i < count; i++ {
		go func() {
			<-start
			w := httptest.NewRecorder()
			a.enqueue(w, func(context.Context, *mail.Outgoing) error {
				delivered.Add(1)
				return nil
			}, &mail.Outgoing{Text: "one send"}, "same-key", "same-hash")
			results <- w
		}()
	}
	close(start)
	var id string
	for i := 0; i < count; i++ {
		w := <-results
		if w.Code != http.StatusOK {
			t.Fatalf("caller %d: %d %s", i, w.Code, w.Body.String())
		}
		var response struct {
			Queued string `json:"queued"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			id = response.Queued
		}
		if id == "" || response.Queued != id {
			t.Fatalf("duplicate queue ids: %q and %q", id, response.Queued)
		}
	}
	a.sendq.mu.Lock()
	jobs := len(a.sendq.sends)
	a.sendq.mu.Unlock()
	a.sendq.budget.mu.Lock()
	budgetJobs := a.sendq.budget.jobs
	a.sendq.budget.mu.Unlock()
	if jobs != 1 || budgetJobs != 1 {
		t.Fatalf("jobs=%d budget=%d, want one", jobs, budgetJobs)
	}
	// The single token cancels ALL retries' accepted work, not just the
	// last one racing to overwrite the key index.
	r := httptest.NewRequest(http.MethodDelete, "/send/"+id, nil)
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	a.handleUndoSend(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("undo: %d", w.Code)
	}
	if !a.stopBackground(time.Second) {
		t.Fatal("send worker did not stop")
	}
	if delivered.Load() != 0 {
		t.Fatal("an undone retry was delivered")
	}
}

func TestEnqueueReplaysBeforeBudgetAndRejectsConflictingBody(t *testing.T) {
	a := &App{sendq: newSendQueue(), log: discardLogger()}
	t.Cleanup(func() { a.stopBackground(time.Second) })
	deliver := func(context.Context, *mail.Outgoing) error { return nil }
	first := httptest.NewRecorder()
	a.enqueue(first, deliver, &mail.Outgoing{Text: "one"}, "key", "hash")
	var releases []func()
	for i := 1; i < sendMaxJobs; i++ {
		release, err := a.sendq.budget.acquire(1)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	replay := httptest.NewRecorder()
	a.enqueue(replay, deliver, &mail.Outgoing{Text: "one"}, "key", "hash")
	if replay.Code != http.StatusOK || replay.Header().Get("X-Idempotent-Replay") != "true" {
		t.Fatalf("full-budget retry: %d %s", replay.Code, replay.Body.String())
	}
	conflict := httptest.NewRecorder()
	a.enqueue(conflict, deliver, &mail.Outgoing{Text: "different"}, "key", "different")
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict: %d", conflict.Code)
	}
}

func TestConcurrentEnqueueDuringShutdownNeverPublishesAcceptance(t *testing.T) {
	a := &App{sendq: newSendQueue(), log: discardLogger()}
	a.tasksGroup().Stop(time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			a.enqueue(w, nil, &mail.Outgoing{}, "key", "hash")
			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("shutdown replayed an unadmitted send: %d", w.Code)
			}
		}()
	}
	wg.Wait()
	if len(a.sendq.sends) != 0 || len(a.sendq.keyed) != 0 || a.sendq.budget.jobs != 0 {
		t.Fatal("shutdown leaked send reservations")
	}
}

func waitForSendWorkers(t *testing.T, a *App) {
	t.Helper()
	done := make(chan struct{})
	go func() { a.tasksGroup().wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("send workers did not finish")
	}
}

func TestSendReceiptsPreventResubmissionAfterWorkerExit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outcome sendOutcome
		status  int
	}{
		{"submitted", sendSubmitted, http.StatusOK},
		{"transport-error", sendAmbiguous, http.StatusConflict},
		{"transport-panic", sendAmbiguous, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &App{sendq: newSendQueue(), log: discardLogger()}
			t.Cleanup(func() { a.stopBackground(time.Second) })
			var delivered atomic.Int32
			deliver := func(context.Context, *mail.Outgoing) error {
				delivered.Add(1)
				if tc.name == "transport-panic" {
					panic("transport interrupted")
				}
				if tc.name == "transport-error" {
					return errors.New("provider acknowledgment lost")
				}
				return nil
			}
			first := httptest.NewRecorder()
			a.enqueueAfter(first, deliver, &mail.Outgoing{Text: "one"}, "receipt-key", "hash", 0)
			if first.Code != http.StatusOK {
				t.Fatalf("initial: %d", first.Code)
			}
			waitForSendWorkers(t, a)
			if len(a.sendq.sends) != 0 || a.sendq.budget.jobs != 0 || a.sendq.budget.bytes != 0 {
				t.Fatal("completed payload still owns queue capacity")
			}
			receipt := a.sendq.receipts["receipt-key"]
			if receipt.outcome != tc.outcome || receipt.expiresAt.IsZero() {
				t.Fatalf("receipt: %+v", receipt)
			}
			for i := 0; i < 3; i++ {
				replay := httptest.NewRecorder()
				a.enqueueAfter(replay, deliver, &mail.Outgoing{Text: "one"}, "receipt-key", "hash", 0)
				if replay.Code != tc.status || replay.Header().Get("X-Idempotent-Replay") != "true" {
					t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
				}
				if tc.outcome == sendSubmitted {
					var result struct {
						Queued string `json:"queued"`
						Undo   int    `json:"undo_seconds"`
						Status string `json:"status"`
					}
					if err := json.Unmarshal(replay.Body.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					if result.Queued != receipt.id || result.Undo != 0 || result.Status != "submitted" {
						t.Fatalf("completed replay: %+v", result)
					}
				} else if !strings.Contains(replay.Body.String(), "may have reached") {
					t.Fatal("uncertain outcome was presented as a definite failure")
				}
			}
			if delivered.Load() != 1 {
				t.Fatalf("receipt retry delivered %d times", delivered.Load())
			}
			if !a.sendq.receipts["receipt-key"].expiresAt.Equal(receipt.expiresAt) {
				t.Fatal("retry extended receipt expiry")
			}
			conflict := httptest.NewRecorder()
			a.enqueueAfter(conflict, deliver, &mail.Outgoing{Text: "changed"}, "receipt-key", "different", 0)
			if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "Key Reused") {
				t.Fatalf("hash conflict: %d %s", conflict.Code, conflict.Body.String())
			}
		})
	}
}

func TestUndoRetainsCancelledReceipt(t *testing.T) {
	a := &App{sendq: newSendQueue(), log: discardLogger()}
	t.Cleanup(func() { a.stopBackground(time.Second) })
	first := httptest.NewRecorder()
	a.enqueue(first, func(context.Context, *mail.Outgoing) error { t.Error("cancelled mail delivered"); return nil }, &mail.Outgoing{}, "undo-key", "hash")
	var response struct {
		Queued string `json:"queued"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodDelete, "/outbox/"+response.Queued, nil)
	r.SetPathValue("id", response.Queued)
	w := httptest.NewRecorder()
	a.handleUndoSend(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("undo: %d", w.Code)
	}
	waitForSendWorkers(t, a)
	if got := a.sendq.receipts["undo-key"].outcome; got != sendCancelled {
		t.Fatalf("undo outcome: %q", got)
	}
	replay := httptest.NewRecorder()
	a.enqueue(replay, nil, &mail.Outgoing{}, "undo-key", "hash")
	if replay.Code != http.StatusConflict || !strings.Contains(replay.Body.String(), "Send Cancelled") {
		t.Fatalf("cancelled replay: %d %s", replay.Code, replay.Body.String())
	}
}

func TestSendReceiptRetentionIsBoundedAndExpires(t *testing.T) {
	q := newSendQueue()
	now := time.Now()
	q.receipts["expired"] = sendReceipt{reqHash: "hash", expiresAt: now.Add(-time.Second)}
	if q.sendReplay("expired", "hash") != nil {
		t.Fatal("expired receipt was replayed")
	}
	for i := 0; i <= sendReceiptLimit; i++ {
		q.receipts[fmt.Sprint(i)] = sendReceipt{expiresAt: now.Add(sendReceiptTTL + time.Duration(i)*time.Second)}
	}
	q.pruneReceipts(now)
	if len(q.receipts) != sendReceiptLimit {
		t.Fatalf("retained %d receipts", len(q.receipts))
	}
	if _, ok := q.receipts["0"]; ok {
		t.Fatal("oldest receipt was not evicted at capacity")
	}
	q.pruneReceipts(now.Add(sendReceiptTTL + time.Duration(sendReceiptLimit+1)*time.Second))
	if len(q.receipts) != 0 {
		t.Fatal("expired receipts were retained")
	}
}

func TestShutdownBeforeSubmissionRetainsCancelledReceipt(t *testing.T) {
	a := &App{sendq: newSendQueue(), log: discardLogger()}
	var delivered atomic.Int32
	w := httptest.NewRecorder()
	a.enqueue(w, func(context.Context, *mail.Outgoing) error { delivered.Add(1); return nil }, &mail.Outgoing{}, "shutdown-key", "hash")
	if w.Code != http.StatusOK {
		t.Fatalf("acceptance: %d", w.Code)
	}
	if !a.stopBackground(time.Second) {
		t.Fatal("shutdown did not cancel the undo timer")
	}
	if delivered.Load() != 0 || a.sendq.receipts["shutdown-key"].outcome != sendCancelled {
		t.Fatal("shutdown lost the pre-submission outcome")
	}
	replay := httptest.NewRecorder()
	a.enqueue(replay, nil, &mail.Outgoing{}, "shutdown-key", "hash")
	if replay.Code != http.StatusConflict {
		t.Fatalf("cancelled replay: %d", replay.Code)
	}
}

func TestHandleSendReceiptIsScopedToOwner(t *testing.T) {
	body := []byte(`{"to":"dest@example.com","subject":"s","text":"hello"}`)
	hash := mutationRequestHash("POST", "/api/send", "", body)
	db, script := openOutboxSteps(t, nil, outboxStored("already-submitted", "submitted", hash), outboxEmpty(), dbStep{kind: "query", rows: &testRows{columns: []string{"mirror", "provider"}}})
	a := &App{db: db, log: discardLogger()}
	for _, tc := range []struct {
		owner string
		code  int
	}{{"owner-1", 200}, {"owner-2", 412}} {
		r := httptest.NewRequest("POST", "/api/send", bytes.NewReader(body))
		r.Header.Set("Idempotency-Key", "shared-key")
		r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, tc.owner))
		w := httptest.NewRecorder()
		a.handleSend(w, r)
		if w.Code != tc.code {
			t.Fatalf("%s: %d %s", tc.owner, w.Code, w.Body.String())
		}
	}
	log := script.log()
	if log[0].args[0] != "owner-1" || log[1].args[0] != "owner-2" {
		t.Fatal("receipt lookup escaped owner scope")
	}
}
