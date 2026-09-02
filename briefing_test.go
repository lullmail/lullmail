package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

type dbStep struct {
	kind string
	rows driver.Rows
	err  error
}

type stepDriver struct {
	mu    sync.Mutex
	steps []dbStep
}

type stepConn struct{ driver *stepDriver }

func (d *stepDriver) Open(string) (driver.Conn, error) { return &stepConn{driver: d}, nil }

func (c *stepConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (c *stepConn) Close() error                        { return nil }
func (c *stepConn) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }

func (c *stepConn) next(kind string) (dbStep, error) {
	c.driver.mu.Lock()
	defer c.driver.mu.Unlock()
	if len(c.driver.steps) == 0 {
		return dbStep{}, fmt.Errorf("unexpected %s", kind)
	}
	step := c.driver.steps[0]
	c.driver.steps = c.driver.steps[1:]
	if step.kind != kind {
		return dbStep{}, fmt.Errorf("got %s, want %s", kind, step.kind)
	}
	return step, nil
}

func (c *stepConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	step, err := c.next("exec")
	if err != nil || step.err != nil {
		return nil, firstError(err, step.err)
	}
	return driver.RowsAffected(1), nil
}

func (c *stepConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	step, err := c.next("query")
	if err != nil || step.err != nil {
		return nil, firstError(err, step.err)
	}
	return step.rows, nil
}

func firstError(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

type testRows struct {
	columns     []string
	values      [][]driver.Value
	terminalErr error
	index       int
}

func (r *testRows) Columns() []string { return r.columns }
func (r *testRows) Close() error      { return nil }
func (r *testRows) Next(dest []driver.Value) error {
	if r.index < len(r.values) {
		copy(dest, r.values[r.index])
		r.index++
		return nil
	}
	if r.terminalErr != nil {
		err := r.terminalErr
		r.terminalErr = nil
		return err
	}
	return io.EOF
}

var stepDriverID atomic.Uint64

func openStepDB(t *testing.T, steps ...dbStep) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("lullmail-step-%d", stepDriverID.Add(1))
	sql.Register(name, &stepDriver{steps: steps})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { db.Close() })
	return db
}

func emptyRows(columns ...string) driver.Rows { return &testRows{columns: columns} }

func requestAsOwner(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	return r.WithContext(context.WithValue(r.Context(), authContextKey{}, "owner-1"))
}

func TestBriefingReturns500OnDatabaseErrors(t *testing.T) {
	dbErr := errors.New("database unavailable")
	tests := []struct {
		name  string
		steps []dbStep
	}{
		{
			name: "query",
			steps: []dbStep{
				{kind: "exec"},
				{kind: "query", err: dbErr},
			},
		},
		{
			name: "scan",
			steps: []dbStep{
				{kind: "exec"},
				{kind: "query", rows: emptyRows("address")},
				{kind: "query", rows: &testRows{columns: []string{"only"}, values: [][]driver.Value{{"bad-row"}}}},
				{kind: "query", rows: emptyRows("account", "thread", "mine", "theirs")},
				{kind: "query", rows: emptyRows("account", "message", "read")},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := &App{db: openStepDB(t, tc.steps...)}
			w := httptest.NewRecorder()
			a.handleBriefing(w, requestAsOwner(http.MethodGet, "/api/briefing"))
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500; body=%s", w.Code, w.Body.String())
			}
		})
	}
}
