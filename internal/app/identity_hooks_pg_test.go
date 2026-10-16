package app

// Real-PostgreSQL regressions for the product's mail.IdentityHooks adapter
// and its engine wiring (docs/identity-contract.md, Phase 3). Given hooks
// and a ReferenceAwareStore, the engine's destroy path reports
// last-membership identities inside the deleting transaction, and the
// product must drop its now-dead reference rows in that SAME unit — no
// cleanupMirrorOrphans pass may be needed to catch them later. The identity
// remap regressions (identity_pg_test.go, through runIdentityMigration's
// sqlHookTx) cover the replacement callback pair. Same skip contract as
// product_pg_test.go: LULL_TEST_DATABASE_URL points at a DISPOSABLE
// scratch database.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/neutron-build/neutron/mail"
)

// seedHookAccount installs one connected account (mirror account row,
// mailbox, product email_accounts row) with no messages yet.
func seedHookAccount(t *testing.T, p productPG, acct mail.AccountID) {
	t.Helper()
	ctx := context.Background()
	if err := p.app.store.PutAccount(ctx, &mail.Account{
		ID: acct, Provider: mail.ProviderIMAP, Email: "it@example.com", Name: "Hooks",
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.app.store.PutMailboxes(ctx, acct, []mail.Mailbox{{ID: "INBOX", Name: "INBOX"}}); err != nil {
		t.Fatal(err)
	}
	sealed, err := sealSecret(p.cfg, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.ExecContext(ctx, `INSERT INTO email_accounts
		(user_id,mirror_account_id,provider,address,cred_ciphertext)
		VALUES ($1,$2,'imap',$3,$4)`, p.uid, string(acct), string(acct)+"@example.com", sealed); err != nil {
		t.Fatal(err)
	}
}

// seedHookMessage mirrors one message and adds a retained product
// reference to it in every reference table the hooks touch (hey_messages,
// push_deliveries, message_action_undo). The undo token derives from the
// seed so distinct seeds never collide.
func seedHookMessage(t *testing.T, p productPG, acct mail.AccountID, id mail.MessageID) {
	t.Helper()
	ctx := context.Background()
	if err := p.app.store.PutEnvelopes(ctx, acct, []mail.Envelope{{
		ID: id, ThreadID: "thread-hooks", MailboxIDs: []mail.MailboxID{"INBOX"},
		Subject: "hook target", From: []mail.Address{{Email: "someone@example.com"}},
	}}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := json.Marshal([]messageUndo{{ID: string(id)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.ExecContext(ctx, `INSERT INTO hey_messages
		(user_id,account_id,message_id,bucket) VALUES ($1,$2,$3,'imbox')`,
		p.uid, string(acct), string(id)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.ExecContext(ctx, `INSERT INTO push_deliveries
		(user_id,account_id,message_id,subscription_hash)
		VALUES ($1,$2,$3,'')`, p.uid, string(acct), string(id)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.ExecContext(ctx, `INSERT INTO message_action_undo
		(token,user_id,account_id,message_id,thread_id,action,snapshot)
		VALUES ($1,$2,$3,$4,'thread-hooks','later',$5)`,
		"undo-"+string(acct)+"-"+string(id), p.uid, string(acct), string(id), string(snapshot)); err != nil {
		t.Fatal(err)
	}
}

func hookReferenceCount(t *testing.T, p productPG, table, acct, id string) int {
	t.Helper()
	var n int
	if err := p.db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM `+table+` WHERE account_id=$1 AND message_id=$2`, acct, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// End to end, hooks wired exactly as newSyncEngine wires them: a destroy
// that orphans a message deletes the product's reference rows inside the
// SAME transaction — the moment SyncMailbox returns they are gone, with no
// orphan sweep in between. Fail-before: with nil hooks (the pre-wiring
// engine), the same sync leaves every product row pointing at a mirror
// identity that no longer exists until cleanupMirrorOrphans runs.
func TestIntegrationEngineDestroyDropsProductReferencesInSameUnit(t *testing.T) {
	p := newProductPG(t)
	p.app.eng.Hooks = productIdentityHooks{}
	ctx := context.Background()
	acct := mail.AccountID("hook-destroy-acct")
	id := mail.HeaderMessageID("<hook-destroy@example.com>")
	seedHookAccount(t, p, acct)
	seedHookMessage(t, p, acct, id)

	ad := &scriptedMailAdapter{pages: []*mail.Changes{{
		Changes: []mail.Change{{Kind: mail.ChangeDestroyed, ID: id}},
		Next:    "c1",
	}}}
	if _, err := p.app.eng.SyncMailbox(ctx, acct, "INBOX", ad); err != nil {
		t.Fatal(err)
	}

	if _, err := p.app.store.Envelope(ctx, acct, id); !errors.Is(err, mail.ErrNoStore) {
		t.Fatalf("destroyed message survived the sync: %v", err)
	}
	for _, table := range []string{"hey_messages", "push_deliveries", "message_action_undo"} {
		if n := hookReferenceCount(t, p, table, string(acct), string(id)); n != 0 {
			t.Fatalf("%s kept %d rows for the destroyed identity; the destroy unit did not carry them", table, n)
		}
	}
}

// The ReferencesDropped decision (docs/identity-contract.md): the callback
// deletes exactly the dropped ids' reference rows, inside the caller's
// transaction — a rollback leaves every row in place — and only within the
// reported account.
func TestIntegrationReferencesDroppedDeletesExactlyDroppedRowsInCallerTx(t *testing.T) {
	p := newProductPG(t)
	ctx := context.Background()
	acct := mail.AccountID("hook-drop-acct")
	dropped := mail.HeaderMessageID("<dropped@example.com>")
	kept := mail.HeaderMessageID("<kept@example.com>")
	seedHookAccount(t, p, acct)
	seedHookMessage(t, p, acct, dropped)
	seedHookMessage(t, p, acct, kept)
	// Another account holds the same identity: the delete is scoped to the
	// reported account.
	seedHookAccount(t, p, mail.AccountID("hook-other-acct"))
	seedHookMessage(t, p, mail.AccountID("hook-other-acct"), dropped)

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := (productIdentityHooks{}).ReferencesDropped(ctx, sqlHookTx{tx: tx}, acct, []mail.MessageID{dropped}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	_ = tx.Rollback()
	for _, table := range []string{"hey_messages", "push_deliveries", "message_action_undo"} {
		if n := hookReferenceCount(t, p, table, string(acct), string(dropped)); n != 1 {
			t.Fatalf("rollback left %s with %d rows for the dropped id", table, n)
		}
	}

	tx, err = p.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := (productIdentityHooks{}).ReferencesDropped(ctx, sqlHookTx{tx: tx}, acct, []mail.MessageID{dropped}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"hey_messages", "push_deliveries", "message_action_undo"} {
		if n := hookReferenceCount(t, p, table, string(acct), string(dropped)); n != 0 {
			t.Fatalf("%s kept %d rows for the dropped id after commit", table, n)
		}
		if n := hookReferenceCount(t, p, table, string(acct), string(kept)); n != 1 {
			t.Fatalf("%s kept %d rows for the undropped id; the delete is not exact", table, n)
		}
		if n := hookReferenceCount(t, p, table, "hook-other-acct", string(dropped)); n != 1 {
			t.Fatalf("%s kept %d rows for another account; the delete is not account-scoped", table, n)
		}
	}
}

// The production engine construction carries the hooks. A nil-Hooks engine
// silently orphans product rows on every destroy — the fail-before state
// of the destroy regression above.
func TestNewSyncEngineWiresProductIdentityHooks(t *testing.T) {
	eng := newSyncEngine(nil, &Config{})
	if _, ok := eng.Hooks.(productIdentityHooks); !ok {
		t.Fatalf("newSyncEngine Hooks = %T, want productIdentityHooks", eng.Hooks)
	}
}
