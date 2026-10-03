package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/neutron-build/neutron/mail"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Scripted transactions exercise admission, hashing, and failure responses.
// They do not validate PostgreSQL semantics; outbox_pg_test.go does that.
type outboxStepDriver struct {
	*stepDriver
	commitErr error
}
type outboxStepConn struct {
	*stepConn
	commitErr error
}
type outboxStepTx struct{ commitErr error }

func (d *outboxStepDriver) Open(string) (driver.Conn, error) {
	return &outboxStepConn{&stepConn{d.stepDriver}, d.commitErr}, nil
}
func (c *outboxStepConn) Begin() (driver.Tx, error) { return &outboxStepTx{c.commitErr}, nil }
func (t *outboxStepTx) Commit() error               { return t.commitErr }
func (t *outboxStepTx) Rollback() error             { return nil }
func openOutboxSteps(t *testing.T, commitErr error, steps ...dbStep) (*sql.DB, *stepDriver) {
	t.Helper()
	name := fmt.Sprintf("outbox-steps-%d", stepDriverID.Add(1))
	script := &stepDriver{steps: steps}
	sql.Register(name, &outboxStepDriver{script, commitErr})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, script
}
func outboxEmpty() dbStep {
	return dbStep{kind: "query", rows: &testRows{columns: []string{"id", "account_id", "state", "undo_until", "request_hash"}}}
}
func outboxUsage(jobs, bytes, receipts int64) dbStep {
	return outboxUsageFor(jobs, bytes, 0, receipts)
}
func outboxUsageFor(jobs, bytes, ownerBytes, receipts int64) dbStep {
	return dbStep{kind: "query", rows: &testRows{columns: []string{"jobs", "bytes", "owner_bytes", "receipts"}, values: [][]driver.Value{{jobs, bytes, ownerBytes, receipts}}}}
}
func outboxStored(id, state, hash string) dbStep {
	return dbStep{kind: "query", rows: &testRows{columns: []string{"id", "account_id", "state", "undo_until", "request_hash"}, values: [][]driver.Value{{id, "mirror-1", state, time.Now().Add(time.Second), hash}}}}
}
func outboxAdmissionSteps() []dbStep {
	return []dbStep{{kind: "exec"}, outboxEmpty(), outboxUsage(0, 0, 0), {kind: "exec"}}
}

func TestOutboxDurableAdmissionAndKeyReplay(t *testing.T) {
	for _, tc := range []struct {
		name      string
		steps     []dbStep
		commitErr error
		want      error
		replay    bool
	}{
		{"new", outboxAdmissionSteps(), nil, nil, false},
		{"replay", []dbStep{{kind: "exec"}, outboxStored("job", "submitted", "hash")}, nil, nil, true},
		{"conflict", []dbStep{{kind: "exec"}, outboxStored("job", "pending", "different")}, nil, errOutboxKeyConflict, true},
		{"full", []dbStep{{kind: "exec"}, outboxEmpty(), outboxUsage(sendMaxJobs, 0, 0)}, nil, errOutboxCapacity, false},
		{"bytes", []dbStep{{kind: "exec"}, outboxEmpty(), outboxUsage(0, outboxMaxBytes, 0)}, nil, errOutboxCapacity, false},
		{"owner bytes", []dbStep{{kind: "exec"}, outboxEmpty(), outboxUsageFor(0, 0, outboxOwnerMaxBytes, 0)}, nil, errOutboxCapacity, false},
		{"receipts", []dbStep{{kind: "exec"}, outboxEmpty(), outboxUsage(0, 0, outboxOwnerReceiptLimit), {kind: "exec"}, outboxUsage(0, 0, outboxOwnerReceiptLimit)}, nil, errOutboxReceiptLimit, false},
		{"receipts pruned", []dbStep{{kind: "exec"}, outboxEmpty(), outboxUsage(0, 0, outboxOwnerReceiptLimit), {kind: "exec"}, outboxUsage(0, 0, outboxOwnerReceiptLimit-10), {kind: "exec"}}, nil, nil, false},
		{"commit uncertain", outboxAdmissionSteps(), errors.New("commit lost"), errors.New("commit lost"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, log := openOutboxSteps(t, tc.commitErr, tc.steps...)
			a := &App{db: db}
			rec, replay, err := a.saveOutbox(context.Background(), "owner", "mirror-1", "key", "hash", "ciphertext", 10)
			if (err == nil) != (tc.want == nil) || (err != nil && err.Error() != tc.want.Error()) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
			if replay != tc.replay {
				t.Fatalf("replay=%v", replay)
			}
			if err == nil && rec.ID == "" {
				t.Fatal("missing id")
			}
			for _, q := range log.log() {
				if strings.Contains(q.query, "INSERT INTO outbox_jobs") && !strings.Contains(q.query, "user_id=$2 AND mirror_account_id=$3") {
					t.Fatal("missing owner/account admission guard")
				}
			}
		})
	}
}

func TestOutboxLostCommitDoesNotAcknowledgeAcceptance(t *testing.T) {
	db, _ := openOutboxSteps(t, errors.New("lost commit"), outboxAdmissionSteps()...)
	a := &App{cfg: &Config{SecretKey: "test"}, db: db, log: discardLogger()}
	r := httptest.NewRequest("POST", "/api/send", nil)
	r.Header.Set("Idempotency-Key", "stable-key")
	w := httptest.NewRecorder()
	a.acceptOutbox(w, r, "owner", "mirror-1", "", &emptyOutgoing, []byte(`{"text":"private"}`))
	if w.Code != 503 || strings.Contains(w.Body.String(), `"queued"`) {
		t.Fatalf("response=%d %s", w.Code, w.Body.String())
	}
}

func TestOutboxRecoveryDoesNotCachePrivateComposition(t *testing.T) {
	a := &App{cfg: &Config{SecretKey: "test"}, log: discardLogger()}
	payload, _ := json.Marshal(outboxPayload{Request: json.RawMessage(`{"to":"dest@example.test","text":"private","attachments":[{"data_base64":"YWJj"}]}`)})
	cipher, err := sealSecret(a.cfg, string(payload))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cipher, "private") {
		t.Fatal("plaintext persisted")
	}
	a.db = openStepDB(t, dbStep{kind: "query", rows: &testRows{columns: []string{"payload", "state", "account", "sent"}, values: [][]driver.Value{{cipher, "ambiguous", "public-account", ""}}}})
	r := httptest.NewRequest("GET", "/api/outbox/id", nil)
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, "owner"))
	r.SetPathValue("id", "job")
	w := httptest.NewRecorder()
	a.handleOutboxDetail(w, r)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), "public-account") {
		t.Fatalf("response=%d %s", w.Code, w.Body.String())
	}
}

func TestOutboxCancelledReplayCannotBeResent(t *testing.T) {
	db, _ := openOutboxSteps(t, nil, outboxStored("original", "cancelled", "hash"))
	a := &App{db: db}
	r := httptest.NewRequest(http.MethodPost, "/send", nil)
	w := httptest.NewRecorder()
	if !a.replayOutbox(w, r, "owner", "key", "hash") || w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"cancelled"`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

var emptyOutgoing mail.Outgoing

func TestOutboxReservationCoversEncryptedQuotedPrintableMIME(t *testing.T) {
	cfg := &Config{SecretKey: "test"}
	outgoing := &mail.Outgoing{
		From:    mail.Address{Email: "sender@example.test"},
		To:      []mail.Address{{Email: "recipient@example.test"}},
		Subject: "Synthetic quota test", Text: strings.Repeat("é", 1<<20),
	}
	request, err := json.Marshal(map[string]string{"to": "recipient@example.test", "subject": outgoing.Subject, "text": outgoing.Text})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(outboxPayload{Outgoing: *outgoing, Request: request})
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := sealSecret(cfg, string(payload))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := outgoing.Render()
	if err != nil {
		t.Fatal(err)
	}
	sent, err := sealSecret(cfg, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if reserve := outboxReservation(outgoing, cipher); reserve < int64(len(sent)) {
		t.Fatalf("reservation=%d encrypted MIME=%d; Sent replacement exceeds admitted quota", reserve, len(sent))
	}
}

func TestOutboxCancelledLookupFailureIsNotReportedAsTooLate(t *testing.T) {
	db := openStepDB(t,
		dbStep{kind: "exec", zeroAffected: true},
		dbStep{kind: "query", err: errors.New("database unavailable")},
	)
	a := &App{db: db}
	r := httptest.NewRequest(http.MethodDelete, "/outbox/job", nil)
	r.SetPathValue("id", "job")
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, "owner"))
	w := httptest.NewRecorder()
	a.cancelOutbox(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("database failure reported as %d: %s", w.Code, w.Body.String())
	}
}

func TestOutboxDiscardedPayloadDoesNotClaimProviderAcceptance(t *testing.T) {
	for _, state := range []string{"failed", "cancelled", "ambiguous"} {
		t.Run(state, func(t *testing.T) {
			a := &App{db: openStepDB(t, dbStep{kind: "query", rows: &testRows{columns: []string{"payload", "state", "account", "sent"}, values: [][]driver.Value{{"", state, "public-account", ""}}}})}
			r := httptest.NewRequest(http.MethodGet, "/outbox/job", nil)
			r.SetPathValue("id", "job")
			r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, "owner"))
			w := httptest.NewRecorder()
			a.handleOutboxDetail(w, r)
			if w.Code != http.StatusGone || strings.Contains(w.Body.String(), "provider accepted") {
				t.Fatalf("discarded %s composition reported as %d: %s", state, w.Code, w.Body.String())
			}
		})
	}
}

func TestOutboxMissingEncryptionKeyRefusesAcceptance(t *testing.T) {
	a := &App{cfg: &Config{}, log: discardLogger()}
	w := httptest.NewRecorder()
	a.acceptOutbox(w, httptest.NewRequest(http.MethodPost, "/send", nil), "owner", "account", "", &emptyOutgoing, []byte(`{"text":"private"}`))
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), `"queued"`) {
		t.Fatalf("unencrypted acceptance=%d %s", w.Code, w.Body.String())
	}
}

func TestOutboxChangedEncryptionKeyCannotExposeRecovery(t *testing.T) {
	cipher, err := sealSecret(&Config{SecretKey: "original"}, `{"request":{"text":"private original content"}}`)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: &Config{SecretKey: "replacement"}, db: openStepDB(t, dbStep{kind: "query", rows: &testRows{columns: []string{"payload", "state", "account", "sent"}, values: [][]driver.Value{{cipher, "failed", "account", ""}}}})}
	r := httptest.NewRequest(http.MethodGet, "/outbox/job", nil)
	r.SetPathValue("id", "job")
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, "owner"))
	w := httptest.NewRecorder()
	a.handleOutboxDetail(w, r)
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "private original content") {
		t.Fatalf("wrong-key recovery=%d %s", w.Code, w.Body.String())
	}
}

func TestOutboxSentReplacementEnforcesReservedBudget(t *testing.T) {
	db, script := openOutboxSteps(t, nil, dbStep{kind: "exec", zeroAffected: true})
	a := &App{cfg: &Config{SecretKey: "test"}, db: db}
	if err := a.recordOutboxAccepted(outboxAttempt{ID: "job", Token: "attempt"}, []byte("synthetic MIME")); err == nil {
		t.Fatal("failed reservation/ownership guard was acknowledged")
	}
	if entries := script.log(); len(entries) != 1 || !strings.Contains(entries[0].query, "AND payload_bytes >= $4") {
		t.Fatal("Sent replacement can exceed its admitted private-payload reservation")
	}
}

type outboxAppendCancellationProbe struct {
	mail.Adapter
	app  *App
	seen bool
}

func (p *outboxAppendCancellationProbe) Append(ctx context.Context, box mail.MailboxID, raw []byte) error {
	p.seen = true
	if box != "sent-box" || string(raw) != "Synthetic submitted MIME" || !accountLeaseHeld(ctx, "mirror-1") {
		return errors.New("Sent filing lost its bytes, mailbox or account lease")
	}
	if !p.app.accountState("mirror-1").seal() {
		return errors.New("account seal failed")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Second):
		return errors.New("account cancellation did not reach Sent append")
	}
}

func TestOutboxSentFilingPreservesIntegratedAccountCancellation(t *testing.T) {
	cfg := &Config{SecretKey: "test"}
	sent, err := sealSecret(cfg, "Synthetic submitted MIME")
	if err != nil {
		t.Fatal(err)
	}
	credential, err := sealSecret(cfg, "synthetic-test-credential")
	if err != nil {
		t.Fatal(err)
	}
	db, script := openOutboxSteps(t, nil,
		dbStep{kind: "query", rows: &testRows{columns: []string{"id", "account", "sent"}, values: [][]driver.Value{{"job", "mirror-1", sent}}}},
		dbStep{kind: "query", rows: &testRows{columns: []string{"mailbox"}, values: [][]driver.Value{{"sent-box"}}}},
		dbStep{kind: "query", rows: &testRows{columns: []string{"owned"}, values: [][]driver.Value{{true}}}},
		dbStep{kind: "query", rows: &testRows{columns: []string{"provider", "address", "username", "host", "port", "ciphertext"}, values: [][]driver.Value{{"imap", "sender@example.test", "sender", "synthetic.invalid", int64(993), credential}}}},
		dbStep{kind: "exec"},
	)
	a := &App{cfg: cfg, db: db, log: discardLogger()}
	probe := &outboxAppendCancellationProbe{app: a}
	a.dial = func(context.Context, mail.AccountID, mail.Credential) (mail.Adapter, func(), error) {
		return probe, func() {}, nil
	}
	if err := a.processSentCopy(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Sent filing cancellation=%v", err)
	}
	if !probe.seen {
		t.Fatal("synthetic Sent append was not exercised")
	}
	select {
	case <-a.accountState("mirror-1").currentDrain():
	default:
		t.Fatal("Sent filing leaked its account lease")
	}
	entries := script.log()
	last := entries[len(entries)-1]
	if last.kind != "exec" || last.args[1] != "ambiguous" || strings.Contains(last.query, "SET state=") {
		t.Fatalf("filing cancellation changed submission outcome or hid uncertainty: %+v", last)
	}
}
