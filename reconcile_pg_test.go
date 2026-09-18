package main

// Real-PostgreSQL coverage for the durable policy-reconciliation
// machinery (audit DATA-08/SYNC-05) and the product migration runner
// (audit OPS-02). Same skip contract as product_pg_test.go:
// LULL_TEST_DATABASE_URL points at a DISPOSABLE scratch database.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neutron-build/neutron/mail"
)

// scriptedMailAdapter is the product-side provider double: fixed
// mailboxes, cursor-keyed enumeration pages (a real provider serves the
// next page from the continuation token, no matter how many times a
// rescan restarts), and optional death after N calls.
type scriptedMailAdapter struct {
	boxes      []mail.Mailbox
	pages      []*mail.Changes
	calls      int
	failAfter  int
	failedWith error
}

func (a *scriptedMailAdapter) Provider() mail.Provider { return mail.ProviderIMAP }

func (a *scriptedMailAdapter) Mailboxes(context.Context) ([]mail.Mailbox, error) {
	return a.boxes, nil
}

func (a *scriptedMailAdapter) Sync(_ context.Context, _ mail.MailboxID, cur mail.Cursor) (*mail.Changes, error) {
	a.calls++
	if a.failAfter > 0 && a.calls > a.failAfter {
		if a.failedWith == nil {
			a.failedWith = errors.New("process killed mid-reconciliation")
		}
		return nil, a.failedWith
	}
	if len(a.pages) == 0 {
		return &mail.Changes{Complete: true}, nil
	}
	if cur == "" {
		return a.pages[0], nil
	}
	for i, page := range a.pages {
		if cur == page.Next && i+1 < len(a.pages) {
			return a.pages[i+1], nil
		}
	}
	// A continuation past the last page (or unknown) is terminal: real
	// enumerations always end on a Complete page.
	return &mail.Changes{Complete: true}, nil
}

func (a *scriptedMailAdapter) Envelopes(_ context.Context, ids []mail.MessageID) ([]mail.Envelope, error) {
	return nil, nil
}
func (a *scriptedMailAdapter) Body(_ context.Context, id mail.MessageID) (*mail.Body, error) {
	return &mail.Body{MessageID: id, Text: "body"}, nil
}
func (a *scriptedMailAdapter) Raw(_ context.Context, _ mail.MessageID) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (a *scriptedMailAdapter) Attachment(_ context.Context, _ mail.MessageID, _ string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (a *scriptedMailAdapter) Apply(context.Context, mail.Operation) error { return nil }
func (a *scriptedMailAdapter) Close() error                           { return nil }

var _ mail.Adapter = (*scriptedMailAdapter)(nil)

// seedReconcileAccount installs one connected account (mirror rows +
// email_accounts + one old message) under a bounded retention window and
// prunes it, producing the exact SYNC-05 starting point: an old message
// the provider still holds but the local mirror no longer does.
func seedReconcileAccount(t *testing.T, p productPG, retentionDays int) (string, string, mail.MessageID) {
	t.Helper()
	ctx := context.Background()
	acct := mail.AccountID(fmt.Sprintf("recon-acct-%d", time.Now().UnixNano()))
	if err := p.app.store.PutAccount(ctx, &mail.Account{
		ID: acct, Provider: mail.ProviderIMAP, Email: "it@example.com", Name: "Integration",
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.app.store.PutMailboxes(ctx, acct, []mail.Mailbox{{ID: "INBOX", Name: "INBOX"}}); err != nil {
		t.Fatal(err)
	}

	old := mail.HeaderMessageID("<restore-me@example.com>")
	envs := []mail.Envelope{
		{
			ID: old, ThreadID: "t-old", MailboxIDs: []mail.MailboxID{"INBOX"},
			Subject: "old message", From: []mail.Address{{Email: "someone@example.com"}},
			ReceivedAt: time.Now().UTC().Add(-40 * 24 * time.Hour),
		},
		{
			ID: mail.HeaderMessageID("<fresh@example.com>"), ThreadID: "t-fresh",
			MailboxIDs: []mail.MailboxID{"INBOX"}, Subject: "fresh message",
			From:       []mail.Address{{Email: "someone@example.com"}}, ReceivedAt: time.Now().UTC(),
		},
	}
	if err := p.app.store.PutEnvelopes(ctx, acct, envs); err != nil {
		t.Fatal(err)
	}
	if err := p.app.store.PutCursor(ctx, acct, "INBOX", "healthy-delta"); err != nil {
		t.Fatal(err)
	}
	if err := p.app.classifyUser(ctx, p.uid); err != nil {
		t.Fatal(err)
	}

	sealed, err := sealSecret(p.cfg, "app-password")
	if err != nil {
		t.Fatal(err)
	}
	var productID string
	if err := p.db.QueryRowContext(ctx, `
		INSERT INTO email_accounts (user_id, mirror_account_id, provider, address, label, username, host, port, cred_ciphertext, retention_days)
		VALUES ($1, $2, 'imap', 'it@example.com', '', '', '', 0, $3, $4)
		RETURNING id::text`,
		p.uid, string(acct), sealed, retentionDays).Scan(&productID); err != nil {
		t.Fatal(err)
	}

	// Prune under the bounded window: the old message leaves the mirror
	// (and its filing state with it) although the provider still holds it.
	if err := p.app.applyAccountRetention(ctx, p.uid, acct, retentionDays); err != nil {
		t.Fatal(err)
	}
	if _, err := p.app.store.Envelope(ctx, acct, old); !errors.Is(err, mail.ErrNoStore) {
		t.Fatalf("setup: old message was not pruned by the bounded retention window")
	}
	return productID, string(acct), old
}

// enumerationPages describes the provider's full INBOX: both messages,
// paginated so the interruption tests have a real between-pages moment.
func enumerationPages() []*mail.Changes {
	env := func(subject, hdr string, age time.Duration) mail.Envelope {
		return mail.Envelope{
			ID: mail.HeaderMessageID(hdr), ThreadID: mail.ThreadID("t-" + subject),
			MailboxIDs: []mail.MailboxID{"INBOX"}, Subject: subject,
			From:       []mail.Address{{Email: "someone@example.com"}},
			ReceivedAt: time.Now().UTC().Add(-age),
		}
	}
	old := env("old message", "<restore-me@example.com>", 40*24*time.Hour)
	fresh := env("fresh message", "<fresh@example.com>", 0)
	return []*mail.Changes{
		{Changes: []mail.Change{
			{Kind: mail.ChangeCreated, ID: old.ID, Envelope: &old},
		}, Next: "enum-p1", More: true, EnumerationStart: true},
		{Changes: []mail.Change{
			{Kind: mail.ChangeCreated, ID: fresh.ID, Envelope: &fresh},
		}, Next: "enum-final", Complete: true},
	}
}

func waitForJobState(t *testing.T, db *sql.DB, mirror, want string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var state string
		if err := db.QueryRow(
			`SELECT state FROM account_reconcile_jobs WHERE account_id = $1`, mirror).Scan(&state); err == nil && state == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("reconcile job never reached state %q", want)
}

func postSettings(t *testing.T, p productPG, productID, op, body string) *httptest.ResponseRecorder {
	t.Helper()
	// Routed through a mux so {id} resolves, and authenticated as the
	// seeded owner: handleAccountItem reads PathValue and the auth ctx.
	mux := http.NewServeMux()
	mux.HandleFunc("POST /accounts/{id}", p.app.handleAccountItem)
	r := httptest.NewRequest(http.MethodPost, "/accounts/"+productID+"?op="+op, strings.NewReader(body))
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// THE SYNC-05 REGRESSION: retain 7 days, prune an unchanged month-old
// message, widen to 90 — the staged rescan restores it without any
// provider-side modification, and the job consumes only at completion.
func TestIntegrationRetentionIncreaseRestoresOlderMessages(t *testing.T) {
	p := newProductPG(t)
	ctx := context.Background()
	productID, mirror, old := seedReconcileAccount(t, p, 7)
	acct := mail.AccountID(mirror)

	ad := &scriptedMailAdapter{boxes: []mail.Mailbox{{ID: "INBOX", Name: "INBOX"}}, pages: enumerationPages()}
	p.app.dial = func(context.Context, mail.AccountID, mail.Credential) (mail.Adapter, func(), error) {
		return ad, func() {}, nil
	}

	w := postSettings(t, p, productID, "retention", `{"days":90}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("retention change = %d %s, want 202", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"full_enumeration":true`) {
		t.Fatalf("response body = %s, want a full-enumeration job", w.Body.String())
	}

	waitForJobState(t, p.db, mirror, "complete")

	restored, err := p.app.store.Envelope(ctx, acct, old)
	if err != nil {
		t.Fatalf("widening retention did not restore the old message: %v", err)
	}
	if restored.Subject != "old message" {
		t.Errorf("restored envelope = %+v", restored)
	}

	var applied, desired int64
	if err := p.db.QueryRowContext(ctx,
		`SELECT applied_policy_version, policy_version FROM email_accounts WHERE mirror_account_id=$1`,
		mirror).Scan(&applied, &desired); err != nil {
		t.Fatal(err)
	}
	if applied != desired || desired != 1 {
		t.Errorf("policy versions applied=%d desired=%d, want 1/1", applied, desired)
	}
	var filing int
	if err := p.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM hey_messages WHERE account_id=$1 AND message_id=$2`,
		mirror, string(old)).Scan(&filing); err != nil {
		t.Fatal(err)
	}
	if filing != 1 {
		t.Errorf("restored message has %d filing rows, want 1 (classification ran)", filing)
	}
	if scans, _ := p.app.store.RunningScans(ctx, acct); len(scans) != 0 {
		t.Errorf("scan rows survived the completed reconciliation: %v", scans)
	}
}

// NARROWING does not request a rescan (nothing to restore) but still
// transitions durably: 202, job completes, no provider contact.
func TestIntegrationRetentionNarrowingIsLocalOnly(t *testing.T) {
	p := newProductPG(t)
	ctx := context.Background()
	// Seed under a 7-day window (the old message is pruned), then narrow
	// further: strictly local, no enumeration.
	productID, mirror, _ := seedReconcileAccount(t, p, 7)

	called := false
	p.app.dial = func(context.Context, mail.AccountID, mail.Credential) (mail.Adapter, func(), error) {
		called = true
		return nil, func() {}, errors.New("no adapter expected")
	}

	w := postSettings(t, p, productID, "retention", `{"days":3}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("retention change = %d, want 202", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"full_enumeration":false`) {
		t.Fatalf("response body = %s, want a local-only job", w.Body.String())
	}
	waitForJobState(t, p.db, mirror, "complete")
	if called {
		t.Error("a narrowing change contacted the provider")
	}
	var applied int64
	if err := p.db.QueryRowContext(ctx,
		`SELECT applied_policy_version FROM email_accounts WHERE mirror_account_id=$1`,
		mirror).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Errorf("applied_policy_version = %d, want 1", applied)
	}
}

// The interruption chain, end to end through the product worker: the
// rescan dies between pages, everything stays, and the retry resumes the
// same durable scan and completes.
func TestIntegrationReconcileInterruptionKeepsStateAndResumes(t *testing.T) {
	p := newProductPG(t)
	ctx := context.Background()
	productID, mirror, _ := seedReconcileAccount(t, p, 7)
	acct := mail.AccountID(mirror)

	// The rescan dies between pages: page 1 stages, the fetch of page 2
	// is the kill.
	dying := &scriptedMailAdapter{
		boxes:     []mail.Mailbox{{ID: "INBOX", Name: "INBOX"}},
		pages:     enumerationPages(),
		failAfter: 1,
	}
	p.app.dial = func(context.Context, mail.AccountID, mail.Credential) (mail.Adapter, func(), error) {
		return dying, func() {}, nil
	}

	w := postSettings(t, p, productID, "retention", `{"days":90}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("retention change = %d, want 202", w.Code)
	}
	waitForJobState(t, p.db, mirror, "failed")

	// Nothing was PRUNED mid-reconciliation: the pre-existing message
	// survives, page 1's staged message is visible (staging only adds),
	// and the durable scan exists for the retry.
	var freshOK bool
	if _, err := p.app.store.Envelope(ctx, acct, mail.HeaderMessageID("<fresh@example.com>")); err == nil {
		freshOK = true
	}
	if !freshOK {
		t.Error("the pre-existing message disappeared mid-reconciliation")
	}
	var count int
	if err := p.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM mail_messages WHERE account_id=$1`, mirror).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count < 1 {
		t.Errorf("mirror holds %d messages mid-reconciliation, want at least the pre-existing one", count)
	}
	if scans, _ := p.app.store.RunningScans(ctx, acct); len(scans) != 1 {
		t.Fatalf("durable scan missing after interruption: %v", scans)
	}

	// The retry (background cadence equivalent) resumes and completes.
	good := &scriptedMailAdapter{boxes: []mail.Mailbox{{ID: "INBOX", Name: "INBOX"}}, pages: enumerationPages()}
	p.app.dial = func(context.Context, mail.AccountID, mail.Credential) (mail.Adapter, func(), error) {
		return good, func() {}, nil
	}
	if err := p.app.processReconcileJobs(ctx, p.uid); err != nil {
		t.Fatalf("resumed reconciliation failed: %v", err)
	}

	var state string
	var lastErr sql.NullString
	if err := p.db.QueryRowContext(ctx,
		`SELECT state, last_error FROM account_reconcile_jobs WHERE account_id=$1`, mirror).
		Scan(&state, &lastErr); err != nil {
		t.Fatal(err)
	}
	if state != "complete" {
		t.Fatalf("resumed job state = %q (err %v), want complete", state, lastErr)
	}
	var total int
	if err := p.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM mail_messages WHERE account_id=$1`, mirror).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Errorf("restored mirror holds %d messages, want 2", total)
	}
}

// The DATA-08 version race: a newer policy replaces the job row while an
// older worker runs; the older worker can never mark its version applied.
func TestIntegrationOlderReconcileJobNeverMarksNewerVersionApplied(t *testing.T) {
	p := newProductPG(t)
	ctx := context.Background()
	productID, mirror, _ := seedReconcileAccount(t, p, 7)

	ad := &scriptedMailAdapter{boxes: []mail.Mailbox{{ID: "INBOX", Name: "INBOX"}}, pages: enumerationPages()}
	p.app.dial = func(context.Context, mail.AccountID, mail.Credential) (mail.Adapter, func(), error) {
		return ad, func() {}, nil
	}

	// v1: widen to 90. Then, before any worker runs, v2: lift to forever.
	// The kicked worker immediately serves the newer job (the row was
	// replaced), so both versions are exercised: the row is owned by v2
	// and the worker completes it.
	if w := postSettings(t, p, productID, "retention", `{"days":90}`); w.Code != http.StatusAccepted {
		t.Fatalf("first change = %d", w.Code)
	}
	if w := postSettings(t, p, productID, "retention", `{"days":0}`); w.Code != http.StatusAccepted {
		t.Fatalf("second change = %d", w.Code)
	}
	var desired int64
	if err := p.db.QueryRowContext(ctx,
		`SELECT policy_version FROM email_accounts WHERE mirror_account_id=$1`, mirror).Scan(&desired); err != nil {
		t.Fatal(err)
	}
	if desired != 2 {
		t.Fatalf("policy_version = %d, want 2", desired)
	}
	var jobVersion int64
	if err := p.db.QueryRowContext(ctx,
		`SELECT policy_version FROM account_reconcile_jobs WHERE account_id=$1`, mirror).Scan(&jobVersion); err != nil {
		t.Fatal(err)
	}
	if jobVersion != 2 {
		t.Fatalf("job policy_version = %d, want the newer 2 to own the row", jobVersion)
	}

	// The kicked worker completes v2.
	waitForJobState(t, p.db, mirror, "complete")
	var applied int64
	if err := p.db.QueryRowContext(ctx,
		`SELECT applied_policy_version FROM email_accounts WHERE mirror_account_id=$1`, mirror).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 2 {
		t.Errorf("applied_policy_version = %d, want 2", applied)
	}

	// A stale worker holding v1 finalizes afterwards: the version guard
	// makes every write it could attempt a no-op — it cannot mark v1
	// applied, cannot regress applied_policy_version, and cannot touch
	// the completed v2 row.
	stale := &reconcileJob{AccountID: mirror, PolicyVersion: 1, State: "running"}
	if err := p.app.finalizeReconcileJob(ctx, stale, nil); err != nil {
		t.Fatalf("stale finalize errored: %v", err)
	}
	if err := p.app.finalizeReconcileJob(ctx, stale, errors.New("late failure")); err == nil {
		t.Fatal("stale failure finalize reported success")
	}
	if err := p.db.QueryRowContext(ctx,
		`SELECT applied_policy_version FROM email_accounts WHERE mirror_account_id=$1`, mirror).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 2 {
		t.Errorf("stale worker moved applied_policy_version to %d, want 2", applied)
	}
	var state string
	if err := p.db.QueryRowContext(ctx,
		`SELECT state FROM account_reconcile_jobs WHERE account_id=$1`, mirror).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "complete" {
		t.Errorf("job state after stale finalize = %q, want complete", state)
	}
}

// SYNC-04 across the two pools: product retention (database/sql) racing
// engine sync writeback (pgx). The shared account advisory lock plus the
// mirror FKs must leave zero orphans and a consistent mirror.
func TestIntegrationProductRetentionRacingEngineSync(t *testing.T) {
	p := newProductPG(t)
	ctx := context.Background()
	_, mirror, _ := seedReconcileAccount(t, p, 7)
	acct := mail.AccountID(mirror)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 15; i++ {
			if err := p.app.applyAccountRetention(ctx, p.uid, acct, 7); err != nil {
				t.Errorf("retention: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 15; i++ {
			id := mail.HeaderMessageID(fmt.Sprintf("<race-%d@example.com>", i))
			env := mail.Envelope{
				ID: id, ThreadID: mail.ThreadID("t-race"), MailboxIDs: []mail.MailboxID{"INBOX"},
				Subject: "race", From: []mail.Address{{Email: "someone@example.com"}},
				ReceivedAt: time.Now().UTC().Add(-30 * 24 * time.Hour),
			}
			if err := p.app.store.PutEnvelopes(ctx, acct, []mail.Envelope{env}); err != nil {
				t.Errorf("writeback: %v", err)
				return
			}
			// The body write is the orphan-prone one: parent may have
			// expired between envelope write and body write. With the FK
			// in force this either lands before the sweep (both deleted
			// together) or is refused — never orphaned.
			if err := p.app.store.PutBody(ctx, acct, &mail.Body{MessageID: id, Text: "b"}); err != nil &&
				!strings.Contains(err.Error(), "foreign key") {
				t.Errorf("body writeback: %v", err)
				return
			}
		}
	}()
	wg.Wait()

	// Final retention pass so the end state must satisfy the policy.
	if err := p.app.applyAccountRetention(ctx, p.uid, acct, 7); err != nil {
		t.Fatal(err)
	}

	var orphanBodies, orphanMembers, expired int
	if err := p.db.QueryRowContext(ctx, `
		SELECT
		  (SELECT COUNT(*) FROM mail_bodies b WHERE NOT EXISTS
		     (SELECT 1 FROM mail_messages m WHERE m.account_id=b.account_id AND m.id=b.message_id)),
		  (SELECT COUNT(*) FROM mail_message_mailboxes mm WHERE NOT EXISTS
		     (SELECT 1 FROM mail_messages m WHERE m.account_id=mm.account_id AND m.id=mm.message_id)),
		  (SELECT COUNT(*) FROM mail_messages m WHERE m.account_id=$1
		     AND (m.received_at AT TIME ZONE 'UTC') < now() - interval '7 day')`,
		mirror).Scan(&orphanBodies, &orphanMembers, &expired); err != nil {
		t.Fatal(err)
	}
	if orphanBodies != 0 || orphanMembers != 0 {
		t.Errorf("orphans after the race: bodies=%d members=%d, want 0/0", orphanBodies, orphanMembers)
	}
	if expired != 0 {
		t.Errorf("%d expired messages survived the final retention pass", expired)
	}
}

// OPS-02, product side: a database converged by the OLD boot path
// (schema.sql only, no ledger, live rows) upgrades through the versioned
// runner without losing data, and the policy machinery lands.
func TestIntegrationProductMigrationConvergesOlderDatabase(t *testing.T) {
	p := newProductPG(t)
	ctx := context.Background()

	// Rewind to a pre-runner installation: baseline tables only, a live
	// legacy row, no ledger.
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tbl := range productTables {
		if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS `+tbl+` CASCADE`); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS account_reconcile_jobs CASCADE`); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	for _, stmt := range splitStatements(schemaSQL) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			tx.Rollback()
			t.Fatalf("legacy baseline: %v\n%s", err, stmt)
		}
	}
	// The drop took the seeded owner with it; the legacy install has its
	// own user, and its account row must survive the upgrade.
	var legacyUID string
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO users (email, display_name) VALUES ('legacy@example.com','Legacy') RETURNING id`); err != nil {
		tx.Rollback()
		t.Fatal(err)
	} else if err := tx.QueryRowContext(ctx,
		`SELECT id FROM users WHERE email='legacy@example.com'`).Scan(&legacyUID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO email_accounts (user_id, mirror_account_id, provider, address, cred_ciphertext)
		VALUES ($1, 'legacy-acct', 'imap', 'legacy@example.com', 'x')`, legacyUID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if err := runProductMigrations(ctx, p.db); err != nil {
		t.Fatalf("legacy database did not converge: %v", err)
	}

	var versions []int64
	rows, err := p.db.QueryContext(ctx, `SELECT version FROM app_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		versions = append(versions, v)
	}
	rows.Close()
	if len(versions) != len(productMigrations) {
		t.Fatalf("ledger = %v, want %d versions", versions, len(productMigrations))
	}

	var applied int64
	if err := p.db.QueryRowContext(ctx,
		`SELECT applied_policy_version FROM email_accounts WHERE mirror_account_id='legacy-acct'`).Scan(&applied); err != nil {
		t.Fatalf("legacy row lost or column missing: %v", err)
	}
	if applied != 0 {
		t.Errorf("legacy row applied_policy_version = %d, want default 0", applied)
	}
	if err := runProductMigrations(ctx, p.db); err != nil {
		t.Fatalf("second run failed: %v", err)
	}
}

// Two product runners racing: both succeed, each version recorded once.
func TestIntegrationConcurrentProductMigrationsSerialize(t *testing.T) {
	p := newProductPG(t)
	ctx := context.Background()
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tbl := range productTables {
		if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS `+tbl+` CASCADE`); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			db2, err := sql.Open("pgx", testDatabaseURL(t))
			if err != nil {
				errs[i] = err
				return
			}
			defer db2.Close()
			errs[i] = runProductMigrations(ctx, db2)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("runner %d failed: %v", i, err)
		}
	}

	rows, err := p.db.QueryContext(ctx, `SELECT version, COUNT(*) FROM app_migrations GROUP BY version ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[int64]int{}
	for rows.Next() {
		var v, n int64
		if err := rows.Scan(&v, &n); err != nil {
			t.Fatal(err)
		}
		got[v] = int(n)
	}
	if len(got) != len(productMigrations) {
		t.Fatalf("ledger = %v, want every version once", got)
	}
	for _, m := range productMigrations {
		if got[m.Version] != 1 {
			t.Errorf("version %d recorded %d times, want exactly 1", m.Version, got[m.Version])
		}
	}
}

// The advisory-lock proof, product side: a runner WAITS while another
// session holds the lock, then proceeds.
func TestIntegrationProductMigrationRunnerWaitsForAdvisoryLock(t *testing.T) {
	p := newProductPG(t)
	ctx := context.Background()
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tbl := range productTables {
		if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS `+tbl+` CASCADE`); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	blocker, err := sql.Open("pgx", testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	conn, err := blocker.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, productLockKey); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- runProductMigrations(ctx, p.db) }()
	select {
	case err := <-done:
		t.Fatalf("product migration completed while the lock was held elsewhere: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, productLockKey); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("product migration after lock release failed: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("product migration never completed after the lock was released")
	}
}
