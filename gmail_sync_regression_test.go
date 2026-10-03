package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neutron-build/neutron/mail"
	"github.com/neutron-build/neutron/mail/dialer"
	"golang.org/x/oauth2"
)

type gmailTokenDriver struct{ state *oauthStateDriver }
type gmailTokenConn struct{ *oauthStateConn }

func (d *gmailTokenDriver) Open(string) (driver.Conn, error) {
	return &gmailTokenConn{&oauthStateConn{driver: d.state}}, nil
}
func (c *gmailTokenConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.driver.mu.Lock()
	defer c.driver.mu.Unlock()
	switch {
	case strings.Contains(query, "SELECT provider, address, username"):
		return &testRows{columns: []string{"provider", "address", "username", "host", "port", "cred_ciphertext"}, values: [][]driver.Value{{"gmail", "synthetic@example.invalid", "", "", int64(0), c.driver.ciphertext}}}, nil
	case strings.Contains(query, "SELECT provider, address, cred_ciphertext"):
		return &testRows{columns: []string{"provider", "address", "cred_ciphertext"}, values: [][]driver.Value{{"gmail", "synthetic@example.invalid", c.driver.ciphertext}}}, nil
	default:
		return &testRows{columns: []string{"cred_ciphertext"}, values: [][]driver.Value{{c.driver.ciphertext}}}, nil
	}
}

type gmailAdmissionStore struct {
	mail.Store
	mu      sync.Mutex
	account mail.Account
	marked  int
}

func (s *gmailAdmissionStore) Accounts(context.Context) ([]mail.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []mail.Account{s.account}, nil
}
func (s *gmailAdmissionStore) Account(context.Context, mail.AccountID) (*mail.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.account
	return &a, nil
}
func (s *gmailAdmissionStore) PutMailboxes(context.Context, mail.AccountID, []mail.Mailbox) error {
	return nil
}
func (s *gmailAdmissionStore) SetNeedsReauth(_ context.Context, _ mail.AccountID, v bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.account.NeedsReauth = v
	s.marked++
	return nil
}

func gmailOAuthFixture(t *testing.T, expiry time.Time) (*App, *oauthStateDriver) {
	t.Helper()
	cfg := &Config{SecretKey: "0123456789abcdef0123456789abcdef", GoogleClientID: "synthetic-client", GoogleClientSecret: "synthetic-secret"}
	raw, _ := json.Marshal(oauth2.Token{AccessToken: "synthetic-old", RefreshToken: "synthetic-refresh", TokenType: "Bearer", Expiry: expiry})
	sealed, err := sealSecret(cfg, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	state := &oauthStateDriver{ciphertext: sealed}
	name := fmt.Sprintf("gmail-refresh-%d", oauthDriverID.Add(1))
	sql.Register(name, &gmailTokenDriver{state})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &App{cfg: cfg, db: db}, state
}
func gmailJSON(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestGmailExpiredReadRefreshesOnceAndRemainsSchedulerEligible(t *testing.T) {
	app, state := gmailOAuthFixture(t, time.Now().Add(time.Hour))
	reads, refreshes := 0, 0
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Host != "gmail.googleapis.com" {
			t.Fatalf("unexpected provider request: %s %s", r.Method, r.URL)
		}
		reads++
		if r.Header.Get("Authorization") == "Bearer synthetic-old" {
			return gmailJSON(401, `{"error":{"code":401,"message":"expired"}}`), nil
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-fresh" {
			t.Fatalf("unexpected bearer")
		}
		return gmailJSON(200, `{"labels":[]}`), nil
	})
	tokenClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		refreshes++
		return gmailJSON(200, `{"access_token":"synthetic-fresh","token_type":"Bearer","expires_in":3600}`), nil
	})}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, tokenClient)
	store := &gmailAdmissionStore{account: mail.Account{ID: mail.AccountID(t.Name()), Provider: mail.ProviderGmail}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := mail.NewEngine(store, log)
	scheduler := mail.NewScheduler(store, eng, nil, log)
	scheduler.Jitter = false
	scheduler.Tokens = app
	scheduler.Resolve = dialer.NewWithGmailRefresh(app.refreshGmailToken)
	var last error
	scheduler.AfterSync = func(_ context.Context, _ mail.Account, _ []mail.SyncReport, err error) { last = err }
	scheduler.RunOnce(ctx)
	if last != nil || refreshes != 1 || reads != 2 || state.updates != 1 || store.account.NeedsReauth {
		t.Fatalf("first run: err=%v refreshes=%d reads=%d updates=%d reauth=%v", last, refreshes, reads, state.updates, store.account.NeedsReauth)
	}
	scheduler.RunOnce(ctx)
	if last != nil || refreshes != 1 || reads != 3 || store.account.NeedsReauth {
		t.Fatalf("second admission: err=%v refreshes=%d reads=%d reauth=%v", last, refreshes, reads, store.account.NeedsReauth)
	}
}

func TestGmailInvalidGrantPausesSchedulerBeforeProviderRead(t *testing.T) {
	app, _ := gmailOAuthFixture(t, time.Now().Add(-time.Hour))
	refreshes := 0
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		refreshes++
		return gmailJSON(400, `{"error":"invalid_grant"}`), nil
	})})
	store := &gmailAdmissionStore{account: mail.Account{ID: mail.AccountID(t.Name()), Provider: mail.ProviderGmail}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	scheduler := mail.NewScheduler(store, mail.NewEngine(store, log), nil, log)
	scheduler.Jitter = false
	scheduler.Tokens = app
	scheduler.Resolve = func(context.Context, mail.AccountID, mail.Credential) (mail.Adapter, func(), error) {
		t.Fatal("invalid grant reached provider resolver")
		return nil, nil, nil
	}
	var last error
	scheduler.AfterSync = func(_ context.Context, _ mail.Account, _ []mail.SyncReport, err error) { last = err }
	scheduler.RunOnce(ctx)
	before := refreshes
	if !errors.Is(last, mail.ErrReauthRequired) || !store.account.NeedsReauth || before == 0 {
		t.Fatalf("err=%v refreshes=%d reauth=%v", last, before, store.account.NeedsReauth)
	}
	scheduler.RunOnce(ctx)
	if refreshes != before {
		t.Fatalf("invalid grant retried on next tick: %d to %d", before, refreshes)
	}
}

func TestGmailRejectedTokenRefreshReusesConcurrentReplacement(t *testing.T) {
	app, state := gmailOAuthFixture(t, time.Now().Add(time.Hour))
	refreshes := 0
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		refreshes++
		return gmailJSON(200, `{"access_token":"synthetic-fresh","token_type":"Bearer","expires_in":3600}`), nil
	})})
	for range 2 {
		cred, err := app.oauthTokenAfterRejection(ctx, "gmail", "same-account", "synthetic@example.invalid", "ignored", "synthetic-old")
		if err != nil || cred.AccessToken != "synthetic-fresh" {
			t.Fatalf("cred=%v err=%v", cred.Provider, err)
		}
	}
	if refreshes != 1 || state.updates != 1 {
		t.Fatalf("refreshes=%d updates=%d", refreshes, state.updates)
	}
}

func TestOAuthRefreshLockWaitIsCancellable(t *testing.T) {
	app, _ := gmailOAuthFixture(t, time.Now().Add(time.Hour))
	unlock, err := oauthRefreshLock(t.Name()).Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = app.oauthTokenAfterRejection(ctx, "gmail", t.Name(), "synthetic@example.invalid", "ignored", "synthetic-old")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lock wait ignored cancellation: %v", err)
	}
}
