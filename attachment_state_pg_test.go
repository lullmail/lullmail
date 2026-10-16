package main

// GMAIL-03 wire contract, executed against real PostgreSQL: the mirror's
// attachment_state smallint (0/1/2) reaches bucket-list and search rows
// as the honest string tri-state. "present" only when a fetch saw the
// part tree and found an attachment; "unknown" when a metadata envelope
// could make no claim — never a silent false. Seeded through the engine
// store's own PutEnvelopes so the smallint values are exactly what a
// real sync writes, and the old has_attachment bool is proven gone from
// the wire. Same skip contract as product_pg_test.go.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/neutron-build/neutron/mail"
)

type attachmentRows struct {
	Rows []struct {
		MessageID       string `json:"message_id"`
		AttachmentState string `json:"attachment_state"`
		HasAttachment   *bool  `json:"has_attachment"`
	} `json:"rows"`
}

func TestIntegrationRowsCarryAttachmentTriState(t *testing.T) {
	p := newProductPG(t)
	ctx := context.Background()

	acct := mail.AccountID("attach-acct")
	if err := p.app.store.PutAccount(ctx, &mail.Account{
		ID: acct, Provider: mail.ProviderIMAP, Email: "attach@example.com", Name: "Attach",
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
		VALUES ($1,'attach-acct','imap','attach@example.com',$2)`, p.uid, sealed); err != nil {
		t.Fatal(err)
	}

	// One thread per tri-state value, each named by the wire string it
	// must surface. unknown is the zero value a Gmail metadata envelope
	// writes; present/absent use the adapter-side mapping.
	states := []struct {
		name  string
		state mail.AttachmentState
	}{
		{"present", mail.AttachmentFromBool(true)},
		{"absent", mail.AttachmentFromBool(false)},
		{"unknown", mail.AttachmentUnknown},
	}
	want := map[string]string{}
	for i, s := range states {
		id := mail.HeaderMessageID("<attach-" + s.name + "@example.com>")
		want[string(id)] = s.name
		if err := p.app.store.PutEnvelopes(ctx, acct, []mail.Envelope{{
			ID: id, ThreadID: mail.ThreadID("thread-attach-" + s.name),
			MailboxIDs: []mail.MailboxID{"INBOX"},
			Subject:    "attachment state row " + s.name,
			From:       []mail.Address{{Email: "someone@example.com"}},
			// Distinct timestamps so row order is deterministic.
			ReceivedAt: time.Now().UTC().Add(-time.Duration(i) * time.Hour),
			Attachment: s.state,
		}}); err != nil {
			t.Fatal(err)
		}
		if _, err := p.db.ExecContext(ctx, `INSERT INTO hey_messages
			(user_id,account_id,message_id,bucket) VALUES ($1,'attach-acct',$2,'imbox')`,
			p.uid, string(id)); err != nil {
			t.Fatal(err)
		}
	}

	call := func(handler func(http.ResponseWriter, *http.Request), target string, bucket bool) attachmentRows {
		t.Helper()
		r := httptest.NewRequest("GET", target, nil)
		if bucket {
			r.SetPathValue("bucket", "imbox")
		}
		r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
		w := httptest.NewRecorder()
		handler(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d body %s", target, w.Code, w.Body.String())
		}
		var page attachmentRows
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatalf("%s decode: %v", target, err)
		}
		return page
	}

	for name, page := range map[string]attachmentRows{
		"bucket list": call(p.app.handleBucket, "/buckets/imbox", true),
		"search":      call(p.app.handleSearch, "/search?q=attachment%20state", false),
	} {
		if len(page.Rows) != len(want) {
			t.Fatalf("%s: %d rows, want %d", name, len(page.Rows), len(want))
		}
		for _, row := range page.Rows {
			state, ok := want[row.MessageID]
			if !ok {
				t.Fatalf("%s: unexpected row %s", name, row.MessageID)
			}
			if row.AttachmentState != state {
				t.Errorf("%s: %s attachment_state = %q, want %q", name, row.MessageID, row.AttachmentState, state)
			}
			// The boolean is retired with the column it read: a server
			// still emitting it would keep lying for every unknown row.
			if row.HasAttachment != nil {
				t.Errorf("%s: %s still carries has_attachment", name, row.MessageID)
			}
		}
	}
}
