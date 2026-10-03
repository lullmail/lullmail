package main

// Crash-safety harness for the durable outbox. Every "process" is a separate
// App with its own connection pool against one real PostgreSQL database; a
// kill closes that pool and cancels its context, so nothing the dead process
// would have written afterwards can land. Provider traffic runs over real
// sockets to internal/faketransport, so the product's real SMTP and IMAP
// clients reach every protocol step. Invariants asserted throughout:
//
//	(i)   a send the user was told is accepted is never lost
//	(ii)  it is never delivered twice without an explicit user action
//	(iii) an uncertain outcome ends ambiguous, payload retained, recoverable

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lullmail/internal/faketransport"
)

type outboxEnv struct {
	t         *testing.T
	p         productPG
	smtp      *faketransport.SMTP
	imap      *faketransport.IMAP
	mirror    string
	publicID  string
	processes []*proc
}

func newOutboxEnv(t *testing.T) *outboxEnv {
	t.Helper()
	p := newProductPG(t)
	smtp, err := faketransport.NewSMTP()
	if err != nil {
		t.Fatal(err)
	}
	imap, err := faketransport.NewIMAP()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(smtp.Close)
	t.Cleanup(imap.Close)
	cred, err := sealSecret(p.cfg, "app-password")
	if err != nil {
		t.Fatal(err)
	}
	e := &outboxEnv{t: t, p: p, smtp: smtp, imap: imap, mirror: "harness-acct"}
	if err := p.db.QueryRow(`INSERT INTO email_accounts(user_id,mirror_account_id,provider,address,username,host,port,smtp_host,smtp_port,cred_ciphertext,sync_enabled)
 VALUES($1,$2,'imap','sender@example.test','','127.0.0.1',$3,'127.0.0.1',$4,$5,false) RETURNING id::text`, p.uid, e.mirror, imap.Port(), smtp.Port(), cred).Scan(&e.publicID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(`INSERT INTO mail_mailboxes(account_id,id,name,role,native) VALUES($1,'Sent','Sent','sent','Sent')`, e.mirror); err != nil {
		t.Fatal(err)
	}
	return e
}

// proc is one application process.
type proc struct {
	env    *outboxEnv
	app    *App
	db     *sql.DB
	ctx    context.Context
	cancel context.CancelFunc
	dead   atomic.Bool
}

type procKilled struct{}

func (e *outboxEnv) newProc() *proc {
	e.t.Helper()
	db, err := sql.Open("pgx", testDatabaseURL(e.t))
	if err != nil {
		e.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	pr := &proc{env: e, db: db, ctx: ctx, cancel: cancel}
	pr.app = &App{cfg: e.p.cfg, db: db, log: discardLogger(), authAttempts: map[string]authAttempt{}, pwFails: map[string]passwordFails{}}
	e.t.Cleanup(func() { cancel(); db.Close() })
	e.processes = append(e.processes, pr)
	return pr
}

// kill is SIGKILL: no further statement from this process reaches the database
// and its sockets close. Safe to call from any goroutine, any number of times.
func (pr *proc) kill() {
	if pr.dead.Swap(true) {
		return
	}
	pr.cancel()
	pr.db.Close()
}

// die kills the process and unwinds the calling goroutine.
func (pr *proc) die() {
	pr.kill()
	panic(procKilled{})
}

// run executes fn and reports whether the process died inside it.
func (pr *proc) run(fn func()) (killed bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(procKilled); !ok {
				panic(r)
			}
			killed = true
		}
	}()
	fn()
	return false
}

// killAt arranges for the process to die at a named outbox boundary.
func (pr *proc) killAt(point string) {
	pr.app.outboxFault = func(p string) error {
		if p == point {
			pr.die()
		}
		return nil
	}
}

// failAt makes a boundary report an error (a lost acknowledgment, say).
func (pr *proc) failAt(point string, err error) {
	pr.app.outboxFault = func(p string) error {
		if p == point {
			return err
		}
		return nil
	}
}

// dropAndKill makes a fake-server step kill the dying client's process and
// drop the connection, as the kernel would on SIGKILL.
func (pr *proc) dropAndKill(step string) faketransport.Hook {
	return func(s string) faketransport.Action {
		if s == step {
			pr.kill()
			return faketransport.Drop
		}
		return faketransport.Continue
	}
}

type sendResult struct {
	Code   int
	Body   string
	ID     string
	Status string
	Replay bool
}

// send submits one composition through the real /send handler.
func (pr *proc) send(key, subject string) sendResult {
	return pr.sendCtx(context.Background(), key, subject, subject)
}

func (pr *proc) sendCtx(ctx context.Context, key, subject, text string) sendResult {
	body, _ := json.Marshal(map[string]any{"to": "friend@example.test", "subject": subject, "text": text, "account_id": pr.env.publicID})
	r := httptest.NewRequest(http.MethodPost, "/api/send", bytes.NewReader(body)).WithContext(context.WithValue(ctx, authContextKey{}, pr.env.p.uid))
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	pr.app.handleSend(w, r)
	res := sendResult{Code: w.Code, Body: w.Body.String(), Replay: w.Header().Get("X-Idempotent-Replay") == "true"}
	var parsed struct {
		Queued string `json:"queued"`
		Status string `json:"status"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &parsed)
	res.ID, res.Status = parsed.Queued, parsed.Status
	return res
}

func (pr *proc) cancelSend(id string) int {
	r := httptest.NewRequest(http.MethodDelete, "/api/outbox/"+id, nil)
	r.SetPathValue("id", id)
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, pr.env.p.uid))
	w := httptest.NewRecorder()
	pr.app.cancelOutbox(w, r)
	return w.Code
}

func (pr *proc) detail(id string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/api/outbox/"+id, nil)
	r.SetPathValue("id", id)
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, pr.env.p.uid))
	w := httptest.NewRecorder()
	pr.app.handleOutboxDetail(w, r)
	return w
}

func (pr *proc) discard(id string) int {
	r := httptest.NewRequest(http.MethodDelete, "/api/outbox/"+id+"/payload", nil)
	r.SetPathValue("id", id)
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, pr.env.p.uid))
	w := httptest.NewRecorder()
	pr.app.handleOutboxDiscard(w, r)
	return w.Code
}

// pass is one worker iteration: sweep, claim and deliver one, file one.
func (pr *proc) pass() (killed bool) {
	return pr.run(func() { _ = pr.app.processOutbox(pr.ctx) })
}

type jobView struct {
	State, Filing, Code string
	Payload, Sent       bool
	Exists              bool
}

func (e *outboxEnv) job(id string) jobView {
	e.t.Helper()
	var v jobView
	err := e.p.db.QueryRow(`SELECT state,filing_state,error_code,payload_ciphertext<>'',sent_ciphertext<>'' FROM outbox_jobs WHERE id::text=$1`, id).Scan(&v.State, &v.Filing, &v.Code, &v.Payload, &v.Sent)
	if err == sql.ErrNoRows {
		return v
	}
	if err != nil {
		e.t.Fatal(err)
	}
	v.Exists = true
	return v
}

func (e *outboxEnv) rows(where string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.p.db.QueryRow(`SELECT count(*) FROM outbox_jobs WHERE `+where, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// makeDue ends every undo window now.
func (e *outboxEnv) makeDue() {
	e.t.Helper()
	if _, err := e.p.db.Exec(`UPDATE outbox_jobs SET undo_until=now()-interval '1 second' WHERE state='pending'`); err != nil {
		e.t.Fatal(err)
	}
}

// ageClaims makes every in-flight claim and filing older than the timeout, as
// if the process that held them had died minutes ago.
func (e *outboxEnv) ageClaims() {
	e.t.Helper()
	if _, err := e.p.db.Exec(`UPDATE outbox_jobs SET started_at=now()-interval '10 minutes' WHERE state='submitting'`); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.p.db.Exec(`UPDATE outbox_jobs SET updated_at=now()-interval '10 minutes' WHERE filing_state='submitting'`); err != nil {
		e.t.Fatal(err)
	}
}

func (e *outboxEnv) delivered(marker string) int { return e.smtp.CountContaining(marker) }
func (e *outboxEnv) filed(marker string) int     { return e.imap.CountContaining(marker) }

// recoverable asserts the visible recovery path for a stopped entry: the
// owner can open the saved composition (or, for a submitted message whose
// Sent filing is unconfirmed, download the saved copy).
func (e *outboxEnv) assertRecoverable(pr *proc, id string) {
	e.t.Helper()
	v := e.job(id)
	switch {
	case v.State == "submitted" && v.Filing == "ambiguous":
		if !v.Sent {
			e.t.Fatalf("submitted message with unconfirmed filing lost its saved copy: %+v", v)
		}
		r := httptest.NewRequest(http.MethodGet, "/api/outbox/"+id+"?format=eml", nil)
		r.SetPathValue("id", id)
		r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, e.p.uid))
		w := httptest.NewRecorder()
		pr.app.handleOutboxDetail(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "Subject:") {
			e.t.Fatalf("saved Sent copy not downloadable: %d %.100s", w.Code, w.Body.String())
		}
	default:
		if !v.Payload {
			e.t.Fatalf("%s entry has no retained composition: %+v", v.State, v)
		}
		if w := pr.detail(id); w.Code != 200 || !strings.Contains(w.Body.String(), `"request"`) {
			e.t.Fatalf("saved composition not readable: %d %.200s", w.Code, w.Body.String())
		}
	}
}

// settle runs recovery as a fresh process would after a crash: aged claims,
// then several worker passes. It returns the process used.
func (e *outboxEnv) settle(passes int) *proc {
	e.t.Helper()
	e.ageClaims()
	pr := e.newProc()
	for i := 0; i < passes; i++ {
		if pr.pass() {
			e.t.Fatal("recovery process died")
		}
	}
	return pr
}

func marker(name string) string {
	return "Harness-" + strings.NewReplacer(" ", "-", "/", "-", ":", "-").Replace(name)
}

func mustSend(t *testing.T, pr *proc, key, subject string) string {
	t.Helper()
	r := pr.send(key, subject)
	if r.Code != 200 || r.ID == "" {
		t.Fatalf("send=%d %s", r.Code, r.Body)
	}
	return r.ID
}

func fmtState(v jobView) string {
	return fmt.Sprintf("state=%s filing=%s code=%s payload=%v sent=%v", v.State, v.Filing, v.Code, v.Payload, v.Sent)
}

var _ = time.Second

func contextWithOwner(ctx context.Context, uid string) context.Context {
	return context.WithValue(ctx, authContextKey{}, uid)
}
