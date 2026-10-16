package app

// The product-tx identity migration driver (docs/identity-contract.md,
// "Migration harness extraction"): the transaction shape the identity
// promotion regressions proved (identity_pg_test.go), moved into production
// form. The engine now calls the same hooks on its own transaction
// (mail-engine/hooks.go, wired in mail.go); this driver remains the one
// proven way to run a whole identity replacement — engine-table writes and
// product remap together — through a single PRODUCT transaction, for
// migration tooling. There is no HTTP surface.
//
// The account advisory lock is the FIRST statement
// (docs/identity-stability.md §4): the transaction then takes owner row
// locks inside admitIdentityWrite, and every mirror-writing transaction
// takes the advisory lock first — any other order here would close a lock
// cycle with account deletion.

import (
	"context"
	"database/sql"

	"github.com/neutron-build/neutron/mail"
)

// identityReplacement is one engine-table replacement for the migration
// driver: the replacement envelope identity plus the thread it carries
// (remap refuses a thread change, so this is the old identity's thread).
type identityReplacement struct {
	OldID  string
	NewID  string
	Thread string
}

// runIdentityMigration replaces every identity in one product transaction,
// in the engine's replacement-before-retirement order: the account advisory
// lock first, then the owner-row fence, the mirror replacement writes
// (envelope, membership, body copy), remapProductIdentity — which also
// records the durable alias for every pair — and finally the retirement of
// the old identities. Any failure rolls back everything, so the old record
// stays readable and the whole unit is retryable. The hooks run through
// sqlHookTx — the same adapter shape the engine's transaction hands them —
// so this driver and the engine execute identical product SQL.
func runIdentityMigration(ctx context.Context, tx *sql.Tx, acct mail.AccountID, replacements ...identityReplacement) error {
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, mail.AccountLockKey(acct)); err != nil {
		_ = tx.Rollback()
		return err
	}
	hookTx := sqlHookTx{tx: tx}
	if err := admitIdentityWrite(ctx, hookTx, acct); err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, rep := range replacements {
		writes := []struct {
			query string
			args  []any
		}{
			// The engine's replacement writes, inside the same transaction.
			{`INSERT INTO mail_messages (account_id,id,thread_id,subject) SELECT account_id,$3,$4,subject FROM mail_messages WHERE account_id=$1 AND id=$2`, []any{string(acct), rep.OldID, rep.NewID, rep.Thread}},
			{`INSERT INTO mail_message_mailboxes (account_id,message_id,mailbox_id) SELECT account_id,$3,mailbox_id FROM mail_message_mailboxes WHERE account_id=$1 AND message_id=$2`, []any{string(acct), rep.OldID, rep.NewID}},
			{`INSERT INTO mail_bodies (account_id,message_id,text_body,html_body,parts,fetched_at) SELECT account_id,$3,text_body,html_body,parts,fetched_at FROM mail_bodies WHERE account_id=$1 AND message_id=$2`, []any{string(acct), rep.OldID, rep.NewID}},
		}
		for _, write := range writes {
			if _, err := tx.ExecContext(ctx, write.query, write.args...); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
	}
	pairs := make([]IdentityPair, 0, len(replacements))
	for _, rep := range replacements {
		pairs = append(pairs, IdentityPair{OldID: rep.OldID, NewID: rep.NewID})
	}
	if err := remapProductIdentity(ctx, hookTx, acct, pairs); err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, rep := range replacements {
		retires := []struct {
			query string
			args  []any
		}{
			{`DELETE FROM mail_message_mailboxes WHERE account_id=$1 AND message_id=$2`, []any{string(acct), rep.OldID}},
			{`DELETE FROM mail_bodies WHERE account_id=$1 AND message_id=$2`, []any{string(acct), rep.OldID}},
			{`DELETE FROM mail_messages WHERE account_id=$1 AND id=$2`, []any{string(acct), rep.OldID}},
		}
		for _, retire := range retires {
			if _, err := tx.ExecContext(ctx, retire.query, retire.args...); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
	}
	return tx.Commit()
}
