package main

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/neutron-build/neutron/mail"
)

func expectAccountCancellation(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Error("account cancellation did not reach the admitted operation")
	}
}

func TestGuardDeliveryPropagatesAccountCancellation(t *testing.T) {
	app := &App{}
	deliver := app.guardDelivery("acct", func(ctx context.Context, _ *mail.Outgoing) error {
		if !accountLeaseHeld(ctx, "acct") {
			t.Error("delivery lost its qualified lease")
		}
		if !app.accountState("acct").seal() {
			t.Fatal("seal failed")
		}
		expectAccountCancellation(t, ctx)
		return ctx.Err()
	})
	if err := deliver(context.Background(), &mail.Outgoing{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("delivery error = %v", err)
	}
	select {
	case <-app.accountState("acct").currentDrain():
	default:
		t.Fatal("delivery leaked its account lease")
	}
}

func TestAccountHTTPWorkPropagatesAccountCancellation(t *testing.T) {
	for _, kind := range []string{"thread", "export"} {
		t.Run(kind, func(t *testing.T) {
			app := &App{db: openStepDB(t, dbStep{kind: "query", rows: &testRows{columns: []string{"mirror_account_id"}, values: [][]driver.Value{{"acct"}}}})}
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !accountLeaseHeld(r.Context(), "acct") {
					t.Error("request lost its qualified lease")
				}
				app.accountState("acct").seal()
				expectAccountCancellation(t, r.Context())
			})
			var h http.Handler = app.accountWorkLifecycle(inner)
			if kind == "export" {
				h = app.accountExportLifecycle(inner)
			}
			r := requestAsOwner(http.MethodGet, "/api/threads/t?account=acct")
			r.SetPathValue("id", "public-acct")
			h.ServeHTTP(httptest.NewRecorder(), r)
			select {
			case <-app.accountState("acct").currentDrain():
			default:
				t.Error("request leaked lease")
			}
		})
	}
}

func TestOwnerDeletionCanCancelAnActiveAccountRequest(t *testing.T) {
	app := &App{db: openStepDB(t,
		dbStep{kind: "query", rows: &testRows{columns: []string{"mirror_account_id"}, values: [][]driver.Value{{"acct"}}}},
		dbStep{kind: "query", rows: &testRows{columns: []string{"mirror_account_id"}, values: [][]driver.Value{{"acct"}}}},
	)}
	sealed := make(chan error, 1)
	h := app.accountWorkLifecycle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		go func() {
			seal, err := app.sealOwnerAccounts(context.Background(), "uid-1")
			if err == nil {
				seal.finish(false)
			}
			sealed <- err
		}()
		expectAccountCancellation(t, r.Context())
	}))
	h.ServeHTTP(httptest.NewRecorder(), requestAsOwner(http.MethodGet, "/api/threads/t?account=acct"))
	select {
	case err := <-sealed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("owner deletion did not drain request")
	}
}

// Each method records the actual context the provider receives. Calls use
// a still-live caller context, so cancellation must come from the resolver.
type cancellationProbeAdapter struct {
	mail.Adapter
	observed context.Context
}

func awaitProbeCancellation(ctx context.Context) error {
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
	}
	return ctx.Err()
}

func (a *cancellationProbeAdapter) Provider() mail.Provider { return mail.ProviderIMAP }
func (a *cancellationProbeAdapter) Close() error            { return nil }
func (a *cancellationProbeAdapter) Mailboxes(ctx context.Context) ([]mail.Mailbox, error) {
	a.observed = ctx
	return nil, awaitProbeCancellation(ctx)
}
func (a *cancellationProbeAdapter) Sync(ctx context.Context, _ mail.MailboxID, _ mail.Cursor) (*mail.Changes, error) {
	a.observed = ctx
	return nil, awaitProbeCancellation(ctx)
}
func (a *cancellationProbeAdapter) Envelopes(ctx context.Context, _ []mail.MessageID) ([]mail.Envelope, error) {
	a.observed = ctx
	return nil, awaitProbeCancellation(ctx)
}
func (a *cancellationProbeAdapter) Body(ctx context.Context, _ mail.MessageID) (*mail.Body, error) {
	a.observed = ctx
	return nil, awaitProbeCancellation(ctx)
}
func (a *cancellationProbeAdapter) Apply(ctx context.Context, _ mail.Operation) error {
	a.observed = ctx
	return awaitProbeCancellation(ctx)
}
func (a *cancellationProbeAdapter) Raw(ctx context.Context, _ mail.MessageID) (io.ReadCloser, error) {
	a.observed = ctx
	return &cancellationProbeReader{ctx: ctx}, nil
}
func (a *cancellationProbeAdapter) Attachment(ctx context.Context, _ mail.MessageID, _ string) (io.ReadCloser, error) {
	a.observed = ctx
	return &cancellationProbeReader{ctx: ctx}, nil
}

type cancellationProbeReader struct {
	ctx    context.Context
	closed bool
}

func (r *cancellationProbeReader) Read([]byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return 0, io.EOF
}
func (r *cancellationProbeReader) Close() error { r.closed = true; return nil }

type cancellationProbeSelector struct{ *cancellationProbeAdapter }

func (a *cancellationProbeSelector) SelectMailbox(ctx context.Context, _ mail.MailboxID) error {
	a.observed = ctx
	return awaitProbeCancellation(ctx)
}

type cancellationProbeAppender struct{ *cancellationProbeAdapter }

func (a *cancellationProbeAppender) Append(ctx context.Context, _ mail.MailboxID, _ []byte) error {
	a.observed = ctx
	return awaitProbeCancellation(ctx)
}

type cancellationProbeBoth struct{ *cancellationProbeAdapter }

func (a *cancellationProbeBoth) SelectMailbox(ctx context.Context, _ mail.MailboxID) error {
	a.observed = ctx
	return awaitProbeCancellation(ctx)
}
func (a *cancellationProbeBoth) Append(ctx context.Context, _ mail.MailboxID, _ []byte) error {
	a.observed = ctx
	return awaitProbeCancellation(ctx)
}

func cancellationResolver(t *testing.T, adapter mail.Adapter) (*App, mail.Adapter, func()) {
	t.Helper()
	app := &App{db: openStepDB(t, dbStep{kind: "query", rows: &testRows{columns: []string{"owned"}, values: [][]driver.Value{{true}}}})}
	app.dial = func(ctx context.Context, _ mail.AccountID, _ mail.Credential) (mail.Adapter, func(), error) {
		return adapter, func() {}, nil
	}
	got, release, err := app.accountResolver()(context.Background(), "acct", mail.Credential{Provider: mail.ProviderIMAP, Password: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return app, got, release
}

func TestAccountResolverBindsLaterProviderOperations(t *testing.T) {
	ops := map[string]func(mail.Adapter) error{
		"mailboxes": func(a mail.Adapter) error { _, err := a.Mailboxes(context.Background()); return err },
		"sync":      func(a mail.Adapter) error { _, err := a.Sync(context.Background(), "INBOX", ""); return err },
		"envelopes": func(a mail.Adapter) error { _, err := a.Envelopes(context.Background(), nil); return err },
		"body":      func(a mail.Adapter) error { _, err := a.Body(context.Background(), "msg"); return err },
		"apply":     func(a mail.Adapter) error { return a.Apply(context.Background(), mail.Operation{}) },
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			app, adapter, _ := cancellationResolver(t, &cancellationProbeAdapter{})
			app.accountState("acct").seal()
			if err := op(adapter); !errors.Is(err, context.Canceled) {
				t.Fatalf("%s after sealing = %v", name, err)
			}
		})
	}
}

func TestAccountResolverStreamsKeepCancellationUntilClose(t *testing.T) {
	for _, kind := range []string{"raw", "attachment"} {
		t.Run(kind, func(t *testing.T) {
			probe := &cancellationProbeAdapter{}
			app, adapter, _ := cancellationResolver(t, probe)
			var reader io.ReadCloser
			var err error
			if kind == "raw" {
				reader, err = adapter.Raw(context.Background(), "msg")
			} else {
				reader, err = adapter.Attachment(context.Background(), "msg", "part")
			}
			if err != nil {
				t.Fatal(err)
			}
			if probe.observed.Err() != nil {
				t.Fatal("stream context canceled before reader was used")
			}
			app.accountState("acct").seal()
			expectAccountCancellation(t, probe.observed)
			if _, err = reader.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
				t.Errorf("read after seal = %v", err)
			}
			if err = reader.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAccountResolverPreservesOptionalCapabilities(t *testing.T) {
	for _, kind := range []string{"none", "selector", "appender", "both"} {
		t.Run(kind, func(t *testing.T) {
			probe := &cancellationProbeAdapter{}
			var original mail.Adapter = probe
			switch kind {
			case "selector":
				original = &cancellationProbeSelector{probe}
			case "appender":
				original = &cancellationProbeAppender{probe}
			case "both":
				original = &cancellationProbeBoth{probe}
			}
			app, adapter, _ := cancellationResolver(t, original)
			selector, selects := adapter.(mail.MailboxSelector)
			appender, appends := adapter.(mail.Appender)
			if selects != (kind == "selector" || kind == "both") || appends != (kind == "appender" || kind == "both") {
				t.Fatalf("capabilities changed: selects=%v appends=%v", selects, appends)
			}
			app.accountState("acct").seal()
			if selects {
				if err := selector.SelectMailbox(context.Background(), "INBOX"); !errors.Is(err, context.Canceled) {
					t.Errorf("select = %v", err)
				}
			}
			if appends {
				if err := appender.Append(context.Background(), "Sent", nil); !errors.Is(err, context.Canceled) {
					t.Errorf("append = %v", err)
				}
			}
		})
	}
}

func TestAccountWorkContextNestedLeaseAndCallerCancellation(t *testing.T) {
	app := &App{}
	parent, cancel := context.WithCancel(context.Background())
	ctx, release, ok := app.beginAccountWorkCtx(parent, "acct")
	if !ok {
		t.Fatal("first admission refused")
	}
	nested, releaseNested, ok := app.beginAccountWorkCtx(ctx, "acct")
	if !ok {
		t.Fatal("nested admission refused")
	}
	app.accountState("acct").mu.Lock()
	active := app.accountState("acct").active
	app.accountState("acct").mu.Unlock()
	if active != 1 {
		t.Errorf("same-account nested lease count = %d", active)
	}
	cancel()
	expectAccountCancellation(t, nested)
	releaseNested()
	release()
	app.accountState("acct").mu.Lock()
	active = app.accountState("acct").active
	app.accountState("acct").mu.Unlock()
	if active != 0 {
		t.Fatalf("leases not drained: %d", active)
	}
}

func TestAccountResolverDialObservesAccountCancellation(t *testing.T) {
	app := &App{db: openStepDB(t, dbStep{kind: "query", rows: &testRows{columns: []string{"owned"}, values: [][]driver.Value{{true}}}})}
	entered := make(chan struct{})
	app.dial = func(ctx context.Context, _ mail.AccountID, _ mail.Credential) (mail.Adapter, func(), error) {
		close(entered)
		return nil, nil, awaitProbeCancellation(ctx)
	}
	finished := make(chan error, 1)
	go func() {
		_, _, err := app.accountResolver()(context.Background(), "acct", mail.Credential{Provider: mail.ProviderIMAP, Password: "synthetic"})
		finished <- err
	}()
	<-entered
	app.accountState("acct").seal()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("dial error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dial did not return")
	}
	select {
	case <-app.accountState("acct").currentDrain():
	default:
		t.Fatal("failed dial leaked its lease")
	}
}

func TestAccountResolverNestedLeaseRemainsOwnedByRequest(t *testing.T) {
	app := &App{db: openStepDB(t, dbStep{kind: "query", rows: &testRows{columns: []string{"owned"}, values: [][]driver.Value{{true}}}})}
	ctx, releaseRequest, ok := app.beginAccountWorkCtx(context.Background(), "acct")
	if !ok {
		t.Fatal("request admission failed")
	}
	defer releaseRequest()
	probe := &cancellationProbeAdapter{}
	app.dial = func(context.Context, mail.AccountID, mail.Credential) (mail.Adapter, func(), error) {
		return probe, func() {}, nil
	}
	_, releaseAdapter, err := app.accountResolver()(ctx, "acct", mail.Credential{Provider: mail.ProviderIMAP, Password: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	releaseAdapter()
	state := app.accountState("acct")
	state.mu.Lock()
	active := state.active
	state.mu.Unlock()
	if active != 1 {
		t.Fatalf("nested resolver released or doubled request lease: %d", active)
	}
	if ctx.Err() != nil {
		t.Fatal("releasing adapter canceled outer request")
	}
}

func TestOwnerDeletionWaitsForConcurrentAccountDeletion(t *testing.T) {
	app := &App{db: openStepDB(t,
		dbStep{kind: "query", rows: &testRows{columns: []string{"mirror_account_id"}, values: [][]driver.Value{{"acct"}}}},
		dbStep{kind: "query", rows: &testRows{columns: []string{"mirror_account_id"}, values: [][]driver.Value{{"acct"}}}},
	)}
	workCtx, release, ok := app.beginAccountWork("acct")
	if !ok {
		t.Fatal("work admission failed")
	}
	singleDone := make(chan struct{})
	go func() {
		defer close(singleDone)
		app.deleteAccount(httptest.NewRecorder(), requestAsOwner(http.MethodDelete, "/api/accounts/id"), "id")
	}()
	// The single-account deletion has sealed the gate, but the provider's
	// cleanup still owns the lease. A second delete must not mistake the
	// sealed state for one that has already retired all work.
	expectAccountCancellation(t, workCtx)
	ownerDone := make(chan error, 1)
	go func() {
		seal, err := app.sealOwnerAccounts(context.Background(), "owner-1")
		if err == nil {
			seal.finish(false)
		}
		ownerDone <- err
	}()
	returnedEarly := false
	select {
	case err := <-ownerDone:
		returnedEarly = true
		t.Errorf("owner deletion passed an account still draining: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case <-singleDone:
	case <-time.After(time.Second):
		t.Fatal("single-account deletion did not return")
	}
	// The stub refuses BeginTx, simulating a failed single-account delete.
	// Both operations must release their locks and restore admission.
	if !returnedEarly {
		select {
		case err := <-ownerDone:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("owner deletion did not resume")
		}
	}
	done, ok := app.beginAccountUse("acct")
	if !ok {
		t.Fatal("failed deletions left account sealed")
	}
	done()
}

func TestAccountAdmissionLookupFailureDoesNotRunUnleasedHandler(t *testing.T) {
	for _, kind := range []string{"query", "scan"} {
		t.Run(kind, func(t *testing.T) {
			failed := dbStep{kind: "query", err: errors.New("temporary database failure")}
			if kind == "scan" {
				failed = dbStep{kind: "query", rows: &testRows{columns: []string{"unexpected", "extra"}, values: [][]driver.Value{{"acct", "extra"}}}}
			}
			db, recorded := openRecordingDB(t, failed,
				dbStep{kind: "query", rows: &testRows{columns: []string{"mirror_account_id"}, values: [][]driver.Value{{"acct"}}}},
			)
			app := &App{db: db}
			called := false
			h := app.accountWorkLifecycle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				// A later ownership query can succeed after a transient
				// admission failure. It must never reach provider work
				// without a deletion lease and joined account context.
				var account string
				if err := db.QueryRowContext(r.Context(), "SELECT mirror_account_id FROM email_accounts").Scan(&account); err != nil {
					t.Errorf("later lookup: %v", err)
				}
			}))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, requestAsOwner(http.MethodGet, "/api/threads/t?account=acct"))
			if called || w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "2" {
				t.Fatalf("lookup failure escaped admission: called=%v status=%d retry=%q", called, w.Code, w.Header().Get("Retry-After"))
			}
			if len(recorded.log()) != 1 {
				t.Fatalf("handler executed queries after failed admission: %+v", recorded.log())
			}
		})
	}
}
