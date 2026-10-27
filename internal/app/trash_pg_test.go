package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/neutron-build/neutron/mail"
)

// trashAdapter is a provider whose message ids survive a move (Gmail,
// JMAP): Apply refiles the message and Envelopes reports where it now is.
type trashAdapter struct {
	scriptedMailAdapter
	where map[mail.MessageID]mail.MailboxID
	ops   []mail.Operation
}

func (a *trashAdapter) Apply(_ context.Context, op mail.Operation) error {
	a.ops = append(a.ops, op)
	for _, id := range op.IDs {
		a.where[id] = op.Target
	}
	return nil
}

func (a *trashAdapter) Envelopes(_ context.Context, ids []mail.MessageID) ([]mail.Envelope, error) {
	out := make([]mail.Envelope, 0, len(ids))
	for _, id := range ids {
		out = append(out, trashEnvelope(id, a.where[id]))
	}
	return out, nil
}

// Sync answers every delta with "no further changes", so the post-trash
// sync cannot prune what the test seeded.
func (a *trashAdapter) Sync(_ context.Context, _ mail.MailboxID, cur mail.Cursor) (*mail.Changes, error) {
	return &mail.Changes{Next: cur}, nil
}

func trashEnvelope(id mail.MessageID, box mail.MailboxID) mail.Envelope {
	thread := map[mail.MessageID]string{"n:imap:a1": "t1", "n:imap:a2": "t1", "n:imap:b1": "t2", "n:imap:c1": "t3"}[id]
	return mail.Envelope{
		ID: id, ThreadID: mail.ThreadID(thread), MailboxIDs: []mail.MailboxID{box},
		Subject: string(id), From: []mail.Address{{Email: "friend@example.com"}},
		ReceivedAt: time.Now().UTC(),
	}
}

func seedTrashAccount(t *testing.T, p productPG) *trashAdapter {
	t.Helper()
	ctx := context.Background()
	acct := mail.AccountID("trash-acct")
	if err := p.app.store.PutAccount(ctx, &mail.Account{ID: acct, Provider: mail.ProviderIMAP, Email: "it@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := p.app.store.PutMailboxes(ctx, acct, []mail.Mailbox{
		{ID: "INBOX", Name: "INBOX", Role: mail.RoleInbox},
		{ID: "Trash", Name: "Trash", Role: mail.RoleTrash},
	}); err != nil {
		t.Fatal(err)
	}
	ad := &trashAdapter{where: map[mail.MessageID]mail.MailboxID{
		"n:imap:a1": "INBOX", "n:imap:a2": "INBOX", "n:imap:b1": "INBOX", "n:imap:c1": "Trash",
	}}
	var envs []mail.Envelope
	for id, box := range ad.where {
		envs = append(envs, trashEnvelope(id, box))
	}
	if err := p.app.store.PutEnvelopes(ctx, acct, envs); err != nil {
		t.Fatal(err)
	}
	for _, box := range []mail.MailboxID{"INBOX", "Trash"} {
		if err := p.app.store.PutCursor(ctx, acct, box, "delta"); err != nil {
			t.Fatal(err)
		}
	}
	sealed, err := sealSecret(p.cfg, "app-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.ExecContext(ctx, `
		INSERT INTO email_accounts (user_id, mirror_account_id, provider, address, label, username, host, port, cred_ciphertext)
		VALUES ($1, $2, 'imap', 'it@example.com', '', '', 'imap.example.com', 993, $3)`,
		p.uid, string(acct), sealed); err != nil {
		t.Fatal(err)
	}
	for id := range ad.where {
		if _, err := p.db.ExecContext(ctx, `INSERT INTO hey_messages (user_id,account_id,message_id,bucket) VALUES ($1,$2,$3,'imbox')`,
			p.uid, string(acct), string(id)); err != nil {
			t.Fatal(err)
		}
	}
	p.app.dial = func(context.Context, mail.AccountID, mail.Credential) (mail.Adapter, func(), error) {
		return ad, func() {}, nil
	}
	return ad
}

func imboxThreads(t *testing.T, p productPG) map[string]bool {
	t.Helper()
	page := fetchBucketPage(t, p, "")
	out := map[string]bool{}
	for _, row := range page.Rows {
		out[row.ThreadID] = true
	}
	return out
}

func trashRequest(t *testing.T, p productPG, message string) (int, map[string]any) {
	t.Helper()
	r := jsonBody(t, "POST", "/messages/"+message+"/trash?account=trash-acct", "")
	r.SetPathValue("message", message)
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
	w := httptest.NewRecorder()
	p.app.handleMessageTrash(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// Mail the provider holds in Trash, trashed here or in any other client,
// must leave every view; trashing a thread moves all of it, once.
func TestIntegrationTrashMovesThreadAndViewsHideTrash(t *testing.T) {
	p := newProductPG(t)
	ad := seedTrashAccount(t, p)

	if got := imboxThreads(t, p); !got["t1"] || !got["t2"] || got["t3"] {
		t.Fatalf("before trash, Inbox threads = %v; want t1 and t2 only (t3 is already in Trash)", got)
	}

	code, out := trashRequest(t, p, "n:imap:a1")
	if code != http.StatusOK || out["trashed"] != float64(2) {
		t.Fatalf("trash: status %d body %v", code, out)
	}
	if len(ad.ops) != 1 || ad.ops[0].Kind != mail.OpMove || ad.ops[0].Target != "Trash" || len(ad.ops[0].IDs) != 2 {
		t.Fatalf("provider ops = %+v; want one move of both t1 messages to Trash", ad.ops)
	}
	if got := imboxThreads(t, p); got["t1"] || !got["t2"] {
		t.Fatalf("after trash, Inbox threads = %v; want t2 only", got)
	}

	code, out = trashRequest(t, p, "n:imap:a2")
	if code != http.StatusOK || out["trashed"] != float64(0) || len(ad.ops) != 1 {
		t.Fatalf("trashing an already-trashed thread: status %d body %v ops %d", code, out, len(ad.ops))
	}
}

func TestIntegrationTrashRefusesAccountWithoutTrashFolder(t *testing.T) {
	p := newProductPG(t)
	ad := seedTrashAccount(t, p)
	if _, err := p.db.ExecContext(context.Background(), `UPDATE mail_mailboxes SET role = NULL WHERE role = 'trash'`); err != nil {
		t.Fatal(err)
	}
	if code, out := trashRequest(t, p, "n:imap:b1"); code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d body %v; want 422", code, out)
	}
	if len(ad.ops) != 0 {
		t.Fatalf("provider received %d operations without a Trash folder", len(ad.ops))
	}
}
