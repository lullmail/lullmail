package main

import (
	"context"
	"database/sql/driver"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neutron-build/neutron/mail"
)

func TestStoredCredentialMapsJMAPSecretToAccessToken(t *testing.T) {
	cred := storedCredential(mail.ProviderJMAP, "owner@example.com", "", "api-token", "api.example.com", 443)
	if cred.AccessToken != "api-token" {
		t.Fatalf("AccessToken = %q, want API token", cred.AccessToken)
	}
	if cred.Password != "" {
		t.Fatalf("Password = %q, want empty for JMAP", cred.Password)
	}

	imap := storedCredential(mail.ProviderIMAP, "owner@example.com", "", "app-password", "imap.example.com", 993)
	if imap.Password != "app-password" || imap.AccessToken != "" {
		t.Fatalf("IMAP credential mapped incorrectly: %+v", imap)
	}
	if imap.Username != "owner@example.com" {
		t.Fatalf("empty username should fall back to address, got %q", imap.Username)
	}
	named := storedCredential(mail.ProviderIMAP, "owner@example.com", "imap-user", "app-password", "imap.example.com", 993)
	if named.Username != "imap-user" || named.Email != "owner@example.com" {
		t.Fatalf("username/email split lost: %+v", named)
	}
}

func TestAccountDeletionWaitsForUseAndCancelsWork(t *testing.T) {
	app := &App{}
	acct := mail.AccountID("account-1")
	workCtx, releaseUse, ok := app.beginAccountWork(acct)
	if !ok {
		t.Fatal("initial account use was rejected")
	}

	started := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		close(started)
		finishDelete, ok := app.beginAccountDeletion(context.Background(), acct)
		if !ok {
			t.Error("deletion was rejected")
			close(finished)
			return
		}
		finishDelete(true)
		close(finished)
	}()
	<-started
	select {
	case <-finished:
		t.Fatal("deletion did not wait for active account use")
	case <-workCtx.Done():
		// Sealing cancels the account context first, then waits for the
		// drain — the intended order.
	case <-time.After(20 * time.Millisecond):
		t.Fatal("sealing did not cancel the in-flight work's context")
	}

	releaseUse()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("deletion stayed blocked after account use ended")
	}
	if _, ok := app.beginAccountUse(acct); ok {
		t.Fatal("committed deletion did not tombstone stale account work")
	}
}

func TestFailedAccountDeletionClearsTombstone(t *testing.T) {
	app := &App{}
	acct := mail.AccountID("account-1")
	finishDelete, ok := app.beginAccountDeletion(context.Background(), acct)
	if !ok {
		t.Fatal("deletion was rejected")
	}
	finishDelete(false)

	release, ok := app.beginAccountUse(acct)
	if !ok {
		t.Fatal("failed deletion left account tombstoned")
	}
	release()
}

// A deletion whose drain window expires aborts and leaves the account
// usable: the wedged operation is the failure, not a hung delete endpoint.
func TestAccountDeletionAbortsWhenWorkNeverDrains(t *testing.T) {
	app := &App{}
	acct := mail.AccountID("account-1")
	if _, release, ok := app.beginAccountWork(acct); !ok {
		t.Fatal("account use was rejected")
	} else {
		defer release()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, ok := app.beginAccountDeletion(ctx, acct); ok {
		t.Fatal("deletion admitted with undrainable work inside a tiny window")
	}
	release, ok := app.beginAccountUse(acct)
	if !ok {
		t.Fatal("aborted deletion left the account tombstoned")
	}
	release()
}

// OPS-05's headline: one account's long operation must not delay another
// account's deletion. The gates are per-account; the owner lock never
// appears on this path.
func TestUnrelatedAccountWorkDoesNotBlockDeletion(t *testing.T) {
	app := &App{}
	if _, release, ok := app.beginAccountWork("account-A"); !ok {
		t.Fatal("account A use was rejected")
	} else {
		defer release()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		finish, ok := app.beginAccountDeletion(context.Background(), "account-B")
		if !ok {
			t.Error("deletion of B was rejected")
			return
		}
		finish(true)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("deletion of account B waited behind account A's work")
	}
}

// Full-owner deletion seals every enumerated account and retires them on
// commit; new work on those accounts fails while unrelated accounts stay
// usable through ownership checks (the seal no longer fences the world).
func TestFullOwnerSealDrainsAndRetires(t *testing.T) {
	app := &App{db: openStepDB(t, dbStep{kind: "query", rows: &testRows{
		columns: []string{"mirror_account_id"},
		values:  [][]driver.Value{{"account-1"}},
	}})}
	acct := mail.AccountID("account-1")
	workCtx, releaseUse, ok := app.beginAccountWork(acct)
	if !ok {
		t.Fatal("initial account use was rejected")
	}

	sealed := make(chan *ownerSeal, 1)
	go func() {
		seal, err := app.sealOwnerAccounts(context.Background(), "uid-1")
		if err != nil {
			t.Error("owner seal failed")
			close(sealed)
			return
		}
		sealed <- seal
	}()
	select {
	case <-sealed:
		t.Fatal("owner seal did not wait for active account use")
	case <-workCtx.Done():
	case <-time.After(20 * time.Millisecond):
	}
	releaseUse()
	var seal *ownerSeal
	select {
	case seal = <-sealed:
	case <-time.After(time.Second):
		t.Fatal("owner seal stayed blocked after account use ended")
	}
	if _, ok := app.beginAccountUse("unrelated-account"); !ok {
		t.Fatal("owner seal fenced an account it never enumerated")
	}
	seal.finish(true)
	if _, ok := app.beginAccountUse(acct); ok {
		t.Fatal("committed owner deletion did not retire sealed account work")
	}
}

// The audit's deadlock scenario: a handler already inside the lifecycle gate
// queues a deletion writer (which waits on the owner lock) and then resolves
// an account, which re-acquires the same read lock. Recursive read locks are
// unsafe while a writer is pending, so the nested use must be a no-op gate
// carried by the context — this test must complete, not hang.
func TestAccountResolverUnderLifecycleGateWithPendingDeletion(t *testing.T) {
	app := &App{db: openStepDB(t,
		dbStep{kind: "query", rows: emptyRows("expr")},
		dbStep{kind: "query", rows: &testRows{
			columns: []string{"provider", "address", "username", "host", "port", "cred_ciphertext"},
			values:  [][]driver.Value{{"imap", "a@x.com", "", "", int64(0), ""}},
		}},
	)}
	resolver := app.accountResolver()

	deletionReturned := make(chan struct{})
	h := app.accountWorkLifecycle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := make(chan struct{})
		go func() {
			close(started)
			finish, _ := app.beginAccountDeletion(context.Background(), "account-1")
			if finish != nil {
				finish(false)
			}
			close(deletionReturned)
		}()
		<-started
		// Give the writer time to pend on the owner lock the gate holds.
		time.Sleep(20 * time.Millisecond)
		done := make(chan struct{})
		go func() {
			defer close(done)
			// The DB stub yields no owned account, so the resolver errors —
			// the point is that it returns at all rather than deadlocking.
			_, _, _ = resolver(r.Context(), "account-1", mail.Credential{})
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("nested account use deadlocked against the pending deletion")
		}
		w.WriteHeader(http.StatusOK)
	}))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, requestAsOwner(http.MethodGet, "/api/threads/x"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	select {
	case <-deletionReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("deletion stayed blocked after the gated request finished")
	}
}

// Settings bodies must carry an explicit value: `{}` used to read as
// "disable sync"/"keep nothing" because absence and zero collapsed into
// one value, and trailing documents were silently ignored (audit 3
// DATA-07).
func TestSettingsDecodeRequiresExplicitValues(t *testing.T) {
	type body struct {
		Days *int `json:"days"`
	}
	for _, tc := range []struct {
		name    string
		payload string
		wantErr bool
		wantNil bool
	}{
		{name: "explicit zero is a real value", payload: `{"days":0}`, wantNil: false},
		{name: "omitted field decodes to nil and the handler rejects it", payload: `{}`, wantNil: true},
		{name: "null field decodes to nil and the handler rejects it", payload: `{"days":null}`, wantNil: true},
		{name: "unknown fields are rejected", payload: `{"days":30,"unit":"days"}`, wantErr: true},
		{name: "trailing document is rejected", payload: `{"days":30} {"days":60}`, wantErr: true},
		{name: "oversized body is rejected", payload: `{"days":30,"pad":"` + string(make([]byte, 32<<10)) + `"}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req body
			r := httptest.NewRequest(http.MethodPost, "/api/accounts/x?op=retention", strings.NewReader(tc.payload))
			w := httptest.NewRecorder()
			err := decodeSettingsJSON(w, r, &req)
			if tc.wantErr {
				if err == nil {
					t.Fatal("decode should have failed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantNil != (req.Days == nil) {
				t.Fatalf("days nil = %v, want nil = %v", req.Days == nil, tc.wantNil)
			}
		})
	}
}
