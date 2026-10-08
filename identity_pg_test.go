package main

// Real-PostgreSQL regressions for the product half of Graph identity
// promotion (review R2-01/R2-02 product targets). The atomic replacement
// transaction belongs to the engine upstream; these tests drive the
// product-side hooks through one product transaction with the same shape the
// engine's promotion unit of work will have: admit on the owner rows, the
// mirror replacement writes, then remapProductIdentity — all committing
// together or not at all.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/neutron-build/neutron/mail"
)

// seedGraphAccount installs a Graph account with one classified message and
// returns its identity. IDs use the engine's native Graph shape.
func seedGraphAccount(t *testing.T, p productPG) string {
	t.Helper()
	ctx := context.Background()
	acct := mail.AccountID("graph-acct")
	if err := p.app.store.PutAccount(ctx, &mail.Account{
		ID: acct, Provider: mail.ProviderGraph, Email: "graph@example.com", Name: "Graph",
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.app.store.PutMailboxes(ctx, acct, []mail.Mailbox{{ID: "inbox", Name: "Inbox"}}); err != nil {
		t.Fatal(err)
	}
	sealed, err := sealSecret(p.cfg, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.ExecContext(ctx, `INSERT INTO email_accounts
		(user_id,mirror_account_id,provider,address,cred_ciphertext)
		VALUES ($1,'graph-acct','graph','graph@example.com',$2)`, p.uid, sealed); err != nil {
		t.Fatal(err)
	}
	id := mail.NativeMessageID(mail.ProviderGraph, "old-1")
	if err := p.app.store.PutEnvelopes(ctx, acct, []mail.Envelope{{
		ID: id, ThreadID: "thread-graph", MailboxIDs: []mail.MailboxID{"inbox"},
		Subject: "graph target", From: []mail.Address{{Email: "someone@example.com"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.ExecContext(ctx, `INSERT INTO hey_messages
		(user_id,account_id,message_id,bucket) VALUES ($1,'graph-acct',$2,'imbox')`,
		p.uid, string(id)); err != nil {
		t.Fatal(err)
	}
	return string(id)
}

func graphAction(t *testing.T, p productPG, messageID, body string) (int, map[string]any) {
	t.Helper()
	r := jsonBody(t, "POST", "/messages/"+messageID+"/action?account=graph-acct", body)
	r.SetPathValue("message", messageID)
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
	w := httptest.NewRecorder()
	p.app.handleMessageAction(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// promoteIdentity runs one promotion inside a single transaction with the
// engine's replacement-before-retirement shape: the replacement envelope is
// written first, product references remap before any retirement, and the old
// identity retires last — all committing together or not at all.
func promoteIdentity(t *testing.T, p productPG, old, newID, newThread string) (commit func() error, err error) {
	t.Helper()
	ctx := context.Background()
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if err := admitIdentityWrite(ctx, tx, "graph-acct"); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	writes := []struct {
		query string
		args  []any
	}{
		// The engine's replacement writes, inside the same transaction.
		{`INSERT INTO mail_messages (account_id,id,thread_id,subject) SELECT account_id,$3,$4,subject FROM mail_messages WHERE account_id=$1 AND id=$2`, []any{"graph-acct", old, newID, newThread}},
		{`INSERT INTO mail_message_mailboxes (account_id,message_id,mailbox_id) SELECT account_id,$3,mailbox_id FROM mail_message_mailboxes WHERE account_id=$1 AND message_id=$2`, []any{"graph-acct", old, newID}},
		{`INSERT INTO mail_bodies (account_id,message_id,text_body,html_body,parts,fetched_at) SELECT account_id,$3,text_body,html_body,parts,fetched_at FROM mail_bodies WHERE account_id=$1 AND message_id=$2`, []any{"graph-acct", old, newID}},
	}
	for _, write := range writes {
		if _, err := tx.ExecContext(ctx, write.query, write.args...); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
	}
	if err := remapProductIdentity(ctx, tx, "graph-acct", []IdentityPair{{OldID: old, NewID: newID}}); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	retires := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM mail_message_mailboxes WHERE account_id=$1 AND message_id=$2`, []any{"graph-acct", old}},
		{`DELETE FROM mail_bodies WHERE account_id=$1 AND message_id=$2`, []any{"graph-acct", old}},
		{`DELETE FROM mail_messages WHERE account_id=$1 AND id=$2`, []any{"graph-acct", old}},
	}
	for _, retire := range retires {
		if _, err := tx.ExecContext(ctx, retire.query, retire.args...); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
	}
	return func() error { return tx.Commit() }, nil
}

func TestIntegrationIdentityRemapMovesEveryProductReferenceAtomically(t *testing.T) {
	p := newProductPG(t)
	old := seedGraphAccount(t, p)
	ctx := context.Background()
	newID := string(mail.NativeMessageID(mail.ProviderGraph, "new-1"))

	// A push delivery references the old id.
	if _, err := p.db.ExecContext(ctx, `INSERT INTO push_deliveries
		(user_id,account_id,message_id,subscription_hash)
		VALUES ($1,'graph-acct',$2,'')`, p.uid, old); err != nil {
		t.Fatal(err)
	}

	// A sealed outbox payload references the old id as its reply parent.
	rawPayload, err := json.Marshal(outboxPayload{
		ReplyParent: old,
		Request:     json.RawMessage(`{"reply_to_message_id":"` + old + `"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealBound(p.cfg, "payload", p.uid, "11111111-1111-1111-1111-111111111111", string(rawPayload))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.ExecContext(ctx, `INSERT INTO outbox_jobs
		(id,user_id,account_id,submission_key,request_hash,state,payload_ciphertext,payload_bytes,undo_until)
		VALUES ('11111111-1111-1111-1111-111111111111',$1,'graph-acct','sub-1','hash-1','pending',$2,$3,now())`,
		p.uid, sealed, len(rawPayload)); err != nil {
		t.Fatal(err)
	}

	// A live undo receipt references the old id and must stay usable.
	code, response := graphAction(t, p, old, `{"action":"later"}`)
	if code != http.StatusOK {
		t.Fatalf("action: %d %+v", code, response)
	}
	token, _ := response["undo_token"].(string)
	if token == "" {
		t.Fatal("no undo token")
	}

	// The inventory sees every reference before promotion, including the
	// sealed outbox payload and the undo snapshot.
	inventoryTx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := p.app.graphReferenceInventory(ctx, inventoryTx, "graph-acct")
	_ = inventoryTx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, id := range ids {
		found[string(id)] = true
	}
	if !found[old] {
		t.Fatalf("inventory missed the classified message: %v", ids)
	}

	commit, err := promoteIdentity(t, p, old, newID, "thread-graph")
	if err != nil {
		t.Fatal(err)
	}
	if err := commit(); err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{"hey_messages", "push_deliveries", "message_action_undo"} {
		var got string
		if err := p.db.QueryRowContext(ctx, `SELECT message_id FROM `+table+` WHERE account_id='graph-acct' LIMIT 1`).Scan(&got); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if got != newID {
			t.Fatalf("%s still references %s", table, got)
		}
	}
	var mirrorOld int
	if err := p.db.QueryRowContext(ctx, `SELECT count(*) FROM mail_messages WHERE account_id='graph-acct' AND id=$1`, old).Scan(&mirrorOld); err != nil {
		t.Fatal(err)
	}
	if mirrorOld != 0 {
		t.Fatal("old mirror identity survived its own promotion")
	}
	var snapshot string
	if err := p.db.QueryRowContext(ctx, `SELECT snapshot FROM message_action_undo WHERE token=$1`, token).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snapshot, newID) || strings.Contains(snapshot, old) {
		t.Fatalf("undo snapshot not remapped: %s", snapshot)
	}
	// The remapped receipt still restores its exact preimage under the new id.
	if code, _ := graphAction(t, p, newID, `{"action":"restore","undo_token":"`+token+`"}`); code != http.StatusOK {
		t.Fatalf("post-remap undo: %d", code)
	}
	var bucket string
	if err := p.db.QueryRowContext(ctx, `SELECT bucket FROM hey_messages WHERE account_id='graph-acct' AND message_id=$1`, newID).Scan(&bucket); err != nil {
		t.Fatal(err)
	}
	if bucket != "imbox" {
		t.Fatalf("post-remap undo restored %q", bucket)
	}
}

func TestIntegrationIdentityRemapRefusesThreadChangeAndRollsBackEverything(t *testing.T) {
	p := newProductPG(t)
	old := seedGraphAccount(t, p)
	ctx := context.Background()
	// The replacement identity would land on another thread: remapping
	// would silently re-file every retained reference.
	newID := string(mail.NativeMessageID(mail.ProviderGraph, "new-thread"))
	_, err := promoteIdentity(t, p, old, newID, "thread-other")
	if err == nil || !strings.Contains(err.Error(), "thread identity") {
		t.Fatalf("thread change accepted: %v", err)
	}
	for _, table := range []string{"hey_messages", "mail_message_mailboxes"} {
		var count int
		if err := p.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE account_id='graph-acct' AND message_id=$1`, old).Scan(&count); err != nil {
			t.Fatal(table, err)
		}
		if count != 1 {
			t.Fatalf("%s rolled back to %d old-id rows", table, count)
		}
	}
	var mirror int
	if err := p.db.QueryRowContext(ctx, `SELECT count(*) FROM mail_messages WHERE account_id='graph-acct' AND id=$1`, old).Scan(&mirror); err != nil {
		t.Fatal(err)
	}
	if mirror != 1 {
		t.Fatalf("mail_messages rolled back to %d old-id rows", mirror)
	}
	// The replacement envelope itself rolled back with everything else.
	var replacements int
	if err := p.db.QueryRowContext(ctx, `SELECT count(*) FROM mail_messages WHERE account_id='graph-acct' AND id=$1`, newID).Scan(&replacements); err != nil {
		t.Fatal(err)
	}
	if replacements != 0 {
		t.Fatal("refused promotion left its replacement envelope behind")
	}
	// A non-Graph reference in the account's retained state must refuse the
	// inventory outright rather than translating a foreign namespace.
	if _, err := p.db.ExecContext(ctx, `INSERT INTO push_deliveries
		(user_id,account_id,message_id,subscription_hash)
		VALUES ($1,'graph-acct','n:imap:foreign','')`, p.uid); err != nil {
		t.Fatal(err)
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.app.graphReferenceInventory(ctx, tx, "graph-acct")
	_ = tx.Rollback()
	if err == nil || !strings.Contains(err.Error(), "non-Graph") {
		t.Fatalf("foreign-namespace reference accepted: %v", err)
	}
}

func TestIntegrationGraphMailboxBindingRefusesWithoutVerifiedContract(t *testing.T) {
	p := newProductPG(t)
	seedGraphAccount(t, p)
	ctx := context.Background()
	// A resolvable Graph OAuth credential (future expiry, no network use)
	// so the refusal provably comes from the missing upstream contract.
	p.cfg.MicrosoftClientID = "client-id"
	p.cfg.MicrosoftClientSecret = "client-secret"
	tokenJSON := `{"access_token":"bearer","token_type":"Bearer","refresh_token":"refresh","expiry":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`
	oauthSealed, err := sealSecret(p.cfg, tokenJSON)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.ExecContext(ctx, `UPDATE email_accounts SET cred_ciphertext=$1 WHERE mirror_account_id='graph-acct'`, oauthSealed); err != nil {
		t.Fatal(err)
	}
	// The upstream verification contract is absent from the vendored engine,
	// so binding must refuse rather than trust an email-derived key.
	if err := p.app.bindGraphMailbox(ctx, p.uid, "graph-acct", ""); err == nil {
		t.Fatal("empty verified key accepted")
	}
	if err := p.app.bindGraphMailbox(ctx, p.uid, "graph-acct", "operator-verified"); err == nil ||
		!strings.Contains(err.Error(), "translator unavailable") {
		t.Fatalf("binding did not refuse on the missing upstream contract: %v", err)
	}
	var key string
	if err := p.db.QueryRowContext(ctx, `SELECT graph_mailbox_key FROM email_accounts WHERE mirror_account_id='graph-acct'`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != "" {
		t.Fatalf("binding wrote an unverified key: %q", key)
	}
}

// The connect-time capture is the only product-side source of a verified
// Graph mailbox key today: /me must be asked for the immutable id, and a
// provider response without one must refuse the connection.
func TestGraphOAuthIdentityRequiresVerifiedMailboxKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1.0/me" {
			w.WriteHeader(404)
			return
		}
		if !strings.Contains(r.URL.Query().Get("$select"), "id") {
			w.WriteHeader(400)
			return
		}
		_, _ = io.WriteString(w, `{"mail":"graph@example.com","displayName":"Graph"}`)
	}))
	defer server.Close()
	client := server.Client()
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Transport = rewriteTransport{base: transport, target: server.URL}
	if _, _, _, err := oauthIdentityWithMailbox(context.Background(), "graph", client); err == nil ||
		!strings.Contains(err.Error(), "verified mailbox key") {
		t.Fatalf("id-less /me accepted: %v", err)
	}
}

type rewriteTransport struct {
	base   http.RoundTripper
	target string
}

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == "graph.microsoft.com" {
		parsed, err := url.Parse(t.target + req.URL.Path + "?" + req.URL.RawQuery)
		if err != nil {
			return nil, err
		}
		req.URL = parsed
	}
	return t.base.RoundTrip(req)
}
