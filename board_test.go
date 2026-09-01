package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
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
