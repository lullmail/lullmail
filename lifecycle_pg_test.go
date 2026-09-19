package main

// Real-PostgreSQL lifecycle regressions (audit OPS-04/OPS-05): the
// background task group drains at shutdown, account deletion never waits
// behind another account's work, and cancellation reaches provider I/O.

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neutron-build/neutron/mail"
)

// blockingAdapter is a provider stand-in whose Sync parks until the
// release channel fires or the sync context is canceled — exactly the
// shape of a provider call in flight when a deletion or the shutdown
// drain arrives. unwind is how long the call takes to land its cleanup
// AFTER cancellation, standing in for writeback that must not be cut.
type blockingAdapter struct {
	provider mail.Provider
	entered  chan struct{}
	release  chan struct{}
	unwind   time.Duration
	canceled atomic.Bool
	returned atomic.Bool
}

func (b *blockingAdapter) Provider() mail.Provider { return b.provider }
func (b *blockingAdapter) Close() error            { return nil }
func (b *blockingAdapter) Mailboxes(ctx context.Context) ([]mail.Mailbox, error) {
	return []mail.Mailbox{{ID: "INBOX", Name: "INBOX", Role: "inbox"}}, nil
}
func (b *blockingAdapter) Sync(ctx context.Context, mailbox mail.MailboxID, cur mail.Cursor) (*mail.Changes, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
		b.returned.Store(true)
		return &mail.Changes{Complete: true}, nil
	case <-ctx.Done():
		b.canceled.Store(true)
		time.Sleep(b.unwind)
		b.returned.Store(true)
		return nil, ctx.Err()
	}
}
func (b *blockingAdapter) Envelopes(ctx context.Context, ids []mail.MessageID) ([]mail.Envelope, error) {
	return nil, nil
}
func (b *blockingAdapter) Body(ctx context.Context, id mail.MessageID) (*mail.Body, error) {
	return nil, errors.New("not implemented")
}
func (b *blockingAdapter) Raw(ctx context.Context, id mail.MessageID) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}
func (b *blockingAdapter) Attachment(ctx context.Context, id mail.MessageID, partID string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}
func (b *blockingAdapter) Apply(ctx context.Context, op mail.Operation) error {
	return errors.New("not implemented")
}

// seedAccount inserts the product and mirror rows one syncable account
// needs, returning the email_accounts id.
func seedAccount(t *testing.T, p productPG, mirror, address string) string {
	t.Helper()
	ctx := context.Background()
	if err := p.app.store.PutAccount(ctx, &mail.Account{
		ID: mail.AccountID(mirror), Provider: mail.ProviderIMAP, Email: address, Name: address,
	}); err != nil {
		t.Fatal(err)
	}
	sealed, err := sealSecret(p.cfg, "app-password")
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := p.db.QueryRowContext(ctx, `INSERT INTO email_accounts
		(user_id,mirror_account_id,provider,address,host,port,cred_ciphertext)
		VALUES ($1,$2,'imap',$3,'imap.example.com',993,$4) RETURNING id::text`,
		p.uid, mirror, address, sealed).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// The OPS-04 headline: shutdown cancels the background root, the
// cancellation reaches the provider call through the account gate's
// context, and the join waits for the in-flight work to land before the
// caller would close the pools — the finalization skip also keeps the
// interrupted sync from smearing "context canceled" into last_error.
func TestIntegrationShutdownDrainsInFlightSync(t *testing.T) {
	p := newProductPG(t)
	seedAccount(t, p, "drain-acct", "drain@example.com")

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	adapter := &blockingAdapter{provider: mail.ProviderIMAP, entered: entered, release: release, unwind: 300 * time.Millisecond}
	p.app.dial = func(ctx context.Context, acct mail.AccountID, cred mail.Credential) (mail.Adapter, func(), error) {
		return adapter, func() {}, nil
	}
	p.app.launchAccountSync("drain-acct")
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("sync never reached the provider call")
	}

	stopReturned := make(chan bool, 1)
	stopBegan := time.Now()
	go func() { stopReturned <- p.app.stopBackground(5 * time.Second) }()
	// The join must outlast the provider call's post-cancel cleanup: a
	// drain that returned before the work landed would close the pools
	// underneath it — the exact defect OPS-04 records.
	select {
	case <-stopReturned:
		t.Fatal("shutdown joined without waiting for the in-flight sync")
	case <-time.After(150 * time.Millisecond):
	}

	select {
	case <-stopReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown drain never completed")
	}
	if elapsed := time.Since(stopBegan); elapsed < adapter.unwind {
		t.Fatalf("shutdown returned after %v, before the work's %v unwind finished", elapsed, adapter.unwind)
	}
	if !adapter.returned.Load() {
		t.Fatal("join returned before the provider call did")
	}
	if !adapter.canceled.Load() {
		t.Fatal("shutdown did not cancel the in-flight provider I/O")
	}
	// The interrupted sync owes no finalization at shutdown: no error is
	// recorded on the account row (the OPS-04 writeback-noise residual).
	var lastError sql.NullString
	if err := p.db.QueryRow(`SELECT last_error FROM email_accounts WHERE mirror_account_id='drain-acct'`).Scan(&lastError); err != nil {
		t.Fatal(err)
	}
	if lastError.Valid && lastError.String != "" {
		t.Fatalf("shutdown recorded a spurious last_error: %q", lastError.String)
	}
	// Admission is sealed once the drain began.
	ran := make(chan struct{}, 1)
	p.app.launch("post-drain", func(context.Context) { close(ran) })
	select {
	case <-ran:
		t.Fatal("work was admitted after shutdown began")
	default:
	}
}

// The OPS-05 headline: deleting account B completes while account A's sync
// is still in flight, and deleting A cancels A's own provider call instead
// of waiting it out.
func TestIntegrationAccountDeletionIndependentAndCancelling(t *testing.T) {
	p := newProductPG(t)
	idA := seedAccount(t, p, "acct-a", "a@example.com")
	idB := seedAccount(t, p, "acct-b", "b@example.com")

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	adapter := &blockingAdapter{provider: mail.ProviderIMAP, entered: entered, release: release}
	p.app.dial = func(ctx context.Context, acct mail.AccountID, cred mail.Credential) (mail.Adapter, func(), error) {
		return adapter, func() {}, nil
	}
	p.app.launchAccountSync("acct-a")
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("account A's sync never started")
	}

	// Deleting B must not wait behind A's parked sync.
	doneB := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodDelete, "/api/accounts/"+idB, nil)
		r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
		p.app.deleteAccount(w, r, idB)
		doneB <- w
	}()
	var wB *httptest.ResponseRecorder
	select {
	case wB = <-doneB:
	case <-time.After(2 * time.Second):
		t.Fatal("deleting account B waited behind account A's work")
	}
	if wB.Code != http.StatusOK {
		t.Fatalf("account B deletion status = %d: %s", wB.Code, wB.Body.String())
	}
	var existsB bool
	if err := p.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM email_accounts WHERE mirror_account_id='acct-b')`).Scan(&existsB); err != nil {
		t.Fatal(err)
	}
	if existsB {
		t.Fatal("account B survived its deletion")
	}
	select {
	case <-entered:
		t.Fatal("account A's sync was disturbed by account B's deletion")
	default:
	}

	// Deleting A seals A's gate: the parked provider call is canceled, the
	// active count drains, and the deletion proceeds.
	doneA := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodDelete, "/api/accounts/"+idA, nil)
		r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
		p.app.deleteAccount(w, r, idA)
		doneA <- w
	}()
	var wA *httptest.ResponseRecorder
	select {
	case wA = <-doneA:
	case <-time.After(2 * time.Second):
		t.Fatal("deleting account A never drained the canceled sync")
	}
	if wA.Code != http.StatusOK {
		t.Fatalf("account A deletion status = %d: %s", wA.Code, wA.Body.String())
	}
	if !adapter.canceled.Load() {
		t.Fatal("deletion did not cancel the account's in-flight provider I/O")
	}
	var existsA bool
	if err := p.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM email_accounts WHERE mirror_account_id='acct-a')`).Scan(&existsA); err != nil {
		t.Fatal(err)
	}
	if existsA {
		t.Fatal("account A survived its deletion")
	}
}

// Duplicate manual syncs for one account coalesce: the second request
// waits on the context-aware gate and returns as soon as its context goes
// away, without ever reaching the provider.
func TestIntegrationDuplicateSyncCoalescesOnContextGate(t *testing.T) {
	p := newProductPG(t)
	seedAccount(t, p, "coalesce-acct", "coalesce@example.com")

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var dials atomic.Int32
	adapter := &blockingAdapter{provider: mail.ProviderIMAP, entered: entered, release: release}
	p.app.dial = func(ctx context.Context, acct mail.AccountID, cred mail.Credential) (mail.Adapter, func(), error) {
		dials.Add(1)
		return adapter, func() {}, nil
	}

	first := make(chan error, 1)
	go func() {
		first <- p.app.syncAccount(context.Background(), "coalesce-acct")
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first sync never dialed")
	}

	secondDone := make(chan error, 1)
	ctx2, cancel := context.WithCancel(context.Background())
	go func() { secondDone <- p.app.syncAccount(ctx2, "coalesce-acct") }()
	select {
	case <-secondDone:
		t.Fatal("second sync ran concurrently instead of queueing")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-secondDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("queued sync returned %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled queued sync did not return promptly")
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first sync failed: %v", err)
	}
	if n := dials.Load(); n != 1 {
		t.Fatalf("provider dials = %d, want exactly one", n)
	}
}

// ownerSession builds a request whose context carries the owner and a
// session hash, the two values the auth middleware normally injects.
func ownerSession(method, target, body, uid, session string) *http.Request {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, rd)
	ctx := context.WithValue(r.Context(), authContextKey{}, uid)
	if session != "" {
		ctx = context.WithValue(ctx, sessionContextKey{}, session)
	}
	return r.WithContext(ctx)
}
