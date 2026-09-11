package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	// zeroAffected makes an exec step report RowsAffected 0 (a conflicted
	// INSERT ... ON CONFLICT, say) instead of the default 1.
	zeroAffected bool
}

// queryLog is one recorded statement: the SQL text plus its arguments, so
// tests can assert how a handler threads parameters into its queries.
type queryLog struct {
	kind  string
	query string
	args  []driver.Value
}

type stepDriver struct {
	mu     sync.Mutex
	steps  []dbStep
	logged []queryLog
}

type stepConn struct{ driver *stepDriver }

func (d *stepDriver) Open(string) (driver.Conn, error) { return &stepConn{driver: d}, nil }

func (c *stepConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (c *stepConn) Close() error                        { return nil }
func (c *stepConn) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }

// CheckNamedValue lets non-default types (the spoke query's []string)
// through unchanged, so the recorder sees the arguments as written.
func (c *stepConn) CheckNamedValue(nv *driver.NamedValue) error {
	if _, ok := nv.Value.([]string); ok {
		return nil
	}
	_, err := driver.DefaultParameterConverter.ConvertValue(nv.Value)
	return err
}

func (d *stepDriver) record(kind, query string, args []driver.NamedValue) {
	values := make([]driver.Value, 0, len(args))
	for _, a := range args {
		values = append(values, a.Value)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.logged = append(d.logged, queryLog{kind: kind, query: query, args: values})
}

func (d *stepDriver) log() []queryLog {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]queryLog{}, d.logged...)
}

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

func (c *stepConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.driver.record("exec", query, args)
	step, err := c.next("exec")
	if err != nil || step.err != nil {
		return nil, firstError(err, step.err)
	}
	if step.zeroAffected {
		return driver.RowsAffected(0), nil
	}
	return driver.RowsAffected(1), nil
}

func (c *stepConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.driver.record("query", query, args)
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
	drv := &stepDriver{steps: steps}
	sql.Register(name, drv)
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { db.Close() })
	return db
}

// openRecordingDB is openStepDB for tests that also need the executed
// statements back.
func openRecordingDB(t *testing.T, steps ...dbStep) (*sql.DB, *stepDriver) {
	t.Helper()
	name := fmt.Sprintf("lullmail-step-%d", stepDriverID.Add(1))
	drv := &stepDriver{steps: steps}
	sql.Register(name, drv)
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { db.Close() })
	return db, drv
}

func emptyRows(columns ...string) driver.Rows { return &testRows{columns: columns} }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

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
