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
	"net/url"
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

// A note to self classified while only its Sent copy had synced comes back
// to the Imbox, unread, once the Inbox copy lands.
func TestSentMailReturnsToTheImboxWhenItsInboxCopyArrives(t *testing.T) {
	pg := newProductPG(t)
	ctx := context.Background()
	seedClassifyFixture(t, pg.db, pg.uid)
	mustClassifyExec(t, pg.db, `DELETE FROM mail_message_mailboxes WHERE message_id='m-selfcopy' AND mailbox_id='mb-in'`)
	if err := pg.app.classifyUser(ctx, pg.uid); err != nil {
		t.Fatalf("classify: %v", err)
	}
	if got := bucketOf(t, pg.db, "m-selfcopy"); got != "sent" {
		t.Fatalf("Sent-only copy bucketed %q; want sent", got)
	}
	mustClassifyExec(t, pg.db, `INSERT INTO mail_message_mailboxes (account_id, message_id, mailbox_id) VALUES ('acct-1','m-selfcopy','mb-in')`)
	if err := pg.app.classifyUser(ctx, pg.uid); err != nil {
		t.Fatalf("classify: %v", err)
	}
	var bucket string
	var read bool
	if err := pg.db.QueryRow(`SELECT bucket, read_at IS NOT NULL FROM hey_messages WHERE message_id='m-selfcopy'`).Scan(&bucket, &read); err != nil {
		t.Fatal(err)
	}
	if bucket != "imbox" || read {
		t.Errorf("after its Inbox copy arrived: bucket=%q read=%v; want imbox, unread", bucket, read)
	}
}

// A snoozed thread nobody else has written in is a follow-up reminder: when
// the snooze returns it to the Imbox it must stay there, not be filed
// straight back under Sent.
func TestReturnedSnoozeOfTheOwnersOwnMailStaysInTheImbox(t *testing.T) {
	pg := newProductPG(t)
	ctx := context.Background()
	seedClassifyFixture(t, pg.db, pg.uid)
	mustClassifyExec(t, pg.db,
		`INSERT INTO mail_messages (account_id, id, thread_id, subject, received_at, from_addrs, to_addrs)
		 VALUES ('acct-1', 'm-nudge', 't-nudge', 's', now(), '[{"email":"tyler@dreamlucidmail.com"}]', '[{"email":"quiet@else.test"}]')`)
	mustClassifyExec(t, pg.db, `INSERT INTO mail_message_mailboxes (account_id, message_id, mailbox_id) VALUES ('acct-1','m-nudge','mb-sent')`)
	if err := pg.app.classifyUser(ctx, pg.uid); err != nil {
		t.Fatalf("classify: %v", err)
	}
	// Snoozed, and its day has come.
	mustClassifyExec(t, pg.db, `UPDATE hey_messages SET bucket='set_aside', set_aside_until=now() - interval '1 minute' WHERE message_id IN ('m-nudge','m-sent')`)
	if err := pg.app.sweepSnoozed(ctx, pg.uid, ""); err != nil {
		t.Fatal(err)
	}
	if err := pg.app.classifyUser(ctx, pg.uid); err != nil {
		t.Fatalf("classify: %v", err)
	}
	if got := bucketOf(t, pg.db, "m-nudge"); got != "imbox" {
		t.Errorf("returned reminder bucketed %q; want imbox", got)
	}
	// A reply that came back with a thread someone else wrote in is still
	// sent mail, not an arrival.
	if got := bucketOf(t, pg.db, "m-sent"); got != "sent" {
		t.Errorf("returned reply in an answered thread bucketed %q; want sent", got)
	}
}

// A mailbox marked outreach is one a machine sends from. Its sent mail lists
// under Outreach, not Sent, and flipping the flag re-sorts both at once
// because nothing is stored differently.
func TestOutreachMailboxSplitsTheSentList(t *testing.T) {
	pg := newProductPG(t)
	ctx := context.Background()
	seedClassifyFixture(t, pg.db, pg.uid)
	if err := pg.app.classifyUser(ctx, pg.uid); err != nil {
		t.Fatalf("classify: %v", err)
	}
	list := func(name string) []string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/buckets/"+name, nil)
		req.SetPathValue("bucket", name)
		w := httptest.NewRecorder()
		pg.app.handleBucket(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s list: %d %s", name, w.Code, w.Body.String())
		}
		var page struct {
			Rows []struct {
				MessageID string `json:"message_id"`
			} `json:"rows"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, r := range page.Rows {
			ids = append(ids, r.MessageID)
		}
		return ids
	}
	if sent, out := list("sent"), list("outreach"); len(sent) != 1 || sent[0] != "m-sent" || len(out) != 0 {
		t.Fatalf("personal mailbox: sent=%v outreach=%v", sent, out)
	}
	mustClassifyExec(t, pg.db, `UPDATE email_accounts SET outreach = true WHERE mirror_account_id = 'acct-1'`)
	if sent, out := list("sent"), list("outreach"); len(sent) != 0 || len(out) != 1 || out[0] != "m-sent" {
		t.Fatalf("outreach mailbox: sent=%v outreach=%v", sent, out)
	}
}

// On an outreach mailbox only the people actually addressed skip the
// Screener. A stranger who merely shares the thread — the bounce, the
// auto-responder — is not vouched for by a cold send.
func TestOutreachThreadsDoNotVouchForStrangers(t *testing.T) {
	for _, outreach := range []bool{false, true} {
		pg := newProductPG(t)
		ctx := context.Background()
		seedClassifyFixture(t, pg.db, pg.uid)
		mustClassifyExec(t, pg.db, `UPDATE email_accounts SET outreach = $1, created_at = now() - interval '1 day' WHERE mirror_account_id = 'acct-1'`, outreach)
		for _, m := range [][2]string{{"m-bounce", "mailer-daemon@rlelectrical.ca"}, {"m-lead", "rbeskau@rlelectrical.ca"}} {
			mustClassifyExec(t, pg.db,
				`INSERT INTO mail_messages (account_id, id, thread_id, subject, received_at, from_addrs, to_addrs)
				 VALUES ('acct-1', $1, 't1', 's', now(), $2, '[{"email":"tyler@dreamlucidmail.com"}]')`,
				m[0], `[{"email":"`+m[1]+`"}]`)
			mustClassifyExec(t, pg.db, `INSERT INTO mail_message_mailboxes (account_id, message_id, mailbox_id) VALUES ('acct-1', $1, 'mb-in')`, m[0])
		}
		if err := pg.app.classifyUser(ctx, pg.uid); err != nil {
			t.Fatalf("classify: %v", err)
		}
		if got := bucketOf(t, pg.db, "m-lead"); got != "imbox" {
			t.Errorf("outreach=%v: a reply from the person addressed bucketed %q; want imbox", outreach, got)
		}
		want := "imbox"
		if outreach {
			want = "screener"
		}
		if got := bucketOf(t, pg.db, "m-bounce"); got != want {
			t.Errorf("outreach=%v: a stranger sharing the thread bucketed %q; want %s", outreach, got, want)
		}
	}
}

// A provider's "all mail" view repeats every folder, the owner's sent mail
// included. In Lull it lists the mail that arrived: sent mail has its own
// place, and the Sent folder itself still shows everything sent.
func TestAllMailFolderListsArrivalsNotSentMail(t *testing.T) {
	pg := newProductPG(t)
	ctx := context.Background()
	seedClassifyFixture(t, pg.db, pg.uid)
	// The inbound reply is in All Mail too, as Gmail has it.
	mustClassifyExec(t, pg.db, `INSERT INTO mail_message_mailboxes (account_id, message_id, mailbox_id) VALUES ('acct-1','m-reply','mb-all')`)
	// A sent message Gmail has only under All Mail so far (Sent Mail has not
	// synced), and not yet classified: nothing marks it but who wrote it.
	if err := pg.app.classifyUser(ctx, pg.uid); err != nil {
		t.Fatalf("classify: %v", err)
	}
	folder := func(name string) string {
		t.Helper()
		w := httptest.NewRecorder()
		pg.app.handleFolder(w, httptest.NewRequest(http.MethodGet, "/api/folder?name="+url.QueryEscape(name), nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	all := folder("[Gmail]/All Mail")
	if !strings.Contains(all, `"m-reply"`) {
		t.Errorf("all mail lost a message that arrived: %s", all)
	}
	if strings.Contains(all, `"m-sent"`) {
		t.Errorf("all mail lists the owner's sent mail: %s", all)
	}
	if sent := folder("[Gmail]/Sent Mail"); !strings.Contains(sent, `"m-sent"`) {
		t.Errorf("the Sent folder itself must still list sent mail: %s", sent)
	}
	w := httptest.NewRecorder()
	pg.app.handleMailboxList(w, httptest.NewRequest(http.MethodGet, "/api/mailboxes", nil))
	if !strings.Contains(strings.ToLower(w.Body.String()), "all mail") {
		t.Errorf("the folder list dropped all mail: %s", w.Body.String())
	}
}
