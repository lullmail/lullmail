package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIntegrationBoardPinnedMutationSnapshot(t *testing.T) {
	p := newProductPG(t)
	messageID, account := seedSnoozeTarget(t, p)
	until := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Microsecond)
	if _, err := p.db.Exec(`UPDATE hey_messages SET bucket='set_aside', read_at=now(), set_aside_until=$3
		WHERE user_id=$1 AND message_id=$2`, p.uid, messageID, until); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(`INSERT INTO board_cards (user_id,account_id,thread_key,title,note)
		VALUES ($1,$2,'thread-snooze','Pinned','')`, p.uid, account); err != nil {
		t.Fatal(err)
	}
	readBoard := func() boardCard {
		r := jsonBody(t, http.MethodGet, "/api/board", "")
		r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, p.uid))
		w := httptest.NewRecorder()
		p.app.handleBoard(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("board status = %d body = %s", w.Code, w.Body.String())
		}
		var result struct {
			Needs []boardCard `json:"needs_you"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Needs) != 1 {
			t.Fatalf("cards = %d, want 1", len(result.Needs))
		}
		return result.Needs[0]
	}
	card := readBoard()
	if card.Read == nil || !*card.Read || card.Bucket != "set_aside" || card.SnoozeUntil != until.Format(time.RFC3339Nano) {
		t.Fatalf("snapshot = %+v, want read dated snooze at %s", card, until.Format(time.RFC3339Nano))
	}
	if _, err := p.db.Exec(`DELETE FROM hey_messages WHERE user_id=$1 AND message_id=$2`, p.uid, messageID); err != nil {
		t.Fatal(err)
	}
	card = readBoard()
	if card.Read != nil || card.Bucket != "" || card.SnoozeUntil != "" || card.MessageID != messageID {
		t.Fatalf("missing state was fabricated or the openable pin was lost: %+v", card)
	}
}
