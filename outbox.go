package main

// Durable outbound ownership. Provider acceptance is not recipient delivery.
// A claim is never re-submitted: interrupted claims become ambiguous. Only
// pending rows are safe to resume, and Sent-copy filing is a separate state.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/neutron-build/neutron/mail"
)

var outboxStatements = []string{
	`CREATE TABLE outbox_jobs (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  account_id text NOT NULL REFERENCES email_accounts(mirror_account_id) ON DELETE CASCADE,
  submission_key text NOT NULL,
  request_hash text NOT NULL,
  state text NOT NULL CHECK (state IN ('pending','submitting','submitted','failed','ambiguous','cancelled')),
  payload_ciphertext text NOT NULL,
  payload_bytes bigint NOT NULL CHECK (payload_bytes >= 0),
  undo_until timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  attempt_id uuid,
  started_at timestamptz,
  filing_state text NOT NULL DEFAULT 'not_started' CHECK (filing_state IN ('not_started','provider','pending','submitting','filed','ambiguous')),
  sent_ciphertext text NOT NULL DEFAULT '',
  error_code text NOT NULL DEFAULT '',
  UNIQUE (user_id,submission_key)
 )`,
	`CREATE INDEX outbox_pending ON outbox_jobs(undo_until) WHERE state = 'pending'`,
	`CREATE INDEX outbox_live ON outbox_jobs(state) WHERE state IN ('pending','submitting')`,
	`CREATE INDEX outbox_settled ON outbox_jobs(updated_at) WHERE state IN ('submitted','failed','ambiguous','cancelled')`,
	`CREATE INDEX outbox_payload ON outbox_jobs(user_id,updated_at) WHERE payload_bytes > 0`,
}

// Retention is two separate clocks, both measured from the row's last state
// change once it has settled (no submission or filing in flight):
//   - private bytes (recoverable composition, saved Sent copy) are cleared
//     after outboxPayloadRetention: that is the window in which a person can
//     still be expected to recover or download them;
//   - the idempotency receipt (key, request hash, outcome; no content) lives
//     for outboxReceiptRetention, deliberately longer, because it is the only
//     thing that turns a late client retry of a lost acknowledgment into a
//     replay instead of a second send.
//
// Receipts are bounded per owner, never globally: one owner reaching the cap
// must not be able to stop anyone else from sending.
const outboxPayloadRetention = 30 * 24 * time.Hour
const outboxReceiptRetention = 90 * 24 * time.Hour
const outboxOwnerReceiptLimit = 50000
const outboxPruneBatch = 500
const outboxClaimTimeout = 2 * time.Minute
const outboxMaxBytes int64 = 512 << 20
const outboxOwnerMaxBytes int64 = 256 << 20
const outboxLockKey int64 = 7812634095511117

// outboxSettled selects rows with no submission or filing in flight. Only
// these are ever expired; pending work is never silently dropped.
const outboxSettled = `state IN ('submitted','failed','ambiguous','cancelled') AND filing_state NOT IN ('pending','submitting')`

var errOutboxCapacity = errors.New("outbox capacity exhausted")
var errOutboxReceiptLimit = errors.New("outbox retained-receipt limit reached")
var errOutboxKeyConflict = errors.New("submission key reused")

type outboxPayload struct {
	Outgoing    mail.Outgoing   `json:"outgoing"`
	Request     json.RawMessage `json:"request"`
	ReplyParent string          `json:"reply_parent"`
}
type outboxAttempt struct{ ID, Token, UserID string }
type outboxContextKey struct{}

type outboxRecord struct {
	ID          string    `json:"id"`
	AccountID   string    `json:"account_id"`
	State       string    `json:"status"`
	Filing      string    `json:"filing_status"`
	Error       string    `json:"error_code,omitempty"`
	Created     time.Time `json:"created_at"`
	UndoUntil   time.Time `json:"undo_until"`
	Recoverable bool      `json:"recoverable"`
	SavedCopy   bool      `json:"saved_sent_copy"`
}

func (a *App) acceptOutbox(w http.ResponseWriter, r *http.Request, uid, account, replyParent string, outgoing *mail.Outgoing, raw []byte) {
	if a.shuttingDown() {
		writeProblem(w, 503, "Shutting Down", "the message was not queued; retry with the same submission key")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		key = newID()
	} // older clients still receive durable ownership
	hash := mutationRequestHash(r.Method, r.URL.Path, r.URL.RawQuery, raw)
	payload, err := json.Marshal(outboxPayload{Outgoing: *outgoing, Request: raw, ReplyParent: replyParent})
	if err != nil {
		writeProblem(w, 500, "Queue Failed", "composition could not be saved")
		return
	}
	// The row id is chosen first because it is part of what the ciphertext is
	// bound to. A replay discards this blob and answers with the original row.
	id := uuid.NewString()
	encrypted, err := sealBound(a.cfg, "payload", uid, id, string(payload))
	if err != nil {
		writeProblem(w, 503, "Queue Unavailable", "composition could not be encrypted; the draft was not queued")
		return
	}
	record, replay, err := a.saveOutbox(r.Context(), uid, account, key, hash, id, encrypted, outboxReservation(outgoing, encrypted))
	if errors.Is(err, errOutboxKeyConflict) {
		sendKeyConflict(w)
		return
	}
	if errors.Is(err, errOutboxCapacity) {
		w.Header().Set("Retry-After", "30")
		writeProblem(w, 429, "Outbox Full", "wait for pending sends to finish or remove saved recoverable compositions before adding more")
		return
	}
	if errors.Is(err, errOutboxReceiptLimit) {
		w.Header().Set("Retry-After", "3600")
		writeProblem(w, 429, "Send Limit Reached", "too many sends are retained for this account; retained records expire 90 days after each send settles")
		return
	}
	if err != nil {
		a.log.Error("outbox acceptance failed", "err", err)
		writeProblem(w, 503, "Queue Unavailable", "acceptance could not be confirmed; keep this draft and retry with the same submission key")
		return
	}
	if replay {
		w.Header().Set("X-Idempotent-Replay", "true")
	}
	remaining := 0
	if record.State == "pending" {
		remaining = max(0, int(time.Until(record.UndoUntil).Seconds()))
	}
	writeJSON(w, map[string]any{"queued": record.ID, "undo_seconds": remaining, "status": record.State, "durable": true})
}

// Reserve enough for both encrypted composition storage and a MIME Sent copy.
// Quoted-printable can triple body bytes and MIME/base64 expands attachments;
// soft line breaks and base64 encryption add expansion too. Five times the
// retained input plus bounded MIME headers covers those encodings; the
// acceptance update also refuses a replacement larger than its reservation.
func outboxReservation(outgoing *mail.Outgoing, ciphertext string) int64 {
	return max(int64(len(ciphertext)), 5*outgoingWeight(outgoing)+(64<<10))
}

// The same transaction checks the key, global private-payload quota, account
// ownership and publication. A commit error is safely resolved by same-key retry.
func (a *App) saveOutbox(ctx context.Context, uid, account, key, hash, id, ciphertext string, size int64) (outboxRecord, bool, error) {
	var result outboxRecord
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return result, false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, outboxLockKey); err != nil {
		return result, false, err
	}
	var oldHash string
	err = tx.QueryRowContext(ctx, `SELECT id::text,account_id,state,undo_until,request_hash FROM outbox_jobs WHERE user_id=$1 AND submission_key=$2`, uid, key).Scan(&result.ID, &result.AccountID, &result.State, &result.UndoUntil, &oldHash)
	if err == nil {
		if oldHash != hash {
			return result, true, errOutboxKeyConflict
		}
		return result, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, false, err
	}
	live, bytes, ownerBytes, receipts, err := readOutboxUsage(ctx, tx, uid)
	if err != nil {
		return result, false, err
	}
	if receipts >= outboxOwnerReceiptLimit {
		// Expired receipts are removed lazily here so a healthy owner is
		// never refused for rows the retention policy has already retired.
		if _, err = tx.ExecContext(ctx, outboxPruneOwnerSQL, uid, int(outboxReceiptRetention.Seconds()), outboxPruneBatch); err != nil {
			return result, false, err
		}
		if live, bytes, ownerBytes, receipts, err = readOutboxUsage(ctx, tx, uid); err != nil {
			return result, false, err
		}
		if receipts >= outboxOwnerReceiptLimit {
			return result, false, errOutboxReceiptLimit
		}
	}
	if live >= sendMaxJobs || size < 0 || size > outboxMaxBytes || bytes > outboxMaxBytes-size || ownerBytes > outboxOwnerMaxBytes-size {
		return result, false, errOutboxCapacity
	}
	result = outboxRecord{ID: id, AccountID: account, State: "pending", UndoUntil: time.Now().UTC().Add(undoWindow)}
	res, err := tx.ExecContext(ctx, `INSERT INTO outbox_jobs(id,user_id,account_id,submission_key,request_hash,state,payload_ciphertext,payload_bytes,undo_until)
 SELECT $1,$2,$3,$4,$5,'pending',$6,$7,$8 FROM email_accounts WHERE user_id=$2 AND mirror_account_id=$3`, result.ID, uid, account, key, hash, ciphertext, size, result.UndoUntil)
	if err != nil {
		return result, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return result, false, err
	}
	if n != 1 {
		return result, false, sql.ErrNoRows
	}
	err = tx.Commit()
	return result, false, err
}

type outboxQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// readOutboxUsage reads the admission counters. Live work and private bytes are
// global (they bound the process); retained receipts and the per-owner byte
// share are scoped to the submitting owner.
func readOutboxUsage(ctx context.Context, q outboxQuerier, uid string) (live int, bytes, ownerBytes int64, receipts int, err error) {
	err = q.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(*) FROM outbox_jobs WHERE state IN ('pending','submitting')),
 (SELECT COALESCE(SUM(payload_bytes),0) FROM outbox_jobs WHERE payload_bytes > 0),
 (SELECT COALESCE(SUM(payload_bytes),0) FROM outbox_jobs WHERE user_id=$1 AND payload_bytes > 0),
 (SELECT COUNT(*) FROM outbox_jobs WHERE user_id=$1)`, uid).Scan(&live, &bytes, &ownerBytes, &receipts)
	return
}

// Both prune statements are bounded batches ordered by age, so a large backlog
// is retired over several passes without long locks.
const outboxPruneOwnerSQL = `DELETE FROM outbox_jobs WHERE id IN (
 SELECT id FROM outbox_jobs WHERE user_id=$1 AND ` + outboxSettled + ` AND updated_at < now()-make_interval(secs=>$2) ORDER BY updated_at LIMIT $3)`
const outboxPruneSQL = `DELETE FROM outbox_jobs WHERE id IN (
 SELECT id FROM outbox_jobs WHERE ` + outboxSettled + ` AND updated_at < now()-make_interval(secs=>$1) ORDER BY updated_at LIMIT $2)`
const outboxPrunePayloadSQL = `UPDATE outbox_jobs SET payload_ciphertext='',sent_ciphertext='',payload_bytes=0 WHERE id IN (
 SELECT id FROM outbox_jobs WHERE payload_bytes > 0 AND ` + outboxSettled + ` AND updated_at < now()-make_interval(secs=>$1) ORDER BY updated_at LIMIT $2)`

func (a *App) startOutboxWorker() {
	a.launch("durable-outbox", func(ctx context.Context) {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			if err := a.processOutbox(ctx); err != nil && ctx.Err() == nil {
				a.log.Error("outbox pass failed", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}

func (a *App) processOutbox(ctx context.Context) error {
	// An expired claim is evidence of uncertainty, never permission to resend.
	if _, err := a.db.ExecContext(ctx, `UPDATE outbox_jobs SET state='ambiguous',error_code='interrupted_submission',updated_at=now()
 WHERE state='submitting' AND started_at < now()-make_interval(secs=>$1)`, int(outboxClaimTimeout.Seconds())); err != nil {
		return err
	}
	if _, err := a.db.ExecContext(ctx, `UPDATE outbox_jobs SET filing_state='ambiguous',error_code='interrupted_filing',updated_at=now()
 WHERE filing_state='submitting' AND updated_at < now()-make_interval(secs=>$1)`, int(outboxClaimTimeout.Seconds())); err != nil {
		return err
	}
	// Pending jobs are never expired. Private bytes and receipts retire on
	// their own clocks (see outboxPayloadRetention); account/owner deletion
	// cascades immediately. Payloads go first so receipts outlive them.
	if _, err := a.db.ExecContext(ctx, outboxPrunePayloadSQL, int(outboxPayloadRetention.Seconds()), outboxPruneBatch); err != nil {
		return err
	}
	if _, err := a.db.ExecContext(ctx, outboxPruneSQL, int(outboxReceiptRetention.Seconds()), outboxPruneBatch); err != nil {
		return err
	}
	job, account, ciphertext, err := a.claimOutbox(ctx)

	if err == nil {
		a.deliverOutbox(ctx, job, account, ciphertext)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return a.processSentCopy(ctx)
}

// Claim and cancellation compete on the same row transition. SKIP LOCKED
// allows several workers without ever assigning one submission twice.
func (a *App) claimOutbox(ctx context.Context) (outboxAttempt, string, string, error) {
	job := outboxAttempt{Token: uuid.NewString()}
	var account, ciphertext string
	err := a.db.QueryRowContext(ctx, `UPDATE outbox_jobs SET state='submitting',attempt_id=$1,started_at=now(),updated_at=now()
 WHERE id=(SELECT id FROM outbox_jobs WHERE state='pending' AND undo_until<=now() ORDER BY undo_until FOR UPDATE SKIP LOCKED LIMIT 1)
 AND state='pending' RETURNING id::text,user_id::text,account_id,payload_ciphertext`, job.Token).Scan(&job.ID, &job.UserID, &account, &ciphertext)
	return job, account, ciphertext, err
}

func (a *App) deliverOutbox(ctx context.Context, job outboxAttempt, account, ciphertext string) {
	state, code := "ambiguous", "submission_outcome_unknown"
	defer func() {
		if recover() != nil {
			state, code = "ambiguous", "submission_panicked"
		}
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// A SMTP acceptance already recorded before filing wins this fallback.
		_, err := a.db.ExecContext(c, `UPDATE outbox_jobs SET state=$3,error_code=$4,updated_at=now(),
   payload_ciphertext=CASE WHEN $3='submitted' THEN '' ELSE payload_ciphertext END,
   payload_bytes=CASE WHEN $3='submitted' THEN 0 WHEN $3='failed' THEN octet_length(payload_ciphertext) ELSE payload_bytes END,
   filing_state=CASE WHEN $3='submitted' THEN 'provider' ELSE filing_state END
   WHERE id=$1 AND attempt_id=$2 AND state IN ('submitting','ambiguous')`, job.ID, job.Token, state, code)
		if err != nil {
			a.log.Error("outbox outcome save failed", "id", job.ID, "err", err)
		}
	}()
	// Nothing has reached a provider yet, so a composition that cannot be
	// opened ends as a visible failure that keeps its ciphertext: restoring
	// the key makes it recoverable. It is never sent, and never retried.
	plaintext, err := openBound(a.cfg, "payload", job.UserID, job.ID, ciphertext)
	if err != nil {
		state, code = "failed", outboxSealCode(err)
		a.log.Error("outbox composition cannot be opened; not sent", "id", job.ID, "code", code)
		return
	}
	var payload outboxPayload
	if err = json.Unmarshal([]byte(plaintext), &payload); err != nil {
		state, code = "failed", "payload_invalid"
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, outboxContextKey{}, job)
	deliver, _, ok := a.deliveryFor(ctx, mail.AccountID(account), payload.ReplyParent)
	if !ok {
		state, code = "failed", "account_unavailable"
		return
	}
	if ctx.Err() != nil {
		state, code = "failed", "cancelled_before_submission"
		return
	}
	if err = deliver(ctx, &payload.Outgoing); err != nil {
		a.log.Warn("outbox submission uncertain", "id", job.ID, "err", err)
		return
	}
	state, code = "submitted", ""
}

// SMTP transport acceptance and the exact Sent-copy bytes commit together.
// A failed commit stays ambiguous, with the original recoverable composition.
func (a *App) recordOutboxAccepted(job outboxAttempt, raw []byte) error {
	cipher, err := sealBound(a.cfg, "sent", job.UserID, job.ID, string(raw))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := a.db.ExecContext(ctx, `UPDATE outbox_jobs SET state='submitted',error_code='',filing_state='pending',sent_ciphertext=$3,
 payload_ciphertext='',payload_bytes=$4,updated_at=now() WHERE id=$1 AND attempt_id=$2 AND state IN ('submitting','ambiguous') AND payload_bytes >= $4`, job.ID, job.Token, cipher, len(cipher))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("outbox acceptance ownership or reserved payload budget lost")
	}
	return nil
}

func (a *App) processSentCopy(ctx context.Context) error {
	var id, owner, account, ciphertext string
	err := a.db.QueryRowContext(ctx, `UPDATE outbox_jobs SET filing_state='submitting',updated_at=now()
 WHERE id=(SELECT id FROM outbox_jobs WHERE state='submitted' AND filing_state='pending' ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1)
 AND filing_state='pending' RETURNING id::text,user_id::text,account_id,sent_ciphertext`).Scan(&id, &owner, &account, &ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	state, code := "ambiguous", "sent_copy_unconfirmed"
	defer func() {
		if recover() != nil {
			state, code = "ambiguous", "sent_copy_panicked"
		}
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, e := a.db.ExecContext(c, `UPDATE outbox_jobs SET filing_state=$2,error_code=$3,updated_at=now(),sent_ciphertext=CASE WHEN $2='filed' THEN '' ELSE sent_ciphertext END,payload_bytes=CASE WHEN $2='filed' THEN 0 ELSE payload_bytes END WHERE id=$1 AND filing_state IN ('submitting','ambiguous')`, id, state, code)
		if e != nil {
			a.log.Error("Sent-copy outcome save failed", "id", id, "err", e)
		}
	}()
	raw, err := openBound(a.cfg, "sent", owner, id, ciphertext)
	if err != nil {
		// No append was attempted. The filing stays unconfirmed, and the saved
		// copy remains downloadable once the key is restored.
		code = "sent_copy_" + outboxSealCode(err)
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	accountCtx, release, ok := a.beginAccountWork(mail.AccountID(account))
	if !ok {
		return errors.New("account closing")
	}
	defer release()
	ctx, stop := joinAccountContext(ctx, accountCtx)
	defer stop()
	ctx = context.WithValue(ctx, accountGateKey{}, mail.AccountID(account))
	var box string
	if err = a.db.QueryRowContext(ctx, `SELECT id FROM mail_mailboxes WHERE account_id=$1 AND role='sent' LIMIT 1`, account).Scan(&box); err != nil {
		return err
	}
	adapter, releaseAdapter, err := a.accountResolver()(ctx, mail.AccountID(account), mail.Credential{})
	if err != nil {
		return err
	}
	defer releaseAdapter()
	appender, ok := adapter.(mail.Appender)
	if !ok {
		return errors.New("provider does not support Sent filing")
	}
	if err = appender.Append(ctx, mail.MailboxID(box), []byte(raw)); err != nil {
		return err
	}
	state, code = "filed", ""
	if a.sched != nil {
		a.sched.Wake(mail.AccountID(account))
	}
	return nil
}

// outboxSealCode names why sealed data could not be opened. The two causes
// have different remedies, so the stored code keeps them apart.
func outboxSealCode(err error) string {
	if errors.Is(err, errSealedInvalid) {
		return "payload_corrupt"
	}
	return "payload_key_unavailable"
}

func writeSealProblem(w http.ResponseWriter, err error, what string) {
	if errors.Is(err, errSealedInvalid) {
		writeProblem(w, 500, "Recovery Unavailable", what+" failed authentication; it is damaged or does not belong to this entry")
		return
	}
	writeProblem(w, 503, "Recovery Unavailable", what+" cannot be opened with the current SECRET_KEY; restore the key it was saved with and try again")
}

func (a *App) cancelOutbox(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeLookupProblem(w, err, "owner")
		return
	}
	res, err := a.db.ExecContext(r.Context(), `UPDATE outbox_jobs SET state='cancelled',updated_at=now(),error_code='',payload_bytes=octet_length(payload_ciphertext) WHERE id::text=$1 AND user_id=$2 AND state='pending' AND undo_until>now()`, r.PathValue("id"), uid)
	if err != nil {
		writeProblem(w, 503, "Cancellation Unconfirmed", "retry cancellation or check Outbox before sending again")
		return
	}
	n, err := res.RowsAffected()
	if err != nil {
		writeProblem(w, 503, "Cancellation Unconfirmed", "check Outbox before sending again")
		return
	}
	if n == 0 {
		var state string
		err = a.db.QueryRowContext(r.Context(), `SELECT state FROM outbox_jobs WHERE id::text=$1 AND user_id=$2`, r.PathValue("id"), uid).Scan(&state)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, 503, "Cancellation Unconfirmed", "retry cancellation or check Outbox before sending again")
			return
		}
		if err == nil && state == "cancelled" {
			writeJSON(w, map[string]string{"cancelled": r.PathValue("id")})
			return
		}
		writeProblem(w, 410, "Too Late", "submission may have started; check Outbox for its outcome")
		return
	}
	writeJSON(w, map[string]string{"cancelled": r.PathValue("id")})
}

func (a *App) handleOutbox(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeLookupProblem(w, err, "owner")
		return
	}
	// Bounded metadata only. Payloads are available individually, never cached.
	rows, err := a.db.QueryContext(r.Context(), `SELECT j.id::text,ea.id::text,j.state,j.filing_state,j.error_code,j.created_at,j.undo_until,j.payload_ciphertext<>'',j.sent_ciphertext<>''
 FROM outbox_jobs j JOIN email_accounts ea ON ea.mirror_account_id=j.account_id AND ea.user_id=j.user_id
 WHERE j.user_id=$1 ORDER BY j.created_at DESC,j.id DESC LIMIT 4096`, uid)
	if err != nil {
		writeProblem(w, 503, "Outbox Unavailable", "saved sends could not be loaded")
		return
	}
	defer rows.Close()
	list := []outboxRecord{}
	for rows.Next() {
		var rec outboxRecord
		if err = rows.Scan(&rec.ID, &rec.AccountID, &rec.State, &rec.Filing, &rec.Error, &rec.Created, &rec.UndoUntil, &rec.Recoverable, &rec.SavedCopy); err != nil {
			writeProblem(w, 503, "Outbox Unavailable", "saved sends could not be loaded")
			return
		}
		list = append(list, rec)
	}
	if err = rows.Err(); err != nil {
		writeProblem(w, 503, "Outbox Unavailable", "saved sends could not be loaded")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, list)
}

func (a *App) handleOutboxDetail(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeLookupProblem(w, err, "owner")
		return
	}
	var cipher, state, account, sent, row string
	err = a.db.QueryRowContext(r.Context(), `SELECT j.payload_ciphertext,j.state,ea.id::text,j.sent_ciphertext,j.id::text FROM outbox_jobs j JOIN email_accounts ea ON ea.mirror_account_id=j.account_id AND ea.user_id=j.user_id WHERE j.user_id=$1 AND j.id::text=$2`, uid, r.PathValue("id")).Scan(&cipher, &state, &account, &sent, &row)
	if err != nil {
		writeLookupProblem(w, err, "outbox entry")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("format") == "eml" && sent != "" {
		raw, e := openBound(a.cfg, "sent", uid, row, sent)
		if e != nil {
			writeSealProblem(w, e, "saved Sent copy")
			return
		}
		w.Header().Set("Content-Type", "message/rfc822")
		w.Header().Set("Content-Disposition", `attachment; filename="submitted-message.eml"`)
		_, _ = w.Write([]byte(raw))
		return
	}
	if cipher == "" {
		writeProblem(w, 410, "Composition Retired", "no recoverable composition remains for this entry; its recorded status is "+state)
		return
	}
	raw, err := openBound(a.cfg, "payload", uid, row, cipher)
	if err != nil {
		writeSealProblem(w, err, "saved composition")
		return
	}
	var payload outboxPayload
	if err = json.Unmarshal([]byte(raw), &payload); err != nil {
		writeProblem(w, 500, "Recovery Unavailable", "saved composition is invalid")
		return
	}
	var request map[string]any
	if err = json.Unmarshal(payload.Request, &request); err != nil {
		writeProblem(w, 500, "Recovery Unavailable", "saved composition is invalid")
		return
	}
	request["account_id"] = account
	writeJSON(w, map[string]any{"status": state, "request": request})
}

// Retry lookup precedes provider/account validation, so expired credentials do
// not hide the previously committed outcome. Insertion rechecks under lock.
func (a *App) replayOutbox(w http.ResponseWriter, r *http.Request, uid, key, hash string) bool {
	var rec outboxRecord
	var oldHash string
	err := a.db.QueryRowContext(r.Context(), `SELECT id::text,account_id,state,undo_until,request_hash FROM outbox_jobs WHERE user_id=$1 AND submission_key=$2`, uid, key).Scan(&rec.ID, &rec.AccountID, &rec.State, &rec.UndoUntil, &oldHash)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		writeProblem(w, 503, "Outbox Unavailable", "previous acceptance could not be checked; retain this draft and retry with the same key")
		return true
	}
	if oldHash != hash {
		sendKeyConflict(w)
		return true
	}
	w.Header().Set("X-Idempotent-Replay", "true")
	remaining := 0
	if rec.State == "pending" {
		remaining = max(0, int(time.Until(rec.UndoUntil).Seconds()))
	}
	writeJSON(w, map[string]any{"queued": rec.ID, "undo_seconds": remaining, "status": rec.State, "durable": true})
	return true
}

// Discard removes private payload bytes, never the retry receipt. It is only
// available once submission and filing have stopped, and requires explicit UI
// confirmation because this local recovery copy cannot be restored afterward.
func (a *App) handleOutboxDiscard(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeLookupProblem(w, err, "owner")
		return
	}
	res, err := a.db.ExecContext(r.Context(), `UPDATE outbox_jobs SET payload_ciphertext='',sent_ciphertext='',payload_bytes=0
 WHERE id::text=$1 AND user_id=$2 AND state IN ('submitted','failed','ambiguous','cancelled') AND filing_state NOT IN ('pending','submitting')`, r.PathValue("id"), uid)
	if err != nil {
		writeProblem(w, 503, "Removal Unconfirmed", "the saved composition may remain; retry removal")
		return
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		writeProblem(w, 409, "Send Still Active", "wait for submission and Sent filing to finish before removing its saved copy")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
