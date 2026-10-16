package main

// Round-5 audit regressions that need real PostgreSQL (the fixes they
// prove are SQL contracts or handler paths the offline suites cannot
// exercise honestly).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/neutron-build/neutron/mail"
)

// Passkey management sits behind the fresh-proof gate (audit 5 AUTH-01):
// a stale session cannot delete a passkey, and a fresh sign-in passes the
// gate (proving the refusal was the gate, not a broken handler).
func TestIntegrationPasskeyDeleteRequiresFreshProof(t *testing.T) {
	p := newProductPG(t)
	stale := seedSession(t, p, 30*time.Minute)
	fresh := seedSession(t, p, time.Minute)

	del := func(session string) *httptest.ResponseRecorder {
		r := jsonBody(t, http.MethodDelete, "/api/security/passkeys/k1", "")
		r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
		r = r.WithContext(context.WithValue(r.Context(), sessionContextKey{}, session))
		w := httptest.NewRecorder()
		p.app.handlePasskeyDelete(w, r)
		return w
	}
	if w := del(stale); w.Code != http.StatusPreconditionRequired {
		t.Fatalf("stale session passkey delete status = %d (%s), want 428", w.Code, w.Body.String())
	}
	// Fresh session passes the gate and fails on the credential itself.
	if w := del(fresh); w.Code == http.StatusPreconditionRequired {
		t.Fatalf("fresh session was still gated: %s", w.Body.String())
	}
}

// The TOTP budget reservation is atomic (audit 5 AUTH-02): concurrent
// wrong guesses from distinct peers cannot all observe an available
// budget — exactly maxTOTPWindowAttempts are admitted, every other
// racer answers 429 in the SAME window.
func TestIntegrationTOTPReservationIsAtomicUnderConcurrency(t *testing.T) {
	p := newProductPG(t)
	seedTOTP(t, p)

	const racers = 25
	start := make(chan struct{})
	results := make(chan int, racers)
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			peer := fmt.Sprintf("198.51.100.%d:9999", 100+i)
			<-start
			w := p.totpAttempt("000000", peer)
			results <- w.Code
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	admitted, refused := 0, 0
	for code := range results {
		switch code {
		case http.StatusUnauthorized:
			admitted++
		case http.StatusTooManyRequests:
			refused++
		default:
			t.Fatalf("unexpected status %d", code)
		}
	}
	if admitted != maxTOTPWindowAttempts {
		t.Fatalf("admitted %d guesses, want exactly %d", admitted, maxTOTPWindowAttempts)
	}
	if refused != racers-maxTOTPWindowAttempts {
		t.Fatalf("refused %d guesses, want %d", refused, racers-maxTOTPWindowAttempts)
	}
}

// Full owner deletion executes with CONNECTED accounts (audit 5 DATA-01):
// the old handler ran DELETEs on its transaction while the account-id
// result set was still open — the busy-connection failure that made
// owner deletion fail exactly when there was mail to erase.
func TestIntegrationFullOwnerDeleteWithConnectedAccounts(t *testing.T) {
	p := newProductPG(t)
	ctx := context.Background()
	session := seedSession(t, p, time.Minute)

	engine, err := mail.Open(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.Close)
	for _, name := range []string{"one", "two"} {
		acct := mail.AccountID("del-" + name)
		if err := engine.PutAccount(ctx, &mail.Account{ID: acct, Provider: mail.ProviderIMAP, Email: name + "@example.com"}); err != nil {
			t.Fatal(err)
		}
		if _, err := p.db.Exec(`INSERT INTO email_accounts
			(user_id, mirror_account_id, provider, address, label, username, host, port, smtp_host, smtp_port, cred_ciphertext, backfill_days)
			VALUES ($1,$2,'imap',$3,'',$3,'example.com',993,'',0,'x',90)`,
			p.uid, string(acct), name+"@example.com"); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			id := mail.HeaderMessageID(fmt.Sprintf("<%s-%d@example.com>", name, i))
			env := mail.Envelope{ID: id, Subject: "s", MailboxIDs: []mail.MailboxID{"INBOX"}}
			if err := engine.PutEnvelopes(ctx, acct, []mail.Envelope{env}); err != nil {
				t.Fatal(err)
			}
		}
		// An unfinished staged scan for each connected mirror, with
		// accumulated seen evidence: full-owner deletion must remove
		// both tables for exactly the deleted mirrors — neither carries
		// a foreign key to the rows the handler already deleted, so
		// nothing else in its list can reclaim them.
		if _, err := p.db.Exec(`INSERT INTO mirror_scans (id, account_id, mailbox_id, continuation, started_at)
			VALUES ($1,$2,'INBOX','page-token',now())`, "scan-del-"+name, "del-"+name); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if _, err := p.db.Exec(`INSERT INTO mirror_scan_seen (scan_id, message_id) VALUES ($1,$2)`,
				"scan-del-"+name, fmt.Sprintf("seen-%s-%d", name, i)); err != nil {
				t.Fatal(err)
			}
		}
	}
	// An unrelated owner keeps its own staged scan, seen evidence and a
	// completed-generation marker: the deletion must not touch them.
	var otherOwner string
	if err := p.db.QueryRow(`INSERT INTO users (email,display_name) VALUES ('other@example.com','Other') RETURNING id`).Scan(&otherOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(`INSERT INTO email_accounts
		(user_id, mirror_account_id, provider, address, label, username, host, port, smtp_host, smtp_port, cred_ciphertext, backfill_days)
		VALUES ($1,'del-survivor','imap','keep@example.com','','keep@example.com','example.com',993,'',0,'x',90)`, otherOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(`INSERT INTO mirror_scans (id, account_id, mailbox_id, continuation, started_at)
		VALUES ('scan-survivor','del-survivor','INBOX','',now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(`INSERT INTO mirror_scan_seen (scan_id, message_id) VALUES ('scan-survivor','seen-other-0')`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(`INSERT INTO mail_scan_done (account_id,generation,mailbox_id) VALUES ('del-survivor',3,'INBOX')`); err != nil {
		t.Fatal(err)
	}

	r := jsonBody(t, http.MethodDelete, "/api/security/account", `{"confirmation":"owner@example.com"}`)
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
	r = r.WithContext(context.WithValue(r.Context(), sessionContextKey{}, session))
	w := httptest.NewRecorder()
	p.app.handleFullAccountDelete(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("full owner delete status = %d: %s", w.Code, w.Body.String())
	}
	for table, want := range map[string]int{"mail_messages": 0, "mail_accounts": 0, "email_accounts": 1} {
		var n int
		if err := p.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want { // the unrelated owner's single product account survives
			t.Errorf("table %s kept %d rows after full deletion, want %d", table, n, want)
		}
	}
	var scans, seen int
	if err := p.db.QueryRow(`SELECT count(*) FROM mirror_scans WHERE account_id IN ('del-one','del-two')`).Scan(&scans); err != nil {
		t.Fatal(err)
	}
	if err := p.db.QueryRow(`SELECT count(*) FROM mirror_scan_seen WHERE scan_id IN ('scan-del-one','scan-del-two')`).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if scans != 0 || seen != 0 {
		t.Errorf("full deletion left staged-scan rows behind: scans=%d seen=%d", scans, seen)
	}
	if err := p.db.QueryRow(`SELECT count(*) FROM mirror_scans WHERE id='scan-survivor'`).Scan(&scans); err != nil {
		t.Fatal(err)
	}
	if err := p.db.QueryRow(`SELECT count(*) FROM mirror_scan_seen WHERE scan_id='scan-survivor'`).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	var done int
	if err := p.db.QueryRow(`SELECT count(*) FROM mail_scan_done WHERE account_id='del-survivor' AND generation=3`).Scan(&done); err != nil {
		t.Fatal(err)
	}
	if scans != 1 || seen != 1 || done != 1 {
		t.Errorf("unrelated owner's staging rows were disturbed: scans=%d seen=%d done=%d", scans, seen, done)
	}
}

// A failure in the full-owner deletion AFTER the staged-scan cleanup must
// roll the whole transaction back — the scan/seen rows, the mirror data
// and the owner all survive together, or the cleanup is not atomic with
// the deletion that justifies it.
func TestIntegrationFullOwnerDeleteRollsBackStagedScanCleanup(t *testing.T) {
	p := newProductPG(t)
	session := seedSession(t, p, time.Minute)

	if _, err := p.db.Exec(`INSERT INTO email_accounts
		(user_id, mirror_account_id, provider, address, label, username, host, port, smtp_host, smtp_port, cred_ciphertext, backfill_days)
		VALUES ($1,'del-rb','imap','rb@example.com','','rb@example.com','example.com',993,'',0,'x',90)`, p.uid); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(`INSERT INTO mirror_scans (id, account_id, mailbox_id, continuation, started_at)
		VALUES ('scan-rb','del-rb','INBOX','page-token',now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(`INSERT INTO mirror_scan_seen (scan_id, message_id) VALUES ('scan-rb','seen-rb-0')`); err != nil {
		t.Fatal(err)
	}
	// The staged cleanup runs per-mirror, BEFORE the owner delete; a
	// failure there proves the earlier deletes were rolled back too.
	if _, err := p.db.Exec(`CREATE OR REPLACE FUNCTION lull_test_owner_delete_fail() RETURNS trigger AS $$
		BEGIN RAISE EXCEPTION 'forced later delete failure'; END; $$ LANGUAGE plpgsql`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.db.Exec(`DROP FUNCTION IF EXISTS lull_test_owner_delete_fail()`) })
	if _, err := p.db.Exec(`CREATE TRIGGER lull_test_owner_delete_fail BEFORE DELETE ON users
		FOR EACH ROW EXECUTE FUNCTION lull_test_owner_delete_fail()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.db.Exec(`DROP TRIGGER IF EXISTS lull_test_owner_delete_fail ON users`) })

	r := jsonBody(t, http.MethodDelete, "/api/security/account", `{"confirmation":"owner@example.com"}`)
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
	r = r.WithContext(context.WithValue(r.Context(), sessionContextKey{}, session))
	w := httptest.NewRecorder()
	p.app.handleFullAccountDelete(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("forced-failure delete status = %d, want 500: %s", w.Code, w.Body.String())
	}
	var owner, account, scans, seen int
	if err := p.db.QueryRow(`SELECT count(*) FROM users WHERE id=$1`, p.uid).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if err := p.db.QueryRow(`SELECT count(*) FROM email_accounts WHERE mirror_account_id='del-rb'`).Scan(&account); err != nil {
		t.Fatal(err)
	}
	if err := p.db.QueryRow(`SELECT count(*) FROM mirror_scans WHERE id='scan-rb'`).Scan(&scans); err != nil {
		t.Fatal(err)
	}
	if err := p.db.QueryRow(`SELECT count(*) FROM mirror_scan_seen WHERE scan_id='scan-rb'`).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if owner != 1 || account != 1 || scans != 1 || seen != 1 {
		t.Fatalf("rollback did not restore the pre-delete state: owner=%d account=%d scans=%d seen=%d", owner, account, scans, seen)
	}
}

// Replacing an unfinished reconcile job preserves its outstanding
// full-enumeration requirement (audit 5 SYNC-07): a restoration request
// must not be defused by a later non-expanding change. A completed job's
// flag is replaceable — the work it recorded is done.
func TestIntegrationReconcileUpsertPreservesFullEnumeration(t *testing.T) {
	p := newProductPG(t)
	ctx := context.Background()

	// The job table foreign-keys the mirror account row.
	if _, err := p.db.Exec(`INSERT INTO email_accounts
		(user_id, mirror_account_id, provider, address, label, username, host, port, smtp_host, smtp_port, cred_ciphertext, backfill_days)
		VALUES ($1,'acct-sup','imap','sup@example.com','','sup@example.com','example.com',993,'',0,'x',90)`,
		p.uid); err != nil {
		t.Fatal(err)
	}
	upsert := func(version int64, full bool) {
		tx, err := p.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := upsertReconcileJobTx(ctx, tx, "acct-sup", version, full); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	full := func() bool {
		var v bool
		if err := p.db.QueryRow(`SELECT full_enumeration FROM account_reconcile_jobs WHERE account_id='acct-sup'`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	upsert(1, true)
	upsert(2, false) // a non-expanding change replaces an UNFINISHED restoration
	if !full() {
		t.Fatal("an unfinished full-enumeration requirement was dropped by job replacement")
	}
	if _, err := p.db.Exec(`UPDATE account_reconcile_jobs SET state='complete' WHERE account_id='acct-sup'`); err != nil {
		t.Fatal(err)
	}
	upsert(3, false) // replacing a COMPLETE job carries the new requirement only
	if full() {
		t.Fatal("a completed job's full-enumeration flag leaked into its replacement")
	}
}

// The connected-account list exposes the reconcile job and both policy
// versions (audit 5 API-04): the response type always declared them, but
// the list query never selected them.
func TestIntegrationAccountListExposesReconcileState(t *testing.T) {
	p := newProductPG(t)
	ctx := context.Background()

	detailVersions := func(id string) (int64, int64) {
		t.Helper()
		r := jsonBody(t, http.MethodGet, "/api/accounts", "")
		r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
		w := httptest.NewRecorder()
		p.app.getAccountJSON(w, r, id)
		if w.Code != http.StatusOK {
			t.Fatalf("detail status = %d: %s", w.Code, w.Body.String())
		}
		var got accountJSON
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got.PolicyVersion, got.AppliedPolicyVersion
	}

	if _, err := p.db.Exec(`INSERT INTO email_accounts
		(user_id, mirror_account_id, provider, address, label, username, host, port, smtp_host, smtp_port, cred_ciphertext, backfill_days, policy_version, applied_policy_version)
		VALUES ($1,'acct-list','imap','list@example.com','','list@example.com','example.com',993,'',0,'x',90,4,2)`,
		p.uid); err != nil {
		t.Fatal(err)
	}
	// A second account with NO reconcile job: the detail endpoint must
	// still report the account's own versions rather than relying on the
	// reconcile object to carry them.
	if _, err := p.db.Exec(`INSERT INTO email_accounts
		(user_id, mirror_account_id, provider, address, label, username, host, port, smtp_host, smtp_port, cred_ciphertext, backfill_days, policy_version, applied_policy_version)
		VALUES ($1,'acct-nojob','imap','nojob@example.com','','nojob@example.com','example.com',993,'',0,'x',90,7,5)`,
		p.uid); err != nil {
		t.Fatal(err)
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := upsertReconcileJobTx(ctx, tx, "acct-list", 4, true); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	r := jsonBody(t, http.MethodGet, "/api/accounts", "")
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
	w := httptest.NewRecorder()
	p.app.listAccountsJSON(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", w.Code, w.Body.String())
	}
	var accounts []struct {
		ID                   string `json:"id"`
		MirrorAccountID      string `json:"mirror_account_id"`
		PolicyVersion        int64  `json:"policy_version"`
		AppliedPolicyVersion int64  `json:"applied_policy_version"`
		Reconcile            *struct {
			State           string `json:"state"`
			FullEnumeration bool   `json:"full_enumeration"`
		} `json:"reconcile"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &accounts); err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 {
		t.Fatalf("accounts = %d, want 2", len(accounts))
	}
	acc := accounts[0]
	if acc.MirrorAccountID != "acct-list" || acc.ID == acc.MirrorAccountID {
		t.Fatalf("account identities missing or conflated: product=%q mirror=%q", acc.ID, acc.MirrorAccountID)
	}
	if acc.PolicyVersion != 4 || acc.AppliedPolicyVersion != 2 {
		t.Fatalf("policy versions = desired %d / applied %d, want 4 / 2", acc.PolicyVersion, acc.AppliedPolicyVersion)
	}
	if acc.Reconcile == nil || acc.Reconcile.State != "pending" || !acc.Reconcile.FullEnumeration {
		t.Fatalf("reconcile state = %+v, want a pending full enumeration", acc.Reconcile)
	}
	// The single-account refresh reads the same desired/applied pair as
	// the list — including when no reconcile job exists to carry them.
	if desired, applied := detailVersions(acc.ID); desired != 4 || applied != 2 {
		t.Fatalf("detail policy versions = desired %d / applied %d, want 4 / 2 (the list shape)", desired, applied)
	}
	var noJobID string
	if err := p.db.QueryRow(`SELECT id::text FROM email_accounts WHERE mirror_account_id='acct-nojob'`).Scan(&noJobID); err != nil {
		t.Fatal(err)
	}
	if desired, applied := detailVersions(noJobID); desired != 7 || applied != 5 {
		t.Fatalf("no-job detail policy versions = desired %d / applied %d, want 7 / 5", desired, applied)
	}
	// Completing the restoration raises applied to the desired version;
	// both endpoints report the settled 4/4 pair.
	if _, err := p.db.Exec(`UPDATE account_reconcile_jobs SET state='complete' WHERE account_id='acct-list'`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(`UPDATE email_accounts SET applied_policy_version=policy_version WHERE mirror_account_id='acct-list'`); err != nil {
		t.Fatal(err)
	}
	if desired, applied := detailVersions(acc.ID); desired != 4 || applied != 4 {
		t.Fatalf("settled detail policy versions = desired %d / applied %d, want 4 / 4", desired, applied)
	}
}
