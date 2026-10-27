package app

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/neutron-build/neutron/mail"
)

// discardedFilter excludes messages the provider holds in Trash or Junk.
// Views are built from local classification, which does not change when the
// owner trashes or junks mail here or in another client; without this a
// deleted message kept appearing in Inbox, Reading and the counts. Folder
// browsing and the thread view do not use it, so Trash itself stays readable.
func discardedFilter(alias string) string {
	return `
		AND NOT EXISTS (
		  SELECT 1 FROM mail_message_mailboxes dm
		  JOIN mail_mailboxes db ON db.account_id = dm.account_id AND db.id = dm.mailbox_id
		  WHERE dm.account_id = ` + alias + `.account_id AND dm.message_id = ` + alias + `.id
		    AND db.role IN ('trash', 'junk'))`
}

// maxTrashThread bounds one request, like thread actions.
const maxTrashThread = 2000

// handleMessageTrash moves a message's whole thread to the account's Trash
// folder at the provider. It is a move, never a permanent delete: the
// engine's OpDelete expunges on IMAP and JMAP. The provider applies the move
// first; the affected folders are then synced so the mirror, and every view
// filtered by discardedFilter, reflect it before the response.
func (a *App) handleMessageTrash(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid, err := a.userID(ctx)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	msg := r.PathValue("message")
	account := r.URL.Query().Get("account")
	if account == "" {
		writeProblem(w, http.StatusBadRequest, "Missing Account", "account is required for message actions")
		return
	}
	var acct, thread string
	if err := a.mutationDB(ctx).QueryRowContext(ctx, `
		SELECT m.account_id, m.thread_id
		FROM mail_messages m
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = $1
		WHERE m.id = $2 AND (m.account_id = $3 OR ea.id::text = $3)
		ORDER BY m.received_at DESC NULLS LAST LIMIT 1`, uid, msg, account).Scan(&acct, &thread); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			a.log.Error("trash lookup failed", "err", err)
		}
		writeLookupProblem(w, err, "message")
		return
	}
	var trash string
	if err := a.mutationDB(ctx).QueryRowContext(ctx,
		`SELECT id FROM mail_mailboxes WHERE account_id = $1 AND role = 'trash' ORDER BY id LIMIT 1`, acct).Scan(&trash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, http.StatusUnprocessableEntity, "No Trash Folder", "this account has no folder its provider marks as Trash")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}

	// Each message not already in Trash, grouped by its lowest mailbox id:
	// the engine selects that mailbox before mutating (IMAP), so one
	// operation must not mix messages from different folders.
	rows, err := a.mutationDB(ctx).QueryContext(ctx, `
		SELECT m.id, min(mm.mailbox_id)
		FROM mail_messages m
		JOIN mail_message_mailboxes mm ON mm.account_id = m.account_id AND mm.message_id = m.id
		WHERE m.account_id = $1 AND m.thread_id = $2
		  AND NOT EXISTS (SELECT 1 FROM mail_message_mailboxes t
		                  WHERE t.account_id = m.account_id AND t.message_id = m.id AND t.mailbox_id = $3)
		GROUP BY m.id
		ORDER BY m.id`, acct, thread, trash)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	groups := map[string][]mail.MessageID{}
	var order []string
	count := 0
	for rows.Next() {
		var id, box string
		if err := rows.Scan(&id, &box); err != nil {
			rows.Close()
			writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
			return
		}
		if _, ok := groups[box]; !ok {
			order = append(order, box)
		}
		groups[box] = append(groups[box], mail.MessageID(id))
		count++
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	if count > maxTrashThread {
		writeProblem(w, http.StatusUnprocessableEntity, "Thread Too Large", "trash supports at most 2000 messages per thread")
		return
	}
	if count == 0 {
		writeJSON(w, map[string]any{"ok": true, "trashed": 0})
		return
	}

	cred, err := a.Token(ctx, mail.AccountID(acct))
	if err != nil {
		writeProblem(w, http.StatusBadGateway, "Credential Failed", err.Error())
		return
	}
	ad, release, err := a.accountResolver()(ctx, mail.AccountID(acct), cred)
	if err != nil {
		writeProblem(w, http.StatusBadGateway, "Connect Failed", err.Error())
		return
	}
	defer release()

	trashed := 0
	for _, box := range order {
		op := mail.Operation{Kind: mail.OpMove, IDs: groups[box], Target: mail.MailboxID(trash)}
		if err := a.eng.Apply(ctx, mail.AccountID(acct), op, ad); err != nil {
			a.syncAfterTrash(r, acct, order[:1+indexOf(order, box)], trash, ad)
			writeProblem(w, http.StatusBadGateway, "Trash Failed", err.Error())
			return
		}
		trashed += len(groups[box])
	}
	a.syncAfterTrash(r, acct, order, trash, ad)
	writeJSON(w, map[string]any{"ok": true, "trashed": trashed})
}

// syncAfterTrash brings the source folders and Trash up to date. Gmail and
// JMAP keep a message's id across a move and the engine refreshes it in
// place; IMAP and Graph only show the move to a sync. Best effort: the
// scheduler converges anyway.
func (a *App) syncAfterTrash(r *http.Request, acct string, boxes []string, trash string, ad mail.Adapter) {
	for _, box := range append(append([]string{}, boxes...), trash) {
		if _, err := a.eng.SyncMailbox(r.Context(), mail.AccountID(acct), mail.MailboxID(box), ad); err != nil {
			a.log.Warn("sync after trash failed", "account", acct, "mailbox", box, "err", err)
		}
	}
}

func indexOf(list []string, item string) int {
	for i, v := range list {
		if v == item {
			return i
		}
	}
	return -1
}
