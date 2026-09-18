package main

// Real-PostgreSQL coverage for keyset pagination (audit DATA-06/DATA-11):
// pages are stable under insertion of newer rows, the continuation walks
// to exhaustion exactly once, and a malformed cursor is a 400 rather
// than a silent first page. Same skip contract as product_pg_test.go.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/neutron-build/neutron/mail"
)

type bucketPage struct {
	Rows []struct {
		MessageID  string `json:"message_id"`
		ThreadID   string `json:"thread_id"`
		ReceivedAt string `json:"received_at"`
	} `json:"rows"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor"`
}

func seedBuckets(t *testing.T, p productPG, count int) {
	t.Helper()
	ctx := context.Background()
	acct := mail.AccountID("page-acct")
	if err := p.app.store.PutAccount(ctx, &mail.Account{
		ID: acct, Provider: mail.ProviderIMAP, Email: "page@example.com", Name: "Pager",
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
		VALUES ($1,'page-acct','imap','page@example.com',$2)`, p.uid, sealed); err != nil {
		t.Fatal(err)
	}
	insertBucketMessages(t, p, 0, count)
}

// insertBucketMessages adds `count` new messages with DISTINCT threads,
// each 1h older than the last, newest first delivered. idBase separates
// seeding waves so a wave inserted mid-pagination is strictly newer.
func insertBucketMessages(t *testing.T, p productPG, idBase, count int) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Add(time.Duration(idBase) * time.Hour)
	for i := 0; i < count; i++ {
		id := mail.HeaderMessageID("<page-" + itoa(idBase) + "-" + itoa(i) + "@example.com>")
		thread := "thread-" + itoa(idBase) + "-" + itoa(i)
		env := mail.Envelope{
			ID: id, ThreadID: mail.ThreadID(thread), MailboxIDs: []mail.MailboxID{"INBOX"},
			Subject: "page row " + thread,
			From:    []mail.Address{{Email: "someone@example.com"}},
			// Distinct timestamps per row; equal-timestamp ties would lean
			// on the id tie-breaker rather than testing it.
			ReceivedAt: now.Add(-time.Duration(i) * time.Hour),
		}
		if err := p.app.store.PutEnvelopes(ctx, mail.AccountID("page-acct"), []mail.Envelope{env}); err != nil {
			t.Fatal(err)
		}
		if _, err := p.db.ExecContext(ctx, `INSERT INTO hey_messages
			(user_id,account_id,message_id,bucket) VALUES ($1,'page-acct',$2,'imbox')`,
			p.uid, string(id)); err != nil {
			t.Fatal(err)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func fetchBucketPage(t *testing.T, p productPG, query string) bucketPage {
	t.Helper()
	r := httptest.NewRequest("GET", "/buckets/imbox"+query, nil)
	r.SetPathValue("bucket", "imbox")
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
	w := httptest.NewRecorder()
	p.app.handleBucket(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("bucket page %s: status %d body %s", query, w.Code, w.Body.String())
	}
	var page bucketPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("bucket page decode: %v", err)
	}
	return page
}

// TestIntegrationKeysetPaginationStableUnderInsertion is DATA-06's core
// regression: after page 1 is delivered, newly arrived mail must not
// shift, duplicate, or drop the rows later pages return.
func TestIntegrationKeysetPaginationStableUnderInsertion(t *testing.T) {
	p := newProductPG(t)
	seedBuckets(t, p, 12)

	page1 := fetchBucketPage(t, p, "?limit=5")
	if len(page1.Rows) != 5 || !page1.HasMore || page1.NextCursor == "" {
		t.Fatalf("page 1: rows=%d has_more=%v cursor=%q", len(page1.Rows), page1.HasMore, page1.NextCursor)
	}

	// Two strictly newer threads land between page 1 and page 2.
	insertBucketMessages(t, p, 100, 2)

	page2 := fetchBucketPage(t, p, "?limit=5&cursor="+page1.NextCursor)
	page3 := fetchBucketPage(t, p, "?limit=5&cursor="+page2.NextCursor)
	if len(page2.Rows) != 5 || !page2.HasMore {
		t.Fatalf("page 2: rows=%d has_more=%v", len(page2.Rows), page2.HasMore)
	}
	if len(page3.Rows) != 2 || page3.HasMore || page3.NextCursor != "" {
		t.Fatalf("page 3 (exhaustion): rows=%d has_more=%v cursor=%q",
			len(page3.Rows), page3.HasMore, page3.NextCursor)
	}

	// No row appears twice, and every original row appears exactly once:
	// the two newer arrivals belong to pages fetched AFTER this walk (a
	// fresh walk would put them on page 1), never to these pages.
	seen := map[string]bool{}
	for _, row := range append(append([]struct {
		MessageID  string `json:"message_id"`
		ThreadID   string `json:"thread_id"`
		ReceivedAt string `json:"received_at"`
	}{}, page1.Rows...), page2.Rows...) {
		if seen[row.MessageID] {
			t.Fatalf("row %s delivered on two pages", row.MessageID)
		}
		seen[row.MessageID] = true
	}
	total := len(page1.Rows) + len(page2.Rows) + len(page3.Rows)
	if total != 12 {
		t.Fatalf("walk delivered %d rows, want the 12 seeded", total)
	}
	for _, row := range page3.Rows {
		if seen[row.MessageID] {
			t.Fatalf("row %s delivered on two pages", row.MessageID)
		}
	}

	// A fresh walk from the top DOES include the new arrivals on page 1.
	fresh := fetchBucketPage(t, p, "?limit=5")
	if fresh.Rows[0].ThreadID != "thread-100-0" {
		t.Fatalf("newest row after insertion = %s, want thread-100-0", fresh.Rows[0].ThreadID)
	}
}

// TestIntegrationPaginationRejectsMalformedCursor: a bad cursor must
// fail loudly, not silently restart pagination (a silent restart
// duplicates rows the client already has).
func TestIntegrationPaginationRejectsMalformedCursor(t *testing.T) {
	p := newProductPG(t)
	seedBuckets(t, p, 3)
	// URL-safe garbage: not valid base64url, so the decoder refuses it.
	// (A cursor with invalid percent-escapes is invisible to Query() and
	// reads as "no cursor" — the request is a plain first page.)
	r := httptest.NewRequest("GET", "/buckets/imbox?cursor=!!!!", nil)
	r.SetPathValue("bucket", "imbox")
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
	w := httptest.NewRecorder()
	p.app.handleBucket(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed cursor: status %d body %s", w.Code, w.Body.String())
	}
	// Valid base64, not a cursor: still a 400.
	r2 := httptest.NewRequest("GET", "/buckets/imbox?cursor=Y3Vyc29y", nil)
	r2.SetPathValue("bucket", "imbox")
	r2 = r2.WithContext(context.WithValue(r2.Context(), authContextKey{}, p.uid))
	w2 := httptest.NewRecorder()
	p.app.handleBucket(w2, r2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("non-cursor payload: status %d body %s", w2.Code, w2.Body.String())
	}
}

// TestIntegrationPaginationLimitBounds clamps rather than rejects: the
// client gets a usable page inside 1..200 either way.
func TestIntegrationPaginationLimitBounds(t *testing.T) {
	p := newProductPG(t)
	seedBuckets(t, p, 3)
	if page := fetchBucketPage(t, p, "?limit=0"); len(page.Rows) != 1 {
		t.Fatalf("limit=0 should clamp to 1, got %d rows", len(page.Rows))
	}
	if page := fetchBucketPage(t, p, "?limit=9999"); len(page.Rows) != 3 {
		t.Fatalf("limit=9999 should clamp to maxPageLimit, got %d rows", len(page.Rows))
	}
}
