package app

// Real-PostgreSQL coverage for the sent-mail classification rule: a
// message the provider filed only in the owner's Sent folder is
// filed under Sent, never an Imbox arrival — even though the owner's own
// address is always a correspondent, which is what let machine-sent
// mail (cold outreach, API sends) surface in the Imbox as if the owner
// had written to themselves. Same skip contract as product_pg_test.go:
// LULL_TEST_DATABASE_URL points at a DISPOSABLE scratch database.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

	if got := bucketOf(t, pg.db, "m-sent"); got != "sent" {
		t.Errorf("sent-only message bucketed %q; sent mail has its own place — want sent", got)
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
	if got := bucketOf(t, pg.db, "m-sent"); got != "sent" {
		t.Errorf("misfiled sent-only row still %q after a pass; the self-heal did not repair it", got)
	}
}

// Gmail over IMAP files a copy of every sent message under "[Gmail]/All
// Mail", and that folder syncs before "Sent Mail". Until the Sent copy
// lands the mirror knows the message only as All Mail — no sent role — and
// the owner's address is always a correspondent, so it used to open in the
// Imbox. Outside the Inbox, mail from the owner is outbound whatever folder
// holds it.
func TestOwnersMailOutsideTheInboxIsNeverTheImbox(t *testing.T) {
	pg := newProductPG(t)
	ctx := context.Background()
	seedClassifyFixture(t, pg.db, pg.uid)
	now := time.Now().UTC()
	add := func(id, from string, boxes ...string) {
		t.Helper()
		mustClassifyExec(t, pg.db,
			`INSERT INTO mail_messages (account_id, id, thread_id, subject, received_at, from_addrs, to_addrs)
			 VALUES ('acct-1', $1, $1, 's', $2, $3, '[{"email":"someone@else.test"}]')`,
			id, now, `[{"email":"`+from+`"}]`)
		for _, b := range boxes {
			mustClassifyExec(t, pg.db,
				`INSERT INTO mail_message_mailboxes (account_id, message_id, mailbox_id) VALUES ('acct-1', $1, $2)`, id, b)
		}
	}
	add("m-allonly", "Tyler@DreamLucidMail.com", "mb-all")
	// An archived reply from a correspondent is still mail that arrived.
	add("m-archived", "rbeskau@rlelectrical.ca", "mb-all")

	if err := pg.app.classifyUser(ctx, pg.uid); err != nil {
		t.Fatalf("classify: %v", err)
	}
	if got := bucketOf(t, pg.db, "m-allonly"); got != "sent" {
		t.Errorf("owner's All Mail copy bucketed %q; want sent", got)
	}
	if got := bucketOf(t, pg.db, "m-archived"); got != "imbox" {
		t.Errorf("archived correspondent mail bucketed %q; want imbox", got)
	}

	// Rows misfiled before the rule repair on the next pass.
	mustClassifyExec(t, pg.db,
		`UPDATE hey_messages SET bucket='imbox' WHERE account_id='acct-1' AND message_id='m-allonly'`)
	if err := pg.app.classifyUser(ctx, pg.uid); err != nil {
		t.Fatalf("second classify: %v", err)
	}
	if got := bucketOf(t, pg.db, "m-allonly"); got != "sent" {
		t.Errorf("misfiled All Mail copy still %q after a pass", got)
	}
	if got := bucketOf(t, pg.db, "m-selfcopy"); got != "imbox" {
		t.Errorf("self-copy in the Inbox moved to %q; Inbox membership still wins", got)
	}
}

// A provider draft is unsent work. It carries the drafts role (plus All Mail
// on Gmail) and must not be listed as mail in any view.
func TestProviderDraftsAreHiddenFromViews(t *testing.T) {
	if got := discardedFilter("m"); !strings.Contains(got, "'drafts'") {
		t.Fatalf("discardedFilter does not exclude drafts: %s", got)
	}
}

// Sent mail is born read, moves out of Receipts on the first pass after an
// upgrade (where it filed before 'sent' existed), stays put while snoozed
// with its thread, and lists under /buckets/sent with the person written to.
func TestSentMailHasItsOwnPlace(t *testing.T) {
	pg := newProductPG(t)
	ctx := context.Background()
	seedClassifyFixture(t, pg.db, pg.uid)
	if err := pg.app.classifyUser(ctx, pg.uid); err != nil {
		t.Fatalf("classify: %v", err)
	}
	var read bool
	if err := pg.db.QueryRow(`SELECT read_at IS NOT NULL FROM hey_messages WHERE message_id='m-sent'`).Scan(&read); err != nil || !read {
		t.Fatalf("sent mail must be born read (read=%v err=%v)", read, err)
	}

	// The pre-'sent' world: filed under Receipts, unread.
	mustClassifyExec(t, pg.db, `UPDATE hey_messages SET bucket='paper_trail', read_at=NULL WHERE message_id='m-sent'`)
	if err := pg.app.classifyUser(ctx, pg.uid); err != nil {
		t.Fatalf("classify: %v", err)
	}
	if got := bucketOf(t, pg.db, "m-sent"); got != "sent" {
		t.Errorf("sent mail left in %q after an upgrade pass; want sent", got)
	}
	if err := pg.db.QueryRow(`SELECT read_at IS NOT NULL FROM hey_messages WHERE message_id='m-sent'`).Scan(&read); err != nil || !read {
		t.Errorf("migrated sent mail must not count as unread (read=%v err=%v)", read, err)
	}

	// Snoozed with its thread: the heal leaves it until it comes back.
	mustClassifyExec(t, pg.db, `UPDATE hey_messages SET bucket='set_aside' WHERE message_id='m-sent'`)
	if err := pg.app.classifyUser(ctx, pg.uid); err != nil {
		t.Fatalf("classify: %v", err)
	}
	if got := bucketOf(t, pg.db, "m-sent"); got != "set_aside" {
		t.Errorf("snoozed sent mail moved to %q; a snooze outranks the filing rule", got)
	}
	mustClassifyExec(t, pg.db, `UPDATE hey_messages SET bucket='sent' WHERE message_id='m-sent'`)

	req := httptest.NewRequest(http.MethodGet, "/api/buckets/sent", nil)
	req.SetPathValue("bucket", "sent")
	w := httptest.NewRecorder()
	pg.app.handleBucket(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("sent list: %d %s", w.Code, w.Body.String())
	}
	var page struct {
		Rows []struct {
			MessageID string `json:"message_id"`
			To        string `json:"to"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 1 || page.Rows[0].MessageID != "m-sent" || page.Rows[0].To != "rbeskau@rlelectrical.ca" {
		t.Errorf("sent list = %+v; want the one sent thread with its recipient", page.Rows)
	}
}
