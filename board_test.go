package main

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBoardReturns500OnRowsError(t *testing.T) {
	dbErr := errors.New("rows failed")
	a := &App{db: openStepDB(t,
		dbStep{kind: "exec"},
		dbStep{kind: "query", rows: emptyRows("address")},
		dbStep{kind: "query", rows: emptyRows("account", "thread", "message", "subject", "from", "received", "preview")},
		dbStep{kind: "query", rows: emptyRows("account", "thread", "mine", "theirs")},
		dbStep{kind: "query", rows: emptyRows("account", "message", "read")},
		dbStep{kind: "query", rows: &testRows{columns: []string{"id", "account", "thread", "title", "note"}, terminalErr: dbErr}},
	)}
	w := httptest.NewRecorder()
	a.handleBoard(w, requestAsOwner(http.MethodGet, "/api/board"))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", w.Code, w.Body.String())
	}
}

// boardSteps is the statement sequence handleBoard runs when the derived
// lists and the manual-notes/done piles are empty: sweep, four briefing
// queries, pinned cards, (live lookup only when a pin is open), notes, done.
func boardSteps(openPin bool) []dbStep {
	pinned := dbStep{kind: "query", rows: emptyRows("id", "account", "thread", "title", "note")}
	if openPin {
		pinned.rows = &testRows{
			columns: []string{"id", "account", "thread", "title", "note"},
			values:  [][]driver.Value{{"card-1", "acct-a", "thread-1", "Title", "Note"}},
		}
	}
	steps := []dbStep{
		{kind: "exec"},
		{kind: "query", rows: emptyRows("address")},
		{kind: "query", rows: emptyRows("account", "thread", "message", "subject", "from", "received", "preview")},
		{kind: "query", rows: emptyRows("account", "thread", "mine", "theirs")},
		{kind: "query", rows: emptyRows("account", "message", "read")},
		pinned,
	}
	if openPin {
		steps = append(steps, dbStep{kind: "query", rows: emptyRows("account", "thread", "message", "subject", "from", "received", "preview", "read", "bucket", "until")})
	}
	return append(steps,
		dbStep{kind: "query", rows: emptyRows("id", "title", "note")},
		dbStep{kind: "query", rows: emptyRows("id", "account", "thread", "title", "note")},
	)
}

func TestBoardPinnedMutationSnapshot(t *testing.T) {
	until := time.Date(2027, 1, 15, 9, 45, 0, 123456000, time.UTC)
	for _, tc := range []struct {
		name   string
		read   driver.Value
		bucket string
		until  driver.Value
	}{
		{"read feed", true, "feed", nil},
		{"unread receipt", false, "paper_trail", nil},
		{"dated snooze", true, "set_aside", until},
		{"missing product state", nil, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := boardSteps(true)
			steps[6].rows = &testRows{
				columns: []string{"account", "thread", "message", "subject", "from", "received", "preview", "read", "bucket", "until"},
				values:  [][]driver.Value{{"acct-a", "thread-1", "message-1", "Title", `[{"email":"sender@example.test"}]`, until, "Preview", tc.read, tc.bucket, tc.until}},
			}
			db, drv := openRecordingDB(t, steps...)
			a := &App{db: db}
			w := httptest.NewRecorder()
			a.handleBoard(w, requestAsOwner(http.MethodGet, "/api/board"))
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
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
			card := result.Needs[0]
			if card.CardID != "card-1" || card.Bucket != tc.bucket {
				t.Fatalf("card = %+v", card)
			}
			if tc.read == nil {
				if card.Read != nil || strings.Contains(w.Body.String(), `"read":`) {
					t.Fatalf("missing state became a known read state: %s", w.Body.String())
				}
			} else if card.Read == nil || *card.Read != tc.read.(bool) {
				t.Fatalf("read = %v, want %v", card.Read, tc.read)
			}
			if tc.until != nil && card.SnoozeUntil != until.Format(time.RFC3339Nano) {
				t.Fatalf("deadline = %q, want exact %q", card.SnoozeUntil, until.Format(time.RFC3339Nano))
			}
			query := drv.log()[6].query
			for _, condition := range []string{"h.account_id = m.account_id", "h.message_id = m.id", "h.user_id = $1", "CASE WHEN h.message_id IS NULL THEN NULL", "m.id DESC"} {
				if !strings.Contains(query, condition) {
					t.Errorf("live state query missing %q: %s", condition, query)
				}
			}
		})
	}
}

func TestBoardDerivedCardCarriesKnownUnreadInboxState(t *testing.T) {
	steps := boardSteps(false)
	steps[2].rows = &testRows{
		columns: []string{"account", "thread", "message", "subject", "from", "received", "preview"},
		values:  [][]driver.Value{{"acct-a", "thread-1", "message-1", "Title", `[{"email":"sender@example.test"}]`, nil, "Preview"}},
	}
	a := &App{db: openStepDB(t, steps...)}
	w := httptest.NewRecorder()
	a.handleBoard(w, requestAsOwner(http.MethodGet, "/api/board"))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"read":false`) || !strings.Contains(w.Body.String(), `"bucket":"imbox"`) {
		t.Fatalf("derived state: status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestBoardScopesCardQueriesToTheSelectedAccount(t *testing.T) {
	db, drv := openRecordingDB(t, boardSteps(true)...)
	a := &App{db: db}
	w := httptest.NewRecorder()
	a.handleBoard(w, requestAsOwner(http.MethodGet, "/api/board?account=acct-a"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	logged := drv.log()
	if len(logged) != 9 {
		t.Fatalf("statements = %d, want 9", len(logged))
	}
	const subquery = "SELECT mirror_account_id FROM email_accounts"
	for _, i := range []int{0, 5, 8} { // sweep, pinned, done
		entry := logged[i]
		if !strings.Contains(entry.query, subquery) {
			t.Errorf("statement %d is not scoped by account:\n%s", i, entry.query)
		}
		if len(entry.args) != 2 || entry.args[1] != "acct-a" {
			t.Errorf("statement %d args = %v, want the account threaded through", i, entry.args)
		}
	}
	if !strings.Contains(logged[6].query, "ea.id = $2") { // live lookup
		t.Errorf("live query is not scoped by account:\n%s", logged[6].query)
	}
	if len(logged[6].args) != 2 || logged[6].args[1] != "acct-a" {
		t.Errorf("live query args = %v, want the account threaded through", logged[6].args)
	}
}

func TestBoardWithoutAccountLensRunsUnscoped(t *testing.T) {
	db, drv := openRecordingDB(t, boardSteps(false)...)
	a := &App{db: db}
	w := httptest.NewRecorder()
	a.handleBoard(w, requestAsOwner(http.MethodGet, "/api/board"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	logged := drv.log()
	if len(logged) != 8 {
		t.Fatalf("statements = %d, want 8", len(logged))
	}
	for i, entry := range logged {
		if strings.Contains(entry.query, "SELECT mirror_account_id FROM email_accounts") || strings.Contains(entry.query, "ea.id = $2") {
			t.Errorf("statement %d is scoped without an account lens:\n%s", i, entry.query)
		}
	}
}

// Pinning must distinguish a card it created from one that already existed:
// the undo of a pin may only delete the former, and a re-pin must leave the
// existing card's state alone (audit 4 F20).
func TestBoardPinReportsCreatedAndLeavesExistingCardsAlone(t *testing.T) {
	pinSteps := func(insertSucceeds bool) []dbStep {
		insert := dbStep{kind: "query", rows: &testRows{
			columns: []string{"id"},
			values:  [][]driver.Value{{"card-new"}},
		}}
		if !insertSucceeds {
			// INSERT ... DO NOTHING RETURNING: no row comes back.
			insert = dbStep{kind: "query", rows: emptyRows("id")}
		}
		steps := []dbStep{
			dbStep{kind: "query", rows: &testRows{
				columns: []string{"account_id", "subject"},
				values:  [][]driver.Value{{"mirror-1", "The thread"}},
			}},
			insert,
		}
		if !insertSucceeds {
			steps = append(steps, dbStep{kind: "query", rows: &testRows{
				columns: []string{"id"},
				values:  [][]driver.Value{{"card-existing"}},
			}})
		}
		return steps
	}

	pinRequest := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/board/pin", strings.NewReader(`{"account":"mirror-1","thread_id":"thread-1"}`))
		return r.WithContext(context.WithValue(r.Context(), authContextKey{}, "owner-1"))
	}

	a := &App{log: discardLogger(), db: openStepDB(t, pinSteps(true)...)}
	w := httptest.NewRecorder()
	a.handleBoardPin(w, pinRequest())
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"created":true`) {
		t.Fatalf("new pin: status = %d body = %s", w.Code, w.Body.String())
	}

	a = &App{log: discardLogger(), db: openStepDB(t, pinSteps(false)...)}
	w = httptest.NewRecorder()
	a.handleBoardPin(w, pinRequest())
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"created":true`) {
		t.Fatalf("existing pin: status = %d body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "card-existing") {
		t.Fatalf("existing pin did not report the standing card id: %s", w.Body.String())
	}
}
