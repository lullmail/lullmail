package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// /people/sent answers for a whole set of correspondents in one call, and
// only from mail the owner sent: From is the account's address AND the
// provider filed it as Sent. A spoofed inbound copy proves nothing.
func TestIntegrationPeopleLastSentIsBatchedAndSentOnly(t *testing.T) {
	p := newProductPG(t)
	seedOutboxAccount(t, p)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := p.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO mail_mailboxes(account_id,id,name,role,native) VALUES('outbox-acct','Sent','Sent','sent','Sent'),('outbox-acct','INBOX','Inbox','inbox','INBOX')`)
	msg := func(id, box, from, to, cc, at string) {
		exec(`INSERT INTO mail_messages(account_id,id,thread_id,subject,sent_at,received_at,from_addrs,to_addrs,cc_addrs)
			VALUES('outbox-acct',$1,$1,'s',$5::timestamp,$5::timestamp,$2,$3,$4)`, id,
			`[{"email":"`+from+`"}]`, `[{"email":"`+to+`"}]`, cc, at)
		exec(`INSERT INTO mail_message_mailboxes(account_id,message_id,mailbox_id) VALUES('outbox-acct',$1,$2)`, id, box)
	}
	msg("m1", "Sent", "Sender@Example.test", "ann@acme.test", `[]`, "2026-10-01 10:00:00")
	msg("m2", "Sent", "sender@example.test", "ann@acme.test", `[{"email":"bob@acme.test"}]`, "2026-10-03 09:30:00")
	msg("m3", "Sent", "sender@example.test", "cara@other.test", `[]`, "2026-10-02 08:00:00")
	// Inbound, forged as the owner: must not count.
	msg("m4", "INBOX", "sender@example.test", "dan@acme.test", `[]`, "2026-10-05 08:00:00")
	// The owner's Sent mail to someone not asked about.
	msg("m5", "Sent", "sender@example.test", "eve@else.test", `[]`, "2026-10-06 08:00:00")

	ask := func(query string) []map[string]string {
		t.Helper()
		w := httptest.NewRecorder()
		p.app.handlePeopleLastSent(w, httptest.NewRequest(http.MethodGet, "/api/people/sent?"+query, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", query, w.Code, w.Body.String())
		}
		var out []map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	got := map[string]string{}
	for _, r := range ask("addr=ANN@acme.test&addr=cara@other.test&addr=dan@acme.test&addr=nobody@x.test") {
		got[r["address"]] = r["last_sent_at"]
	}
	if len(got) != 2 || got["ann@acme.test"] != "2026-10-03T09:30:00Z" || got["cara@other.test"] != "2026-10-02T08:00:00Z" {
		t.Fatalf("by address: %v", got)
	}
	got = map[string]string{}
	for _, r := range ask("domain=acme.test") {
		got[r["address"]] = r["last_sent_at"]
	}
	if len(got) != 2 || got["bob@acme.test"] != "2026-10-03T09:30:00Z" || got["dan@acme.test"] != "" {
		t.Fatalf("by domain: %v", got)
	}
	if out := ask(""); len(out) != 0 {
		t.Fatalf("empty ask answered %v", out)
	}
}
