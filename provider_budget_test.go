package main

// Provider-transport budget and reply-context regressions (deeper pass
// LUL-GRAPH-01, LUL-D11, LUL-D12). These prove, with the production
// serializers and fake provider transports, that (a) a composition a
// provider is guaranteed to refuse on size is rejected synchronously
// BEFORE durable acceptance with the draft retained, (b) a multi-file
// Graph composition over the aggregate write budget still sends via the
// per-request draft route whose every piece fits, and (c) a Gmail reply
// carries the parent's native thread id captured at admission, not
// synthesized or trusted from the client.

import (
	"context"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neutron-build/neutron/mail"
	"golang.org/x/oauth2"
)

// providerApp builds the App the oauth tests use: a scripted credential
// lookup for one OAuth account whose stored token is still fresh.
func providerApp(t *testing.T, provider string) *App {
	t.Helper()
	cfg := &Config{
		SecretKey: "0123456789abcdef0123456789abcdef",
	}
	switch provider {
	case "gmail":
		cfg.GoogleClientID = "google-client"
		cfg.GoogleClientSecret = "google-secret"
	default:
		cfg.MicrosoftClientID = "client"
		cfg.MicrosoftClientSecret = "secret"
		cfg.MicrosoftTenant = "common"
	}
	token, err := json.Marshal(oauth2.Token{AccessToken: provider + "-token", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealSecret(cfg, string(token))
	if err != nil {
		t.Fatal(err)
	}
	return &App{cfg: cfg, log: discardLogger(), db: openStepDB(t,
		dbStep{kind: "query", rows: &testRows{
			columns: []string{"provider", "address", "username", "host", "port", "cred_ciphertext"},
			values:  [][]driver.Value{{provider, "owner@example.com", "", "", int64(0), sealed}},
		}},
		dbStep{kind: "query", rows: &testRows{
			columns: []string{"cred_ciphertext"},
			values:  [][]driver.Value{{sealed}},
		}},
	)}
}

// captureTransport records every provider request: method, path and body.
// overBudget, when nonzero, is the documented write cap the fake provider
// enforces: any JSON body past it is a fixture failure, asserted when the
// test finishes so every request the product made is inspected.
type capturedRequest struct {
	Method string
	Path   string
	Body   string
}

func captureTransport(t *testing.T, overBudget int, respond func(capturedRequest) string) *[]capturedRequest {
	t.Helper()
	var mu sync.Mutex
	captured := []capturedRequest{}
	saved := providerHTTP.Transport
	t.Cleanup(func() {
		providerHTTP.Transport = saved
		if overBudget <= 0 {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, req := range captured {
			if len(req.Body) > overBudget {
				t.Errorf("provider refused a %d-byte %s %s body as over the %d-byte write budget", len(req.Body), req.Method, req.Path, overBudget)
			}
		}
	})
	providerHTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var raw []byte
		if r.Body != nil {
			raw, _ = io.ReadAll(r.Body)
		}
		req := capturedRequest{Method: r.Method, Path: r.URL.Path, Body: string(raw)}
		mu.Lock()
		captured = append(captured, req)
		mu.Unlock()
		reply := "{}"
		if r.Method == http.MethodPut {
			// Upload-session chunk: acknowledge the received range so the
			// next chunk continues, exactly as the existing session tests
			// fake it.
			var start, end int
			if n, perr := fmt.Sscanf(r.Header.Get("Content-Range"), "bytes %d-%d", &start, &end); perr == nil && n == 2 {
				reply = fmt.Sprintf(`{"nextExpectedRanges":["%d-"]}`, end+1)
			}
		} else if respond != nil {
			reply = respond(req)
		}
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(reply)),
		}, nil
	})
	return &captured
}

func graphReplyFor(req capturedRequest) string {
	switch {
	case req.Method == http.MethodPost && req.Path == "/v1.0/me/messages":
		return `{"id":"draft-1"}`
	case strings.HasSuffix(req.Path, "/createUploadSession"):
		return `{"uploadUrl":"https://outlook.office.com/attachment-sessions/s1"}`
	}
	return "{}"
}

// Two individually small files are each "inline", pass every raw-byte cap,
// and make one sendMail request Graph refuses on its documented write
// limit: the aggregate JSON carries both files' base64. The composition
// still sends — through the draft route, whose PATCH and per-file
// attachment POSTs each fit the budget — with exactly one final send.
func TestGraphMultiAttachmentAggregateOverBudgetRoutesThroughDraft(t *testing.T) {
	a := providerApp(t, "graph")
	captured := captureTransport(t, graphJSONWriteBudget, graphReplyFor)

	file := make([]byte, 2<<20)
	out := &mail.Outgoing{
		To:      []mail.Address{{Email: "peer@example.com"}},
		Subject: "aggregate",
		Text:    "fresh",
		Attachments: []mail.Attachment{
			{Filename: "a.bin", ContentType: "application/octet-stream", Data: file},
			{Filename: "b.bin", ContentType: "application/octet-stream", Data: file},
		},
	}
	// Admission preflight passes: every piece the draft route sends fits.
	if problem := validateTransportComposition("graph", out); problem != "" {
		t.Fatalf("two 2 MiB files refused before the queue: %q", problem)
	}
	if err := a.sendOAuth(context.Background(), "graph", "acct-1", out, "", ""); err != nil {
		t.Fatal(err)
	}

	var sendMail, draftCreate, patch, attachmentPOST, finalSend, uploadSession int
	for _, req := range *captured {
		switch {
		case req.Method == http.MethodPost && strings.HasSuffix(req.Path, "/me/sendMail"):
			sendMail++
			t.Errorf("an aggregate over the write budget took the one-shot sendMail route")
		case req.Method == http.MethodPost && req.Path == "/v1.0/me/messages":
			draftCreate++
		case req.Method == http.MethodPatch:
			patch++
		case req.Method == http.MethodPost && strings.HasSuffix(req.Path, "/attachments"):
			attachmentPOST++
		case req.Method == http.MethodPost && strings.HasSuffix(req.Path, "/send"):
			finalSend++
		case req.Method == http.MethodPost && strings.HasSuffix(req.Path, "/createUploadSession"):
			uploadSession++
		}
	}
	if draftCreate != 1 || patch != 1 || attachmentPOST != 2 || finalSend != 1 || uploadSession != 0 {
		t.Fatalf("route = create %d, patch %d, attachments %d, send %d, sessions %d; want 1/1/2/1/0",
			draftCreate, patch, attachmentPOST, finalSend, uploadSession)
	}
	if sendMail != 0 {
		t.Fatalf("sendMail calls = %d, want 0", sendMail)
	}
}

// A file of exactly 3 MiB sits at Microsoft's documented upload-session
// minimum, and its base64 inline JSON would not fit the write budget —
// at and above the cap is the session route. One byte either side of the
// boundary stays on the expected route, and a tiny file keeps the fast
// one-shot path.
func TestGraphAttachmentBoundaryAtSessionMinimum(t *testing.T) {
	atCap := make([]byte, graphAttachmentMax)
	overCap := make([]byte, graphAttachmentMax+1)
	inline, sessions, err := graphAttachments([]mail.Attachment{{Filename: "at.bin", Data: atCap}})
	if err != nil {
		t.Fatal(err)
	}
	if len(inline) != 0 || len(sessions) != 1 {
		t.Fatalf("exactly 3 MiB: inline=%d sessions=%d, want the session route", len(inline), len(sessions))
	}
	inline, sessions, err = graphAttachments([]mail.Attachment{{Filename: "over.bin", Data: overCap}})
	if err != nil {
		t.Fatal(err)
	}
	if len(inline) != 0 || len(sessions) != 1 {
		t.Fatalf("3 MiB+1: inline=%d sessions=%d, want the session route", len(inline), len(sessions))
	}
	inline, sessions, err = graphAttachments([]mail.Attachment{{Filename: "tiny.txt", ContentType: "text/plain", Data: []byte("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(inline) != 1 || len(sessions) != 0 {
		t.Fatalf("tiny file: inline=%d sessions=%d, want inline", len(inline), len(sessions))
	}

	// The tiny file keeps the fast one-shot route end to end.
	a := providerApp(t, "graph")
	captured := captureTransport(t, graphJSONWriteBudget, graphReplyFor)
	out := &mail.Outgoing{To: []mail.Address{{Email: "peer@example.com"}}, Subject: "tiny", Text: "fresh",
		Attachments: []mail.Attachment{{Filename: "tiny.txt", ContentType: "text/plain", Data: []byte("hi")}}}
	if err := a.sendOAuth(context.Background(), "graph", "acct-1", out, "", ""); err != nil {
		t.Fatal(err)
	}
	if len(*captured) != 1 || (*captured)[0].Method != http.MethodPost || !strings.HasSuffix((*captured)[0].Path, "/me/sendMail") {
		t.Fatalf("tiny composition took %d requests, want a single sendMail", len(*captured))
	}
}

// A provider that refuses every over-budget write request still sees every
// supported multi-file composition complete: the aggregate case splits
// across the draft route, and the session case carries no inline JSON at
// all.
func TestGraphSupportedCompositionsNeverExceedTheWriteBudget(t *testing.T) {
	for name, atts := range map[string][]mail.Attachment{
		"two inline 2 MiB files": {
			{Filename: "a.bin", Data: make([]byte, 2<<20)},
			{Filename: "b.bin", Data: make([]byte, 2<<20)},
		},
		"one 3 MiB session file plus a tiny inline file": {
			{Filename: "tiny.txt", ContentType: "text/plain", Data: []byte("hi")},
			{Filename: "big.bin", Data: make([]byte, 3<<20)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			a := providerApp(t, "graph")
			captureTransport(t, graphJSONWriteBudget, graphReplyFor)
			out := &mail.Outgoing{To: []mail.Address{{Email: "peer@example.com"}}, Subject: "s", Text: "fresh", Attachments: atts}
			if problem := validateTransportComposition("graph", out); problem != "" {
				t.Fatalf("supported composition refused at admission: %q", problem)
			}
			if err := a.sendOAuth(context.Background(), "graph", "acct-1", out, "", ""); err != nil {
				t.Fatalf("delivery failed: %v", err)
			}
		})
	}
}

// Compositions with no legal split are refused synchronously — before
// durable acceptance, with the draft retained — and never attempted at
// delivery either: a body too large for any single request, and a file
// below the upload-session minimum whose inline JSON is over the budget.
func TestGraphUnsplittableCompositionsAreRefusedSynchronously(t *testing.T) {
	bigBody := strings.Repeat("x", graphJSONWriteBudget)
	if problem := validateTransportComposition("graph", &mail.Outgoing{
		To: []mail.Address{{Email: "peer@example.com"}}, Subject: "s", Text: bigBody,
	}); problem == "" || !strings.Contains(problem, "body") {
		t.Fatalf("oversized body-only composition: problem = %q", problem)
	}
	// 3 MiB - 1: above the inline JSON crossover, below the session
	// minimum — no legal route exists.
	unsplittable := make([]byte, graphAttachmentMax-1)
	if problem := validateTransportComposition("graph", &mail.Outgoing{
		To: []mail.Address{{Email: "peer@example.com"}}, Subject: "s", Text: "hi",
		Attachments: []mail.Attachment{{Filename: "hole.bin", Data: unsplittable}},
	}); problem == "" || !strings.Contains(problem, "hole.bin") {
		t.Fatalf("unsplittable single file: problem = %q", problem)
	}
	// A file just under the crossover keeps its inline route: 2.8 MiB
	// base64 plus its JSON wrapper fits the budget.
	fits := make([]byte, 2936012)
	if problem := validateTransportComposition("graph", &mail.Outgoing{
		To: []mail.Address{{Email: "peer@example.com"}}, Subject: "s", Text: "hi",
		Attachments: []mail.Attachment{{Filename: "fits.bin", Data: fits}},
	}); problem != "" {
		t.Fatalf("inline-fitting single file refused: %q", problem)
	}
	// The early upload-session branch cannot hide an impossible body:
	// an oversize session file PLUS the oversized body is still refused.
	if problem := validateTransportComposition("graph", &mail.Outgoing{
		To: []mail.Address{{Email: "peer@example.com"}}, Subject: "s", Text: bigBody,
		Attachments: []mail.Attachment{{Filename: "big.bin", Data: make([]byte, 3<<20)}},
	}); problem == "" || !strings.Contains(problem, "body") {
		t.Fatalf("oversize file masking an oversized body: problem = %q", problem)
	}

	// Delivery holds the same line with nothing attempted at the
	// provider: the draft is created, the PATCH is refused locally, and
	// the leftover draft is cleaned up.
	a := providerApp(t, "graph")
	captured := captureTransport(t, 0, graphReplyFor)
	err := a.sendOAuth(context.Background(), "graph", "acct-1", &mail.Outgoing{
		To: []mail.Address{{Email: "peer@example.com"}}, Subject: "s", Text: bigBody,
		Attachments: []mail.Attachment{{Filename: "tiny.txt", ContentType: "text/plain", Data: []byte("hi")}},
	}, "", "")
	if !mail.IsNotSubmitted(err) {
		t.Fatalf("unsplittable delivery error = %v, want NotSubmitted", err)
	}
	var patch, send, attachments, cleanup int
	for _, req := range *captured {
		switch {
		case req.Method == http.MethodPatch:
			patch++
		case req.Method == http.MethodPost && strings.HasSuffix(req.Path, "/send"):
			send++
		case req.Method == http.MethodPost && strings.HasSuffix(req.Path, "/attachments"):
			attachments++
		case req.Method == http.MethodDelete:
			cleanup++
		}
	}
	if patch != 0 || send != 0 || attachments != 0 || cleanup != 1 {
		t.Fatalf("provider saw patch=%d send=%d attachments=%d cleanup=%d, want only the draft cleanup", patch, send, attachments, cleanup)
	}
}

// Gmail has no complete-message preflight in the attachment validator;
// MIME expansion grows attachments (base64 plus line breaks) and text
// (quoted-printable) past every input cap. The note's deterministic
// counterexample — two 12 MiB files, a 1 MiB body of '=', input JSON
// under the 34 MiB request cap, rendered MIME over the 35 MiB budget —
// must be refused before the queue, computed here with the production
// renderer rather than a modeled lower bound.
func TestGmailPreflightRejectsMIMEExpansionBeforeQueue(t *testing.T) {
	body := strings.Repeat("=", 1<<20)
	out := &mail.Outgoing{
		To:      []mail.Address{{Email: "peer@example.com"}},
		Subject: "expansion",
		Text:    body,
		Attachments: []mail.Attachment{
			{Filename: "a.bin", ContentType: "application/octet-stream", Data: make([]byte, 12<<20)},
			{Filename: "b.bin", ContentType: "application/octet-stream", Data: make([]byte, 12<<20)},
		},
	}
	// The input side really is inside every existing cap: 24 MiB decoded
	// attachments (25 MiB cap), each file under 15 MiB.
	problem := validateTransportComposition("gmail", out)
	if problem == "" {
		t.Fatal("MIME-expanding Gmail composition passed the preflight")
	}
	if !strings.Contains(problem, "Gmail") {
		t.Fatalf("preflight problem does not name the Gmail limit: %q", problem)
	}

	// Attachments just under the raw caps with a realistic UTF-8 dual
	// alternative body stay admissible: the preflight must not tighten
	// into false rejections.
	ok := &mail.Outgoing{
		To:      []mail.Address{{Email: "peer@example.com"}, {Name: "Ünïcode", Email: "u@example.com"}},
		Subject: "résumé — perfectly ordinary",
		Text:    "plain body with ünïcode and line breaks\r\nsecond line",
		HTML:    "<p>html body with ünïcode</p>",
		Attachments: []mail.Attachment{
			{Filename: "a.bin", ContentType: "application/octet-stream", Data: make([]byte, 10<<20)},
			{Filename: "b.bin", ContentType: "application/octet-stream", Data: make([]byte, 10<<20)},
		},
	}
	if problem := validateTransportComposition("gmail", ok); problem != "" {
		t.Fatalf("admissible composition refused: %q", problem)
	}
	// Bcc rides the rendered MIME (the raw upload has no envelope), and
	// the preflight bounds those bytes too.
	ok.Bcc = []mail.Address{{Email: "bcc@example.com"}}
	raw, err := renderGmailMIME(ok)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Bcc:") {
		t.Fatal("Gmail MIME dropped the Bcc header")
	}
}

// Delivery re-renders and rechecks the exact bytes: a composition that
// somehow passed admission (legacy payload) is refused before anything
// reaches the provider, as a known-unsent failure rather than a
// provider-side 413 after acceptance.
func TestGmailDeliveryRefusesOverBudgetMIMEWithoutProviderCall(t *testing.T) {
	a := providerApp(t, "gmail")
	captured := captureTransport(t, 0, nil)
	out := &mail.Outgoing{
		To:      []mail.Address{{Email: "peer@example.com"}},
		Subject: "expansion",
		Text:    strings.Repeat("=", 1<<20),
		Attachments: []mail.Attachment{
			{Filename: "a.bin", ContentType: "application/octet-stream", Data: make([]byte, 12<<20)},
			{Filename: "b.bin", ContentType: "application/octet-stream", Data: make([]byte, 12<<20)},
		},
	}
	err := a.sendOAuth(context.Background(), "gmail", "acct-1", out, "", "")
	if !mail.IsNotSubmitted(err) {
		t.Fatalf("over-budget delivery error = %v, want NotSubmitted", err)
	}
	if len(*captured) != 0 {
		t.Fatalf("provider was contacted %d times for an over-budget message", len(*captured))
	}
}

// A Gmail reply sends the parent's native thread id beside the raw MIME,
// with the RFC threading headers still inside the MIME itself.
func TestGmailReplySendsPersistedNativeThreadID(t *testing.T) {
	a := providerApp(t, "gmail")
	captured := captureTransport(t, 0, nil)
	out := &mail.Outgoing{
		To:        []mail.Address{{Email: "peer@example.com"}},
		Subject:   "Re: thread",
		Text:      "reply",
		InReplyTo: "parent@example.com",
		References: []string{
			"root@example.com",
			"parent@example.com",
		},
	}
	if err := a.sendOAuth(context.Background(), "gmail", "acct-1", out, "n:gmail:MSG-1", "T-123"); err != nil {
		t.Fatal(err)
	}
	if len(*captured) != 1 {
		t.Fatalf("gmail requests = %d, want 1", len(*captured))
	}
	var sent struct {
		Raw      string `json:"raw"`
		ThreadID string `json:"threadId"`
	}
	if err := json.Unmarshal([]byte((*captured)[0].Body), &sent); err != nil {
		t.Fatal(err)
	}
	if sent.ThreadID != "T-123" {
		t.Fatalf("threadId = %q, want the parent's native T-123", sent.ThreadID)
	}
	raw, err := base64.RawURLEncoding.DecodeString(sent.Raw)
	if err != nil {
		t.Fatal(err)
	}
	mime := string(raw)
	if !strings.Contains(mime, "In-Reply-To: <parent@example.com>") ||
		!strings.Contains(mime, "References: <root@example.com> <parent@example.com>") {
		t.Fatalf("reply MIME lost its RFC threading headers:\n%.300s", mime)
	}
}

// A legacy pending payload with no persisted thread id recovers it from
// the mirror's parent envelope while that parent is still retained; when
// retention has removed the parent, the reply still sends — without
// inventing a thread id — carrying only the RFC headers.
func TestGmailReplyRecoversLegacyThreadIDFromMirror(t *testing.T) {
	testGmailReplyWithStore(t, "T-mirror-9", "T-mirror-9")
	testGmailReplyWithStore(t, "", "")
}

func testGmailReplyWithStore(t *testing.T, storedThread, wantThread string) {
	t.Helper()
	p := newProductPG(t)
	p.cfg.GoogleClientID = "google-client"
	p.cfg.GoogleClientSecret = "google-secret"
	ctx := context.Background()
	token, err := json.Marshal(oauth2.Token{AccessToken: "gmail-token", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	sealedToken, err := sealSecret(p.cfg, string(token))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(`INSERT INTO email_accounts
		(user_id, mirror_account_id, provider, address, label, username, host, port, smtp_host, smtp_port, cred_ciphertext)
		VALUES ($1,'gmail-acct','gmail','owner@example.com','','owner@example.com','',0,'',0,$2)`, p.uid, sealedToken); err != nil {
		t.Fatal(err)
	}
	if storedThread != "" {
		if err := p.app.store.PutEnvelopes(ctx, "gmail-acct", []mail.Envelope{{
			ID: mail.NativeMessageID(mail.ProviderGmail, "MSG-1"), ThreadID: mail.ThreadID(storedThread),
			MailboxIDs: []mail.MailboxID{"INBOX"}, ReceivedAt: time.Now().UTC(),
		}}); err != nil {
			t.Fatal(err)
		}
	}

	captured := captureTransport(t, 0, nil)
	out := &mail.Outgoing{
		To:        []mail.Address{{Email: "peer@example.com"}},
		Subject:   "Re: legacy",
		Text:      "reply",
		InReplyTo: "parent@example.com",
	}
	if err := p.app.sendOAuth(ctx, "gmail", "gmail-acct", out, "n:gmail:MSG-1", ""); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte((*captured)[0].Body), &sent); err != nil {
		t.Fatal(err)
	}
	if wantThread == "" {
		if _, ok := sent["threadId"]; ok {
			t.Fatalf("reply invented a thread id for an unmirrored parent: %v", sent["threadId"])
		}
	} else if sent["threadId"] != wantThread {
		t.Fatalf("threadId = %v, want %q recovered from the mirror", sent["threadId"], wantThread)
	}
}

// The admission side of LUL-D12: handleSend captures the parent's native
// thread id into the encrypted outbox payload — from the ownership-checked
// parent envelope, scoped by the account join, never the client body. Two
// accounts can hold the same provider-local message id with different
// native threads; each reply must persist its own account's thread, a
// fresh send must persist none, and a restart-equivalent delivery pass
// (payload → deliveryFor, no in-memory state) must submit exactly the
// persisted id.
func TestGmailReplyAdmissionPersistsAccountScopedThreadContext(t *testing.T) {
	p := newProductPG(t)
	p.cfg.GoogleClientID = "google-client"
	p.cfg.GoogleClientSecret = "google-secret"
	ctx := context.Background()
	token, err := json.Marshal(oauth2.Token{AccessToken: "gmail-token", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealSecret(p.cfg, string(token))
	if err != nil {
		t.Fatal(err)
	}
	// Two Gmail accounts, each holding a message with the SAME native id
	// but a different native thread.
	public := map[string]string{}
	for name, thread := range map[string]string{"gmail-a": "T-A", "gmail-b": "T-B"} {
		if err := p.app.store.PutEnvelopes(ctx, mail.AccountID(name), []mail.Envelope{{
			ID: mail.NativeMessageID(mail.ProviderGmail, "MSG-1"), ThreadID: mail.ThreadID(thread),
			MailboxIDs: []mail.MailboxID{"INBOX"}, ReceivedAt: time.Now().UTC(),
		}}); err != nil {
			t.Fatal(err)
		}
		var id string
		if err := p.db.QueryRow(`INSERT INTO email_accounts
			(user_id, mirror_account_id, provider, address, label, username, host, port, smtp_host, smtp_port, cred_ciphertext)
			VALUES ($1,$2,'gmail',$3,'',$3,'',0,'',0,$4) RETURNING id::text`, p.uid, name, name+"@example.com", sealed).Scan(&id); err != nil {
			t.Fatal(err)
		}
		public[name] = id
	}

	post := func(body string) string {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
		w := httptest.NewRecorder()
		p.app.handleSend(w, r)
		if w.Code != 200 {
			t.Fatalf("send status = %d: %s", w.Code, w.Body.String())
		}
		var queued struct {
			ID string `json:"queued"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &queued); err != nil || queued.ID == "" {
			t.Fatalf("no durable id in %s", w.Body.String())
		}
		return queued.ID
	}
	payloadOf := func(id string) outboxPayload {
		t.Helper()
		var ciphertext string
		if err := p.db.QueryRow(`SELECT payload_ciphertext FROM outbox_jobs WHERE id=$1`, id).Scan(&ciphertext); err != nil {
			t.Fatal(err)
		}
		plain, err := openBound(p.cfg, "payload", p.uid, id, ciphertext)
		if err != nil {
			t.Fatal(err)
		}
		var payload outboxPayload
		if err := json.Unmarshal([]byte(plain), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}

	replyA := post(`{"to":"peer@example.com","subject":"Re: t","text":"reply","account_id":"` + public["gmail-a"] + `","reply_to_message_id":"n:gmail:MSG-1"}`)
	replyB := post(`{"to":"peer@example.com","subject":"Re: t","text":"reply","account_id":"` + public["gmail-b"] + `","reply_to_message_id":"n:gmail:MSG-1"}`)
	fresh := post(`{"to":"peer@example.com","subject":"fresh","text":"hi","account_id":"` + public["gmail-a"] + `"}`)
	if got := payloadOf(replyA); got.ReplyParent != "n:gmail:MSG-1" || got.ReplyThreadID != "T-A" {
		t.Fatalf("account A's reply persisted %+v/%+v, want parent n:gmail:MSG-1 thread T-A", got.ReplyParent, got.ReplyThreadID)
	}
	if got := payloadOf(replyB); got.ReplyThreadID != "T-B" {
		t.Fatalf("same provider-local id cross-selected: account B's reply persisted thread %q, want T-B", got.ReplyThreadID)
	}
	if got := payloadOf(fresh); got.ReplyParent != "" || got.ReplyThreadID != "" {
		t.Fatalf("fresh send persisted reply context %+v/%+v, want none", got.ReplyParent, got.ReplyThreadID)
	}

	// Restart-equivalent delivery: everything the worker has is the
	// decoded payload; the fake Gmail transport must receive exactly the
	// persisted (account-scoped) thread id.
	captured := captureTransport(t, 0, nil)
	payload := payloadOf(replyA)
	deliver, _, ok := p.app.deliveryFor(ctx, "gmail-a", payload.ReplyParent, payload.ReplyThreadID)
	if !ok {
		t.Fatal("no delivery closure for gmail-a")
	}
	if err := deliver(ctx, &mail.Outgoing{To: []mail.Address{{Email: "peer@example.com"}}, Subject: "Re: t", Text: "reply", InReplyTo: "p@example.com"}); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte((*captured)[0].Body), &sent); err != nil {
		t.Fatal(err)
	}
	if sent["threadId"] != "T-A" {
		t.Fatalf("delivered threadId = %v, want the persisted account-scoped T-A", sent["threadId"])
	}
}
