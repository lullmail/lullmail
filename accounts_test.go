package main

import (
	"testing"
	"time"

	"github.com/neutron-build/neutron/mail"
)

func TestStoredCredentialMapsJMAPSecretToAccessToken(t *testing.T) {
	cred := storedCredential(mail.ProviderJMAP, "owner@example.com", "api-token", "api.example.com", 443)
	if cred.AccessToken != "api-token" {
		t.Fatalf("AccessToken = %q, want API token", cred.AccessToken)
	}
	if cred.Password != "" {
		t.Fatalf("Password = %q, want empty for JMAP", cred.Password)
	}

	imap := storedCredential(mail.ProviderIMAP, "owner@example.com", "app-password", "imap.example.com", 993)
	if imap.Password != "app-password" || imap.AccessToken != "" {
		t.Fatalf("IMAP credential mapped incorrectly: %+v", imap)
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
