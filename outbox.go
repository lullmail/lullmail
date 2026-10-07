package main

// Durable outbound ownership. Provider acceptance is not recipient delivery.
// A claim is never re-submitted: interrupted claims become ambiguous. Only
// pending rows are safe to resume, and Sent-copy filing is a separate state.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
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

// outboxOwnerMaxJobs is one owner's share of the process-wide active-send
// bound (sendMaxJobs), so a single owner can never fill it for everyone.
const outboxOwnerMaxJobs = 4

// outboxWorkers bounds concurrent provider attempts in one process. At most
// one attempt per account runs at a time: one account's sends stay in order,
// and a stalled provider delays only its own account.
const outboxWorkers = 4

// outboxDrainGrace is how long attempts already in flight may keep running
// after a graceful stop begins; the stop claims nothing new. It fits inside
// shutdownBudget together with the outcome write that follows it.
const outboxDrainGrace = 15 * time.Second

// outboxSettled selects rows with no submission or filing in flight. Only
// these are ever expired; pending work is never silently dropped.
const outboxSettled = `state IN ('submitted','failed','ambiguous','cancelled') AND filing_state NOT IN ('pending','submitting')`

var errOutboxCapacity = errors.New("outbox capacity exhausted")
var errOutboxOwnerCapacity = errors.New("owner's share of active sends exhausted")
var errOutboxAccountGone = errors.New("sending account is not connected")
var errOutboxReceiptLimit = errors.New("outbox retained-receipt limit reached")

// errOutboxFenced: a newer build has migrated this database. This build must
// neither accept nor claim outbox work, so two builds can never both own it.
var errOutboxFenced = errors.New("database was migrated by a newer build; this build no longer owns outbound work")

// The single-writer fence. Ownership of outbox rows follows the schema
// version recorded in app_migrations: a build may admit or claim intents only
// while no migration newer than the last one it knows has been applied. A
// newer build raises the ledger when it migrates, which silences every older
// build still running (blue/green overlap, a late rollback restart) at its
// next admission or claim, atomically with that statement. It cannot reach a
// build that predates the outbox: that code never reads this table. See
// docs/durable-outbox.md for the deploy rule that covers it.
func outboxSupportedSchema() int64 { return productMigrations[len(productMigrations)-1].Version }

const outboxSchemaNewerSQL = `(SELECT COALESCE(MAX(version),0) FROM app_migrations)`

func (a *App) outboxFenced(ctx context.Context) (bool, error) {
	var newest int64
	if err := a.db.QueryRowContext(ctx, `SELECT `+outboxSchemaNewerSQL).Scan(&newest); err != nil {
		return false, err
	}
	return newest > outboxSupportedSchema(), nil
}

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
	// Serialize this acceptance against account deletion's count-and-seal
	// pair: the admission gate is held from before the account-use lease
	// through saveOutbox's commit (including its uncertain-commit return),
	// so a disconnect either observes this row in its first count and
	// refuses without cancelling anything, or seals before this send enters
	// and the send is refused without ever being acknowledged. The gate is
	// per-account and never held while waiting for the global outbox SQL
	// lock, so unrelated mailboxes cannot head-of-line block and no
	// lock-order cycle with the account lease can form.
	unlockAdmission, err := a.outboxAdmissionGateOf(mail.AccountID(account)).Lock(r.Context())
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "Account Changing",
			"the sending account is being disconnected; the message was not queued, retry with the same submission key")
		return
	}
	defer unlockAdmission()
	// Admission joins the account's gate so a deletion that has sealed it
	// is not raced: the deletion waits for this commit and then counts the
	// row, instead of cascading away a send it never saw.
	release, ok := a.beginAccountUse(mail.AccountID(account))
	if !ok {
		writeProblem(w, 409, "Account Being Removed", "the sending account is being disconnected; the message was not queued")
		return
	}
	defer release()
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
	if errors.Is(err, errOutboxOwnerCapacity) {
		w.Header().Set("Retry-After", "10")
		writeProblem(w, 429, "Too Many Sends In Progress", fmt.Sprintf("you already have %d sends waiting or being submitted; the message was not queued, send it again once one of them finishes", outboxOwnerMaxJobs))
		return
	}
	if errors.Is(err, errOutboxAccountGone) {
		writeProblem(w, 404, "Account Not Found", "the sending account is not connected (it may have just been removed); the message was not queued, choose a connected account and send again")
		return
	}
	if errors.Is(err, errOutboxFenced) {
		w.Header().Set("Retry-After", "5")
		writeProblem(w, 503, "Server Updating", "this server version is being replaced; the message was not queued, retry with the same submission key")
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
	var newest int64
	if err = tx.QueryRowContext(ctx, `SELECT `+outboxSchemaNewerSQL).Scan(&newest); err != nil {
		return result, false, err
	}
	if newest > outboxSupportedSchema() {
		return result, false, errOutboxFenced
	}
	live, ownerLive, bytes, ownerBytes, receipts, err := readOutboxUsage(ctx, tx, uid)
	if err != nil {
		return result, false, err
	}
	if receipts >= outboxOwnerReceiptLimit {
		// Expired receipts are removed lazily here so a healthy owner is
		// never refused for rows the retention policy has already retired.
		if _, err = tx.ExecContext(ctx, outboxPruneOwnerSQL, uid, int(outboxReceiptRetention.Seconds()), outboxPruneBatch); err != nil {
			return result, false, err
		}
		if live, ownerLive, bytes, ownerBytes, receipts, err = readOutboxUsage(ctx, tx, uid); err != nil {
			return result, false, err
		}
		if receipts >= outboxOwnerReceiptLimit {
			return result, false, errOutboxReceiptLimit
		}
	}
	if live >= sendMaxJobs || size < 0 || size > outboxMaxBytes || bytes > outboxMaxBytes-size || ownerBytes > outboxOwnerMaxBytes-size {
		return result, false, errOutboxCapacity
	}
	if ownerLive >= outboxOwnerMaxJobs {
		return result, false, errOutboxOwnerCapacity
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
		// Not this owner's connected account (or deleted since validation):
		// retrying the same request can never succeed.
		return result, false, errOutboxAccountGone
	}
	if err = a.outboxPoint("accept:before-commit"); err != nil {
		return result, false, err
	}
	if err = tx.Commit(); err != nil {
		return result, false, err
	}
	// The row is durable from here; an error models only a lost acknowledgment.
	return result, false, a.outboxPoint("accept:after-commit")
}

type outboxQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// readOutboxUsage reads the admission counters. Live work and private bytes are
// global (they bound the process); the owner's live share, retained receipts
// and the per-owner byte share are scoped to the submitting owner.
func readOutboxUsage(ctx context.Context, q outboxQuerier, uid string) (live, ownerLive int, bytes, ownerBytes int64, receipts int, err error) {
	err = q.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(*) FROM outbox_jobs WHERE state IN ('pending','submitting')),
 (SELECT COUNT(*) FROM outbox_jobs WHERE user_id=$1 AND state IN ('pending','submitting')),
 (SELECT COALESCE(SUM(payload_bytes),0) FROM outbox_jobs WHERE payload_bytes > 0),
 (SELECT COALESCE(SUM(payload_bytes),0) FROM outbox_jobs WHERE user_id=$1 AND payload_bytes > 0),
 (SELECT COUNT(*) FROM outbox_jobs WHERE user_id=$1)`, uid).Scan(&live, &ownerLive, &bytes, &ownerBytes, &receipts)
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

// outboxPoint marks a persistence or provider boundary. It is a no-op unless a
// test installed outboxFault, which may return an error (to model a lost
// acknowledgment) or panic (to model the process dying right there).
func (a *App) outboxPoint(point string) error {
	if a.outboxFault == nil {
		return nil
	}
	return a.outboxFault(point)
}

// logFenced reports a passive build at most once a minute.
func (a *App) logFenced() {
	a.outboxFenceLog.Lock()
	defer a.outboxFenceLog.Unlock()
	if time.Since(a.outboxFenceLogged) > time.Minute {
		a.outboxFenceLogged = time.Now()
		a.log.Warn("outbox fenced: the database schema is newer than this build; not accepting or claiming outbound work")
	}
}

// startOutboxWorker runs the outbox loop as one background unit. The loop
// itself does only short database work (sweep, prune, claim), so the
// interrupted-claim sweep runs every tick however long a provider stalls;
// provider attempts run beside it, bounded by outboxWorkers and one per
// account. When the server stops, the loop stops claiming at once, then waits
// for the attempts it already started (bounded by outboxDrainGrace) so the
// background join covers them.
func (a *App) startOutboxWorker() {
	a.launch("durable-outbox", a.runOutboxWorker)
}

func (a *App) runOutboxWorker(ctx context.Context) {
	s := &outboxSlots{busy: map[string]bool{}, done: make(chan struct{}, outboxWorkers)}
	defer s.wg.Wait()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := a.dispatchOutbox(ctx, s); err != nil && ctx.Err() == nil {
			a.log.Error("outbox pass failed", "err", err)
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		case <-s.done:
		}
	}
}

// outboxSlots tracks this process's attempts in flight, by account.
type outboxSlots struct {
	mu   sync.Mutex
	busy map[string]bool
	wg   sync.WaitGroup
	done chan struct{}
}

// free reports whether another attempt may start, and the accounts that
// already have one (never nil: a NULL array would exclude every row).
func (s *outboxSlots) free() (bool, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	busy := make([]string, 0, len(s.busy))
	for account := range s.busy {
		busy = append(busy, account)
	}
	return len(busy) < outboxWorkers, busy
}

// start runs one attempt for account. The caller has just claimed it.
func (s *outboxSlots) start(a *App, account string, attempt func()) {
	s.mu.Lock()
	s.busy[account] = true
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			if p := recover(); p != nil {
				a.log.Error("outbox attempt panicked", "account", account, "panic", p)
			}
			s.mu.Lock()
			delete(s.busy, account)
			s.mu.Unlock()
			select {
			case s.done <- struct{}{}:
			default:
			}
		}()
		attempt()
	}()
}

// sweepOutbox runs the statements that need no provider: interrupted claims
// and filings become ambiguous, and retention prunes in bounded batches. It
// reports false when this build is fenced and must not claim.
func (a *App) sweepOutbox(ctx context.Context) (bool, error) {
	if fenced, err := a.outboxFenced(ctx); err != nil {
		return false, err
	} else if fenced {
		a.logFenced()
		return false, nil
	}
	// An expired claim is evidence of uncertainty, never permission to resend.
	if _, err := a.db.ExecContext(ctx, `UPDATE outbox_jobs SET state='ambiguous',error_code='interrupted_submission',updated_at=now()
 WHERE state='submitting' AND started_at < now()-make_interval(secs=>$1)`, int(outboxClaimTimeout.Seconds())); err != nil {
		return false, err
	}
	if _, err := a.db.ExecContext(ctx, `UPDATE outbox_jobs SET filing_state='ambiguous',error_code='interrupted_filing',updated_at=now()
 WHERE filing_state='submitting' AND updated_at < now()-make_interval(secs=>$1)`, int(outboxClaimTimeout.Seconds())); err != nil {
		return false, err
	}
	// Pending jobs are never expired. Private bytes and receipts retire on
	// their own clocks (see outboxPayloadRetention); account/owner deletion
	// cascades immediately. Payloads go first so receipts outlive them.
	if _, err := a.db.ExecContext(ctx, outboxPrunePayloadSQL, int(outboxPayloadRetention.Seconds()), outboxPruneBatch); err != nil {
		return false, err
	}
	if _, err := a.db.ExecContext(ctx, outboxPruneSQL, int(outboxReceiptRetention.Seconds()), outboxPruneBatch); err != nil {
		return false, err
	}
	return true, nil
}

// dispatchOutbox sweeps, then claims due submissions and leftover Sent
// filings for accounts with nothing in flight here, until the slots are full.
func (a *App) dispatchOutbox(ctx context.Context, s *outboxSlots) error {
	if ok, err := a.sweepOutbox(ctx); err != nil || !ok {
		return err
	}
	for {
		ok, busy := s.free()
		if !ok {
			return nil
		}
		if ctx.Err() != nil {
			return nil
		}
		// A claim is never cut by the stop signal: a cancelled client can
		// still see its UPDATE commit, which would strand the row in
		// submitting until the sweep calls it ambiguous. A claimed job is
		// always started, and the drain waits for it.
		claimCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		job, account, ciphertext, err := a.claimOutboxExcept(claimCtx, busy)
		cancel()
		if err == nil {
			s.start(a, account, func() { a.attemptOutbox(ctx, job, account, ciphertext) })
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		claimCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		filing, err := a.claimSentCopy(claimCtx, "", busy)
		cancel()
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		s.start(a, filing.account, func() {
			work, stop := a.outboxWork(ctx)
			defer stop()
			_ = a.fileSentCopy(work, filing)
		})
	}
}

// processOutbox is one synchronous pass: sweep, claim and attempt one
// submission (with its Sent filing), then file one leftover Sent copy.
func (a *App) processOutbox(ctx context.Context) error {
	if ok, err := a.sweepOutbox(ctx); err != nil || !ok {
		return err
	}
	job, account, ciphertext, err := a.claimOutbox(ctx)
	if err == nil {
		a.attemptOutbox(ctx, job, account, ciphertext)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return a.processSentCopy(ctx)
}

// outboxDetachedKey marks provider work that a graceful stop lets finish.
type outboxDetachedKey struct{}

// outboxWork detaches an attempt that has already been claimed from the
// worker's cancellation. A graceful stop cancels ctx: from then on nothing new
// is claimed, but the attempt keeps running for the drain grace and is
// cancelled only after it, ending ambiguous or failed by the usual rules. The
// marker also lets the account lease ignore the shutdown while still
// honouring an account deletion (beginAccountWorkCtx).
func (a *App) outboxWork(ctx context.Context) (context.Context, func()) {
	grace := a.outboxGrace
	if grace <= 0 {
		grace = outboxDrainGrace
	}
	work, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, func() { time.AfterFunc(grace, cancel) })
	return context.WithValue(work, outboxDetachedKey{}, true), func() {
		stop()
		cancel()
	}
}

// attemptOutbox submits one claimed job and then files its Sent copy, both
// as the same detached attempt.
func (a *App) attemptOutbox(ctx context.Context, job outboxAttempt, account, ciphertext string) {
	work, stop := a.outboxWork(ctx)
	defer stop()
	a.deliverOutbox(work, job, account, ciphertext)
	if work.Err() != nil {
		return // past the drain grace: a pending filing is claimed by the next process
	}
	filing, err := a.claimSentCopy(work, job.ID, []string{})
	if err == nil {
		_ = a.fileSentCopy(work, filing)
	} else if !errors.Is(err, sql.ErrNoRows) {
		a.log.Error("Sent-copy claim failed", "id", job.ID, "err", err)
	}
}

// Claim and cancellation compete on the same row transition. SKIP LOCKED
// allows several workers without ever assigning one submission twice.
func (a *App) claimOutbox(ctx context.Context) (outboxAttempt, string, string, error) {
	return a.claimOutboxExcept(ctx, []string{})
}

// claimOutboxExcept claims the oldest due job whose account is not in busy.
func (a *App) claimOutboxExcept(ctx context.Context, busy []string) (outboxAttempt, string, string, error) {
	job := outboxAttempt{Token: uuid.NewString()}
	var account, ciphertext string
	err := a.db.QueryRowContext(ctx, `UPDATE outbox_jobs SET state='submitting',attempt_id=$1,started_at=now(),updated_at=now()
 WHERE id=(SELECT id FROM outbox_jobs WHERE state='pending' AND undo_until<=now() AND account_id <> ALL($3::text[]) ORDER BY undo_until FOR UPDATE SKIP LOCKED LIMIT 1)
 AND state='pending' AND `+outboxSchemaNewerSQL+` <= $2 RETURNING id::text,user_id::text,account_id,payload_ciphertext`, job.Token, outboxSupportedSchema(), busy).Scan(&job.ID, &job.UserID, &account, &ciphertext)
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
	_ = a.outboxPoint("claim:after")
	deliver, _, ok := a.deliveryFor(ctx, mail.AccountID(account), payload.ReplyParent)
	if !ok {
		state, code = "failed", "account_unavailable"
		return
	}
	if ctx.Err() != nil {
		state, code = "failed", "cancelled_before_submission"
		return
	}
	_ = a.outboxPoint("deliver:before-submit")
	if err = deliver(ctx, &payload.Outgoing); err != nil {
		if mail.IsNotSubmitted(err) {
			// The provider provably never accepted it: a failed entry the
			// user can recover, not an alarming "may have been sent".
			a.log.Warn("outbox submission refused before acceptance", "id", job.ID, "err", err)
			state, code = "failed", "not_submitted"
			return
		}
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

// processSentCopy claims and files any one Sent copy that is waiting.
func (a *App) processSentCopy(ctx context.Context) error {
	filing, err := a.claimSentCopy(ctx, "", []string{})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return a.fileSentCopy(ctx, filing)
}

type sentCopyClaim struct{ id, owner, account, ciphertext string }

// claimSentCopy claims the filing of row id, or ("") of the oldest waiting
// row whose account is not in busy.
func (a *App) claimSentCopy(ctx context.Context, id string, busy []string) (sentCopyClaim, error) {
	var c sentCopyClaim
	err := a.db.QueryRowContext(ctx, `UPDATE outbox_jobs SET filing_state='submitting',updated_at=now()
 WHERE id=(SELECT id FROM outbox_jobs WHERE state='submitted' AND filing_state='pending' AND ($1='' OR id::text=$1) AND account_id <> ALL($2::text[]) ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1)
 AND filing_state='pending' RETURNING id::text,user_id::text,account_id,sent_ciphertext`, id, busy).Scan(&c.id, &c.owner, &c.account, &c.ciphertext)
	return c, err
}

func (a *App) fileSentCopy(ctx context.Context, c sentCopyClaim) error {
	id, owner, account, ciphertext := c.id, c.owner, c.account, c.ciphertext
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
	ctx, release, ok := a.beginAccountWorkCtx(ctx, mail.AccountID(account))
	if !ok {
		return errors.New("account closing")
	}
	defer release()
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
	_ = a.outboxPoint("filing:before-append")
	if err = appender.Append(ctx, mail.MailboxID(box), []byte(raw)); err != nil {
		return err
	}
	_ = a.outboxPoint("filing:after-append")
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
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Has("key") {
		a.outboxByKey(w, r, uid, r.URL.Query().Get("key"))
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
	writeJSON(w, list)
}

// outboxByKey answers GET /outbox?key=<submission key>: the outcome of the
// owner's submission under that key, in the same shape as a list entry, or
// 404 when this server holds no such submission. It is how a client resolves
// a send whose acknowledgment never arrived. Like the list it carries
// outcomes only, never the composition, so it is safe on the agent surface.
func (a *App) outboxByKey(w http.ResponseWriter, r *http.Request, uid, key string) {
	var rec outboxRecord
	err := sql.ErrNoRows
	if key != "" {
		err = a.db.QueryRowContext(r.Context(), `SELECT j.id::text,ea.id::text,j.state,j.filing_state,j.error_code,j.created_at,j.undo_until,j.payload_ciphertext<>'',j.sent_ciphertext<>''
 FROM outbox_jobs j JOIN email_accounts ea ON ea.mirror_account_id=j.account_id AND ea.user_id=j.user_id
 WHERE j.user_id=$1 AND j.submission_key=$2`, uid, key).Scan(&rec.ID, &rec.AccountID, &rec.State, &rec.Filing, &rec.Error, &rec.Created, &rec.UndoUntil, &rec.Recoverable, &rec.SavedCopy)
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeProblem(w, 404, "Not Found", "no submission with this key is held by this server")
		return
	}
	if err != nil {
		writeProblem(w, 503, "Outbox Unavailable", "the submission could not be looked up; try again")
		return
	}
	writeJSON(w, rec)
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
