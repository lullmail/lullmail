package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The mirror's received_at is a naive TIMESTAMP holding UTC digits. People
// and the Screener used to send max(received_at)::text — "2026-10-03
// 09:30:00", no zone — which a browser reads as local time (and Safari does
// not parse at all), so every "last …" time was off by the viewer's UTC
// offset. Both must answer an explicit UTC instant, in any session timezone.
func TestIntegrationPeopleAndScreenerTimesAreExplicitUTC(t *testing.T) {
	p := newProductPG(t)
	seedOutboxAccount(t, p)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := p.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	msg := func(id, from, bucket string) {
		exec(`INSERT INTO mail_messages(account_id,id,thread_id,subject,received_at,from_addrs)
			VALUES('outbox-acct',$1,$1,'s','2026-10-03 09:30:00'::timestamp,$2)`, id, `[{"email":"`+from+`"}]`)
		exec(`INSERT INTO hey_messages(user_id,account_id,message_id,bucket) VALUES($1,'outbox-acct',$2,$3)`, p.uid, id, bucket)
	}
	msg("known", "ann@acme.test", "imbox")
	msg("new", "stranger@else.test", "screener")
	exec(`INSERT INTO hey_senders(user_id,sender_key,allowed,route) VALUES($1,'ann@acme.test',true,'imbox')`, p.uid)

	get := func(h http.HandlerFunc, path string) []map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var out []map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	const want = "2026-10-03T09:30:00Z"
	people := get(p.app.handlePeople, "/api/people")
	if len(people) != 1 || people[0]["last_at"] != want {
		t.Fatalf("people last_at: %v", people)
	}
	screener := get(p.app.handleScreener, "/api/screener")
	if len(screener) != 1 || screener[0]["newest"] != want {
		t.Fatalf("screener newest: %v", screener)
	}
}
