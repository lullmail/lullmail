package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/neutron-build/neutron/mail"
)

// Product half of the identity contract (docs/identity-contract.md). The
// engine's identity transactions call these hooks through the engine's own
// mail.HookTx handle — the engine owns the connection, so the product's
// writes commit with the envelope, membership and body replacement, before
// any old ID is retired — and productIdentityHooks below adapts them to
// mail.IdentityHooks for the engine wiring in mail.go.
//
// sqlHookTx adapts a product *sql.Tx to the same interface, so the
// migration driver (runIdentityMigration) and the identity regressions run
// the IDENTICAL hook SQL on a product transaction. *sql.Row satisfies
// mail.HookRow directly; *sql.Rows does not (its Close returns an error and
// the contract's does not), so Query wraps it in sqlHookRows.
type sqlHookTx struct{ tx *sql.Tx }

var _ mail.HookTx = sqlHookTx{}

func (h sqlHookTx) Exec(ctx context.Context, query string, args ...any) error {
	_, err := h.tx.ExecContext(ctx, query, args...)
	return err
}

func (h sqlHookTx) QueryRow(ctx context.Context, query string, args ...any) mail.HookRow {
	return h.tx.QueryRowContext(ctx, query, args...)
}

func (h sqlHookTx) Query(ctx context.Context, query string, args ...any) (mail.HookRows, error) {
	rows, err := h.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return sqlHookRows{rows: rows}, nil
}

// sqlHookRows adapts *sql.Rows to mail.HookRows; Close drops the error
// that Err already carries.
type sqlHookRows struct{ rows *sql.Rows }

var _ mail.HookRows = sqlHookRows{}

func (r sqlHookRows) Next() bool             { return r.rows.Next() }
func (r sqlHookRows) Scan(dest ...any) error { return r.rows.Scan(dest...) }
func (r sqlHookRows) Err() error             { return r.rows.Err() }
func (r sqlHookRows) Close()                 { _ = r.rows.Close() }

// IdentityPair is one old→new message identity replacement inside a
// promotion. IDs are the engine's canonical MessageID strings.
type IdentityPair struct {
	OldID string
	NewID string
}

// identityAliasStatements is the Phase 1 identity-stability machinery
// (docs/identity-contract.md, "Alias table"): every identity remap records
// old→new in one durable product table, which outbox delivery and reply
// seeding consult when a stored reference names an identity a later remap
// retired from the mirror. Sealed outbox ciphertext is never rewritten; the
// alias IS the durable resolution.
var identityAliasStatements = []string{
	`CREATE TABLE message_identity_aliases (
  account_id text NOT NULL,
  old_id     text NOT NULL,
  new_id     text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (account_id, old_id)
)`,
}

// admitIdentityWrite fences a promotion on the owning user and account rows
// inside the promotion transaction itself. No pool calls or commits here.
func admitIdentityWrite(ctx context.Context, tx mail.HookTx, acct mail.AccountID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var owner string
	return tx.QueryRow(ctx, `SELECT u.id::text FROM users u JOIN email_accounts ea ON ea.user_id=u.id
 WHERE ea.mirror_account_id=$1 FOR UPDATE OF u,ea`, string(acct)).Scan(&owner)
}

// remapProductIdentity moves every retained product reference for the given
// pairs and records one durable alias per pair. It runs inside the engine's
// promotion transaction: either every product reference moves with the
// envelope — alias included — or the whole promotion rolls back.
func remapProductIdentity(ctx context.Context, tx mail.HookTx, acct mail.AccountID, pairs []IdentityPair) error {
	for _, pair := range pairs {
		if pair.OldID == pair.NewID {
			continue
		}
		// Thread references are a different namespace. Refuse an unexpected
		// thread rewrite rather than guessing which pinned/queued
		// conversation it denotes.
		var oldThread, newThread string
		if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT thread_id FROM mail_messages WHERE account_id=$1 AND id=$2),''),COALESCE((SELECT thread_id FROM mail_messages WHERE account_id=$1 AND id=$3),'')`, string(acct), pair.OldID, pair.NewID).Scan(&oldThread, &newThread); err != nil {
			return err
		}
		if oldThread != "" && oldThread != newThread {
			return fmt.Errorf("identity promotion changes thread identity; explicit thread migration required")
		}
		for _, query := range []string{
			`UPDATE hey_messages SET message_id=$3 WHERE account_id=$1 AND message_id=$2`,
			`UPDATE push_deliveries SET message_id=$3 WHERE account_id=$1 AND message_id=$2`,
			`UPDATE message_action_undo SET message_id=$3 WHERE account_id=$1 AND message_id=$2`,
		} {
			if err := tx.Exec(ctx, query, string(acct), pair.OldID, pair.NewID); err != nil {
				return err
			}
		}
		needle, err := json.Marshal([]map[string]string{{"id": pair.OldID}})
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT token,snapshot FROM message_action_undo WHERE account_id=$1 AND snapshot @> $2::jsonb FOR UPDATE`, string(acct), string(needle))
		if err != nil {
			return err
		}
		type update struct {
			token string
			raw   []byte
		}
		var updates []update
		for rows.Next() {
			var token string
			var raw []byte
			if err := rows.Scan(&token, &raw); err != nil {
				rows.Close()
				return err
			}
			var states []messageUndo
			if err := json.Unmarshal(raw, &states); err != nil {
				rows.Close()
				return err
			}
			for i := range states {
				if states[i].ID == pair.OldID {
					states[i].ID = pair.NewID
				}
			}
			// Snapshot ordering is canonical message ID order, including
			// after remap, so restore-time state comparison stays aligned.
			sortMessageUndo(states)
			raw, err = undoSnapshotBytes(states)
			if err != nil {
				rows.Close()
				return err
			}
			updates = append(updates, update{token, raw})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, u := range updates {
			if err := tx.Exec(ctx, `UPDATE message_action_undo SET snapshot=$2::jsonb WHERE token=$1`, u.token, string(u.raw)); err != nil {
				return err
			}
		}
		// Nothing may point at a retired identity: collapse every alias
		// that currently resolves to the old id onto the new one BEFORE
		// recording this hop, so no chain ever forms. The upsert keeps a
		// repeated promotion idempotent.
		if err := tx.Exec(ctx, `UPDATE message_identity_aliases SET new_id=$3 WHERE account_id=$1 AND new_id=$2`, string(acct), pair.OldID, pair.NewID); err != nil {
			return err
		}
		if err := tx.Exec(ctx, `INSERT INTO message_identity_aliases (account_id,old_id,new_id) VALUES ($1,$2,$3)
			ON CONFLICT (account_id,old_id) DO UPDATE SET new_id=excluded.new_id`, string(acct), pair.OldID, pair.NewID); err != nil {
			return err
		}
	}
	// Durable aliases resolve the encrypted ReplyParent and old clients at
	// the two consultation points (outbox delivery, reply seeding). Never
	// rewrite outbox ciphertext, request hash, AAD, or thread IDs; board
	// pins are thread-qualified and sticky notes have no message identity.
	return nil
}

func sortMessageUndo(states []messageUndo) {
	sort.Slice(states, func(i, j int) bool { return states[i].ID < states[j].ID })
}

// productIdentityHooks is the product's mail.IdentityHooks adapter: the
// engine's identity transactions (see mail-engine/hooks.go) call these
// inside their own unit of work, after the account advisory lock. Every
// method works only through the HookTx handle — no pool connections, no
// engine-store calls, no additional locks, none of the caller's DDL — per
// the contract's prohibitions. The callbacks are the same functions the
// migration driver runs on a product transaction (sqlHookTx), so both
// drivers execute identical product SQL. No receiver state: the hooks own
// nothing the handle does not carry.
type productIdentityHooks struct{}

var _ mail.IdentityHooks = productIdentityHooks{}

func (productIdentityHooks) AdmitIdentityWrite(ctx context.Context, tx mail.HookTx, acct mail.AccountID) error {
	if err := admitIdentityWrite(ctx, tx, acct); err != nil {
		return fmt.Errorf("admit identity write: %w", err)
	}
	return nil
}

func (productIdentityHooks) RemapIdentity(ctx context.Context, tx mail.HookTx, acct mail.AccountID, pairs []mail.IdentityPair) error {
	product := make([]IdentityPair, len(pairs))
	for i, pair := range pairs {
		product[i] = IdentityPair{OldID: string(pair.OldID), NewID: string(pair.NewID)}
	}
	if err := remapProductIdentity(ctx, tx, acct, product); err != nil {
		return fmt.Errorf("remap identity: %w", err)
	}
	return nil
}

// ReferencesDropped records the contract's decision for identities deleted
// with no known replacement: the mirror row dies in THIS transaction, so
// the product's references are dead the moment it commits. Deleting them
// here — not fabricating undo snapshots into history, not resurrecting
// anything — is deterministic and atomic: there is no orphan window for
// cleanupMirrorOrphans to sweep later.
func (productIdentityHooks) ReferencesDropped(ctx context.Context, tx mail.HookTx, acct mail.AccountID, ids []mail.MessageID) error {
	if len(ids) == 0 {
		return nil
	}
	dropped := make([]string, len(ids))
	for i, id := range ids {
		dropped[i] = string(id)
	}
	for _, query := range []string{
		`DELETE FROM hey_messages WHERE account_id=$1 AND message_id = ANY($2)`,
		`DELETE FROM push_deliveries WHERE account_id=$1 AND message_id = ANY($2)`,
		`DELETE FROM message_action_undo WHERE account_id=$1 AND message_id = ANY($2)`,
	} {
		if err := tx.Exec(ctx, query, string(acct), dropped); err != nil {
			return fmt.Errorf("drop references for deleted identities: %w", err)
		}
	}
	return nil
}

// resolveReplyParent resolves a stored reply-parent identity to the identity
// the mirror carries now — the consultation point for outbox delivery
// (sendqueue.go deliveryFor). The mirror wins while it still holds the
// identity; only a miss consults message_identity_aliases, and at most ONE
// hop: remap collapses chains on write, so a second hop can only follow a
// second remap, whose own write collapses the chain again. With no alias the
// stored identity stands and behavior is exactly today's.
func (a *App) resolveReplyParent(ctx context.Context, account mail.AccountID, replyParent string) string {
	if replyParent == "" {
		return ""
	}
	var resolved string
	err := a.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT id FROM mail_messages WHERE account_id=$1 AND id=$2),(SELECT new_id FROM message_identity_aliases WHERE account_id=$1 AND old_id=$2),'')`, string(account), replyParent).Scan(&resolved)
	if err != nil || resolved == "" {
		return replyParent
	}
	return resolved
}

// identityAliasForUser resolves one alias hop inside the account scope the
// reply seed pinned — the consultation point for reply seeding (sendqueue.go
// handleSend), where a stored seed names its parent by an identity current
// at seed time (a recovered outbox draft carries one inside sealed
// ciphertext, which is never rewritten) and a later remap can retire it.
// Owner-fenced like the parent lookup it retries; empty means no alias, and
// the miss keeps today's behavior.
func (a *App) identityAliasForUser(ctx context.Context, uid, account, oldID string) string {
	if oldID == "" {
		return ""
	}
	var newID string
	err := a.db.QueryRowContext(ctx, `SELECT a.new_id FROM message_identity_aliases a JOIN email_accounts ea ON ea.mirror_account_id=a.account_id WHERE ea.user_id=$1 AND (ea.id::text=$2 OR a.account_id=$2) AND a.old_id=$3`, uid, account, oldID).Scan(&newID)
	if err != nil {
		return ""
	}
	return newID
}

// graphIdentityTranslator is the product-side view of the upstream Graph
// translation contract (translateExchangeIds batches plus authenticated /me
// verification). The vendored engine's adapter does not implement it yet;
// binding verification refuses until the reviewed upstream change is
// re-vendored, rather than trusting an email-derived key.
type graphIdentityTranslator interface {
	GraphMailboxKey(ctx context.Context) (string, error)
}

// graphReferenceInventory enumerates every retained product reference to
// Graph message identities for one account, including reference-only IDs
// whose mirror row expired. The engine's migration consults it so no
// retained reference is left behind by a bounded translation batch. SQL and
// payload integrity checks use the supplied migration transaction.
func (a *App) graphReferenceInventory(ctx context.Context, tx mail.HookTx, acct mail.AccountID) ([]mail.MessageID, error) {
	ids := map[mail.MessageID]bool{}
	add := func(id string) error {
		if id == "" {
			return nil
		}
		key := mail.MessageID(id)
		if err := key.Validate(); err != nil {
			return err
		}
		if !strings.HasPrefix(id, "n:graph:") {
			return fmt.Errorf("non-Graph reference in Graph account")
		}
		ids[key] = true
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT message_id FROM hey_messages WHERE account_id=$1 UNION SELECT message_id FROM push_deliveries WHERE account_id=$1 UNION SELECT message_id FROM message_action_undo WHERE account_id=$1`, string(acct))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		if err := add(id); err != nil {
			rows.Close()
			return nil, err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.Query(ctx, `SELECT snapshot FROM message_action_undo WHERE account_id=$1`, string(acct))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, err
		}
		var states []messageUndo
		if err := json.Unmarshal(raw, &states); err != nil {
			rows.Close()
			return nil, err
		}
		for _, state := range states {
			if err := add(state.ID); err != nil {
				rows.Close()
				return nil, err
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.Query(ctx, `SELECT id::text,user_id::text,payload_ciphertext FROM outbox_jobs WHERE account_id=$1 AND payload_ciphertext<>''`, string(acct))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, owner, sealed string
		if err := rows.Scan(&id, &owner, &sealed); err != nil {
			rows.Close()
			return nil, err
		}
		raw, err := openBound(a.config(), "payload", owner, id, sealed)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("cannot inventory retained outbox payload: %w", err)
		}
		var payload outboxPayload
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			rows.Close()
			return nil, err
		}
		if err := add(payload.ReplyParent); err != nil {
			rows.Close()
			return nil, err
		}
		var request struct {
			ReplyParent string `json:"reply_to_message_id"`
		}
		if len(payload.Request) > 0 {
			if err := json.Unmarshal(payload.Request, &request); err != nil {
				rows.Close()
				return nil, err
			}
			if err := add(request.ReplyParent); err != nil {
				rows.Close()
				return nil, err
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := make([]mail.MessageID, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

// bindGraphMailbox is explicit upgrade preparation for a historical account.
// expected is an operator-verified /me ID, never an email-derived key. A fresh
// authentication/binding mismatch refuses; migration still verifies /me around
// every translation batch once the upstream contract exists. No mirror state
// changes in this binding transaction. The account advisory lock is the FIRST
// statement: this transaction also takes owner row locks (users, then
// email_accounts), and every mirror-writing transaction takes the advisory
// lock first — any other order here closes a lock cycle with account deletion
// (docs/identity-stability.md §4).
func (a *App) bindGraphMailbox(ctx context.Context, uid string, acct mail.AccountID, expected string) error {
	if expected == "" {
		return fmt.Errorf("verified Graph mailbox key required")
	}
	cred, err := a.Token(ctx, acct)
	if err != nil {
		return err
	}
	if cred.Provider != mail.ProviderGraph {
		return fmt.Errorf("not a Graph account")
	}
	var originalSeal string
	if err := a.db.QueryRowContext(ctx, `SELECT cred_ciphertext FROM email_accounts WHERE user_id=$1 AND mirror_account_id=$2`, uid, string(acct)).Scan(&originalSeal); err != nil {
		return err
	}
	ad, release, err := newResolver()(ctx, acct, cred)
	if err != nil {
		return err
	}
	defer release()
	translator, ok := ad.(graphIdentityTranslator)
	if !ok {
		return fmt.Errorf("Graph translator unavailable: the vendored engine does not expose mailbox-key verification yet")
	}
	actual, err := translator.GraphMailboxKey(ctx)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("authenticated Graph mailbox does not match verified binding")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, mail.AccountLockKey(acct)); err != nil {
		return err
	}
	if _, err := lockAuthUser(ctx, tx, uid); err != nil {
		return err
	}
	var seal, existing string
	if err := tx.QueryRowContext(ctx, `SELECT cred_ciphertext,graph_mailbox_key FROM email_accounts WHERE user_id=$1 AND mirror_account_id=$2 AND provider='graph' FOR UPDATE`, uid, string(acct)).Scan(&seal, &existing); err != nil {
		return err
	}
	if seal != originalSeal || (existing != "" && existing != actual) {
		return fmt.Errorf("Graph account binding changed during verification")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE email_accounts SET graph_mailbox_key=$3 WHERE user_id=$1 AND mirror_account_id=$2`, uid, string(acct), actual); err != nil {
		return err
	}
	return tx.Commit()
}
