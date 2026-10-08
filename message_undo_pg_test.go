package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/neutron-build/neutron/mail"
)

func undoRequestBody(t *testing.T, response map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"action": "restore", "undo_token": response["undo_token"]})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestIntegrationUndoRejectsABAConsumedAuthorityAndUnclassifiedArrival(t *testing.T) {
	p := newProductPG(t)
	msg, _ := seedSnoozeTarget(t, p)
	code, first := actionRequest(t, p, msg, `{"action":"feed"}`)
	if code != 200 {
		t.Fatal(code, first)
	}
	stale := undoRequestBody(t, first)
	for _, action := range []string{"later", "feed"} {
		if code, _ := actionRequest(t, p, msg, fmt.Sprintf(`{"action":%q}`, action)); code != 200 {
			t.Fatal(code)
		}
	}
	if code, _ := actionRequest(t, p, msg, stale); code != 409 {
		t.Fatalf("ABA undo=%d", code)
	}
	code, latest := actionRequest(t, p, msg, `{"action":"imbox"}`)
	if code != 200 {
		t.Fatal(code)
	}
	consumed := undoRequestBody(t, latest)
	if code, _ := actionRequest(t, p, msg, consumed); code != 200 {
		t.Fatal(code)
	}
	if code, _ := actionRequest(t, p, msg, `{"action":"later"}`); code != 200 {
		t.Fatal(code)
	}
	if code, _ := actionRequest(t, p, msg, consumed); code != 409 {
		t.Fatalf("reused token=%d", code)
	}
	code, latest = actionRequest(t, p, msg, `{"action":"feed"}`)
	if code != 200 {
		t.Fatal(code)
	}
	arrival := mail.NativeMessageID(mail.ProviderIMAP, "new-unclassified-member")
	if err := p.app.store.PutEnvelopes(context.Background(), "snooze-acct", []mail.Envelope{{ID: arrival, ThreadID: "thread-snooze"}}); err != nil {
		t.Fatal(err)
	}
	if code, _ := actionRequest(t, p, msg, undoRequestBody(t, latest)); code != 409 {
		t.Fatalf("membership undo=%d", code)
	}
}

func TestIntegrationUndoOneConflictingRowRollsBackAndDirectWriterFences(t *testing.T) {
	p := newProductPG(t)
	msg, _ := seedSnoozeTarget(t, p)
	ctx := context.Background()
	other := mail.NativeMessageID(mail.ProviderIMAP, "zz-conflicting-last-row")
	if err := p.app.store.PutEnvelopes(ctx, "snooze-acct", []mail.Envelope{{ID: other, ThreadID: "thread-snooze"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(`INSERT INTO hey_messages(user_id,account_id,message_id,bucket) VALUES($1,'snooze-acct',$2,'later')`, p.uid, string(other)); err != nil {
		t.Fatal(err)
	}
	code, response := actionRequest(t, p, msg, `{"action":"feed"}`)
	if code != 200 {
		t.Fatal(code)
	}
	// A same-value update from any writer still represents intervening intent.
	if _, err := p.db.Exec(`UPDATE hey_messages SET bucket='feed' WHERE account_id='snooze-acct' AND message_id=$1`, string(other)); err != nil {
		t.Fatal(err)
	}
	if code, _ := actionRequest(t, p, msg, undoRequestBody(t, response)); code != 409 {
		t.Fatal(code)
	}
	var count int
	if err := p.db.QueryRow(`SELECT count(*) FROM hey_messages WHERE account_id='snooze-acct' AND bucket='feed'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("partial restore: %d %v", count, err)
	}
	var consumed bool
	if err := p.db.QueryRow(`SELECT consumed_at IS NOT NULL FROM message_action_undo WHERE token=$1`, response["undo_token"]).Scan(&consumed); err != nil || consumed {
		t.Fatalf("failed restore consumed authority: %v %v", consumed, err)
	}
	// Client replacement state is never accepted, even with an otherwise valid token.
	if code, _ := actionRequest(t, p, msg, `{"action":"restore","restore":[{"id":"forged"}]}`); code != 422 {
		t.Fatal(code)
	}
}

func TestIntegrationUndoActionBoundAndExpired(t *testing.T) {
	p := newProductPG(t)
	msg, _ := seedSnoozeTarget(t, p)
	code, response := actionRequest(t, p, msg, `{"action":"feed"}`)
	if code != 200 {
		t.Fatal(code)
	}
	token := response["undo_token"]
	// A token must name its original representative and thread, not just the owner.
	if _, err := p.db.Exec(`UPDATE message_action_undo SET message_id='other' WHERE token=$1`, token); err != nil {
		t.Fatal(err)
	}
	if code, _ := actionRequest(t, p, msg, undoRequestBody(t, response)); code != 409 {
		t.Fatal(code)
	}
	if _, err := p.db.Exec(`UPDATE message_action_undo SET message_id=$2,expires_at=now()-interval '1 second' WHERE token=$1`, token, msg); err != nil {
		t.Fatal(err)
	}
	if code, _ := actionRequest(t, p, msg, undoRequestBody(t, response)); code != 409 {
		t.Fatal(code)
	}
}

func TestIntegrationUndoMessageCountBoundaries(t *testing.T) {
	p := newProductPG(t)
	msg, _ := seedSnoozeTarget(t, p)
	ctx := context.Background()
	envs := make([]mail.Envelope, 1999)
	for i := range envs {
		envs[i] = mail.Envelope{ID: mail.NativeMessageID(mail.ProviderIMAP, fmt.Sprintf("bounded-%04d", i)), ThreadID: "thread-snooze"}
	}
	if err := p.app.store.PutEnvelopes(ctx, "snooze-acct", envs); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(`INSERT INTO hey_messages(user_id,account_id,message_id,bucket) SELECT $1,account_id,id,'later' FROM mail_messages WHERE account_id='snooze-acct' AND id<>$2`, p.uid, msg); err != nil {
		t.Fatal(err)
	}
	if code, response := actionRequest(t, p, msg, `{"action":"feed"}`); code != 200 {
		t.Fatal(code, response)
	}
	if err := p.app.store.PutEnvelopes(ctx, "snooze-acct", []mail.Envelope{{ID: mail.NativeMessageID(mail.ProviderIMAP, "bounded-over"), ThreadID: "thread-snooze"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(`INSERT INTO hey_messages(user_id,account_id,message_id,bucket) VALUES($1,'snooze-acct','n:imap:bounded-over','later')`, p.uid); err != nil {
		t.Fatal(err)
	}
	if code, _ := actionRequest(t, p, msg, `{"action":"imbox"}`); code != 422 {
		t.Fatalf("2001 action=%d", code)
	}
	var count int
	p.db.QueryRow(`SELECT count(*) FROM hey_messages WHERE account_id='snooze-acct' AND bucket='imbox'`).Scan(&count)
	if count != 0 {
		t.Fatalf("oversized action changed %d rows", count)
	}
}

func TestUndoSnapshotByteBoundaries(t *testing.T) {
	state := []messageUndo{{ID: "x", Before: messageState{Bucket: "imbox"}, After: messageState{Bucket: "feed"}}}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	base := len(raw) - 1
	for _, delta := range []int{-1, 0, 1} {
		state[0].ID = strings.Repeat("x", int(idempotencyBodyLimit)-256-base+delta)
		encoded, _ := json.Marshal(state)
		if len(encoded) != int(idempotencyBodyLimit)-256+delta {
			t.Fatal("fixture boundary mismatch")
		}
		_, err := undoSnapshotBytes(state)
		if (err != nil) != (delta > 0) {
			t.Fatalf("boundary %d: %v", delta, err)
		}
	}
}
