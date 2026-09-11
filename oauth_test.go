package main

import (
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
	atts, err := graphAttachments([]mail.Attachment{
		{Filename: "invoice.pdf", ContentType: "application/pdf", Data: []byte("PDF")},
		{Filename: "blob.bin", Data: []byte("B")},
	})
	if err != nil {
		t.Fatal(err)
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

func TestGraphAttachmentsRejectOversizedFilesBeforeSending(t *testing.T) {
	big := make([]byte, graphAttachmentMax+1)
	if atts, err := graphAttachments([]mail.Attachment{{Filename: "big.pdf", Data: big}}); err == nil || atts != nil {
		t.Fatalf("per-file over-limit attachment accepted: %v", err)
	}
	many := make([]mail.Attachment, 0, 9)
	for range 9 {
		many = append(many, mail.Attachment{Filename: "f", Data: make([]byte, 3<<20)})
	}
	if atts, err := graphAttachments(many); err == nil || atts != nil {
		t.Fatalf("over-total attachments accepted: %v", err)
	}
	if _, err := graphAttachments(nil); err != nil {
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
