package main

// Phase 1 identity-alias regressions (docs/identity-contract.md, "Alias
// table"): every remap records a durable old→new alias in the same
// transaction, chains collapse on write so nothing ever resolves to a
// retired identity, and the two documented consultation points — outbox
// delivery (deliveryFor) and reply seeding (handleSend's parent resolution)
// — follow exactly one alias hop when the stored identity no longer exists
// in the mirror. A miss without an alias keeps today's behavior.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neutron-build/neutron/mail"
	"golang.org/x/oauth2"
)

// A remap writes its alias; a second remap collapses the existing chain:
// A→B→C becomes A→C, never a two-hop resolution. Fail-before: without the
// alias write, promotions leave no rows at all and the second remap has
// nothing to collapse — delivery and seeding keep missing forever.
func TestIntegrationRemapRecordsAliasAndCollapsesChains(t *testing.T) {
	p := newProductPG(t)
	first := seedGraphAccount(t, p)
	ctx := context.Background()
	second := string(mail.NativeMessageID(mail.ProviderGraph, "new-1"))
	third := string(mail.NativeMessageID(mail.ProviderGraph, "new-2"))

	if err := promoteIdentity(t, p, first, second, "thread-graph"); err != nil {
		t.Fatal(err)
	}
	alias := func(old string) string {
		var got string
		if err := p.db.QueryRowContext(ctx, `SELECT new_id FROM message_identity_aliases WHERE account_id='graph-acct' AND old_id=$1`, old).Scan(&got); err != nil {
			t.Fatalf("alias for %s: %v", old, err)
		}
		return got
	}
	if got := alias(first); got != second {
		t.Fatalf("first remap recorded %s→%s, want →%s", first, got, second)
	}

	if err := promoteIdentity(t, p, second, third, "thread-graph"); err != nil {
		t.Fatal(err)
	}
	if got := alias(first); got != third {
		t.Fatalf("chain did not collapse: %s→%s, want →%s", first, got, third)
	}
	if got := alias(second); got != third {
		t.Fatalf("second remap recorded %s→%s, want →%s", second, got, third)
	}
	var pointingAtRetired int
	if err := p.db.QueryRowContext(ctx, `SELECT count(*) FROM message_identity_aliases WHERE account_id='graph-acct' AND new_id=$1`, second).Scan(&pointingAtRetired); err != nil {
		t.Fatal(err)
	}
	if pointingAtRetired != 0 {
		t.Fatalf("%d aliases still point at the retired identity %s", pointingAtRetired, second)
	}
}

// seedGmailAliasedParent installs a Gmail account with a fresh send token
// and one mirror message, then retires that message's identity through the
// production migration driver — the way a real remap does — leaving the
// alias row as the only link from the old identity to the new one.
func seedGmailAliasedParent(t *testing.T, p productPG, thread string) (oldParent, newParent, public string) {
	t.Helper()
	ctx := context.Background()
	token, err := json.Marshal(oauth2.Token{AccessToken: "gmail-token", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	sealedToken, err := sealSecret(p.cfg, string(token))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.db.QueryRowContext(ctx, `INSERT INTO email_accounts
		(user_id, mirror_account_id, provider, address, label, username, host, port, smtp_host, smtp_port, cred_ciphertext)
		VALUES ($1,'gmail-acct','gmail','owner@example.com','','owner@example.com','',0,'',0,$2) RETURNING id::text`, p.uid, sealedToken).Scan(&public); err != nil {
		t.Fatal(err)
	}
	oldParent = string(mail.NativeMessageID(mail.ProviderGmail, "MSG-1"))
	newParent = string(mail.NativeMessageID(mail.ProviderGmail, "MSG-2"))
	if err := p.app.store.PutEnvelopes(ctx, "gmail-acct", []mail.Envelope{{
		ID: mail.NativeMessageID(mail.ProviderGmail, "MSG-1"), ThreadID: mail.ThreadID(thread),
		MailboxIDs: []mail.MailboxID{"INBOX"}, ReceivedAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatal(err)
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runIdentityMigration(ctx, tx, "gmail-acct", identityReplacement{OldID: oldParent, NewID: newParent, Thread: thread}); err != nil {
		t.Fatal(err)
	}
	return oldParent, newParent, public
}

// A sealed reply payload whose ReplyParent was remapped after admission —
// the identity no longer exists in the mirror, and ciphertext is never
// rewritten — still threads at delivery through the alias. Fail-before:
// without the alias consultation, the legacy thread-id recovery misses the
// parent and the send silently drops its thread id. A miss without an
// alias keeps today's behavior: no invented thread id.
func TestIntegrationDeliveryResolvesAliasedReplyParent(t *testing.T) {
	p := newProductPG(t)
	p.cfg.GoogleClientID = "google-client"
	p.cfg.GoogleClientSecret = "google-secret"
	oldParent, _, _ := seedGmailAliasedParent(t, p, "T-alias-1")
	ctx := context.Background()

	captured := captureTransport(t, 0, nil)
	out := &mail.Outgoing{
		To:        []mail.Address{{Email: "peer@example.com"}},
		Subject:   "Re: aliased",
		Text:      "reply",
		InReplyTo: "parent@example.com",
	}
	// A legacy-shaped payload: the stored parent identity, no persisted
	// thread id (sendqueue recovers it from the mirror at delivery).
	deliver, _, ok := p.app.deliveryFor(ctx, "gmail-acct", oldParent, "")
	if !ok {
		t.Fatal("no delivery closure for gmail-acct")
	}
	if err := deliver(ctx, out); err != nil {
		t.Fatal(err)
	}
	deliver, _, ok = p.app.deliveryFor(ctx, "gmail-acct", "n:gmail:GONE", "")
	if !ok {
		t.Fatal("no delivery closure for the unaliased miss")
	}
	if err := deliver(ctx, out); err != nil {
		t.Fatal(err)
	}
	if len(*captured) != 2 {
		t.Fatalf("gmail requests = %d, want 2", len(*captured))
	}
	var aliased map[string]any
	if err := json.Unmarshal([]byte((*captured)[0].Body), &aliased); err != nil {
		t.Fatal(err)
	}
	if aliased["threadId"] != "T-alias-1" {
		t.Fatalf("aliased parent delivered threadId = %v, want the remapped parent's T-alias-1", aliased["threadId"])
	}
	var unaliased map[string]any
	if err := json.Unmarshal([]byte((*captured)[1].Body), &unaliased); err != nil {
		t.Fatal(err)
	}
	if _, ok := unaliased["threadId"]; ok {
		t.Fatalf("unaliased miss invented a thread id: %v", unaliased["threadId"])
	}
}

// A stored reply seed — the same reply_to_message_id field a recovered
// outbox draft carries in sealed ciphertext, which is never rewritten —
// still admits when a remap retired the parent identity: one alias hop
// resolves the current parent, and the newly sealed payload carries it.
// Fail-before: admission answers 404 "no such reply parent" and the
// recovered draft can never be sent. A miss without an alias keeps today's
// refusal.
func TestIntegrationSendAdmissionResolvesAliasedReplySeed(t *testing.T) {
	p := newProductPG(t)
	p.cfg.GoogleClientID = "google-client"
	p.cfg.GoogleClientSecret = "google-secret"
	oldParent, newParent, public := seedGmailAliasedParent(t, p, "T-seed-1")
	ctx := context.Background()

	post := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
		w := httptest.NewRecorder()
		p.app.handleSend(w, r)
		return w
	}
	w := post(`{"to":"peer@example.com","subject":"Re: t","text":"reply","account_id":"` + public + `","reply_to_message_id":"` + oldParent + `"}`)
	if w.Code != 200 {
		t.Fatalf("aliased seed admission = %d: %s", w.Code, w.Body.String())
	}
	var queued struct {
		ID string `json:"queued"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &queued); err != nil || queued.ID == "" {
		t.Fatalf("no durable id in %s", w.Body.String())
	}
	var ciphertext string
	if err := p.db.QueryRowContext(ctx, `SELECT payload_ciphertext FROM outbox_jobs WHERE id=$1`, queued.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	plain, err := openBound(p.cfg, "payload", p.uid, queued.ID, ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	var payload outboxPayload
	if err := json.Unmarshal([]byte(plain), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ReplyParent != newParent || payload.ReplyThreadID != "T-seed-1" {
		t.Fatalf("aliased seed persisted %q/%q, want parent %q thread T-seed-1", payload.ReplyParent, payload.ReplyThreadID, newParent)
	}

	w = post(`{"to":"peer@example.com","subject":"Re: t","text":"reply","account_id":"` + public + `","reply_to_message_id":"n:gmail:NOSUCH"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unaliased seed admission = %d, want today's 404: %s", w.Code, w.Body.String())
	}
}
