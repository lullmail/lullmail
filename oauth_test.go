package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
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
