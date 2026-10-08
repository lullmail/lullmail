package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// The sequence prevents delete/reinsert ABA too. Every state UPDATE, including
// an intentional same-value write, advances authority across all SQL writers.
var messageUndoStatements = []string{
	`CREATE SEQUENCE IF NOT EXISTS hey_message_revision_seq AS bigint NO CYCLE`,
	`ALTER TABLE hey_messages ADD COLUMN mutation_revision bigint NOT NULL DEFAULT nextval('hey_message_revision_seq')`,
	`CREATE OR REPLACE FUNCTION advance_hey_message_revision() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.mutation_revision := nextval('hey_message_revision_seq'); RETURN NEW; END $$`,
	`CREATE TRIGGER hey_message_revision BEFORE UPDATE OF bucket,read_at,set_aside_until ON hey_messages FOR EACH ROW EXECUTE FUNCTION advance_hey_message_revision()`,
	`CREATE TABLE message_action_undo (
   token text PRIMARY KEY,
   user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
   account_id text NOT NULL REFERENCES email_accounts(mirror_account_id) ON DELETE CASCADE,
   message_id text NOT NULL, thread_id text NOT NULL, action text NOT NULL,
   snapshot jsonb NOT NULL,
   created_at timestamptz NOT NULL DEFAULT now(),
   expires_at timestamptz NOT NULL DEFAULT now() + interval '24 hours',
   consumed_at timestamptz
 )`,
	`CREATE INDEX message_action_undo_expiry ON message_action_undo(expires_at)`,
}

var errUndoConflict = errors.New("undo authority is stale, consumed, expired, or belongs to another action")

func undoSnapshotBytes(undo []messageUndo) ([]byte, error) {
	if len(undo) == 0 || len(undo) > 2000 {
		return nil, errUndoConflict
	}
	raw, err := json.Marshal(undo)
	if err != nil {
		return nil, err
	}
	// Retain the original snapshot budget independently of the tiny token request.
	if len(raw) > idempotencyBodyLimit-256 {
		return nil, errUndoConflict
	}
	return raw, nil
}

// Caller holds the owner row. All checks and consumption share its mutation tx.
func restoreMessageUndo(ctx context.Context, tx *sql.Tx, uid, acct, msg, thread, token string) error {
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT snapshot FROM message_action_undo
 WHERE token=$1 AND user_id=$2 AND account_id=$3 AND message_id=$4 AND thread_id=$5
 AND consumed_at IS NULL AND expires_at>now() FOR UPDATE`, token, uid, acct, msg, thread).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return errUndoConflict
	}
	if err != nil {
		return err
	}
	var undo []messageUndo
	if err := json.Unmarshal(raw, &undo); err != nil {
		return err
	}
	if _, err := undoSnapshotBytes(undo); err != nil {
		return err
	}
	current, err := threadUndoStates(ctx, tx, uid, acct, thread)
	if err != nil {
		return err
	}
	// Include unclassified arrivals: a new mirror member cannot be silently ignored.
	var members int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM mail_messages WHERE account_id=$1 AND thread_id=$2`, acct, thread).Scan(&members); err != nil {
		return err
	}
	if members != len(undo) || len(current) != len(undo) {
		return errUndoConflict
	}
	for i, state := range undo {
		if state.ID != current[i].ID || state.After.Revision != current[i].Before.Revision {
			return errUndoConflict
		}
	}
	for _, state := range undo {
		res, err := tx.ExecContext(ctx, `UPDATE hey_messages SET bucket=$4,read_at=$5::timestamptz,set_aside_until=$6::timestamptz
   WHERE user_id=$1 AND account_id=$2 AND message_id=$3 AND mutation_revision=$7`, uid, acct, state.ID, state.Before.Bucket, state.Before.ReadAt, state.Before.Until, state.After.Revision)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return errUndoConflict
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE message_action_undo SET consumed_at=now() WHERE token=$1 AND consumed_at IS NULL`, token)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("%w: consumption", errUndoConflict)
	}
	return nil
}
