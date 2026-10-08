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

// Product half of Graph identity promotion (review R2-01/R2-02). The atomic
// replacement transaction itself belongs to the engine upstream first; these
// hooks are the callbacks that transaction must run on the product's tables.
// They take an explicit transaction so the engine's promotion unit of work —
// not a product pool — commits them together with envelope, membership and
// body replacement, before any old ID is retired. The vendored engine has no
// promotion callback yet (see AUDIT_OPEN.md); identity promotion tests drive
// the hooks through a product transaction with the same shape.
//
// identityTx is satisfied by *sql.Tx today. When the reviewed upstream
// contract lands, its promotion transaction supplies an adapter with these
// methods and the wiring in mail.go follows.
type identityTx interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// IdentityPair is one old→new message identity replacement inside a
// promotion. IDs are the engine's canonical MessageID strings.
type IdentityPair struct {
	OldID string
	NewID string
}

// admitIdentityWrite fences a promotion on the owning user and account rows
// inside the promotion transaction itself. No pool calls or commits here.
func admitIdentityWrite(ctx context.Context, tx identityTx, acct mail.AccountID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var owner string
	return tx.QueryRowContext(ctx, `SELECT u.id::text FROM users u JOIN email_accounts ea ON ea.user_id=u.id
 WHERE ea.mirror_account_id=$1 FOR UPDATE OF u,ea`, string(acct)).Scan(&owner)
}

// remapProductIdentity moves every retained product reference for the given
// pairs. It runs inside the engine's promotion transaction: either every
// product reference moves with the envelope or the whole promotion rolls
// back.
func remapProductIdentity(ctx context.Context, tx identityTx, acct mail.AccountID, pairs []IdentityPair) error {
	for _, pair := range pairs {
		if pair.OldID == pair.NewID {
			continue
		}
		// Thread references are a different namespace. Refuse an unexpected
		// thread rewrite rather than guessing which pinned/queued
		// conversation it denotes.
		var oldThread, newThread string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT thread_id FROM mail_messages WHERE account_id=$1 AND id=$2),''),COALESCE((SELECT thread_id FROM mail_messages WHERE account_id=$1 AND id=$3),'')`, string(acct), pair.OldID, pair.NewID).Scan(&oldThread, &newThread); err != nil {
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
			if _, err := tx.ExecContext(ctx, query, string(acct), pair.OldID, pair.NewID); err != nil {
				return err
			}
		}
		needle, err := json.Marshal([]map[string]string{{"id": pair.OldID}})
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT token,snapshot FROM message_action_undo WHERE account_id=$1 AND snapshot @> $2::jsonb FOR UPDATE`, string(acct), string(needle))
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
			if _, err := tx.ExecContext(ctx, `UPDATE message_action_undo SET snapshot=$2::jsonb WHERE token=$1`, u.token, string(u.raw)); err != nil {
				return err
			}
		}
	}
	// Durable upstream aliases resolve encrypted ReplyParent and old clients.
	// Never rewrite outbox ciphertext, request hash, AAD, or thread IDs; board
	// pins are thread-qualified and sticky notes have no message identity.
	return nil
}

func sortMessageUndo(states []messageUndo) {
	sort.Slice(states, func(i, j int) bool { return states[i].ID < states[j].ID })
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
// payload integrity checks use the supplied promotion transaction.
func (a *App) graphReferenceInventory(ctx context.Context, tx identityTx, acct mail.AccountID) ([]mail.MessageID, error) {
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
	rows, err := tx.QueryContext(ctx, `SELECT message_id FROM hey_messages WHERE account_id=$1 UNION SELECT message_id FROM push_deliveries WHERE account_id=$1 UNION SELECT message_id FROM message_action_undo WHERE account_id=$1`, string(acct))
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
	rows, err = tx.QueryContext(ctx, `SELECT snapshot FROM message_action_undo WHERE account_id=$1`, string(acct))
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
	rows, err = tx.QueryContext(ctx, `SELECT id::text,user_id::text,payload_ciphertext FROM outbox_jobs WHERE account_id=$1 AND payload_ciphertext<>''`, string(acct))
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
// changes in this binding transaction.
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
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, mail.AccountLockKey(acct)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE email_accounts SET graph_mailbox_key=$3 WHERE user_id=$1 AND mirror_account_id=$2`, uid, string(acct), actual); err != nil {
		return err
	}
	return tx.Commit()
}
