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
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neutron-build/neutron/mail"
	"golang.org/x/oauth2"
)

type oauthStateDriver struct {
	mu         sync.Mutex
	ciphertext string
	updates    int
	forceZero  bool
}

type oauthStateConn struct{ driver *oauthStateDriver }

func (d *oauthStateDriver) Open(string) (driver.Conn, error) { return &oauthStateConn{driver: d}, nil }
func (c *oauthStateConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not supported")
}
func (c *oauthStateConn) Close() error              { return nil }
func (c *oauthStateConn) Begin() (driver.Tx, error) { return nil, errors.New("not supported") }

func (c *oauthStateConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	c.driver.mu.Lock()
	defer c.driver.mu.Unlock()
	return &testRows{columns: []string{"cred_ciphertext"}, values: [][]driver.Value{{c.driver.ciphertext}}}, nil
}

func (c *oauthStateConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.Contains(query, "AND cred_ciphertext=$3") {
		return nil, errors.New("OAuth update is not compare-and-swap")
	}
	c.driver.mu.Lock()
	defer c.driver.mu.Unlock()
	if c.driver.forceZero {
		return driver.RowsAffected(0), nil
	}
	if args[2].Value != c.driver.ciphertext {
		return driver.RowsAffected(0), nil
	}
	c.driver.ciphertext = args[0].Value.(string)
	c.driver.updates++
	return driver.RowsAffected(1), nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

var oauthDriverID atomic.Uint64

func TestGraphAttachmentsSerializeAsFileAttachments(t *testing.T) {
	atts, sessions, err := graphAttachments([]mail.Attachment{
		{Filename: "invoice.pdf", ContentType: "application/pdf", Data: []byte("PDF")},
		{Filename: "blob.bin", Data: []byte("B")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("small files must stay inline, got %d session files", len(sessions))
	}
	if len(atts) != 2 {
		t.Fatalf("entries = %d, want 2", len(atts))
	}
	first := atts[0]
	if first["@odata.type"] != "#microsoft.graph.fileAttachment" {
		t.Errorf("@odata.type = %v, want fileAttachment", first["@odata.type"])
	}
	if first["name"] != "invoice.pdf" {
		t.Errorf("name = %v, want invoice.pdf", first["name"])
	}
	if first["contentType"] != "application/pdf" {
		t.Errorf("contentType = %v, want the attachment's content type", first["contentType"])
	}
	if first["contentBytes"] != base64.StdEncoding.EncodeToString([]byte("PDF")) {
		t.Errorf("contentBytes = %v, want base64 data", first["contentBytes"])
	}
	if atts[1]["contentType"] != "application/octet-stream" {
		t.Errorf("empty content type did not fall back to octet-stream: %v", atts[1]["contentType"])
	}
}

// Files above the inline cap are no longer refused — they ride upload
// sessions at delivery — but the 25 MB total still rejects the whole set
// before anything is sent.
func TestGraphAttachmentsSplitOversizeFilesAndCapTheTotal(t *testing.T) {
	big := make([]byte, graphAttachmentMax+1)
	inline, sessions, err := graphAttachments([]mail.Attachment{
		{Filename: "small.txt", ContentType: "text/plain", Data: []byte("hi")},
		{Filename: "big.pdf", ContentType: "application/pdf", Data: big},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(inline) != 1 || inline[0]["name"] != "small.txt" {
		t.Fatalf("inline entries = %+v, want only the small file", inline)
	}
	if len(sessions) != 1 || sessions[0].Filename != "big.pdf" || len(sessions[0].Data) != len(big) {
		t.Fatalf("session entries = %+v, want the oversize file verbatim", sessions)
	}
	if sessions[0].ContentType != "application/pdf" {
		t.Fatalf("session entry lost its content type: %q", sessions[0].ContentType)
	}

	many := make([]mail.Attachment, 0, 9)
	for range 9 {
		many = append(many, mail.Attachment{Filename: "f", Data: make([]byte, 3<<20)})
	}
	if _, _, err := graphAttachments(many); err == nil {
		t.Fatal("over-total attachments accepted")
	}
	if _, _, err := graphAttachments(nil); err != nil {
		t.Fatalf("no attachments should pass: %v", err)
	}
}

func TestOAuthRefreshSerializesCASAndPreservesRefreshToken(t *testing.T) {
	cfg := &Config{
		SecretKey:             "0123456789abcdef0123456789abcdef",
		MicrosoftClientID:     "client",
		MicrosoftClientSecret: "secret",
		MicrosoftTenant:       "common",
	}
	old := oauth2.Token{
		AccessToken:  "expired-access",
		RefreshToken: "rotated-refresh",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(-time.Hour),
	}
	raw, err := json.Marshal(&old)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealSecret(cfg, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	state := &oauthStateDriver{ciphertext: sealed}
	driverName := fmt.Sprintf("lullmail-oauth-%d", oauthDriverID.Add(1))
	sql.Register(driverName, state)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(2)
	t.Cleanup(func() { db.Close() })

	var refreshes atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		refreshes.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"fresh-access","token_type":"Bearer","expires_in":3600}`)),
		}, nil
	})}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, client)
	a := &App{cfg: cfg, db: db}

	start := make(chan struct{})
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			cred, err := a.oauthToken(ctx, "graph", "account-1", "owner@example.com", sealed)
			if err == nil && cred.AccessToken != "fresh-access" {
				err = errors.New("returned stale access token")
			}
			errs <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}

	if got := refreshes.Load(); got != 1 {
		t.Fatalf("token endpoint calls = %d, want 1", got)
	}
	state.mu.Lock()
	stored, updates := state.ciphertext, state.updates
	state.mu.Unlock()
	if updates != 1 {
		t.Fatalf("credential updates = %d, want 1", updates)
	}
	plain, err := openSecret(cfg, stored)
	if err != nil {
		t.Fatal(err)
	}
	var fresh oauth2.Token
	if err := json.Unmarshal([]byte(plain), &fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.RefreshToken != old.RefreshToken {
		t.Fatalf("refresh token = %q, want preserved %q", fresh.RefreshToken, old.RefreshToken)
	}
}

func TestOAuthRefreshReturnsErrorWhenCASUpdatesNoRows(t *testing.T) {
	cfg := &Config{
		SecretKey:             "0123456789abcdef0123456789abcdef",
		MicrosoftClientID:     "client",
		MicrosoftClientSecret: "secret",
		MicrosoftTenant:       "common",
	}
	old := oauth2.Token{
		AccessToken:  "expired-access",
		RefreshToken: "old-refresh",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(-time.Hour),
	}
	raw, err := json.Marshal(&old)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealSecret(cfg, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	state := &oauthStateDriver{ciphertext: sealed, forceZero: true}
	driverName := fmt.Sprintf("lullmail-oauth-%d", oauthDriverID.Add(1))
	sql.Register(driverName, state)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"fresh-access","refresh_token":"rotated-refresh","token_type":"Bearer","expires_in":3600}`)),
		}, nil
	})}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, client)
	a := &App{cfg: cfg, db: db}

	cred, err := a.oauthToken(ctx, "graph", "account-1", "owner@example.com", sealed)
	if err == nil {
		t.Fatalf("oauthToken returned credential with access token %q after zero-row CAS", cred.AccessToken)
	}
	if !strings.Contains(err.Error(), "changed concurrently") {
		t.Fatalf("oauthToken error = %q, want concurrent change error", err)
	}
	state.mu.Lock()
	stored, updates := state.ciphertext, state.updates
	state.mu.Unlock()
	if stored != sealed || updates != 0 {
		t.Fatalf("credential state changed after zero-row CAS: updates=%d", updates)
	}
}

// A Graph reply must never try to carry In-Reply-To/References through
// internetMessageHeaders (Graph documents x--only custom headers): the
// documented path is createReply, whose draft carries the threading headers
// the service built, then a shape-and-send.
func TestGraphReplyThreadsThroughCreateReply(t *testing.T) {
	cfg := &Config{
		SecretKey:             "0123456789abcdef0123456789abcdef",
		MicrosoftClientID:     "client",
		MicrosoftClientSecret: "secret",
		MicrosoftTenant:       "common",
	}
	token, err := json.Marshal(oauth2.Token{AccessToken: "graph-token", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealSecret(cfg, string(token))
	if err != nil {
		t.Fatal(err)
	}
	credRow := &testRows{
		columns: []string{"provider", "address", "username", "host", "port", "cred_ciphertext"},
		values:  [][]driver.Value{{"graph", "owner@example.com", "", "", int64(0), sealed}},
	}
	a := &App{cfg: cfg, log: discardLogger(), db: openStepDB(t,
		dbStep{kind: "query", rows: credRow},
		dbStep{kind: "query", rows: &testRows{
			columns: []string{"cred_ciphertext"},
			values:  [][]driver.Value{{sealed}},
		}},
	)}

	type call struct {
		method, path string
		body         string
	}
	var mu sync.Mutex
	var calls []call
	saved := providerHTTP.Transport
	t.Cleanup(func() { providerHTTP.Transport = saved })
	providerHTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := ""
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			body = string(raw)
		}
		mu.Lock()
		calls = append(calls, call{r.Method, r.URL.Path, body})
		mu.Unlock()
		reply := "{}"
		switch {
		case strings.HasSuffix(r.URL.Path, "/createReply"):
			reply = `{"id":"draft-1"}`
		}
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(reply)),
		}, nil
	})

	out := &mail.Outgoing{
		To:          []mail.Address{{Email: "peer@example.com"}},
		Subject:     "Re: hello",
		Text:        "reply body",
		InReplyTo:   "parent@example.com",
		References:  []string{"parent@example.com"},
		Attachments: []mail.Attachment{{Filename: "a.txt", ContentType: "text/plain", Data: []byte("hi")}},
	}
	if err := a.sendOAuth(context.Background(), "graph", "acct-1", out, "n:graph:AAMkParent"); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 4 {
		t.Fatalf("graph calls = %d, want 4: %+v", len(calls), calls)
	}
	want := []struct{ method, path string }{
		{http.MethodPost, "/v1.0/me/messages/AAMkParent/createReply"},
		{http.MethodPatch, "/v1.0/me/messages/draft-1"},
		{http.MethodPost, "/v1.0/me/messages/draft-1/attachments"},
		{http.MethodPost, "/v1.0/me/messages/draft-1/send"},
	}
	for i, w := range want {
		if calls[i].method != w.method || calls[i].path != w.path {
			t.Fatalf("call %d = %s %s, want %s %s", i, calls[i].method, calls[i].path, w.method, w.path)
		}
	}
	for i, c := range calls {
		if strings.Contains(c.body, "internetMessageHeaders") {
			t.Fatalf("call %d still sends internetMessageHeaders: %s", i, c.body)
		}
	}
	if !strings.Contains(calls[1].body, `"subject":"Re: hello"`) || !strings.Contains(calls[1].body, "peer@example.com") {
		t.Fatalf("draft patch lost the composer's subject or recipients: %s", calls[1].body)
	}
}

// Fresh (non-reply) Graph sends keep using sendMail — and the payload must
// not carry the unsupported headers field either.
func TestGraphFreshSendUsesSendMailWithoutCustomHeaders(t *testing.T) {
	cfg := &Config{
		SecretKey:             "0123456789abcdef0123456789abcdef",
		MicrosoftClientID:     "client",
		MicrosoftClientSecret: "secret",
		MicrosoftTenant:       "common",
	}
	token, err := json.Marshal(oauth2.Token{AccessToken: "graph-token", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealSecret(cfg, string(token))
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: cfg, log: discardLogger(), db: openStepDB(t,
		dbStep{kind: "query", rows: &testRows{
			columns: []string{"provider", "address", "username", "host", "port", "cred_ciphertext"},
			values:  [][]driver.Value{{"graph", "owner@example.com", "", "", int64(0), sealed}},
		}},
		dbStep{kind: "query", rows: &testRows{
			columns: []string{"cred_ciphertext"},
			values:  [][]driver.Value{{sealed}},
		}},
	)}

	var mu sync.Mutex
	var bodies []string
	saved := providerHTTP.Transport
	t.Cleanup(func() { providerHTTP.Transport = saved })
	providerHTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, r.Method+" "+r.URL.Path+" "+string(raw))
		mu.Unlock()
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader("{}")),
		}, nil
	})

	out := &mail.Outgoing{To: []mail.Address{{Email: "peer@example.com"}}, Subject: "hello", Text: "fresh"}
	if err := a.sendOAuth(context.Background(), "graph", "acct-1", out, ""); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 || !strings.HasPrefix(bodies[0], "POST /v1.0/me/sendMail ") {
		t.Fatalf("calls = %v, want a single sendMail", bodies)
	}
	if strings.Contains(bodies[0], "internetMessageHeaders") {
		t.Fatalf("sendMail payload still carries unsupported custom headers: %s", bodies[0])
	}
}

// A fresh Graph send with a file above the inline cap goes through a draft:
// shape it, carry small files inline, stream the oversize one through an
// upload session in 320 KiB-multiple chunks, then send the draft — never
// sendMail, whose payload Graph would reject outright (audit 6 F11).
func TestGraphFreshSendWithLargeAttachmentUsesUploadSession(t *testing.T) {
	cfg := &Config{
		SecretKey:             "0123456789abcdef0123456789abcdef",
		MicrosoftClientID:     "client",
		MicrosoftClientSecret: "secret",
		MicrosoftTenant:       "common",
	}
	token, err := json.Marshal(oauth2.Token{AccessToken: "graph-token", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealSecret(cfg, string(token))
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: cfg, log: discardLogger(), db: openStepDB(t,
		dbStep{kind: "query", rows: &testRows{
			columns: []string{"provider", "address", "username", "host", "port", "cred_ciphertext"},
			values:  [][]driver.Value{{"graph", "owner@example.com", "", "", int64(0), sealed}},
		}},
		dbStep{kind: "query", rows: &testRows{
			columns: []string{"cred_ciphertext"},
			values:  [][]driver.Value{{sealed}},
		}},
	)}

	big := make([]byte, graphAttachmentMax+1)
	for i := range big {
		big[i] = byte(i % 251)
	}
	type uploadChunk struct {
		contentRange string
		data         []byte
	}
	var mu sync.Mutex
	var calls []string
	var chunks []uploadChunk
	saved := providerHTTP.Transport
	t.Cleanup(func() { providerHTTP.Transport = saved })
	providerHTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPut {
			raw, _ := io.ReadAll(r.Body)
			chunks = append(chunks, uploadChunk{r.Header.Get("Content-Range"), raw})
			calls = append(calls, "PUT "+r.URL.Host)
			return &http.Response{
				StatusCode: http.StatusAccepted,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader("{}")),
			}, nil
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		reply := "{}"
		switch {
		case r.URL.Path == "/v1.0/me/messages" && r.Method == http.MethodPost:
			reply = `{"id":"draft-9"}`
		case strings.HasSuffix(r.URL.Path, "/createUploadSession"):
			reply = `{"uploadUrl":"https://outlook.office.com/attachment-sessions/abc"}`
		}
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(reply)),
		}, nil
	})

	out := &mail.Outgoing{
		To:      []mail.Address{{Email: "peer@example.com"}},
		Subject: "big",
		Text:    "fresh",
		Attachments: []mail.Attachment{
			{Filename: "big.bin", ContentType: "application/octet-stream", Data: big},
			{Filename: "small.txt", ContentType: "text/plain", Data: []byte("hi")},
		},
	}
	if err := a.sendOAuth(context.Background(), "graph", "acct-1", out, ""); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"POST /v1.0/me/messages",
		"PATCH /v1.0/me/messages/draft-9",
		"POST /v1.0/me/messages/draft-9/attachments",
		"POST /v1.0/me/messages/draft-9/attachments/createUploadSession",
	}
	for range (len(big) + graphUploadChunk - 1) / graphUploadChunk {
		want = append(want, "PUT outlook.office.com")
	}
	want = append(want, "POST /v1.0/me/messages/draft-9/send")
	if len(calls) != len(want) {
		t.Fatalf("graph calls = %+v, want %+v", calls, want)
	}
	for i, w := range want {
		if calls[i] != w {
			t.Fatalf("call %d = %q, want %q (all: %+v)", i, calls[i], w, calls)
		}
	}
	var reassembled []byte
	for i, chunk := range chunks {
		var start, end, total int
		if n, err := fmt.Sscanf(chunk.contentRange, "bytes %d-%d/%d", &start, &end, &total); err != nil || n != 3 {
			t.Fatalf("chunk %d Content-Range = %q", i, chunk.contentRange)
		}
		if total != len(big) {
			t.Fatalf("chunk %d total = %d, want %d", i, total, len(big))
		}
		if end-start+1 != len(chunk.data) {
			t.Fatalf("chunk %d range %s does not match %d bytes", i, chunk.contentRange, len(chunk.data))
		}
		if start != len(reassembled) {
			t.Fatalf("chunk %d starts at %d, want %d", i, start, len(reassembled))
		}
		if i < len(chunks)-1 && len(chunk.data)%(320<<10) != 0 {
			t.Fatalf("non-final chunk %d size %d is not a 320 KiB multiple", i, len(chunk.data))
		}
		reassembled = append(reassembled, chunk.data...)
	}
	if !bytes.Equal(reassembled, big) {
		t.Fatal("chunks do not reassemble the attachment")
	}
}

// An upload-session failure happens strictly before the provider send
// boundary: the leftover draft is deleted and the error is terminal
// (NotSubmittedError), so the outbox records a refusal rather than an
// ambiguity.
func TestGraphUploadFailureDeletesDraftAndIsTerminal(t *testing.T) {
	cfg := &Config{
		SecretKey:             "0123456789abcdef0123456789abcdef",
		MicrosoftClientID:     "client",
		MicrosoftClientSecret: "secret",
		MicrosoftTenant:       "common",
	}
	token, err := json.Marshal(oauth2.Token{AccessToken: "graph-token", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealSecret(cfg, string(token))
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: cfg, log: discardLogger(), db: openStepDB(t,
		dbStep{kind: "query", rows: &testRows{
			columns: []string{"provider", "address", "username", "host", "port", "cred_ciphertext"},
			values:  [][]driver.Value{{"graph", "owner@example.com", "", "", int64(0), sealed}},
		}},
		dbStep{kind: "query", rows: &testRows{
			columns: []string{"cred_ciphertext"},
			values:  [][]driver.Value{{sealed}},
		}},
	)}

	var mu sync.Mutex
	var calls []string
	saved := providerHTTP.Transport
	t.Cleanup(func() { providerHTTP.Transport = saved })
	providerHTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		status := http.StatusAccepted
		reply := "{}"
		switch {
		case r.URL.Path == "/v1.0/me/messages" && r.Method == http.MethodPost:
			reply = `{"id":"draft-9"}`
		case strings.HasSuffix(r.URL.Path, "/createUploadSession"):
			status = http.StatusInternalServerError
			reply = `{"error":{"message":"storage unavailable"}}`
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(reply)),
		}, nil
	})

	out := &mail.Outgoing{
		To:          []mail.Address{{Email: "peer@example.com"}},
		Subject:     "big",
		Text:        "fresh",
		Attachments: []mail.Attachment{{Filename: "big.bin", Data: make([]byte, graphAttachmentMax+1)}},
	}
	err = a.sendOAuth(context.Background(), "graph", "acct-1", out, "")
	if err == nil {
		t.Fatal("sendOAuth accepted a failed upload session")
	}
	var terminal *mail.NotSubmittedError
	if !errors.As(err, &terminal) {
		t.Fatalf("error %v is not terminal (NotSubmittedError)", err)
	}

	mu.Lock()
	defer mu.Unlock()
	var deleted, sent bool
	for _, c := range calls {
		if strings.HasPrefix(c, "DELETE /v1.0/me/messages/") {
			deleted = true
		}
		if strings.HasSuffix(c, "/send") {
			sent = true
		}
	}
	if !deleted {
		t.Fatalf("leftover draft was not deleted: %+v", calls)
	}
	if sent {
		t.Fatalf("the draft was sent despite the upload failure: %+v", calls)
	}
}
