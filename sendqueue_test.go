package main

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestDecodeAttachments(t *testing.T) {
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

	if atts, problem := decodeAttachments(nil, 25<<20); atts != nil || problem != "" {
		t.Fatalf("empty set = %v, %q; want nil, ok", atts, problem)
	}
	atts, problem := decodeAttachments([]sendAttachmentRequest{
		{Filename: "a.txt", ContentType: "text/plain", DataB64: b64("hello")},
		{Filename: "b.bin", DataB64: b64("xyz")},
	}, 25<<20)
	if problem != "" || len(atts) != 2 || string(atts[0].Data) != "hello" || atts[1].ContentType != "" {
		t.Fatalf("valid set = %+v, %q", atts, problem)
	}

	for _, tc := range []struct {
		name string
		reqs []sendAttachmentRequest
		want string
	}{
		{"bad base64", []sendAttachmentRequest{{Filename: "a", DataB64: "!!!"}}, "not valid base64"},
		{"missing data", []sendAttachmentRequest{{Filename: "a"}}, "no data"},
	} {
		if _, problem := decodeAttachments(tc.reqs, 25<<20); problem == "" || !strings.Contains(problem, tc.want) {
			t.Errorf("%s: problem = %q, want it to mention %q", tc.name, problem, tc.want)
		}
	}

	big := base64.StdEncoding.EncodeToString(make([]byte, 15<<20+1))
	if _, problem := decodeAttachments([]sendAttachmentRequest{{Filename: "big", DataB64: big}}, 25<<20); !strings.Contains(problem, "15 MiB") {
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
			db: openStepDB(t,
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
			),
		}
	}
	file := func(size int) sendAttachmentRequest {
		return sendAttachmentRequest{Filename: "f.bin", DataB64: base64.StdEncoding.EncodeToString(make([]byte, size))}
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

// Graph's 3 MB per-file limit must reject the request BEFORE queue
// acceptance, not inside delivery after the browser draft is gone
// (audit SEND-06).
func TestHandleSendRejectsGraphOversizeAttachmentsPreQueue(t *testing.T) {
	cfg := &Config{SecretKey: "0123456789abcdef0123456789abcdef"}
	a := &App{
		cfg:   cfg,
		log:   discardLogger(),
		sendq: newSendQueue(),
		db: openStepDB(t,
			dbStep{kind: "query", rows: &testRows{
				columns: []string{"mirror_account_id", "provider"},
				values:  [][]driver.Value{{"mirror-1", "graph"}},
			}},
		),
	}
	big := base64.StdEncoding.EncodeToString(make([]byte, 3<<20+1))
	body, _ := json.Marshal(map[string]any{
		"to": "dest@example.com", "subject": "big", "text": "hi",
		"attachments": []sendAttachmentRequest{{Filename: "f.bin", DataB64: big}},
	})
	r := httptest.NewRequest(http.MethodPost, "/api/send", bytes.NewReader(body))
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, "owner-1"))
	w := httptest.NewRecorder()
	a.handleSend(w, r)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "3 MB") {
		t.Fatalf("graph oversize attachment: status = %d body = %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "queued") {
		t.Fatal("an oversize Graph attachment was queued anyway")
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
	guarded := a.guardDelivery(acct, func(ctx context.Context, _ *mail.Outgoing) error {
		close(delivered)
		<-ctx.Done() // hold the lease like a slow submission would
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
	case <-deletionDone:
		t.Fatal("deletion completed while a delivery held the account lease")
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
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
				kind:  "query",
				rows:  &testRows{columns: []string{"mirror_account_id", "provider"}, values: [][]driver.Value{{"mirror-1", "jmap"}}},
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
