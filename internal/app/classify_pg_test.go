package app

// Real-PostgreSQL coverage for the sent-mail classification rule: a
// message the provider filed only in the owner's Sent folder is
// reference, never an Imbox arrival — even though the owner's own
// address is always a correspondent, which is what let machine-sent
// mail (cold outreach, API sends) surface in the Imbox as if the owner
// had written to themselves. Same skip contract as product_pg_test.go:
// LULL_TEST_DATABASE_URL points at a DISPOSABLE scratch database.

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func seedClassifyFixture(t *testing.T, db *sql.DB, uid string) {
	t.Helper()
	mustClassifyExec(t, db,
		`INSERT INTO email_accounts (user_id, mirror_account_id, provider, address, cred_ciphertext, backfill_days)
		 VALUES ($1, 'acct-1', 'imap', 'tyler@dreamlucidmail.com', 'x', 0)`, uid)
	for _, mb := range [][4]string{
		{"mb-in", "INBOX", "inbox", "native-in"},
		{"mb-sent", "[Gmail]/Sent Mail", "sent", "native-sent"},
		{"mb-all", "[Gmail]/All Mail", "all", "native-all"},
	} {
		mustClassifyExec(t, db,
			`INSERT INTO mail_mailboxes (account_id, id, name, role, native) VALUES ('acct-1', $1, $2, $3, $4)`,
			mb[0], mb[1], mb[2], mb[3])
	}
	now := time.Now().UTC()
	// A cold send the provider filed only in Sent (+ All Mail): the exact
	// shape Akiroo's outreach leaves in the mirror.
	insertMessage := func(id, thread, from, to string, boxes ...string) {
		t.Helper()
		mustClassifyExec(t, db,
			`INSERT INTO mail_messages (account_id, id, thread_id, subject, received_at, from_addrs, to_addrs)
			 VALUES ('acct-1', $1, $2, $3, $4, $5, $6)`,
			id, thread, "subject "+id, now,
			`[{"name":"Tyler","email":"`+from+`"}]`,
			`[{"email":"`+to+`"}]`)
		for _, b := range boxes {
			mustClassifyExec(t, db,
				`INSERT INTO mail_message_mailboxes (account_id, message_id, mailbox_id) VALUES ('acct-1', $1, $2)`,
				id, b)
		}
	}
	insertMessage("m-sent", "t1", "tyler@dreamlucidmail.com", "rbeskau@rlelectrical.ca", "mb-sent", "mb-all")
	// The prospect replies: inbound, Inbox only.
	insertMessage("m-reply", "t1", "rbeskau@rlelectrical.ca", "tyler@dreamlucidmail.com", "mb-in")
	// A self-copy that also reaches the Inbox (a Bcc to self): received
	// mail, whatever else it is filed as.
	insertMessage("m-selfcopy", "t2", "tyler@dreamlucidmail.com", "micah@micahrich.com", "mb-in", "mb-sent")
}

func mustClassifyExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("seed (%s): %v", q, err)
	}
}

func bucketOf(t *testing.T, db *sql.DB, messageID string) string {
	t.Helper()
	var bucket string
	if err := db.QueryRow(
		`SELECT bucket FROM hey_messages WHERE account_id='acct-1' AND message_id=$1`, messageID,
	).Scan(&bucket); err != nil {
		t.Fatalf("bucket of %s: %v", messageID, err)
	}
	return bucket
}

func TestSentOnlyMailIsNeverTheImbox(t *testing.T) {
	pg := newProductPG(t)
	ctx := context.Background()
	seedClassifyFixture(t, pg.db, pg.uid)

	if err := pg.app.classifyUser(ctx, pg.uid); err != nil {
		t.Fatalf("classify: %v", err)
	}

	if got := bucketOf(t, pg.db, "m-sent"); got != "paper_trail" {
		t.Errorf("sent-only message bucketed %q; sent mail is reference, not the decision queue — want paper_trail", got)
	}
	if got := bucketOf(t, pg.db, "m-reply"); got != "imbox" {
		t.Errorf("inbound reply bucketed %q; a correspondent's new mail belongs in the Imbox", got)
	}
	if got := bucketOf(t, pg.db, "m-selfcopy"); got != "imbox" {
		t.Errorf("self-copy that reaches the Inbox bucketed %q; Inbox membership makes it received mail", got)
	}
}

func TestSentOnlyMailSelfHealsOutOfTheImbox(t *testing.T) {
	pg := newProductPG(t)
	ctx := context.Background()
	seedClassifyFixture(t, pg.db, pg.uid)

	// Pre-fix rows: the classifier already misfiled sent-only mail.
	if err := pg.app.classifyUser(ctx, pg.uid); err != nil {
		t.Fatalf("classify: %v", err)
	}
	mustClassifyExec(t, pg.db,
		`UPDATE hey_messages SET bucket='imbox' WHERE account_id='acct-1' AND message_id='m-sent'`)

	// A later pass (empty batch) must repair it, like the other self-heals.
	if err := pg.app.classifyUser(ctx, pg.uid); err != nil {
		t.Fatalf("second classify: %v", err)
	}
	if got := bucketOf(t, pg.db, "m-sent"); got != "paper_trail" {
		t.Errorf("misfiled sent-only row still %q after a pass; the self-heal did not repair it", got)
	}
}
