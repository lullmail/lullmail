package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The Outbox has a row in the navigation only while it holds something, and
// /counts is what says so. A send that went through, or one the owner
// cancelled, is finished business and must not keep the row alive; a send in
// flight or one that stopped short must.
func TestIntegrationCountsReportUnfinishedSendsOnly(t *testing.T) {
	p := newProductPG(t)
	seedOutboxAccount(t, p)
	_, otherAcct := secondOwner(t, p, "someone-else@example.test")

	outbox := func(query string) int {
		t.Helper()
		w := httptest.NewRecorder()
		p.app.handleCounts(w, httptest.NewRequest(http.MethodGet, "/api/counts"+query, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("counts%s: %d %s", query, w.Code, w.Body)
		}
		var got map[string]int
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		n, ok := got["outbox"]
		if !ok {
			t.Fatalf("counts%s has no outbox number: %s", query, w.Body)
		}
		return n
	}
	if n := outbox(""); n != 0 {
		t.Fatalf("empty outbox counted %d", n)
	}

	bulkReceipts(t, p, p.uid, "outbox-acct", "sent-", "submitted", "1 hour", 3)
	bulkReceipts(t, p, p.uid, "outbox-acct", "gone-", "cancelled", "1 hour", 2)
	if n := outbox(""); n != 0 {
		t.Fatalf("finished sends counted as outstanding: %d", n)
	}

	bulkReceipts(t, p, p.uid, "outbox-acct", "wait-", "pending", "1 minute", 1)
	bulkReceipts(t, p, p.uid, "outbox-acct", "going-", "submitting", "1 minute", 1)
	bulkReceipts(t, p, p.uid, "outbox-acct", "fail-", "failed", "1 hour", 1)
	bulkReceipts(t, p, p.uid, "outbox-acct", "maybe-", "ambiguous", "1 hour", 1)
	// Another owner's stuck send is not this owner's business.
	var other string
	if err := p.db.QueryRow(`SELECT user_id::text FROM email_accounts WHERE mirror_account_id=$1`, otherAcct).Scan(&other); err != nil {
		t.Fatal(err)
	}
	bulkReceipts(t, p, other, otherAcct, "theirs-", "failed", "1 hour", 5)
	if n := outbox(""); n != 4 {
		t.Fatalf("outstanding sends = %d, want 4", n)
	}

	// The mailbox lens narrows it like every other number.
	var mine string
	if err := p.db.QueryRow(`SELECT id::text FROM email_accounts WHERE mirror_account_id='outbox-acct'`).Scan(&mine); err != nil {
		t.Fatal(err)
	}
	if n := outbox("?account=" + mine); n != 4 {
		t.Fatalf("lens on the sending mailbox = %d, want 4", n)
	}
	if _, err := p.db.Exec(`INSERT INTO email_accounts(user_id,mirror_account_id,provider,address,cred_ciphertext) VALUES($1,'quiet-acct','imap','quiet@example.test','fixture')`, p.uid); err != nil {
		t.Fatal(err)
	}
	var quiet string
	if err := p.db.QueryRow(`SELECT id::text FROM email_accounts WHERE mirror_account_id='quiet-acct'`).Scan(&quiet); err != nil {
		t.Fatal(err)
	}
	if n := outbox("?account=" + quiet); n != 0 {
		t.Fatalf("lens on a mailbox with no sends = %d, want 0", n)
	}
}
