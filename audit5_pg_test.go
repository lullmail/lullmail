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
	}

	r := jsonBody(t, http.MethodDelete, "/api/security/account", `{"confirmation":"owner@example.com"}`)
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
	r = r.WithContext(context.WithValue(r.Context(), sessionContextKey{}, session))
	w := httptest.NewRecorder()
	p.app.handleFullAccountDelete(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("full owner delete status = %d: %s", w.Code, w.Body.String())
	}
	for _, table := range []string{"mail_messages", "mail_accounts", "email_accounts"} {
		var n int
		if err := p.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("table %s kept %d rows after full deletion", table, n)
		}
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

	if _, err := p.db.Exec(`INSERT INTO email_accounts
		(user_id, mirror_account_id, provider, address, label, username, host, port, smtp_host, smtp_port, cred_ciphertext, backfill_days, policy_version)
		VALUES ($1,'acct-list','imap','list@example.com','','list@example.com','example.com',993,'',0,'x',90,4)`,
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
		PolicyVersion        int64  `json:"policy_version"`
		AppliedPolicyVersion int64  `json:"applied_policy_version"`
		Reconcile            *struct {
			State            string `json:"state"`
			FullEnumeration bool   `json:"full_enumeration"`
		} `json:"reconcile"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &accounts); err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 {
		t.Fatalf("accounts = %d, want 1", len(accounts))
	}
	acc := accounts[0]
	if acc.PolicyVersion != 4 || acc.AppliedPolicyVersion != 0 {
		t.Fatalf("policy versions = desired %d / applied %d, want 4 / 0", acc.PolicyVersion, acc.AppliedPolicyVersion)
	}
	if acc.Reconcile == nil || acc.Reconcile.State != "pending" || !acc.Reconcile.FullEnumeration {
		t.Fatalf("reconcile state = %+v, want a pending full enumeration", acc.Reconcile)
	}
}
