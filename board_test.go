package main

import (
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
		steps = append(steps, dbStep{kind: "query", rows: emptyRows("account", "thread", "message", "subject", "from", "received", "preview")})
	}
	return append(steps,
		dbStep{kind: "query", rows: emptyRows("id", "title", "note")},
		dbStep{kind: "query", rows: emptyRows("id", "account", "thread", "title", "note")},
	)
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
