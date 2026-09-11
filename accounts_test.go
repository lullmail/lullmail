package main

import (
	"database/sql/driver"
	"net/http"
	"net/http/httptest"
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

func TestAccountDeletionWaitsForUseAndTombstones(t *testing.T) {
	app := &App{}
	acct := mail.AccountID("account-1")
	releaseUse, ok := app.beginAccountUse(acct)
	if !ok {
		t.Fatal("initial account use was rejected")
	}

	started := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		close(started)
		finishDelete, ok := app.beginAccountDeletion(acct)
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
	case <-time.After(20 * time.Millisecond):
	}

	releaseUse()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("deletion stayed blocked after account use ended")
	}
	if release, ok := app.beginAccountUse(acct); ok {
		release()
		t.Fatal("committed deletion did not tombstone stale account work")
	}
}

func TestFailedAccountDeletionClearsTombstone(t *testing.T) {
	app := &App{}
	acct := mail.AccountID("account-1")
	finishDelete, ok := app.beginAccountDeletion(acct)
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

func TestFullAccountDeletionAcquiresOnceBlocksAndRetires(t *testing.T) {
	app := &App{}
	acct := mail.AccountID("account-1")
	releaseUse, ok := app.beginAccountUse(acct)
	if !ok {
		t.Fatal("initial account use was rejected")
	}

	acquired := make(chan func([]mail.AccountID, bool), 1)
	go func() {
		acquired <- app.beginFullAccountDeletion()
	}()
	select {
	case <-acquired:
		t.Fatal("full deletion did not wait for active account use")
	case <-time.After(20 * time.Millisecond):
	}

	releaseUse()
	var finishDelete func([]mail.AccountID, bool)
	select {
	case finishDelete = <-acquired:
	case <-time.After(time.Second):
		t.Fatal("full deletion stayed blocked after account use ended")
	}

	useResult := make(chan bool, 1)
	go func() {
		release, ok := app.beginAccountUse(acct)
		if ok {
			release()
		}
		useResult <- ok
	}()
	select {
	case <-useResult:
		t.Fatal("account work was not blocked during full deletion")
	case <-time.After(20 * time.Millisecond):
	}

	finishDelete([]mail.AccountID{acct}, true)
	select {
	case ok := <-useResult:
		if ok {
			t.Fatal("committed full deletion did not retire account work")
		}
	case <-time.After(time.Second):
		t.Fatal("account work stayed blocked after full deletion finished")
	}

	release, ok := app.beginAccountUse("unrelated-account")
	if !ok {
		t.Fatal("full deletion did not release the owner lock")
	}
	release()
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
			finish, _ := app.beginAccountDeletion("account-1")
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
