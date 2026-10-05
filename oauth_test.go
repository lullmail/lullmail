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
	"net/url"
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
		contentRange  string
		contentType   string
		authorization string
		length        int64
		data          []byte
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
			chunks = append(chunks, uploadChunk{r.Header.Get("Content-Range"), r.Header.Get("Content-Type"), r.Header.Get("Authorization"), r.ContentLength, raw})
			calls = append(calls, "PUT "+r.URL.Host)
			reply := "{}"
			var start, end int
			if n, err := fmt.Sscanf(r.Header.Get("Content-Range"), "bytes %d-%d", &start, &end); err == nil && n == 2 {
				reply = fmt.Sprintf(`{"nextExpectedRanges":["%d-"]}`, end+1)
			}
			return &http.Response{
				StatusCode: http.StatusAccepted,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(reply)),
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
		if chunk.contentType != "application/octet-stream" {
			t.Fatalf("chunk %d Content-Type = %q, want application/octet-stream", i, chunk.contentType)
		}
		if chunk.authorization != "" {
			t.Fatalf("chunk %d leaked an Authorization header on the pre-authenticated upload URL: %q", i, chunk.authorization)
		}
		if chunk.length != int64(len(chunk.data)) {
			t.Fatalf("chunk %d Content-Length = %d, want %d", i, chunk.length, len(chunk.data))
		}
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

// A deterministic upload-session refusal (explicit 4xx) happens strictly
// before the provider send boundary: the leftover draft is deleted and the
// error is terminal (NotSubmittedError), so the outbox records a refusal
// rather than an ambiguity. Transport-level and 5xx failures of the same
// call are covered separately below — they stay ambiguous.
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
			status = http.StatusBadRequest
			reply = `{"error":{"message":"attachment session refused"}}`
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

// The pre-authenticated upload URL is only ever accepted from the one
// documented Outlook origin, over https, without userinfo. Everything else
// is refused before any request is built.
func TestValidateGraphUploadURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		ok   bool
	}{
		{"documented host", "https://outlook.office.com/attachment-sessions/abc?token=1", true},
		{"mixed-case host", "https://Outlook.Office.COM/attachment-sessions/abc", true},
		{"explicit default port", "https://outlook.office.com:443/attachment-sessions/abc", true},
		{"non-default port", "https://outlook.office.com:444/attachment-sessions/abc", false},
		{"fragment", "https://outlook.office.com/attachment-sessions/abc#frag", false},
		{"plaintext", "http://outlook.office.com/attachment-sessions/abc", false},
		{"foreign host", "https://attacker.example/upload?authtoken=1", false},
		{"suffix lookalike", "https://outlook.office.com.evil.example/upload", false},
		{"userinfo", "https://user:pass@outlook.office.com/attachment-sessions/abc", false},
		{"missing host", "https:///attachment-sessions/abc", false},
		{"not a url", "outlook office", false},
	} {
		err := validateGraphUploadURL(tc.raw)
		if tc.ok && err != nil {
			t.Errorf("%s: validateGraphUploadURL(%q) = %v, want accepted", tc.name, tc.raw, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: validateGraphUploadURL(%q) accepted an untrusted url", tc.name, tc.raw)
		}
	}
}

type graphCallLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *graphCallLog) add(call string) {
	l.mu.Lock()
	l.calls = append(l.calls, call)
	l.mu.Unlock()
}

func (l *graphCallLog) has(prefix string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// graphFault is one injected failure: a nonzero status answers with that
// status and marker body; a zero status with ranges set answers a
// successful upload PUT whose nextExpectedRanges disagree with the bytes
// sent; a bare zero status reads the whole request body and then fails the
// transport, modeling a connection lost after the bytes were written — the
// server may have committed the operation the caller never saw.
type graphFault struct {
	status int
	body   string
	ranges string
}

// graphFaultTransport serves the draft-shaping happy sequence (the fresh
// POST /me/messages shape when parent is empty, the createReply shape
// otherwise) and injects the configured fault at the first matching call.
func graphFaultTransport(parent string, faults map[string]graphFault) (roundTripFunc, *graphCallLog) {
	log := &graphCallLog{}
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			_, _ = io.ReadAll(r.Body)
		}
		key := r.Method + " " + r.URL.Path
		log.add(key)
		if fault, ok := faults[key]; ok {
			switch {
			case fault.ranges != "":
				return &http.Response{
					StatusCode: http.StatusAccepted,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"nextExpectedRanges":["` + fault.ranges + `"]}`)),
				}, nil
			case fault.status == 0:
				return nil, fmt.Errorf("connection lost after write: %s", key)
			default:
				return &http.Response{
					StatusCode: fault.status,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(fault.body)),
				}, nil
			}
		}
		reply := "{}"
		switch {
		case parent != "" && strings.HasSuffix(r.URL.Path, "/createReply"):
			reply = `{"id":"draft-9"}`
		case parent == "" && r.Method == http.MethodPost && r.URL.Path == "/v1.0/me/messages":
			reply = `{"id":"draft-9"}`
		case strings.HasSuffix(r.URL.Path, "/createUploadSession"):
			reply = `{"uploadUrl":"https://outlook.office.com/attachment-sessions/abc"}`
		case r.Method == http.MethodPut:
			var start, end int
			if n, err := fmt.Sscanf(r.Header.Get("Content-Range"), "bytes %d-%d", &start, &end); err == nil && n == 2 {
				reply = fmt.Sprintf(`{"nextExpectedRanges":["%d-"]}`, end+1)
			}
		}
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(reply)),
		}, nil
	}), log
}

// A redirect on the pre-authenticated upload URL must not be followed: the
// PUT bytes and the URL's embedded token would travel to an unvetted
// destination. The 302 itself surfaces as an error without any request to
// the redirect target, and the unfollowed 3xx is not a deterministic
// refusal, so the outcome stays ambiguous.
func TestGraphUploadDoesNotFollowRedirects(t *testing.T) {
	saved := providerHTTP.Transport
	t.Cleanup(func() { providerHTTP.Transport = saved })
	var redirectSeen bool
	providerHTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			_, _ = io.ReadAll(r.Body)
		}
		if r.Method == http.MethodPut {
			redirectSeen = true
			return &http.Response{
				StatusCode: http.StatusFound,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
					"Location":     []string{"https://evil.example/collect?token=leaked"},
				},
				Body: io.NopCloser(strings.NewReader("{}")),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"uploadUrl":"https://outlook.office.com/attachment-sessions/abc"}`)),
		}, nil
	})

	err := graphUploadAttachment(context.Background(), mail.Credential{AccessToken: "graph-token"}, "/me/messages/draft-9",
		mail.Attachment{Filename: "big.bin", ContentType: "application/octet-stream", Data: make([]byte, graphAttachmentMax+1)})
	if err == nil {
		t.Fatal("a redirected upload PUT was treated as success")
	}
	if !redirectSeen {
		t.Fatal("the upload PUT never happened")
	}
	if mail.IsNotSubmitted(err) {
		t.Fatalf("an unfollowed redirect must stay ambiguous, got terminal: %v", err)
	}
	var ge *graphHTTPError
	if !errors.As(err, &ge) || ge.Status != http.StatusFound {
		t.Fatalf("error %v does not carry the 302 status", err)
	}
}

// Only deterministic refusals (explicit 4xx answers, fully-local failures)
// may classify as NotSubmittedError. A transport failure or 5xx after the
// request bytes were written leaves the remote draft state unknown, so the
// outbox must record ambiguity even though the message was never /sent.
func TestGraphDraftSendFailureClassification(t *testing.T) {
	oversize := mail.Attachment{Filename: "big.bin", ContentType: "application/octet-stream", Data: make([]byte, graphAttachmentMax+1)}
	small := mail.Attachment{Filename: "a.txt", ContentType: "text/plain", Data: []byte("hi")}
	for _, tc := range []struct {
		name             string
		faults           map[string]graphFault
		atts             []mail.Attachment
		wantNotSubmitted bool
		wantCleanup      bool
		wantNoUploadPut  bool
		wantContains     []string
	}{
		{
			name:             "draft create 400",
			faults:           map[string]graphFault{"POST /v1.0/me/messages": {status: 400, body: `{"error":{"message":"nope"}}`}},
			wantNotSubmitted: true,
		},
		{
			name:   "draft create response lost",
			faults: map[string]graphFault{"POST /v1.0/me/messages": {}},
		},
		{
			name:             "patch 429",
			faults:           map[string]graphFault{"PATCH /v1.0/me/messages/draft-9": {status: 429, body: `{"error":{"message":"throttled"}}`}},
			wantNotSubmitted: true,
			wantCleanup:      true,
		},
		{
			name:        "patch 500",
			faults:      map[string]graphFault{"PATCH /v1.0/me/messages/draft-9": {status: 500, body: `{"error":{"message":"boom"}}`}},
			wantCleanup: true,
		},
		{
			name:        "patch response lost after write",
			faults:      map[string]graphFault{"PATCH /v1.0/me/messages/draft-9": {}},
			wantCleanup: true,
		},
		{
			name:             "inline attachment 400",
			faults:           map[string]graphFault{"POST /v1.0/me/messages/draft-9/attachments": {status: 400, body: `{"error":{"message":"bad attachment"}}`}},
			atts:             []mail.Attachment{small},
			wantNotSubmitted: true,
			wantCleanup:      true,
		},
		{
			name:             "upload session 400",
			faults:           map[string]graphFault{"POST /v1.0/me/messages/draft-9/attachments/createUploadSession": {status: 400, body: `{"error":{"message":"refused"}}`}},
			atts:             []mail.Attachment{oversize},
			wantNotSubmitted: true,
			wantCleanup:      true,
		},
		{
			name:        "upload session 500",
			faults:      map[string]graphFault{"POST /v1.0/me/messages/draft-9/attachments/createUploadSession": {status: 500, body: `{"error":{"message":"storage down"}}`}},
			atts:        []mail.Attachment{oversize},
			wantCleanup: true,
		},
		{
			name:        "upload session response lost",
			faults:      map[string]graphFault{"POST /v1.0/me/messages/draft-9/attachments/createUploadSession": {}},
			atts:        []mail.Attachment{oversize},
			wantCleanup: true,
		},
		{
			name:             "upload url from foreign origin",
			faults:           map[string]graphFault{"POST /v1.0/me/messages/draft-9/attachments/createUploadSession": {status: 202, body: `{"uploadUrl":"https://attacker.example/upload?authtoken=1"}`}},
			atts:             []mail.Attachment{oversize},
			wantNotSubmitted: true,
			wantCleanup:      true,
			wantNoUploadPut:  true,
		},
		{
			name:             "upload chunk 413",
			faults:           map[string]graphFault{"PUT /attachment-sessions/abc": {status: 413, body: `{"error":{"message":"chunk too large"}}`}},
			atts:             []mail.Attachment{oversize},
			wantNotSubmitted: true,
			wantCleanup:      true,
		},
		{
			name:        "final chunk stored but response lost",
			faults:      map[string]graphFault{"PUT /attachment-sessions/abc": {}},
			atts:        []mail.Attachment{oversize},
			wantCleanup: true,
		},
		{
			name:        "upload session range diverged",
			faults:      map[string]graphFault{"PUT /attachment-sessions/abc": {ranges: "0-"}},
			atts:        []mail.Attachment{oversize},
			wantCleanup: true,
		},
		{
			name:             "send 400",
			faults:           map[string]graphFault{"POST /v1.0/me/messages/draft-9/send": {status: 400, body: `{"error":{"message":"no recipients"}}`}},
			atts:             []mail.Attachment{oversize},
			wantNotSubmitted: true,
			wantCleanup:      true,
		},
		{
			name:        "send response lost stays ambiguous",
			faults:      map[string]graphFault{"POST /v1.0/me/messages/draft-9/send": {}},
			atts:        []mail.Attachment{oversize},
			wantCleanup: true,
		},
		{
			name: "cleanup failure is retained with the primary uncertainty",
			faults: map[string]graphFault{
				"PATCH /v1.0/me/messages/draft-9":  {},
				"DELETE /v1.0/me/messages/draft-9": {status: 500, body: `{"error":{"message":"delete exploded"}}`},
			},
			wantCleanup:  true,
			wantContains: []string{"connection lost after write", "delete exploded"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := providerHTTP.Transport
			t.Cleanup(func() { providerHTTP.Transport = saved })
			transport, log := graphFaultTransport("", tc.faults)
			providerHTTP.Transport = transport

			out := &mail.Outgoing{To: []mail.Address{{Email: "peer@example.com"}}, Subject: "t", Text: "body", Attachments: tc.atts}
			inline, sessions, err := graphAttachments(out.Attachments)
			if err != nil {
				t.Fatal(err)
			}
			a := &App{}
			err = a.graphDraftSend(context.Background(), mail.Credential{Provider: mail.Provider("graph"), AccessToken: "graph-token"}, out, inline, sessions)
			if err == nil {
				t.Fatal("graphDraftSend succeeded despite the injected fault")
			}
			if got := mail.IsNotSubmitted(err); got != tc.wantNotSubmitted {
				t.Fatalf("IsNotSubmitted = %v, want %v (err: %v)", got, tc.wantNotSubmitted, err)
			}
			if got := log.has("DELETE /v1.0/me/messages/draft-9"); got != tc.wantCleanup {
				t.Fatalf("leftover draft cleanup = %v, want %v (calls: %v)", got, tc.wantCleanup, log.calls)
			}
			if tc.wantNoUploadPut && log.has("PUT ") {
				t.Fatalf("upload PUT happened despite an untrusted session url: %v", log.calls)
			}
			for _, sub := range tc.wantContains {
				if !strings.Contains(err.Error(), sub) {
					t.Fatalf("error %q does not retain %q", err.Error(), sub)
				}
			}
		})
	}
}

// The reply path shares the draft-shaping classification: a deterministic
// refusal before /send is terminal, a lost response after the bytes were
// written is not — including on the oversize upload the audit called out.
func TestGraphReplySendFailureClassification(t *testing.T) {
	oversize := mail.Attachment{Filename: "big.bin", ContentType: "application/octet-stream", Data: make([]byte, graphAttachmentMax+1)}
	for _, tc := range []struct {
		name             string
		faults           map[string]graphFault
		atts             []mail.Attachment
		wantNotSubmitted bool
		wantCleanup      bool
	}{
		{
			name:             "createReply 400",
			faults:           map[string]graphFault{"POST /v1.0/me/messages/AAMkParent/createReply": {status: 400, body: `{"error":{"message":"no parent"}}`}},
			atts:             []mail.Attachment{oversize},
			wantNotSubmitted: true,
		},
		{
			name:        "patch 500",
			faults:      map[string]graphFault{"PATCH /v1.0/me/messages/draft-9": {status: 500, body: `{"error":{"message":"boom"}}`}},
			atts:        []mail.Attachment{oversize},
			wantCleanup: true,
		},
		{
			name:        "upload chunk response lost",
			faults:      map[string]graphFault{"PUT /attachment-sessions/abc": {}},
			atts:        []mail.Attachment{oversize},
			wantCleanup: true,
		},
		{
			name:             "send 400",
			faults:           map[string]graphFault{"POST /v1.0/me/messages/draft-9/send": {status: 400, body: `{"error":{"message":"no recipients"}}`}},
			atts:             []mail.Attachment{oversize},
			wantNotSubmitted: true,
			wantCleanup:      true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := providerHTTP.Transport
			t.Cleanup(func() { providerHTTP.Transport = saved })
			transport, log := graphFaultTransport("AAMkParent", tc.faults)
			providerHTTP.Transport = transport

			out := &mail.Outgoing{
				To:          []mail.Address{{Email: "peer@example.com"}},
				Subject:     "Re: t",
				Text:        "body",
				InReplyTo:   "parent@example.com",
				Attachments: tc.atts,
			}
			a := &App{}
			err := a.graphReplySend(context.Background(), mail.Credential{Provider: mail.Provider("graph"), AccessToken: "graph-token"}, out, "n:graph:AAMkParent")
			if err == nil {
				t.Fatal("graphReplySend succeeded despite the injected fault")
			}
			if got := mail.IsNotSubmitted(err); got != tc.wantNotSubmitted {
				t.Fatalf("IsNotSubmitted = %v, want %v (err: %v)", got, tc.wantNotSubmitted, err)
			}
			if got := log.has("DELETE /v1.0/me/messages/draft-9"); got != tc.wantCleanup {
				t.Fatalf("leftover draft cleanup = %v, want %v (calls: %v)", got, tc.wantCleanup, log.calls)
			}
		})
	}
}

// Every non-final upload chunk needs positive evidence the service accepted
// exactly the bytes through end-1: a 2xx whose body carries no matching
// nextExpectedRanges must not advance the local offset, or a malformed or
// rewritten answer can turn the stored attachment silently incomplete. The
// final chunk is different — Graph answers with the created attachment
// resource there, so it need not carry ranges (audit 8 F01).
func TestGraphUploadRequiresContinuationAcknowledgement(t *testing.T) {
	twoChunks := make([]byte, graphAttachmentMax+1)
	if graphUploadChunk >= len(twoChunks) {
		t.Fatal("fixture must span more than one chunk")
	}
	firstEnd := graphUploadChunk
	oneChunk := []byte("hello")
	for _, tc := range []struct {
		name      string
		data      []byte
		putBodies []string
		wantErr   string
		wantPUTs  int
	}{
		{
			name:      "intermediate empty JSON object",
			data:      twoChunks,
			putBodies: []string{`{}`},
			wantErr:   "no nextExpectedRanges",
			wantPUTs:  1,
		},
		{
			name:      "intermediate empty body",
			data:      twoChunks,
			putBodies: []string{``},
			wantErr:   "no continuation acknowledgement",
			wantPUTs:  1,
		},
		{
			name:      "intermediate unrelated JSON",
			data:      twoChunks,
			putBodies: []string{`{"foo":"bar"}`},
			wantErr:   "no nextExpectedRanges",
			wantPUTs:  1,
		},
		{
			name: "intermediate disjoint continuation ranges",
			data: twoChunks,
			putBodies: []string{fmt.Sprintf(`{"nextExpectedRanges":["%d-","2000000-2500000"]}`, firstEnd)},
			wantErr:   "unsupported continuation ranges",
			wantPUTs:  1,
		},
		{
			name:      "intermediate wrong range",
			data:      twoChunks,
			putBodies: []string{`{"nextExpectedRanges":["0-"]}`},
			wantErr:   "stored state diverged",
			wantPUTs:  1,
		},
		{
			name:      "intermediate undecodable body",
			data:      twoChunks,
			putBodies: []string{`{"nextExpectedRanges":[`},
			wantErr:   "undecodable",
			wantPUTs:  1,
		},
		{
			name: "acknowledged continuation then final attachment metadata",
			data: twoChunks,
			putBodies: []string{
				fmt.Sprintf(`{"nextExpectedRanges":["%d-"]}`, firstEnd),
				`{"id":"AA==","name":"big.bin"}`,
			},
			wantPUTs: 2,
		},
		{
			name: "final answer reporting nothing further",
			data: twoChunks,
			putBodies: []string{
				fmt.Sprintf(`{"nextExpectedRanges":["%d-"]}`, firstEnd),
				fmt.Sprintf(`{"nextExpectedRanges":["%d-"]}`, len(twoChunks)),
			},
			wantPUTs: 2,
		},
		{
			name:      "single final chunk with attachment metadata",
			data:      oneChunk,
			putBodies: []string{`{"id":"AA==","name":"big.bin"}`},
			wantPUTs:  1,
		},
		{
			name:      "single final chunk empty body",
			data:      oneChunk,
			putBodies: []string{``},
			wantPUTs:  1,
		},
		{
			name:      "final chunk still expecting bytes",
			data:      oneChunk,
			putBodies: []string{`{"nextExpectedRanges":["99-"]}`},
			wantErr:   "unexpectedly expects",
			wantPUTs:  1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := providerHTTP.Transport
			t.Cleanup(func() { providerHTTP.Transport = saved })
			var puts int
			providerHTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Body != nil {
					_, _ = io.ReadAll(r.Body)
				}
				if r.Method != http.MethodPut {
					return &http.Response{
						StatusCode: http.StatusAccepted,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"uploadUrl":"https://outlook.office.com/attachment-sessions/abc"}`)),
					}, nil
				}
				if puts >= len(tc.putBodies) {
					t.Errorf("upload PUT %d was sent but only %d answers were scripted", puts+1, len(tc.putBodies))
					return nil, errors.New("unexpected PUT")
				}
				body := tc.putBodies[puts]
				puts++
				return &http.Response{
					StatusCode: http.StatusAccepted,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(body)),
				}, nil
			})

			err := graphUploadAttachment(context.Background(), mail.Credential{AccessToken: "graph-token"}, "/me/messages/draft-9",
				mail.Attachment{Filename: "big.bin", ContentType: "application/octet-stream", Data: tc.data})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("upload failed: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("upload error = %v, want it to contain %q", err, tc.wantErr)
			}
			if puts != tc.wantPUTs {
				t.Fatalf("upload PUTs = %d, want %d", puts, tc.wantPUTs)
			}
		})
	}
}

// Provider message and draft ids are opaque identifiers: interpolating one
// carrying reserved path, query or fragment characters must address that one
// message resource, never rewrite the URL's structure. The wire form keeps
// the escapes (EscapedPath) while the decoded path still names exactly the
// id (audit 8 F03).
func TestGraphPathsEscapeProviderIDs(t *testing.T) {
	for _, id := range []string{"abc/def", "abc?def", "abc#def", "abc%2Fdef", "abc def"} {
		t.Run(id, func(t *testing.T) {
			saved := providerHTTP.Transport
			t.Cleanup(func() { providerHTTP.Transport = saved })
			escaped := url.PathEscape(id)
			var mu sync.Mutex
			var wire, decoded []string
			providerHTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Body != nil {
					_, _ = io.ReadAll(r.Body)
				}
				mu.Lock()
				wire = append(wire, r.Method+" "+r.URL.EscapedPath())
				decoded = append(decoded, r.URL.Path)
				mu.Unlock()
				if r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.Fragment != "" {
					t.Errorf("%s %s: id leaked into query/fragment structure (rawQuery=%q fragment=%q)", r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.URL.Fragment)
				}
				reply := "{}"
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/createReply") {
					reply = `{"id":"` + id + `"}`
				}
				return &http.Response{
					StatusCode: http.StatusAccepted,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(reply)),
				}, nil
			})

			cred := mail.Credential{Provider: mail.Provider("graph"), AccessToken: "graph-token"}
			out := &mail.Outgoing{
				To:        []mail.Address{{Email: "peer@example.com"}},
				Subject:   "t",
				Text:      "body",
				InReplyTo: "parent@example.com",
			}
			a := &App{}
			if err := a.graphReplySend(context.Background(), cred, out, "n:graph:"+id); err != nil {
				t.Fatal(err)
			}
			wantWire := []string{
				"POST /v1.0/me/messages/" + escaped + "/createReply",
				"PATCH /v1.0/me/messages/" + escaped,
				"POST /v1.0/me/messages/" + escaped + "/send",
			}
			wantDecoded := []string{
				"/v1.0/me/messages/" + id + "/createReply",
				"/v1.0/me/messages/" + id,
				"/v1.0/me/messages/" + id + "/send",
			}
			mu.Lock()
			defer mu.Unlock()
			if len(wire) != len(wantWire) {
				t.Fatalf("graph calls = %v, want %v", wire, wantWire)
			}
			for i := range wantWire {
				if wire[i] != wantWire[i] {
					t.Fatalf("call %d wire path = %q, want %q", i, wire[i], wantWire[i])
				}
				if decoded[i] != wantDecoded[i] {
					t.Fatalf("call %d decoded path = %q, want %q", i, decoded[i], wantDecoded[i])
				}
			}
		})
	}
}

// A draft send whose submission deadline expires mid-draft must still
// attempt the cleanup DELETE: cleanup derives a fresh bounded context from
// the account lifetime, not from the exhausted attempt context. The DELETE
// must arrive on a live request context with its own short deadline (audit
// 8 F02).
func TestGraphDraftCleanupOutlivesSubmissionDeadline(t *testing.T) {
	a := &App{}
	leaseCtx, release, ok := a.beginAccountWorkCtx(context.Background(), "acct-cleanup")
	if !ok {
		t.Fatal("account lease refused")
	}
	defer release()
	ctx, cancel := context.WithTimeout(leaseCtx, 50*time.Millisecond)
	defer cancel()

	var mu sync.Mutex
	var deleteSeen bool
	var deleteSlack time.Duration
	saved := providerHTTP.Transport
	t.Cleanup(func() { providerHTTP.Transport = saved })
	providerHTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			_, _ = io.ReadAll(r.Body)
		}
		if r.Method == http.MethodPatch {
			// The provider stalls until the submission deadline fires.
			select {
			case <-r.Context().Done():
				return nil, r.Context().Err()
			case <-time.After(5 * time.Second):
				return nil, errors.New("patch never observed the deadline")
			}
		}
		if r.Method == http.MethodDelete {
			mu.Lock()
			deleteSeen = true
			if deadline, ok := r.Context().Deadline(); ok {
				deleteSlack = time.Until(deadline)
			}
			mu.Unlock()
		}
		reply := "{}"
		if r.Method == http.MethodPost && r.URL.Path == "/v1.0/me/messages" {
			reply = `{"id":"draft-9"}`
		}
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(reply)),
		}, nil
	})

	out := &mail.Outgoing{To: []mail.Address{{Email: "peer@example.com"}}, Subject: "t", Text: "body"}
	err := a.graphDraftSend(ctx, mail.Credential{Provider: mail.Provider("graph"), AccessToken: "graph-token"}, out, nil, nil)
	if err == nil {
		t.Fatal("graphDraftSend succeeded despite the expired submission deadline")
	}
	if mail.IsNotSubmitted(err) {
		t.Fatalf("deadline expiry must stay ambiguous: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !deleteSeen {
		t.Fatal("cleanup DELETE was not attempted after the submission deadline expired")
	}
	if deleteSlack <= 0 || deleteSlack > graphCleanupTimeout {
		t.Fatalf("cleanup DELETE deadline slack = %v, want a fresh bound within %v", deleteSlack, graphCleanupTimeout)
	}
}

// Account deletion must still win over cleanup: the DELETE derives from the
// account lifetime, so a sealed account cancels it before any bytes are
// written (audit 8 F02).
func TestGraphDraftCleanupAbortedByAccountDeletion(t *testing.T) {
	a := &App{}
	acct := mail.AccountID("acct-cleanup-delete")
	ctx, release, ok := a.beginAccountWorkCtx(context.Background(), acct)
	if !ok {
		t.Fatal("account lease refused")
	}
	defer release()

	var mu sync.Mutex
	var deleteCtxErr error
	saved := providerHTTP.Transport
	t.Cleanup(func() { providerHTTP.Transport = saved })
	providerHTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			_, _ = io.ReadAll(r.Body)
		}
		if r.Method == http.MethodPatch {
			// Deletion begins while the provider call is in flight.
			a.accountState(acct).seal()
			return nil, errors.New("connection lost after write: PATCH /v1.0/me/messages/draft-9")
		}
		if r.Method == http.MethodDelete {
			mu.Lock()
			deleteCtxErr = r.Context().Err()
			mu.Unlock()
			return nil, deleteCtxErr
		}
		reply := "{}"
		if r.Method == http.MethodPost && r.URL.Path == "/v1.0/me/messages" {
			reply = `{"id":"draft-9"}`
		}
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(reply)),
		}, nil
	})

	out := &mail.Outgoing{To: []mail.Address{{Email: "peer@example.com"}}, Subject: "t", Text: "body"}
	err := a.graphDraftSend(ctx, mail.Credential{Provider: mail.Provider("graph"), AccessToken: "graph-token"}, out, nil, nil)
	if err == nil {
		t.Fatal("graphDraftSend succeeded despite the sealed account")
	}
	if !strings.Contains(err.Error(), "connection lost after write") {
		t.Fatalf("error %q lost the primary submission failure", err.Error())
	}
	mu.Lock()
	defer mu.Unlock()
	if deleteCtxErr == nil {
		t.Fatal("cleanup DELETE ran on a live context after the account was sealed for deletion")
	}
}
