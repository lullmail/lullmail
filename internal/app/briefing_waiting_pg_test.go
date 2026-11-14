package app

import (
	"context"
	"testing"
)

// Sent-only mail is classified out of the Imbox (it is a copy of something
// sent, not arrival). The turn analysis must still see it: when it looked at
// Imbox rows alone, the owner's reply was never the thread's latest message
// and "you're waiting" stayed empty for every IMAP account.
func TestIntegrationWaitingCountsTheOwnersSentOnlyReply(t *testing.T) {
	p := newProductPG(t)
	seedOutboxAccount(t, p) // owner address: sender@example.test
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := p.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO mail_mailboxes(account_id,id,name,role,native) VALUES('outbox-acct','Sent','Sent','sent','Sent'),('outbox-acct','INBOX','Inbox','inbox','INBOX')`)
	msg := func(id, thread, box, bucket, from, to, at string) {
		exec(`INSERT INTO mail_messages(account_id,id,thread_id,subject,received_at,from_addrs,to_addrs)
			VALUES('outbox-acct',$1,$2,$2,$5::timestamp,$3,$4)`, id, thread, from, to, at)
		exec(`INSERT INTO mail_message_mailboxes(account_id,message_id,mailbox_id) VALUES('outbox-acct',$1,$2)`, id, box)
		exec(`INSERT INTO hey_messages(user_id,account_id,message_id,bucket) VALUES($1,'outbox-acct',$2,$3)`, p.uid, id, bucket)
	}
	const me = `[{"name":"Owner","email":"sender@example.test"}]`
	const ari = `[{"name":"Ari Patel","email":"ari@northstar.test"}]`
	const cara = `[{"email":"cara@other.test"}]`

	// They wrote, the owner answered from Sent: waiting on Ari.
	msg("a1", "answered", "INBOX", "imbox", ari, me, "2026-10-01 10:00:00")
	msg("a2", "answered", "Sent", "sent", me, ari, "2026-10-01 12:00:00")
	// The owner answered, then they wrote back: their mail needs the owner.
	msg("b1", "theirs", "INBOX", "imbox", cara, me, "2026-10-02 10:00:00")
	msg("b2", "theirs", "Sent", "sent", me, cara, "2026-10-02 11:00:00")
	msg("b3", "theirs", "INBOX", "imbox", cara, me, "2026-10-02 12:00:00")
	// Outreach nobody answered: no Imbox mail in the thread, so not a card.
	msg("c1", "cold", "Sent", "sent", me, `[{"email":"stranger@else.test"}]`, "2026-10-03 10:00:00")

	needs, waiting, err := p.app.briefThreads(context.Background(), p.uid, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(waiting) != 1 || waiting[0].ThreadID != "answered" || waiting[0].MessageID != "a2" {
		t.Fatalf("waiting = %+v, want the answered thread", waiting)
	}
	if waiting[0].WaitingOn != "Ari Patel <ari@northstar.test>" {
		t.Fatalf("waiting_on = %q", waiting[0].WaitingOn)
	}
	if len(needs) != 1 || needs[0].ThreadID != "theirs" || needs[0].MessageID != "b3" {
		t.Fatalf("needs you = %+v, want the thread they answered", needs)
	}
}
