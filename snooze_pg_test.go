package main

// Real-PostgreSQL coverage for the absolute-UTC snooze contract (audit
// DATA-07/DATA-05): the stored deadline is the caller's exact instant,
// until:null parks the thread as someday, undo restores the exact prior
// instant (microsecond equality, not a re-derived day count), and the
// deprecated relative path still applies. Same skip contract as
// product_pg_test.go.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/neutron-build/neutron/mail"
)

func seedSnoozeTarget(t *testing.T, p productPG) (string, string) {
	t.Helper()
	ctx := context.Background()
	acct := mail.AccountID("snooze-acct")
	if err := p.app.store.PutAccount(ctx, &mail.Account{
		ID: acct, Provider: mail.ProviderIMAP, Email: "snooze@example.com", Name: "Snoozer",
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
		VALUES ($1,'snooze-acct','imap','snooze@example.com',$2)`, p.uid, sealed); err != nil {
		t.Fatal(err)
	}
	id := mail.HeaderMessageID("<snooze-me@example.com>")
	if err := p.app.store.PutEnvelopes(ctx, acct, []mail.Envelope{{
		ID: id, ThreadID: "thread-snooze", MailboxIDs: []mail.MailboxID{"INBOX"},
		Subject: "snooze target", From: []mail.Address{{Email: "someone@example.com"}},
		ReceivedAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.ExecContext(ctx, `INSERT INTO hey_messages
		(user_id,account_id,message_id,bucket) VALUES ($1,'snooze-acct',$2,'imbox')`,
		p.uid, string(id)); err != nil {
		t.Fatal(err)
	}
	return string(id), "snooze-acct"
}

func snoozeState(t *testing.T, p productPG, messageID string) (string, sql.NullTime) {
	t.Helper()
	var bucket string
	var until sql.NullTime
	if err := p.db.QueryRow(
		`SELECT bucket, set_aside_until FROM hey_messages WHERE user_id=$1 AND message_id=$2`,
		p.uid, messageID).Scan(&bucket, &until); err != nil {
		t.Fatal(err)
	}
	return bucket, until
}

func actionRequest(t *testing.T, p productPG, messageID, body string) (int, map[string]any) {
	t.Helper()
	r := jsonBody(t, "POST", "/messages/"+messageID+"/action?account=snooze-acct", body)
	r.SetPathValue("message", messageID)
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
	w := httptest.NewRecorder()
	p.app.handleMessageAction(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// TestIntegrationSnoozeUntilAbsoluteAndUndoExactness: an absolute until
// is stored verbatim; moving away drops the date; "undo" (set_aside with
// the SAME instant) restores it exactly — no day-rounding drift.
func TestIntegrationSnoozeUntilAbsoluteAndUndoExactness(t *testing.T) {
	p := newProductPG(t)
	messageID, _ := seedSnoozeTarget(t, p)

	intended := time.Now().UTC().Add(36 * time.Hour).Truncate(time.Microsecond)
	code, out := actionRequest(t, p, messageID, `{"action":"set_aside","until":"`+intended.Format(time.RFC3339Nano)+`"}`)
	if code != http.StatusOK {
		t.Fatalf("absolute snooze: status %d", code)
	}
	if got := out["snooze_until"]; got == nil {
		t.Fatal("response does not echo the applied deadline")
	}
	bucket, until := snoozeState(t, p, messageID)
	if bucket != "set_aside" || !until.Valid || !until.Time.Equal(intended) {
		t.Fatalf("stored snooze = %s/%v, want set_aside/%s", bucket, until.Time, intended.Format(time.RFC3339Nano))
	}

	// The user files the thread elsewhere; the date drops.
	if code, _ := actionRequest(t, p, messageID, `{"action":"feed"}`); code != http.StatusOK {
		t.Fatalf("move: status %d", code)
	}
	if bucket, until := snoozeState(t, p, messageID); bucket != "feed" || until.Valid {
		t.Fatalf("after move = %s/%v, want feed/NULL", bucket, until.Time)
	}

	// Undo: restore the EXACT prior instant (what the dashboard's undo
	// snapshot now sends — the row's original snooze_until verbatim).
	if code, _ := actionRequest(t, p, messageID, `{"action":"set_aside","until":"`+intended.Format(time.RFC3339Nano)+`"}`); code != http.StatusOK {
		t.Fatalf("undo snooze: status %d", code)
	}
	bucket, until = snoozeState(t, p, messageID)
	if bucket != "set_aside" || !until.Time.Equal(intended) {
		t.Fatalf("undo restored %s/%v, want exact %s", bucket, until.Time, intended.Format(time.RFC3339Nano))
	}
}

// TestIntegrationSnoozeUntilNullIsSomeday: the explicit null parks the
// thread in the someday bucket with no return date.
func TestIntegrationSnoozeUntilNullIsSomeday(t *testing.T) {
	p := newProductPG(t)
	messageID, _ := seedSnoozeTarget(t, p)
	code, _ := actionRequest(t, p, messageID, `{"action":"set_aside","until":null}`)
	if code != http.StatusOK {
		t.Fatalf("until:null: status %d", code)
	}
	bucket, until := snoozeState(t, p, messageID)
	if bucket != "later" || until.Valid {
		t.Fatalf("until:null stored %s/%v, want later/NULL", bucket, until.Time)
	}
}

// TestIntegrationSnoozeValidationAndLegacyPath: garbage until is a 422
// (never silently applied), and the deprecated until_days form keeps
// working for un-migrated callers.
func TestIntegrationSnoozeValidationAndLegacyPath(t *testing.T) {
	p := newProductPG(t)
	messageID, _ := seedSnoozeTarget(t, p)
	if code, _ := actionRequest(t, p, messageID, `{"action":"set_aside","until":"next tuesday"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("garbage until: status %d, want 422", code)
	}
	before := time.Now().UTC()
	if code, _ := actionRequest(t, p, messageID, `{"action":"set_aside","until_days":7}`); code != http.StatusOK {
		t.Fatalf("legacy until_days: status %d", code)
	}
	bucket, until := snoozeState(t, p, messageID)
	if bucket != "set_aside" || !until.Valid {
		t.Fatalf("legacy snooze stored %s/%v", bucket, until.Time)
	}
	lo := before.Add(7*24*time.Hour - time.Minute)
	hi := before.Add(7*24*time.Hour + time.Minute)
	if until.Time.Before(lo) || until.Time.After(hi) {
		t.Fatalf("legacy deadline %v not within a minute of now+7d", until.Time)
	}
}
