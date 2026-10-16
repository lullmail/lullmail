package app

// Lock-order regression for the account advisory lock
// (docs/identity-stability.md §4). bindGraphMailbox used to take owner row
// locks (users, then email_accounts) BEFORE the account advisory lock — the
// inverse of every mirror-writing transaction; deleteAccount takes the
// advisory lock first and then locks email_accounts rows. Two transactions
// in those two orders form a lock cycle, which PostgreSQL resolves by
// aborting one side with SQLSTATE 40P01.
//
// The first test reproduces the OLD order directly on two connections and
// proves the cycle is real (the production function now takes the advisory
// lock first and cannot produce it). The second test runs the NEW order
// under the same interleaving pressure and proves both sides complete: the
// advisory lock serializes the transactions instead of cycling with them.

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/neutron-build/neutron/mail"
)

const lockOrderWait = 20 * time.Second

func seedLockOrderAccount(t *testing.T, p productPG) mail.AccountID {
	t.Helper()
	ctx := context.Background()
	acct := mail.AccountID("graph-acct")
	sealed, err := sealSecret(p.cfg, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.ExecContext(ctx, `INSERT INTO email_accounts
		(user_id,mirror_account_id,provider,address,cred_ciphertext)
		VALUES ($1,$2,'graph','graph@example.com',$3)`, p.uid, string(acct), sealed); err != nil {
		t.Fatal(err)
	}
	return acct
}

func isDeadlockDetected(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40P01"
}

// txStep runs one statement on a transaction with a bounded context and
// reports its error on the channel, so a parked statement never hangs the
// test beyond lockOrderWait.
func txStep(done chan<- error, tx *sql.Tx, query string, args ...any) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), lockOrderWait)
		defer cancel()
		_, err := tx.ExecContext(ctx, query, args...)
		done <- err
	}()
}

func TestIntegrationLockOrderRowLockBeforeAdvisoryDeadlocks(t *testing.T) {
	p := newProductPG(t)
	acct := seedLockOrderAccount(t, p)
	ctx := context.Background()
	advisory := `SELECT pg_advisory_xact_lock($1)`
	rowLock := `SELECT id FROM email_accounts WHERE mirror_account_id=$1 FOR UPDATE`

	// Side A in the OLD bindGraphMailbox order: row lock first.
	a, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Rollback()
	if _, err := a.ExecContext(ctx, rowLock, string(acct)); err != nil {
		t.Fatal(err)
	}
	// Side B in the mirror-writer order: advisory lock first.
	b, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Rollback()
	if _, err := b.ExecContext(ctx, advisory, mail.AccountLockKey(acct)); err != nil {
		t.Fatal(err)
	}

	// A now parks on the advisory lock B holds...
	aDone := make(chan error, 1)
	txStep(aDone, a, advisory, mail.AccountLockKey(acct))
	// ...long enough for the wait to register server-side...
	time.Sleep(200 * time.Millisecond)
	// ...then B parks on the row lock A holds. Both sides now wait on each
	// other: a cycle. PostgreSQL's deadlock detector must abort one side.
	bDone := make(chan error, 1)
	txStep(bDone, b, rowLock, string(acct))

	var aborted error
	deadline := time.After(lockOrderWait)
	for aborted == nil {
		select {
		case err := <-aDone:
			aborted = err
		case err := <-bDone:
			aborted = err
		case <-deadline:
			t.Fatal("old lock order produced no deadlock under a complete lock cycle")
		}
	}
	if !isDeadlockDetected(aborted) {
		t.Fatalf("cycle resolved without a deadlock abort (unexpected error: %v)", aborted)
	}
}

func TestIntegrationLockOrderAdvisoryFirstCompletes(t *testing.T) {
	p := newProductPG(t)
	acct := seedLockOrderAccount(t, p)
	ctx := context.Background()
	advisory := `SELECT pg_advisory_xact_lock($1)`
	rowLock := `SELECT id FROM email_accounts WHERE mirror_account_id=$1 FOR UPDATE`

	// Side B (mirror writer): advisory first, then the row lock, then commit.
	b, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Rollback()
	if _, err := b.ExecContext(ctx, advisory, mail.AccountLockKey(acct)); err != nil {
		t.Fatal(err)
	}

	// Side A in the NEW bindGraphMailbox order parks on the advisory lock —
	// holding nothing — so B's row lock cannot meet a waiter it depends on.
	a, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Rollback()
	aDone := make(chan error, 1)
	txStep(aDone, a, advisory, mail.AccountLockKey(acct))
	time.Sleep(200 * time.Millisecond)

	if _, err := b.ExecContext(ctx, rowLock, string(acct)); err != nil {
		t.Fatalf("row lock under advisory-first order failed: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatal(err)
	}
	// B's commit released the advisory lock, so A proceeds, takes the row
	// lock on the committed state and completes. No cycle, no abort.
	select {
	case err := <-aDone:
		if err != nil {
			t.Fatalf("advisory-first waiter failed: %v", err)
		}
	case <-time.After(lockOrderWait):
		t.Fatal("advisory-first waiter never acquired the released lock")
	}
	if _, err := a.ExecContext(ctx, rowLock, string(acct)); err != nil {
		t.Fatal(err)
	}
	if err := a.Commit(); err != nil {
		t.Fatal(err)
	}
}
