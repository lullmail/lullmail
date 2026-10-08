package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/neutron-build/neutron/mail"
)

func TestIntegrationThreadPagesAndTargetedBody(t *testing.T) {
	p := newProductPG(t)
	seedAccount(t, p, "page-acct", "page@example.test")
	ctx := context.Background()
	for i := 0; i < 55; i++ {
		id := mail.NativeMessageID(mail.ProviderIMAP, fmt.Sprintf("page-%03d", i))
		if err := p.app.store.PutEnvelopes(ctx, "page-acct", []mail.Envelope{{ID: id, ThreadID: "page-thread", Subject: "page", ReceivedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}); err != nil {
			t.Fatal(err)
		}
		if err := p.app.store.PutBody(ctx, "page-acct", &mail.Body{MessageID: id, Text: "cached private body", Parts: []mail.BodyPart{{PartID: "2", ContentID: "logo", Type: "image/png", Disposition: "inline"}}}); err != nil {
			t.Fatal(err)
		}
	}
	request := func(query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/threads/page-thread?account=page-acct&page=1"+query, nil)
		r.SetPathValue("thread", "page-thread")
		r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
		w := httptest.NewRecorder()
		p.app.handleThread(w, r)
		return w
	}
	var page struct {
		Rows    []struct{ ID, Body string }
		HasMore bool   `json:"has_more"`
		Cursor  string `json:"next_cursor"`
	}
	w := request("")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 50 || !page.HasMore || page.Cursor == "" || page.Rows[0].ID != "n:imap:page-005" || page.Rows[49].ID != "n:imap:page-054" {
		t.Fatalf("unexpected page: %+v", page)
	}
	for _, r := range page.Rows {
		if r.Body != "" {
			t.Fatal("envelope page included body")
		}
	}
	w = request("&cursor=" + page.Cursor)
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 5 || page.HasMore {
		t.Fatalf("unexpected tail: %+v", page)
	}
	if w := request("&cursor=broken"); w.Code != 400 {
		t.Fatalf("bad cursor: %d", w.Code)
	}
	r := httptest.NewRequest("GET", "/messages/n:imap:page-000/body?account=page-acct", nil)
	r.SetPathValue("message", "n:imap:page-000")
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
	w = httptest.NewRecorder()
	p.app.handleMessageBody(w, r)
	var body struct {
		Body   string
		Inline []inlinePart `json:"inline_parts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Body != "cached private body" || len(body.Inline) != 1 || body.Inline[0].ContentID != "logo" {
		t.Fatalf("body: %+v", body)
	}
	r = httptest.NewRequest("GET", "/messages/n:imap:page-000/body?account=other", nil)
	r.SetPathValue("message", "n:imap:page-000")
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
	w = httptest.NewRecorder()
	p.app.handleMessageBody(w, r)
	if w.Code != 404 {
		t.Fatalf("scope: %d", w.Code)
	}
}

func TestIntegrationMixedThreadUndoPreservesExactStates(t *testing.T) {
	p := newProductPG(t)
	msg, _ := seedSnoozeTarget(t, p)
	ctx := context.Background()
	other := mail.NativeMessageID(mail.ProviderIMAP, "mixed-other")
	if err := p.app.store.PutEnvelopes(ctx, "snooze-acct", []mail.Envelope{{ID: other, ThreadID: "thread-snooze"}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Microsecond)
	read := deadline.Add(-time.Hour)
	if _, err := p.db.ExecContext(ctx, `INSERT INTO hey_messages(user_id,account_id,message_id,bucket,read_at,set_aside_until) VALUES($1,'snooze-acct',$2,'set_aside',$3,$4)`, p.uid, string(other), read, deadline); err != nil {
		t.Fatal(err)
	}
	code, response := actionRequest(t, p, msg, `{"action":"feed"}`)
	if code != 200 {
		t.Fatalf("action: %d %+v", code, response)
	}
	raw, _ := json.Marshal(map[string]any{"action": "restore", "undo_token": response["undo_token"]})
	if code, _ = actionRequest(t, p, msg, string(raw)); code != 200 {
		t.Fatalf("undo: %d", code)
	}
	var bucket string
	var gotRead, gotUntil time.Time
	if err := p.db.QueryRowContext(ctx, `SELECT bucket,read_at,set_aside_until FROM hey_messages WHERE user_id=$1 AND account_id='snooze-acct' AND message_id=$2`, p.uid, string(other)).Scan(&bucket, &gotRead, &gotUntil); err != nil {
		t.Fatal(err)
	}
	if bucket != "set_aside" || !gotRead.Equal(read) || !gotUntil.Equal(deadline) {
		t.Fatalf("lost exact state: %s %s %s", bucket, gotRead, gotUntil)
	}
	code, response = actionRequest(t, p, msg, `{"action":"feed"}`)
	raw, _ = json.Marshal(map[string]any{"action": "restore", "undo_token": response["undo_token"]})
	if code, _ = actionRequest(t, p, msg, `{"action":"later"}`); code != 200 {
		t.Fatal(code)
	}
	if code, _ = actionRequest(t, p, msg, string(raw)); code != 409 {
		t.Fatalf("stale undo: %d", code)
	}
}

func TestIntegrationOwnerInitializationIsInstallationScoped(t *testing.T) {
	p := newProductPG(t)
	if _, err := p.db.Exec(`DELETE FROM users`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := p.app.ensureOwner(context.Background(), fmt.Sprint(i), fmt.Sprintf("owner-%d@example.test", i)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if err := p.app.ensureOwner(context.Background(), "changed configuration", "changed@example.test"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := p.db.QueryRow(`SELECT count(*) FROM users`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("owner count: %d", count)
	}

	if _, err := p.db.Exec(`UPDATE users SET webauthn_handle=NULL`); err != nil {
		t.Fatal(err)
	}
	if err := p.app.ensureOwner(context.Background(), "legacy", "legacy@example.test"); err != nil {
		t.Fatal(err)
	}
	var size int
	if err := p.db.QueryRow(`SELECT octet_length(webauthn_handle) FROM users`).Scan(&size); err != nil {
		t.Fatal(err)
	}
	if size != 32 {
		t.Fatalf("legacy handle length: %d", size)
	}
}

func TestIntegrationUndoSnapshotAboveSmallMutationBudget(t *testing.T) {
	p := newProductPG(t)
	msg, _ := seedSnoozeTarget(t, p)
	ctx := context.Background()
	envs := []mail.Envelope{}
	for i := 0; i < 500; i++ {
		envs = append(envs, mail.Envelope{ID: mail.NativeMessageID(mail.ProviderIMAP, fmt.Sprintf("large-undo-snapshot-%04d", i)), ThreadID: "thread-snooze"})
	}
	if err := p.app.store.PutEnvelopes(ctx, "snooze-acct", envs); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.ExecContext(ctx, `INSERT INTO hey_messages(user_id,account_id,message_id,bucket) SELECT $1,account_id,id,'feed' FROM mail_messages WHERE account_id='snooze-acct' AND id<>$2`, p.uid, msg); err != nil {
		t.Fatal(err)
	}
	code, response := actionRequest(t, p, msg, `{"action":"read"}`)
	if code != 200 {
		t.Fatalf("action: %d %+v", code, response)
	}
	raw, _ := json.Marshal(map[string]any{"action": "restore", "undo_token": response["undo_token"]})
	snapshot, _ := json.Marshal(response["undo"])
	if len(snapshot) <= 64<<10 {
		t.Fatalf("fixture too small: %d", len(raw))
	}
	if code, response = actionRequest(t, p, msg, string(raw)); code != 200 {
		t.Fatalf("restore: %d %+v", code, response)
	}
}
