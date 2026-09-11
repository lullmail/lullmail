package main

import (
	"archive/zip"
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPersonalExportKeepsBoardAccountIdentity(t *testing.T) {
	created := time.Date(2026, time.September, 11, 9, 0, 0, 0, time.UTC)
	a := &App{
		log: discardLogger(),
		db: openStepDB(t,
			dbStep{kind: "query", rows: emptyRows("id", "x", "y", "text", "color", "created_at", "updated_at")},
			dbStep{kind: "query", rows: &testRows{
				columns: []string{"id", "account_id", "thread_key", "title", "note", "done_at", "created_at"},
				values: [][]driver.Value{
					{"card-1", "mirror-a", "thread-shared", "Pinned on A", "note", nil, created},
					{"card-2", "mirror-b", "thread-shared", "Pinned on B", "", nil, created},
					{"card-3", "", "", "Manual", "", nil, created},
				},
			}},
		),
	}
	w := httptest.NewRecorder()
	a.handlePersonalExport(w, requestAsOwner(http.MethodGet, "/api/personal/export"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatalf("response is not a readable zip: %v", err)
	}
	var cards []map[string]any
	for _, f := range zr.File {
		if f.Name != "board.json" {
			continue
		}
		rc, _ := f.Open()
		raw, _ := io.ReadAll(rc)
		rc.Close()
		if err := json.Unmarshal(raw, &cards); err != nil {
			t.Fatalf("board.json is not JSON: %v", err)
		}
	}
	if len(cards) != 3 {
		t.Fatalf("cards = %d, want 3", len(cards))
	}
	byTitle := map[string]map[string]any{}
	for _, c := range cards {
		byTitle[c["title"].(string)] = c
	}
	// The same thread id on two accounts must stay distinguishable after
	// export: the pair (account_id, thread_id), not the thread alone.
	if byTitle["Pinned on A"]["account_id"] != "mirror-a" || byTitle["Pinned on B"]["account_id"] != "mirror-b" {
		t.Fatalf("account identity lost: A=%v B=%v",
			byTitle["Pinned on A"]["account_id"], byTitle["Pinned on B"]["account_id"])
	}
	if _, has := byTitle["Manual"]["account_id"]; has {
		t.Fatal("manual card carries an account id")
	}
}

// An entry that cannot be written must surface as an error the handler can
// answer 500 with — not be swallowed by a zip writer that reports it only
// at Close, past the point where a 200 could still be avoided.
func TestWriteZipEntriesReportsEntryWriteFailure(t *testing.T) {
	// The writer records its first failure and every later entry
	// operation returns it, so a poisoned writer fails inside the entry
	// loop — the branch the handler must answer 500 for, not swallow.
	zw := zip.NewWriter(errWriter{})
	if err := zw.Close(); err == nil {
		t.Fatal("fixture: sink failure did not surface at Close")
	}
	if err := writeZipEntries(zw, map[string][]byte{"board.json": []byte("[]")}); err == nil {
		t.Fatal("entry write failure was not reported")
	}
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
