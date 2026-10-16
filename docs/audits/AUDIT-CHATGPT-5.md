# Lull Mail — source-code audit and remediation report

**Repository:** [lullmail/lullmail](https://github.com/lullmail/lullmail)  
**Audited snapshot:** [`39e251862ef3a744fcbbc9c5541116f81d65a8ce`](https://github.com/lullmail/lullmail/commit/39e251862ef3a744fcbbc9c5541116f81d65a8ce)  
**Snapshot commit timestamp:** September 19, 2026, 04:26:37 UTC  
**Audit date:** September 19, 2026  
**Deliverable:** one Markdown report containing findings, proposed implementation code, migration considerations, and regression tests.

## Executive assessment

This review identified **42 prioritized findings: 20 High, 19 Medium, and 3 Low**. No Critical rating is assigned. The most important themes are persistence of accepted outgoing mail, authorization of durable credentials, correctness of account-work admission and deletion, browser owner/generation isolation, and safe recovery of provider and policy synchronization.

The most urgent fixes are **AUTH-01/AUTH-02**, **SEND-01/SEND-02**, **LIFE-01–LIFE-03**, **OFF-01–OFF-03**, and **SYNC-01/SYNC-03–SYNC-07**. Several failures compose: a non-durable send plus immediate draft retirement loses the user's only recovery path; a replay-lock failure plus a non-atomic mutation ledger undermines deduplication; an incomplete provider listing plus destructive completion can prune valid local data.

**Evidence boundary.** Actual source files were read through GitHub at the pinned commit, not inferred from the README or copied from previous audit documents. This is a broad but **not exhaustive line-by-line review of every file**, and it cannot certify that no further bugs exist. Some findings are concurrency windows demonstrated by source control flow rather than executed application reproductions. Conditional compatibility/privacy risks are labeled individually.

**Validation performed:** four isolated Go tests and ten isolated JavaScript tests passed. The Go harness was run with the race detector. These are small extracted/modeled control-flow tests—not the repository's own test suite, a full build, a PostgreSQL integration run, or a live-provider test. The full repository could not be cloned from this execution environment; GitHub source reads remained available. Browser navigation was blocked by the available environment, so the reader privacy scenario was not browser-verified. No repository changes or pull request were made.

**How to use the implementation blocks.** Small fixes are replacement code or insertion points in the named function. Larger changes are explicitly coordinated schema/API/worker refactors, with implementation building blocks and transaction boundaries. They are **proposed, unmerged code**, not a fully integrated or compiled patch set. New helpers/types/schema are identified as additions. Merge related changes together, reconcile imports/call sites/test fixtures, and run the regression matrix before deployment. Do not apply every SQL block indiscriminately to a running installation.

## Severity and evidence

**High** means a plausible durable-account compromise, loss of user-created content, unsafe deletion, or a synchronization/lifecycle failure that can strand core functionality. **Medium** means a narrower correctness, privacy, availability, or significant UX failure. **Low** means a limited compatibility or response-contract defect. These are engineering priorities, not CVSS scores.

“Source-confirmed” means the relevant behavior is directly present in the pinned implementation. It does not imply a successful end-to-end exploit or provider reproduction. “Isolated model” identifies a small harness illustrating that control flow. Conditional scenarios remain conditional; their exact environmental triggers are described.

## Finding index

| ID | Priority | Finding |
|---|---|---|
| [AUTH-01](#auth-01) | High | Existing sessions can enroll or delete passkeys without proving control of an existing credential |
| [AUTH-02](#auth-02) | High | The shared TOTP guessing budget is checked and charged non-atomically |
| [AUTH-03](#auth-03) | Medium | Password reauthentication can grant freshness using a proof from an older credential epoch |
| [DATA-01](#data-01) | High | Full account deletion executes SQL while its transaction result set is still open |
| [LIFE-01](#life-01) | High | An account-gate marker can claim a lease that was never acquired, or authorize the wrong account |
| [LIFE-02](#life-02) | High | Account cancellation is not consistently propagated, and account context access has a race |
| [SEND-01](#send-01) | High | Accepted sends and their only recoverable draft can disappear |
| [SEND-02](#send-02) | High | Send requests have no idempotent acceptance key |
| [LIFE-03](#life-03) | High | Shutdown can report a false drain or acknowledge work that was never launched |
| [API-01](#api-01) | High | The mutation ledger is not atomic with the mutation it claims to make idempotent |
| [API-02](#api-02) | Medium | Transient failures are permanently replayed, and replay loses response metadata |
| [OFF-01](#off-01) | High | A failed offline migration can delete the only copies of legacy drafts |
| [OFF-02](#off-02) | High | Owner changes do not fence all asynchronous reads, writes, replay, and UI publication |
| [OFF-03](#off-03) | High | Contended Web Locks still execute replay, and worker errors can execute it a second time |
| [OFF-04](#off-04) | Medium | A persisted retry deadline is calculated but not scheduled after restart |
| [DRAFT-01](#draft-01) | High | Attachment state diverges between the composer, draft stack, and persistent draft |
| [DRAFT-02](#draft-02) | Medium | Async attachment reads and delayed saves can outlive the draft they belong to |
| [SYNC-01](#sync-01) | High | Multi-page Graph initial syncs lose the enumeration-complete signal |
| [SYNC-02](#sync-02) | High | Graph message identities are not made stable across folder moves |
| [SYNC-03](#sync-03) | High | An expired recovery-scan cursor is retried indefinitely instead of being replaced |
| [SYNC-04](#sync-04) | High | JMAP enumeration can skip valid mail and discard deletion evidence before pruning |
| [SYNC-05](#sync-05) | High | Policy reconciliation discards its own staged progress on every retry |
| [SYNC-06](#sync-06) | High | Canceled reconciliation jobs can remain running until the server restarts |
| [SYNC-07](#sync-07) | High | Superseded policy jobs can lose required restoration work or apply stale destructive settings |
| [JMAP-01](#jmap-01) | Medium | Referenced body parts missing from bodyValues are accepted as complete content |
| [JMAP-02](#jmap-02) | Medium | Discovered JMAP API and download destinations lack an explicit credential-transport trust policy |
| [DATA-02](#data-02) | Medium | Unified keyset pagination is not unique across connected accounts |
| [DATA-03](#data-03) | Medium | The legacy default snooze stores NULL instead of a three-day deadline |
| [DATA-04](#data-04) | Medium | Thread reads return unbounded cached message bodies despite a bounded eager-fetch count |
| [DATA-05](#data-05) | Medium | Creating a connected account is split across two database transactions |
| [API-03](#api-03) | Low | Some accepted JSON responses commit headers before Content-Type is set |
| [API-04](#api-04) | Medium | Account-list responses omit reconciliation status even though the response type declares it |
| [SEND-03](#send-03) | Low | The attachment decoder rejects a valid zero-byte attachment |
| [GMAIL-01](#gmail-01) | Low | Raw-message decoding is less tolerant than the existing Gmail part decoder |
| [OPS-01](#ops-01) | Medium | Byte limits and decode semaphores do not bound slow request-body lifetimes |
| [OPS-02](#ops-02) | Medium | Some background database work cannot observe the worker context |
| [OPS-03](#ops-03) | Medium | Export budgets are per request, while aggregate disk, memory, and canceled work remain unbounded |
| [WEB-01](#web-01) | Medium | The service worker has no automatic deployment-versioned cache lifecycle |
| [WEB-02](#web-02) | Medium | Raw email HTML is parsed before remote-content removal can be guaranteed |
| [WEB-03](#web-03) | Medium | Inline cid images have no complete metadata-to-rendering path |
| [MCP-01](#mcp-01) | Medium | The MCP client accepts insecure remote origins for bearer-token requests |
| [UI-01](#ui-01) | Medium | Reauthentication retry failures can disappear after the modal has already closed |

## Detailed findings

<a id="auth-01"></a>

### AUTH-01 — Existing sessions can enroll or delete passkeys without proving control of an existing credential

**Priority:** High  
**Evidence:** Source-confirmed

**Pinned source:** [`auth.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/auth.go) — handlePasskeyRegisterBegin, registration completion, passkey deletion; [`reauth.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/reauth.go) — requireRecentReauth and the exception described in the file header; [`dashboard/src/app/views/SecurityView.tsx`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/views/SecurityView.tsx) — addPasskey.

**Problem and impact.** The recent-authentication gate explicitly excludes passkey ceremonies. A registration ceremony with user verification proves that someone controls the **new** authenticator; it does not prove that they control an existing account credential. An attacker possessing a valid, old session can enroll their own passkey and gain persistent access. Requiring user verification on the attacker's newly registered authenticator does not close this gap. Passkey removal should receive the same protection as other credential changes.

**Implementation.** Add the existing gate at the start of registration-begin, registration-finish, and deletion. Do not apply this mechanically to the initial, separately authorized bootstrap ceremony. Also bind saved registration state to the initiating session and authentication epoch, and revalidate those values inside the credential-write transaction. A begin-time check alone is insufficient.

```go
// At the start of each authenticated passkey-management handler:
if !a.requireRecentReauth(w, r) {
    return
}
```

Use this transaction-level check immediately before inserting/deleting credentials. The caller must already have validated the WebAuthn response and matched its saved ceremony to this session; pass the epoch captured when the ceremony began.

```go
func authorizeCredentialWriteTx(ctx context.Context, tx *sql.Tx,
    uid, sessionHash string, ceremonyEpoch int64) error {
    var epoch int64
    if err := tx.QueryRowContext(ctx,
        `SELECT auth_epoch FROM users WHERE id=$1 FOR UPDATE`, uid).
        Scan(&epoch); err != nil {
        return err
    }
    if epoch != ceremonyEpoch || sessionHash == "" || sessionHash == "bootstrap" {
        return errors.New("credential authorization expired")
    }
    var fresh bool
    err := tx.QueryRowContext(ctx, `
        SELECT GREATEST(created_at, COALESCE(reauthenticated_at, created_at))
                 > now() - interval '10 minutes'
        FROM auth_sessions
        WHERE id_hash=$1 AND user_id=$2 AND auth_epoch=$3
          AND expires_at > now()`, sessionHash, uid, epoch).Scan(&fresh)
    if err != nil { return err }
    if !fresh { return errors.New("fresh existing-credential proof required") }
    return nil
}
```

The handler must translate authorization expiry to 428/409, not expose the internal error as a database failure. Wrap the browser's passkey operation in the same `gated(...)` UX used for other security actions; after reauthentication, begin a **new** WebAuthn ceremony rather than resubmitting an expired challenge.

**Regression tests.** A session older than ten minutes cannot begin, finish, or delete a passkey without confirmation. A challenge begun before password rotation, logout, or session revocation cannot finish afterward. A newly verified attacker-controlled authenticator alone never satisfies the existing-credential gate.

<a id="auth-02"></a>

### AUTH-02 — The shared TOTP guessing budget is checked and charged non-atomically

**Priority:** High  
**Evidence:** Source-confirmed

**Pinned source:** [`reauth.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/reauth.go) — totpBudgetExhausted and recordTOTPFailure; [`auth.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/auth.go) — standalone TOTP sign-in.

**Problem and impact.** Admission reads the counter before verification; only a later wrong result increments it. Concurrent callers can all observe an available budget and proceed. Recording on the request context after verification also means cancellation/database failure can lose the charge. The upsert makes increments atomic, not the admission decision.

**Implementation.** Reserve a slot atomically **before** verifying the code, count all admitted attempts, and fail closed on reservation errors. Retain the existing per-peer limiter as a separate defense.

```go
func (a *App) reserveTOTPAttempt(ctx context.Context, uid string) (bool, error) {
    window := totpWindowStart(time.Now())
    var attempts int
    err := a.db.QueryRowContext(ctx, `
        INSERT INTO auth_factor_windows(user_id,factor,window_start,attempts)
        VALUES ($1,'totp',$2,1)
        ON CONFLICT (user_id,factor,window_start) DO UPDATE
          SET attempts=auth_factor_windows.attempts+1
          WHERE auth_factor_windows.attempts < $3
        RETURNING attempts`, uid, window, maxTOTPWindowAttempts).Scan(&attempts)
    if errors.Is(err, sql.ErrNoRows) { return false, nil }
    if err != nil { return false, err }
    return true, nil
}
```

Replace the read/check/late-charge sequence with this admission call. An exhausted budget returns 429 with a `Retry-After` calculated from the fixed window. A database failure returns 503 and **does not** proceed to TOTP verification. Remove the late `recordTOTPFailure` call so failures are not double-counted. Preserve any existing code-reuse protection.

**Regression tests.** Release 50 simultaneous requests for one user at a barrier: no more than ten may enter verification in one window, even across different peer addresses. Cancel requests before/after reservation; canceled or failed reservations must never fall through into verification.

<a id="auth-03"></a>

### AUTH-03 — Password reauthentication can grant freshness using a proof from an older credential epoch

**Priority:** Medium  
**Evidence:** Source-confirmed concurrency window

**Pinned source:** [`reauth.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/reauth.go) — handleReauthenticate; [`auth.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/auth.go) — credential rotation and session epoch handling.

**Problem and impact.** Reauthentication reads and verifies only the password hash, then stamps a session if its epoch matches the **current** user epoch. A concurrent credential change can restamp that same retained session into the new epoch while the old password verification is still running. The old proof can then grant freshness in the new epoch. This is narrower than a general revoked-session bypass: it requires the session retained by the concurrent credential change.

**Implementation.** Read hash and user epoch together before the expensive KDF. After verification, lock the user row, compare the captured epoch and hash with the current values, then update only that epoch's session. Keep the KDF outside the database lock.

```go
// Before verifyPassword:
var encoded string
var proofEpoch int64
err := a.db.QueryRowContext(r.Context(), `
    SELECT p.hash,u.auth_epoch FROM auth_passwords p
    JOIN users u ON u.id=p.user_id WHERE p.user_id=$1`, uid).
    Scan(&encoded, &proofEpoch)
// Handle err; acquirePasswordWork; verify the captured encoded hash.

// After successful verification, within a transaction:
var currentEpoch int64
var currentHash string
err = tx.QueryRowContext(r.Context(), `
    SELECT u.auth_epoch,p.hash FROM users u
    JOIN auth_passwords p ON p.user_id=u.id
    WHERE u.id=$1 FOR UPDATE OF u`, uid).Scan(&currentEpoch, &currentHash)
if err != nil { return err } // inside an error-returning transaction helper
if currentEpoch != proofEpoch || currentHash != encoded {
    return errors.New("credential changed during confirmation")
}
res, err := tx.ExecContext(r.Context(), `
    UPDATE auth_sessions SET reauthenticated_at=now()
    WHERE id_hash=$1 AND user_id=$2 AND auth_epoch=$3 AND expires_at>now()`,
    session, uid, proofEpoch)
```

Check `RowsAffected()==1`, commit, and only then report success. Apply the same “authorize under the credential transaction's user lock” principle to other sensitive mutations; middleware freshness is not a substitute for a commit-time authorization check.

**Regression test.** Pause the KDF, rotate the password while retaining the requesting session, resume the old verification, and require a conflict rather than a fresh timestamp.

<a id="data-01"></a>

### DATA-01 — Full account deletion executes SQL while its transaction result set is still open

**Priority:** High  
**Evidence:** Source-confirmed; PostgreSQL reproduction still required

**Pinned source:** [`auth.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/auth.go) — full owner-account deletion loop over connected mirror accounts.

**Problem and impact.** The full-delete handler obtains account IDs with a transaction query and executes deletion statements on that same transaction while iterating its still-open rows. With pgx, the connection is busy until the rows are consumed/closed. Deletion of an owner with connected mailboxes can therefore fail, although an empty-account test can pass. See the [pgx connection/rows contract](https://pkg.go.dev/github.com/jackc/pgx/v5#Rows).

**Implementation.** Collect account IDs, close/check the result set, and only then perform deletes. Preserve the surrounding transaction, reauthentication, work-drain, and rollback logic.

```go
func mirrorIDsForDelete(ctx context.Context, tx *sql.Tx, uid string) ([]string, error) {
    rows, err := tx.QueryContext(ctx,
        `SELECT mirror_account_id FROM email_accounts WHERE user_id=$1`, uid)
    if err != nil { return nil, err }
    var ids []string
    for rows.Next() {
        var id string
        if err := rows.Scan(&id); err != nil {
            _ = rows.Close()
            return nil, err
        }
        ids = append(ids, id)
    }
    scanErr := rows.Err()
    closeErr := rows.Close()
    if err := errors.Join(scanErr, closeErr); err != nil { return nil, err }
    return ids, nil
}
// The existing tx.ExecContext deletion loop belongs AFTER this returns.
```

Ensure full-owner deletion and single-mailbox deletion use the same ordered cleanup helper, including staged scans, reconciliation jobs, and any new outbox tables. This is a recommendation to keep their contracts aligned, not a claim that every child table currently leaks.

**Regression tests.** Real PostgreSQL tests with zero, one, and two connected accounts; seed messages, bodies, membership rows, product state, and active scans. Verify successful erasure, absence of all owned rows, and rollback on an injected failure.

<a id="life-01"></a>

### LIFE-01 — An account-gate marker can claim a lease that was never acquired, or authorize the wrong account

**Priority:** High  
**Evidence:** Source-confirmed

**Pinned source:** [`app.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/app.go) — accountWorkLifecycle, holdRequestAccount, beginAccountUseCtx.

**Problem and impact.** Request middleware marks `accountGateKey{}` as held even when `holdRequestAccount` did nothing because the account was absent, lookup failed, or deletion had sealed admission. Nested code trusts the boolean marker for any account. This defeats the deletion/work-admission barrier and can permit provider/database work after it should have been rejected.

**Implementation.** Replace the boolean marker with the exact leased mirror account ID. Make acquisition return an explicit error; set the marker only after success. A route with no resolved account must acquire a lease later, after its ownership lookup; an owner-wide read lock is not an account-specific lease.

```go
// accountGateKey already exists; change its context value's type.
func accountLeaseHeld(ctx context.Context, id mail.AccountID) bool {
    held, ok := ctx.Value(accountGateKey{}).(mail.AccountID)
    return ok && held != "" && held == id
}

// In middleware, after account ownership has been resolved:
accountCtx, release, ok := a.beginAccountWork(mirrorID)
if !ok {
    writeProblem(w, http.StatusConflict, "Account Closing", "retry after account deletion finishes")
    return
}
defer release()
requestCtx, cancel := context.WithCancel(r.Context())
stop := context.AfterFunc(accountCtx, cancel)
defer stop()
defer cancel()
requestCtx = context.WithValue(requestCtx, accountGateKey{}, mirrorID)
next.ServeHTTP(w, r.WithContext(requestCtx))
```

In `beginAccountUseCtx`, bypass nested acquisition only when `accountLeaseHeld(ctx,id)` is true. Do not convert a lookup failure to a successful no-op; distinguish missing account, unavailable database, and sealed account. Release the owner lock on every failure path.

**Regression tests.** Failed lookup, missing account, sealed account, and a context holding account A while attempting account B. Start deletion between ownership lookup and lease acquisition; the request must fail closed.

<a id="life-02"></a>

### LIFE-02 — Account cancellation is not consistently propagated, and account context access has a race

**Priority:** High  
**Evidence:** Source-confirmed concurrency defects

**Pinned source:** [`app.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/app.go) — beginAccountWork and account-state unsealing; [`reconcile.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/reconcile.go) — reconcileByFullEnumeration; [`sendqueue.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/sendqueue.go) — delivery account lease; [`resolver.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/resolver.go) — provider operations.

**Problem and impact.** `beginAccountWork` reads the mutable `state.ctx` after releasing its mutex, while unsealing replaces it under the mutex. Separately, several callers acquire a lease but keep using the original request/job context. Reconciliation switches to the gate context only if it is **already** canceled; it does not subscribe to future cancellation. Deletion can wait on work it cannot interrupt, and an unbounded owner `RWMutex.Lock` wait can occur before the intended deletion timeout starts.

**Implementation.** Capture the family context under the mutex. Join cancellation without discarding the request deadline or values.

```go
// Inside beginAccountWork, before state.mu.Unlock():
accountCtx := state.ctx
// ... increment admitted work under the same lock ...
state.mu.Unlock()
return accountCtx, release, true

func joinAccountContext(parent, account context.Context) (context.Context, func()) {
    ctx, cancel := context.WithCancel(parent)
    stop := context.AfterFunc(account, cancel)
    if account.Err() != nil { cancel() }
    return ctx, func() { stop(); cancel() }
}
```

Use the joined context for credential refresh, dialing, sync, reads, attachments, and SMTP. Replace the conditional context substitution in reconciliation with the helper. For owner deletion, seal owner admission first and wait on a drain channel with `select` on the deletion context instead of starting the timeout only after a blocking `RWMutex.Lock`.

```go
select {
case <-ownerDrained:
    // Proceed to transactional erasure.
case <-deleteCtx.Done():
    // Abort deletion; leave stored data intact and return a retryable failure.
    return deleteCtx.Err()
}
```

**Regression tests.** Concurrent acquire/seal/unseal under `go test -race`; a provider blocked in I/O must observe account cancellation; a held owner lease must not let deletion wait beyond its configured budget. The context-joining helper was exercised in the isolated Go probes; the repository-wide race scenario was not run here.

<a id="send-01"></a>

### SEND-01 — Accepted sends and their only recoverable draft can disappear

**Priority:** High  
**Evidence:** Source-confirmed

**Pinned source:** [`sendqueue.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/sendqueue.go) — in-memory queue, worker completion/error handling; [`dashboard/src/app/lib/actions.ts`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/lib/actions.ts) — sendMail; [`dashboard/src/app/ui/Compose.tsx`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/ui/Compose.tsx) — send and draft retirement; [`mail-engine/send.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/send.go) — Sender.Send and Message-ID generation.

**Problem and impact.** The server acknowledges a queued send held only in memory. Worker errors are logged, and there is no durable status endpoint for the browser to observe. The composer retires/deletes its local draft after the queued response, before delivery. A restart or provider failure can therefore lose both the accepted send and the user's usable draft. A buffered completion channel that nobody consumes is not a delivery contract.

**Implementation — coordinated schema/API/worker change.** Introduce a durable encrypted outbox. Save the full payload, attachments, stable Message-ID, and request hash before returning 202. Expose job status; keep a recoverable draft/outbox entry until an explicit outcome. Do not represent a queued response as “sent.”

```sql
CREATE TABLE outgoing_jobs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  account_id uuid NOT NULL REFERENCES email_accounts(id) ON DELETE CASCADE,
  request_key text NOT NULL,
  request_hash text NOT NULL,
  payload_ciphertext bytea NOT NULL,
  message_id text NOT NULL,
  state text NOT NULL CHECK (state IN
    ('queued','sending','sent','failed','unknown','cancelled')),
  send_after timestamptz NOT NULL,
  lease_until timestamptz,
  last_error text,
  accepted_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  UNIQUE(user_id, request_key)
);
CREATE INDEX outgoing_jobs_ready ON outgoing_jobs(send_after)
  WHERE state='queued';
```

Claim a due job atomically, under the same account-admission rules used for deletion:

```sql
WITH next_job AS (
  SELECT id FROM outgoing_jobs
  WHERE state='queued' AND send_after<=now()
  ORDER BY send_after,id FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE outgoing_jobs j
SET state='sending', lease_until=now()+interval '2 minutes'
FROM next_job n WHERE j.id=n.id RETURNING j.*;
```

Add a renderer entry point that accepts the persisted Message-ID, and separate rendering from submission. `Sender.Send` currently mints a new ID on every call and discards the raw bytes/ID on failure; merely retrying that function is not a durable-send design.

```go
// Add inside mail-engine; validate the caller-supplied ID before rendering.
func (msg *Outgoing) RenderWithMessageID(id string, includeBcc bool) ([]byte, error) {
    normalized := NormalizeMessageIDHeader(id)
    if normalized == "" || strings.ContainsAny(normalized, "\r\n\x00") {
        return nil, errors.New("invalid Message-ID")
    }
    return msg.render("<"+normalized+">", includeBcc)
}
```

Persist the serialized message once. For SMTP, exclude Bcc from transmitted headers and preserve Bcc recipients separately for the envelope. Add `GET /api/outbox/{id}` scoped to the authenticated owner. Undo must be an atomic `UPDATE ... WHERE state='queued' AND send_after>now()`; it must not race an already claimed submission.

**Important delivery semantics.** A crash after remote acceptance but before the local success record is an **unknown outcome**, not automatically retryable failure. Expired `sending` leases must become `unknown`, not return to `queued`. Reconcile against a provider's sent copy/Message-ID where possible or require an explicit user decision. This report does not promise exactly-once SMTP delivery.

**Regression tests.** Kill the process immediately after 202; restart during the undo window; reject SMTP authentication; fail DATA before and after possible acceptance; lose the HTTP response; fail sent-folder archival. Each case must retain an intelligible, recoverable outbox entry with no silent automatic duplicate.

**Sent-folder filing is a separate outcome.** `deliveryFor` calls `fileSent` after SMTP success and returns success regardless of filing failure. Do not retry SMTP to repair that failure. Extend the durable job with separate archival state and retry only the stored raw message's append, with duplicate detection where the provider supports it.

```sql
ALTER TABLE outgoing_jobs ADD COLUMN archive_state text NOT NULL DEFAULT 'not_needed'
  CHECK (archive_state IN ('not_needed','pending','stored','failed','unknown'));
ALTER TABLE outgoing_jobs ADD COLUMN archive_error text;
-- After SMTP acceptance, preserve state='sent' even if archive_state='failed'.
```

The UI should distinguish “sent; sent-folder copy unavailable” from “not sent.” If the append itself has an ambiguous outcome, reconcile before repeating it rather than assuming no copy was stored.


<a id="send-02"></a>

### SEND-02 — Send requests have no idempotent acceptance key

**Priority:** High  
**Evidence:** Source-confirmed

**Pinned source:** [`sendqueue.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/sendqueue.go) — send handler; [`idempotency.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/idempotency.go) — explicit exclusion of the send route; [`dashboard/src/app/lib/actions.ts`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/lib/actions.ts) — sendMail; [`mcp/tools.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mcp/tools.go) — send_mail.

**Problem and impact.** `/send` is deliberately excluded from generic mutation idempotency. The undo window does not prevent duplicate submission when the first response is lost and a client retries. Browser, MCP, and other clients need the same durable acceptance contract.

**Implementation.** Use the outbox's unique `(user_id,request_key)` constraint from SEND-01. Hash the validated, canonicalized immutable send payload. On conflict, compare the stored hash and return the existing job; return 409 when the key is reused for different content. Do not wrap the 34 MiB send route in the current 1 MiB generic buffer.

```ts
// Store these with the draft/send attempt, not in a fresh variable per retry.
type SendAttempt = { key: string; serialized: string };
function newSendAttempt(payload: unknown): SendAttempt {
  return { key: crypto.randomUUID(), serialized: JSON.stringify(payload) };
}
async function submitAttempt(attempt: SendAttempt): Promise<Response> {
  return fetch('/api/send', {
    method: 'POST', credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', 'Idempotency-Key': attempt.key },
    body: attempt.serialized,
  });
}
```

```sql
INSERT INTO outgoing_jobs
  (user_id,account_id,request_key,request_hash,payload_ciphertext,
   message_id,state,send_after)
VALUES ($1,$2,$3,$4,$5,$6,'queued',now()+interval '5 seconds')
ON CONFLICT (user_id,request_key) DO NOTHING
RETURNING id;
-- If no row is returned, SELECT id,request_hash for that owner/key;
-- equal hash -> return the original job; unequal hash -> 409.
```

Keep an ambiguous attempt frozen until its job is resolved; editing a payload requires a new intent/key, not reusing the key with changed attachments. Add an optional stable send-attempt key to the MCP tool schema and pass it through to the HTTP header.

**Regression tests.** Concurrent identical submissions with one key create one job. A dropped acceptance response followed by the same request returns that job. Different content with the same key is rejected. An unknown SMTP outcome is not blindly resubmitted.

<a id="life-03"></a>

### LIFE-03 — Shutdown can report a false drain or acknowledge work that was never launched

**Priority:** High  
**Evidence:** Source-confirmed

**Pinned source:** [`lifecycle.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/lifecycle.go) — Stop and task admission; [`app.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/app.go) — launch; [`sendqueue.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/sendqueue.go) — enqueue/budget reservation; [`cmd_serve.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/cmd_serve.go) — shutdown ordering.

**Problem and impact.** A later `Stop` call returns true as soon as the group is marked closed, even when an earlier stop timed out or workers remain alive. `launch` discards the task group's admission result; enqueue can reserve a send and acknowledge it although shutdown rejected its worker. Database pools can be closed while accepted handlers/tasks are still running. The durable outbox fixes loss after admission, but correct shutdown semantics are still needed.

**Implementation.** All stop callers must wait on one shared drain channel. Reject admission once shutdown or parent cancellation starts, and return the admission result to callers.

```go
type taskGroup struct {
    mu sync.Mutex
    wg sync.WaitGroup
    ctx context.Context
    cancel context.CancelFunc
    closed bool
    done chan struct{}
}
func newTaskGroup(parent context.Context) *taskGroup {
    ctx,cancel := context.WithCancel(parent)
    return &taskGroup{ctx:ctx,cancel:cancel,done:make(chan struct{})}
}
func (g *taskGroup) Go(fn func(context.Context)) bool {
    g.mu.Lock()
    defer g.mu.Unlock()
    if g.closed || g.ctx.Err()!=nil { return false }
    g.wg.Add(1)
    go func(){ defer g.wg.Done(); fn(g.ctx) }()
    return true
}
func (g *taskGroup) Stop(wait context.Context) bool {
    g.mu.Lock()
    if !g.closed {
        g.closed=true
        g.cancel()
        go func(){ g.wg.Wait(); close(g.done) }()
    }
    g.mu.Unlock()
    select {
    case <-g.done: return true
    case <-wait.Done(): return false
    }
}
```

Adapt logging/panic policy from the existing implementation; do not silently turn a worker panic into successful delivery. Make `App.launch` return `bool`. For the current queue, rejection must remove the reservation and release its memory budget before returning 503. With the durable outbox, acceptance is the database commit, not successful immediate goroutine launch; a later worker can claim the job.

Stop new HTTP admission, cancel/join account work, wait for active handlers/tasks, and then close pools. On `http.Server.Shutdown` timeout, explicitly close remaining connections and keep the process from treating an incomplete drain as clean completion.

**Regression tests.** Repeated stop calls while one worker is blocked; launch racing shutdown; canceled parent before admission; rejected send enqueue; a handler that outlives the graceful timeout. The shared-drain implementation was exercised with the race detector in the isolated probes.

<a id="api-01"></a>

### API-01 — The mutation ledger is not atomic with the mutation it claims to make idempotent

**Priority:** High  
**Evidence:** Source-confirmed

**Pinned source:** [`idempotency.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/idempotency.go) — withIdempotency and recordIdempotentResponse; [`classify.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/classify.go) — mutable handlers with independent database writes/transactions.

**Problem and impact.** The wrapper holds a transaction for the ledger but invokes a handler that commits its own writes independently. A crash, canceled request, or failed ledger commit after the handler commits rolls the ledger back while preserving the mutation. A retry runs it again. The code acknowledges this in a comment, but the top-level “exactly one execution” contract is stronger than the implementation. Holding one pool connection for the wrapper while the handler needs another also creates a hold-and-wait pool-exhaustion pattern under concurrency.

**Implementation — handler refactor required.** Database-only mutations and their response records must execute in the **same transaction**. Refactor handlers into transaction-aware domain functions; do not simply put a transaction in context while leaving their independent `BeginTx`/`Commit` calls intact. External effects must be represented by a durable outbox, not executed inside an alleged exactly-once SQL wrapper.

```go
type mutationResult struct {
    Status int
    ContentType string
    Body []byte
}
type localMutation func(context.Context, *sql.Tx) (mutationResult, error)

// Core of the claimed-key path; surrounding code retains the existing
// request hash comparison and concurrent-key conflict handling.
func runClaimedMutation(ctx context.Context, tx *sql.Tx,
    uid, key string, apply localMutation) (mutationResult, error) {
    result, err := apply(ctx, tx) // ALL domain writes use this tx.
    if err != nil { return mutationResult{}, err }
    if result.Status < 200 || result.Status >= 300 {
        return mutationResult{}, errors.New("mutation did not commit successfully")
    }
    _, err = tx.ExecContext(ctx, `
        UPDATE api_mutations
        SET response_status=$3,response_content_type=$4,response_body=$5
        WHERE user_id=$1 AND mutation_key=$2`,
        uid,key,result.Status,result.ContentType,result.Body)
    if err != nil { return mutationResult{}, err }
    if err = tx.Commit(); err != nil { return mutationResult{}, err }
    return result,nil
}
```

The caller owns rollback on every error, including panic unwinding. A deterministic validation failure can be returned before claiming a key; an external send is accepted by inserting its durable job in the transaction. Preserve the existing same-key/different-body 409 rule.

**Regression tests.** Crash/fault immediately before ledger commit; a mutation that changes data then fails; concurrent same-key callers; a pool capped at one connection; many concurrent distinct keys. The domain change and its replay record must either both commit or neither commit.

<a id="api-02"></a>

### API-02 — Transient failures are permanently replayed, and replay loses response metadata

**Priority:** Medium  
**Evidence:** Source-confirmed

**Pinned source:** [`idempotency.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/idempotency.go) — recordIdempotentResponse and replay branch; [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/lib/offline.ts) — retry handling.

**Problem and impact.** Every handler response, including 429/5xx, is stored. Retrying a recoverable outage with the same key replays the old error instead of attempting the operation after recovery. The browser correctly retries those classes, so it can loop against an immutable failure. Replay also restores only Content-Type, not other meaningful response headers. Finally, the 30-day ledger retention is not aligned with an explicit expiry for queued offline mutations: a very old replay can execute again after its key was pruned.

**Implementation.** First implement API-01 so rollback really rolls back the mutation. Persist successful committed results, not transient transport/service failures. Return transient headers directly, including Retry-After. Store a safe allowlist of metadata when successful responses require it. Do not replay Set-Cookie or connection-specific headers.

```go
func durableReplayStatus(status int) bool {
    return status >= 200 && status < 300
}
func replayHeaders(h http.Header) map[string]string {
    out := map[string]string{}
    for _, name := range []string{"Content-Type", "Location", "ETag"} {
        if value := h.Get(name); value != "" { out[name]=value }
    }
    return out
}
```

```sql
ALTER TABLE api_mutations
  ADD COLUMN response_headers jsonb NOT NULL DEFAULT '{}'::jsonb;
```

Give offline queue entries an explicit server-compatible expiry and surface expired entries for review instead of automatically replaying them. Alternatively retain compact `(owner,key,request_hash,outcome)` tombstones longer than response bodies, so pruning a large response cannot reauthorize execution.

```ts
const MAX_AUTOMATIC_REPLAY_AGE_MS = 29 * 24 * 60 * 60 * 1000;
function replayTooOld(queuedAt: number, now = Date.now()): boolean {
  return now - queuedAt > MAX_AUTOMATIC_REPLAY_AGE_MS;
}
// Persist a visible terminal "review required" state; do not silently delete it.
```

**Regression tests.** A first 503 followed by recovery with the same key succeeds once; a successful response replays matching allowed metadata; an entry older than the contract's deduplication horizon is not automatically executed.

<a id="off-01"></a>

### OFF-01 — A failed offline migration can delete the only copies of legacy drafts

**Priority:** High  
**Evidence:** Source-confirmed; failure-path model exercised

**Pinned source:** [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/lib/offline.ts) — migrateV1Storage and openDB upgrade handling.

**Problem and impact.** The migration sets its completion marker before the IndexedDB work is known to have committed. Its error handler logs a warning, but legacy `es-drafts` / `es-draft-*` keys are removed afterward anyway. A quota error or aborted transaction can therefore destroy the source drafts and prevent a retry. The upgrade handler also reads `oldVersion` from the request rather than the `IDBVersionChangeEvent`, so its version-dependent cleanup does not test the actual previous version.

**Implementation.** Make migration commit the boundary between copying and cleanup. Preserve all original keys and leave the completion marker unset on any failure. Use stable IDs and idempotent puts so interruption during cleanup is safe to retry. Pass the actual version-change event to upgrade logic.

```ts
function transactionDone(tx: IDBTransaction): Promise<void> {
  return new Promise((resolve, reject) => {
    tx.oncomplete = () => resolve();
    tx.onabort = () => reject(tx.error ?? new Error("Storage transaction aborted"));
    tx.onerror = () => reject(tx.error ?? new Error("Storage transaction failed"));
  });
}

// Replacement control flow around the existing legacy decoding / record mapping.
async function commitLegacyMigration(
  db: IDBDatabase,
  records: unknown[],
  originalKeys: string[],
  completedKey: string,
): Promise<void> {
  const tx = db.transaction("drafts", "readwrite");
  const done = transactionDone(tx);
  for (const record of records) tx.objectStore("drafts").put(record);
  await done; // A failed copy must leave every original intact.
  // Marker failure also leaves originals intact. Repeating the puts is safe.
  localStorage.setItem(completedKey, "1");
  for (const key of originalKeys) localStorage.removeItem(key);
}

request.onupgradeneeded = (event: IDBVersionChangeEvent) => {
  const previousVersion = event.oldVersion;
  // Use previousVersion in the existing schema / legacy-cache upgrade branches.
};
```

The helper assumes the existing `drafts` store's key-path-compatible records, including the verified owner namespace. Do not migrate a previous owner's legacy data into the new owner's namespace. Keep legacy decoding errors visible rather than treating an unreadable draft as an empty draft to be discarded.

**Regression tests.** Inject quota failures, transaction aborts, malformed legacy records, interruption immediately after commit, and interruption during legacy-key removal. Re-running must preserve exactly one copy of each successfully migrated draft, without removing unmigrated originals.

<a id="off-02"></a>

### OFF-02 — Owner changes do not fence all asynchronous reads, writes, replay, and UI publication

**Priority:** High  
**Evidence:** Source-confirmed; late-response control-flow model exercised

**Pinned source:** [`dashboard/src/app/lib/api.ts`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/lib/api.ts) — request, memoryResponses, refreshAuth; [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/lib/offline.ts) — clearOfflineData, prepareOfflineOwner, cacheResponse, replayDueMutations and draft APIs; [`dashboard/src/app/App.tsx`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/App.tsx) — startup ordering; [`dashboard/src/app/lib/store.ts`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/lib/store.ts) — draft hydration and pending writes.

**Problem and impact.** The current generation check prevents some stale responses from entering the persistent cache, but `request()` still returns the private response to its caller. Its memory cache is keyed only by route, and successful authentication as a different owner does not itself clear that cache. A failed old mutation can be queued using the **new current owner**. Replay validates generation before the loop rather than before every send and after every asynchronous boundary. Startup begins offline replay before authentication/namespace preparation has settled. `clearOfflineData()` does not itself advance the generation before wiping; draft operations and some writes after `await openDB()` are not consistently fenced.

This is a browser/session isolation issue, especially on owner replacement, installation replacement, logout/login, or multiple tabs. It is **not** evidence that the server lets one authenticated user query another user's mailbox.

**Implementation.** Use one immutable owner snapshot for each operation. Reject stale results before returning them, before queueing a failed operation, and before every replay request. Namespace memory-cache keys. Invalidate the generation and abort pending work **before** erasing storage or publishing a new owner. Do not replay mutations until a current authenticated server status has verified the namespace; offline reads may remain a separately designed, clearly signaled mode.

```ts
type OwnerSnapshot = Readonly<{ namespace: string; generation: number }>;

function captureOwner(): OwnerSnapshot {
  return { namespace: offlineOwner(), generation: offlineGeneration() };
}
function assertOwner(expected: OwnerSnapshot): void {
  if (!expected.namespace || offlineOwner() !== expected.namespace ||
      !generationCurrent(expected.generation)) {
    throw new DOMException("Account changed during operation", "AbortError");
  }
}
function memoryKey(owner: OwnerSnapshot, path: string): string {
  return JSON.stringify([owner.namespace, owner.generation, path]);
}

// Inside request(), for protected routes only:
// const owner = captureOwner(); use memoryKey(owner,path) for cache access.
// After await fetch(), after await res.json(), and before returning a result:
// assertOwner(owner);
// In the network-error path, assertOwner(owner) BEFORE queueMutation.
// Pass owner into queueMutation instead of having it infer a later owner.
```

An in-memory/localStorage check alone is still racy across tabs: the write can commit after another tab's wipe. Put authoritative namespace/generation metadata in IndexedDB and check it in the **same readwrite transaction** as each cache/draft/queue write. The following implementation pattern introduces a `meta` store with key `owner`; migration must create it and all writers must use this gate.

```ts
function guardedPut(
  db: IDBDatabase, storeName: string, value: unknown, expected: OwnerSnapshot,
): Promise<void> {
  return new Promise((resolve, reject) => {
    const tx = db.transaction(["meta", storeName], "readwrite");
    let stale = false;
    tx.oncomplete = () => resolve();
    tx.onabort = () => reject(stale
      ? new DOMException("Stale owner write", "AbortError")
      : tx.error ?? new Error("Storage write aborted"));
    tx.onerror = () => reject(tx.error ?? new Error("Storage write failed"));
    const read = tx.objectStore("meta").get("owner");
    read.onsuccess = () => {
      const active = read.result as OwnerSnapshot | undefined;
      if (!active || active.namespace !== expected.namespace ||
          active.generation !== expected.generation) {
        stale = true;
        tx.abort();
        return;
      }
      tx.objectStore(storeName).put(value);
    };
  });
}
```

Perform namespace metadata change plus old cache/draft/queue deletion in one transaction over those stores. Broadcast the change to other tabs, cancel network work, clear visible message/draft state and memory caches, and reject late hydration. Recheck replay's captured namespace before each request; bind replay to the expected installation/user server-side as defense in depth. Do not silently turn an owner-mismatch error into offline queueing.

**Regression tests.** Slow GET completes after logout; failed POST completes after owner replacement; owner changes between replay items; draft save races a wipe from another tab; authenticated owner A becomes authenticated owner B without an intervening unauthenticated response. None may publish, persist, or replay A's data as B's.

<a id="off-03"></a>

### OFF-03 — Contended Web Locks still execute replay, and worker errors can execute it a second time

**Priority:** High  
**Evidence:** Source-confirmed; isolated JavaScript reproduction

**Pinned source:** [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/lib/offline.ts) — withReplayLock; [`dashboard/src/app/lib/offline.test.ts`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/lib/offline.test.ts) — replay lock test only exercises unavailable lock manager.

**Problem and impact.** With `ifAvailable: true`, the lock callback is called with `null` when another tab holds the lock. Passing `run` directly ignores that argument and starts a second replay anyway. A broad catch around the lock request also falls back to `run()`, including when it was the worker itself that threw; the failed pass may therefore execute twice. The [Web Locks callback contract](https://developer.mozilla.org/en-US/docs/Web/API/LockManager/request) explicitly distinguishes a granted lock from a null callback argument.

**Implementation.** Detect unavailable API support separately, honor a null lock, and let worker errors propagate without rerunning the worker. A browser without Web Locks needs an IndexedDB lease or a documented fallback relying on correct server idempotency; lack of a lock is not proof of exclusivity.

```ts
export async function withReplayLock<T>(run: () => Promise<T>): Promise<T | undefined> {
  if (!("locks" in navigator)) {
    // Preserve the current fallback only with the server guarantees in API-01.
    return run();
  }
  return navigator.locks.request(
    "lullmail-offline-replay", { mode: "exclusive", ifAvailable: true },
    async (lock) => {
      if (lock === null) return undefined;
      return run();
    },
  );
}
```

Use the existing shared lock-name constant rather than introducing a different name in only one caller. A skipped pass should arrange another attempt when the active tab finishes or on the next scheduled retry. Do not wrap this function in a caller that retries all thrown errors by immediately invoking `run` outside the lock.

**Regression tests.** Two tabs; null lock callback executes zero mutations; worker throws after its first mutation and is called exactly once; explicit API-unavailable fallback is tested separately. The first two failure shapes were reproduced with the isolated harness in this report.

<a id="off-04"></a>

### OFF-04 — A persisted retry deadline is calculated but not scheduled after restart

**Priority:** Medium  
**Evidence:** Source-confirmed; isolated scheduling model

**Pinned source:** [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/lib/offline.ts) — replayPlan and replayDueMutations; [`dashboard/src/app/lib/offline.test.ts`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/lib/offline.test.ts) — helper-only retry tests.

**Problem and impact.** `replayPlan()` correctly returns `retryAt` for a backed-off head entry, but the replay driver takes the due entries without carrying that deadline into its next timer. When no entries are due after reload, no request runs to calculate a new retry. Queued work can sit indefinitely until a separate event triggers replay. Passing helper tests does not exercise this integration.

**Implementation.** Initialize the driver's next deadline from the plan and preserve ordering when the head is backed off.

```ts
const plan = replayPlan(items, owner, Date.now());
let retryAt: number | undefined = plan.retryAt;
for (const item of plan.due) {
  // Existing replay logic, plus the owner checks in OFF-02.
  // On retryable failure, persist nextAttemptAt, assign it to retryAt,
  // then break; never let newer mutations overtake this item.
}
if (retryAt !== undefined) {
  scheduleReplay(Math.max(0, retryAt - Date.now()));
}
```

`scheduleReplay` here denotes a small wrapper around the module's existing timeout/replay entry point; replace/cancel its previous timer and cap unusually long delays so JavaScript's timer range cannot overflow. Keep server `Retry-After` as a lower bound on the retry delay.

**Regression tests.** Persist a backed-off head, reload with an online browser, advance a fake clock past the deadline, and assert that the real replay driver sends once without a new `online` event. Repeat with a newer queued mutation to verify ordering.

<a id="draft-01"></a>

### DRAFT-01 — Attachment state diverges between the composer, draft stack, and persistent draft

**Priority:** High  
**Evidence:** Source-confirmed; isolated state-divergence model

**Pinned source:** [`dashboard/src/app/ui/Compose.tsx`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/ui/Compose.tsx) — attachment state and persistence effect; [`dashboard/src/app/lib/store.ts`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/lib/store.ts) — draftStack, updateDraft, stash/cycle/hydration.

**Problem and impact.** The composer owns attachment state and saves it directly, while the draft stack keeps its earlier attachment seed. Parking or cycling drafts remounts the composer from that stale seed. A later persistence effect or whole-draft flush can overwrite the saved attachment list with the stale or empty one. Text and attachment writers should not independently overwrite the same draft record.

**Implementation.** Make the draft stack the single authoritative in-memory document. Every attachment change updates the exact draft ID, and one persistence path writes the resulting full snapshot. Remove the competing direct attachment effect. The following adapter keeps the implementation independent of the existing signal's full draft type.

```ts
type DraftWithAttachments<A> = { id: string; attachments?: A[] };
function replaceDraftAttachments<D extends DraftWithAttachments<A>, A>(
  drafts: D[], id: string, attachments: A[],
): D[] {
  return drafts.map(draft => draft.id === id
    ? { ...draft, attachments: [...attachments] }
    : draft);
}

// In the existing store module, use the captured composing draft id:
// draftStack.value = replaceDraftAttachments(draftStack.value, draftId, next);
// Schedule the SAME full-snapshot persistence used for text changes.
// Render from that stack entry; do not retain another writable attachment copy.
```

Persist a monotonic revision with each draft and reject older writes in one transaction, so a delayed text save cannot replace a newer attachment snapshot. Undo-send should restore the complete original draft including attachments, sending account, CC/BCC, and reply parent, not only visible text fields.

**Regression tests.** Add an attachment, park/reopen, cycle to another draft and back, edit text while an attachment save is pending, reload, and undo a queued send. The attachment bytes and metadata must survive in the correct draft exactly once.

<a id="draft-02"></a>

### DRAFT-02 — Async attachment reads and delayed saves can outlive the draft they belong to

**Priority:** Medium  
**Evidence:** Source-confirmed lifecycle gaps; browser integration needed

**Pinned source:** [`dashboard/src/app/ui/Compose.tsx`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/ui/Compose.tsx) — addFiles, send and attachment saving; [`dashboard/src/app/lib/store.ts`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/lib/store.ts) — draft ID allocation and pending field writes; [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/lib/offline.ts) — draft save/delete operations.

**Problem and impact.** A file read can start while the composer is idle, then complete after send or a draft switch. The message may send without that file, or the completion may write to a retired draft. A retirement fence for one writer does not protect separate delayed field writers. Time-plus-per-tab-counter draft IDs also need a cross-tab uniqueness guarantee rather than assuming two tabs cannot create a draft in the same millisecond.

**Implementation.** Track pending reads per draft, disable send until they finish, capture the draft ID and owner snapshot before awaiting, and discard completion if the draft has closed or changed. Use `crypto.randomUUID()` for new draft IDs. Put a revision/tombstone check in the same IndexedDB transaction as every draft write and delete.

```ts
function newDraftID(): string { return crypto.randomUUID(); }

type ReadTicket = { draftId: string; generation: number };
async function readDraftFile(
  file: File, ticket: ReadTicket,
  isLive: (t: ReadTicket) => boolean,
): Promise<Uint8Array> {
  if (!isLive(ticket)) throw new DOMException("Draft ended", "AbortError");
  const bytes = new Uint8Array(await file.arrayBuffer());
  if (!isLive(ticket)) throw new DOMException("Draft ended", "AbortError");
  return bytes;
}

// Surround each read with per-draft pendingReads++ / -- in finally.
// Send eligibility requires pendingReads===0 as well as !sending.
// After the read, update the captured draft's single authoritative snapshot.
```

Reserve the aggregate attachment byte budget **before** starting reads and release the reservation on failure; individual-file checks do not enforce the combined server limit. Surface file-read and persistence failures instead of showing a saved/sent state. Deletion should atomically write a tombstone revision before removing the data, so already-scheduled saves cannot resurrect it; expire tombstones only after all writers from the previous generation are impossible.

**Regression tests.** Pause `arrayBuffer()`, send/switch/delete the draft, then release the read. The send is blocked while pending; the late completion cannot resurrect a deleted draft or attach the file to another draft. Create drafts simultaneously in two tabs and verify distinct IDs.

<a id="sync-01"></a>

### SYNC-01 — Multi-page Graph initial syncs lose the enumeration-complete signal

**Priority:** High  
**Evidence:** Source-confirmed; isolated two-page model exercised

**Pinned source:** [`mail-engine/graph/adapter.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/graph/adapter.go) — Sync; [`mail-engine/sync.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/sync.go) — resumeScan; [`mail-engine/adapter.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/adapter.go) — Changes.Complete contract.

**Problem and impact.** Graph determines `initial` from `cur == ""`. The first page of a multi-page initial response is not complete; every subsequent page has a nonempty cursor, so the final page also fails the `initial && nextLink == ""` completion test. A staged scan never reaches `FinishScan`, and can keep polling a delta link as though enumeration were still running. One-page mocks do not expose this bug.

**Implementation.** Persist the enumeration phase in a versioned cursor. Carry it through `nextLink` pages; only the terminal full-enumeration page sets `Complete`. Ordinary incremental delta responses must not be marked complete merely because they have no next page.

```go
type graphCursorV2 struct {
    URL         string `json:"url"`
    Enumerating bool   `json:"enumerating"`
}
func encodeGraphCursor(c graphCursorV2) mail.Cursor {
    b, _ := json.Marshal(c) // A struct of string/bool cannot fail to marshal.
    return mail.Cursor("graph-v2:" + base64.RawURLEncoding.EncodeToString(b))
}
func decodeGraphCursor(cur mail.Cursor) (graphCursorV2, error) {
    if cur == "" { return graphCursorV2{Enumerating: true}, nil }
    const prefix = "graph-v2:"
    if !strings.HasPrefix(string(cur), prefix) {
        return graphCursorV2{}, mail.ErrCursorInvalid
    }
    if len(cur) > 64<<10 { return graphCursorV2{}, mail.ErrCursorInvalid }
    b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(string(cur), prefix))
    if err != nil { return graphCursorV2{}, mail.ErrCursorInvalid }
    var c graphCursorV2
    if json.Unmarshal(b, &c) != nil || c.URL == "" {
        return graphCursorV2{}, mail.ErrCursorInvalid
    }
    return c, nil
}

func finishGraphPage(c graphCursorV2, nextLink, deltaLink string,
    changes *mail.Changes) error {
    if nextLink != "" {
        changes.More = true
        changes.Next = encodeGraphCursor(graphCursorV2{nextLink, c.Enumerating})
        return nil
    }
    if deltaLink == "" { return errors.New("graph: missing terminal delta link") }
    changes.More = false
    changes.Complete = c.Enumerating
    changes.Next = encodeGraphCursor(graphCursorV2{deltaLink, false})
    return nil
}
```

In `Sync`, use the decoded URL, retain the existing trusted Graph-endpoint validation, and set `EnumerationStart` on the original empty-cursor request. Decode errors must become the engine's typed reset path. Existing bare Graph cursors require an explicit migration: a running old-format scan should restart safely from empty, not have its ambiguous phase guessed. The simple decoder above deliberately resets legacy cursors; pair that rollout with SYNC-03 so a staged legacy cursor does not wedge recovery.

**Regression tests.** Two initial pages, three pages, `MaxPages=1` across process restarts, empty mailbox, and an incremental delta response with no changes. Exactly the final initial page completes enumeration.

<a id="sync-02"></a>

### SYNC-02 — Graph message identities are not made stable across folder moves

**Priority:** High  
**Evidence:** Source-confirmed omission; provider behavior documented, not live-tested

**Pinned source:** [`mail-engine/graph/adapter.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/graph/adapter.go) — authenticated HTTP requests and native message IDs; [`sendqueue.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/sendqueue.go) — Graph sending path.

**Problem and impact.** The adapter persists ordinary Graph message IDs but does not consistently request immutable IDs. Microsoft's default Outlook IDs can change when an item moves, which breaks identity-based local state and references. [Microsoft's immutable-ID documentation](https://learn.microsoft.com/en-us/graph/outlook-immutable-id) requires the preference on each relevant request, not only account setup. This finding concerns stable identity within one mailbox; moving to an archive mailbox is a different boundary.

**Implementation.** Add the preference centrally for all Graph message requests, including delta pages, direct message/body/attachment operations and draft/send operations where message IDs are involved. Preserve other preferences.

```go
type immutableGraphTransport struct{ base http.RoundTripper }
func (t immutableGraphTransport) RoundTrip(r *http.Request) (*http.Response, error) {
    clone := r.Clone(r.Context())
    clone.Header = r.Header.Clone()
    // Install this transport only on the already origin-restricted Graph client.
    clone.Header.Add("Prefer", `IdType="ImmutableId"`)
    base := t.base
    if base == nil { base = http.DefaultTransport }
    return base.RoundTrip(clone)
}
```

Do not simply turn the header on and leave old primary keys behind. Translate stored IDs with Graph's `translateExchangeIds` API in bounded batches, retain an old-to-new mapping, and transactionally migrate all message-keyed data: envelopes, bodies, memberships, product state, staged seen sets, note/reply references, and queued intents. Preserve case. Where cached browser keys cannot be translated, version/invalidate those caches explicitly without discarding drafts. Microsoft documents batches of up to 1,000 IDs and compatibility of delta links with both ID formats.

**Regression tests.** Move one message between folders and assert that its canonical local identity, note references, read/snooze state and body remain the same. Run an old-ID migration with failures partway through and verify resumability without duplicate messages.

<a id="sync-03"></a>

### SYNC-03 — An expired recovery-scan cursor is retried indefinitely instead of being replaced

**Priority:** High  
**Evidence:** Source-confirmed

**Pinned source:** [`mail-engine/sync.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/sync.go) — resumeScan reset/error path; [`mail-engine/store.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/store.go) — BeginScan replaces only scan bookkeeping; [`mail-engine/jmap/adapter.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/jmap/adapter.go) — expired baseline error in changesSince.

**Problem and impact.** If the provider rejects a staged scan's continuation, `resumeScan` returns an error and leaves that same continuation stored. The next run retries it. Avoiding an infinite loop *within one call* does not create recovery *across calls*. JMAP's expired baseline also becomes a generic error rather than the engine's typed cursor-invalid condition.

**Implementation.** On typed cursor expiry, discard the invalid scan bookkeeping and create a fresh scan while retaining live mail. Bound restart attempts per invocation; do not recursively retry an uncooperative provider. Normalize JMAP baseline expiry to `mail.ErrCursorInvalid`.

```go
// In changesSince's cannotCalculateChanges branch:
return "", nil, fmt.Errorf("jmap: baseline expired: %w", mail.ErrCursorInvalid)
```

The following decision helper makes the bounded reset policy explicit. Apply it to `Changes.Reset` and `errors.Is(err, mail.ErrCursorInvalid)` in `resumeScan`.

```go
type scanResetAction int
const (
    resetNotNeeded scanResetAction = iota
    resetAndContinue
    resetAndDefer
)
func scanResetDecision(reset bool, err error, alreadyRestarted bool) scanResetAction {
    if !reset && !errors.Is(err, ErrCursorInvalid) { return resetNotNeeded }
    if alreadyRestarted { return resetAndDefer }
    return resetAndContinue
}
```

For either reset action call `scans.BeginScan(ctx,acct,box)` once to replace the rejected continuation. For `resetAndContinue`, resume its empty cursor with the remaining page budget and a `restarted=true` flag. For `resetAndDefer`, leave the new empty scan durable, record a bounded retry time, and return a retryable provider error. Never call `FinishScan` or prune on a rejected or incomplete enumeration.

**Regression tests.** Expired continuation on a resumed scan; expiry during a first recovery; provider repeatedly rejecting even empty recovery; process restart after the replacement scan commits. Old readable mail must remain until a valid complete scan provides deletion evidence.

<a id="sync-04"></a>

### SYNC-04 — JMAP enumeration can skip valid mail and discard deletion evidence before pruning

**Priority:** High  
**Evidence:** Source-confirmed protocol/state-machine defects; offset-shift model exercised

**Pinned source:** [`mail-engine/jmap/adapter.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/jmap/adapter.go) — initialSync, changesSince, initial cursor decoding; [`mail-engine/sync.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/sync.go) — resumeScan ignores ChangeDestroyed; [`mail-engine/store.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/store.go) — ApplyScanPage and FinishScan.

**Problem and impact.** JMAP lists by numeric position without retaining/checking `queryState`. Capturing an Email state before enumeration does not stabilize that positional list. For example, read `[a,b]` from `[a,b,c,d]`; delete `a`; the next offset-two page is `[d]`, so unchanged `c` is skipped. Email/changes cannot report an unchanged skipped object. Marking the listing complete lets `FinishScan` prune valid local mail.

Two related defects compound recovery: the final catch-up reads only one `maxChanges=500` page and ignores `hasMoreChanges`; and catch-up destroys are returned as `ChangeDestroyed`, but staged scan processing explicitly ignores them. A message seen on an earlier page and later destroyed can remain in the seen set while the terminal cursor advances past its deletion. Malformed versioned initial cursors also decode to a zero state without an explicit reset, risking a restart mixed into existing scan bookkeeping.

**Implementation.** Represent listing and catch-up as distinct durable phases. Persist query state with the offset; refuse to authorize pruning when the query state changes. Either restart the listing without deleting live mail, or implement a correct queryChanges-based reconciliation. Drain catch-up to `hasMoreChanges=false` across bounded pages, persist its continuation, and apply negative evidence to the staged seen set in the same transaction as the corresponding page. [RFC 8620 sections 5.2, 5.5 and 5.6](https://www.rfc-editor.org/rfc/rfc8620.html#section-5.5) define the separate object-change and query-state contracts.

```go
type initialCursorV2 struct {
    Phase      string `json:"phase"` // "listing" or "catchup"
    Position   int    `json:"position"`
    QueryState string `json:"query_state"`
    Baseline   string `json:"baseline"`
    SinceState string `json:"since_state,omitempty"`
}
func verifyQueryPage(c initialCursorV2, returnedState string, position int) error {
    if returnedState == "" || position != c.Position {
        return fmt.Errorf("jmap: unusable query page: %w", mail.ErrCursorInvalid)
    }
    if c.QueryState != "" && returnedState != c.QueryState {
        return fmt.Errorf("jmap: query shifted during enumeration: %w", mail.ErrCursorInvalid)
    }
    return nil
}
```

After the final listing page, return `More=true`, `Complete=false`, and a cursor in `catchup` phase. Each catch-up page returns its changes and persists `newState`. Only the page with `hasMoreChanges=false` may set `Complete=true`. Every typed reset starts a new seen set, per SYNC-03.

Extend the scan-page transaction to accept destroyed IDs; for this mailbox, remove them from both staged presence and live membership before publishing the final cursor. The existing `FinishScan` orphan cleanup can then remove messages that have no remaining membership. Account-global JMAP destroys should ultimately be represented explicitly in the adapter contract rather than ambiguously as a mailbox-local removal.

```sql
-- In the same ApplyScanPage transaction, AFTER upserts/positive evidence:
DELETE FROM mirror_scan_seen
WHERE scan_id=$1 AND message_id=ANY($2::text[]);

DELETE FROM mail_message_mailboxes
WHERE account_id=$3 AND mailbox_id=$4 AND message_id=ANY($2::text[]);

-- Then advance mirror_scans.continuation; FinishScan may run only when
-- listing consistency and ALL catch-up pages have been verified.
```

For catch-up updates, record presence for the scanned mailbox only when the complete returned membership set actually contains it. Fail on missing/malformed required query or change state rather than treating a malformed page as an empty authoritative result. A perpetually changing mailbox may need queryChanges rather than repeated restarts; safe refusal to prune is preferable to a false completion.

**Regression tests.** Delete an early-page item; insert at the front; move a message away; modify an early-page message; destroy a previously seen message; generate more than 500 catch-up changes; expire the baseline; and resume each phase after a crash. Assert no valid skipped object is pruned and no deleted object is retained past its applied deletion state.

<a id="sync-05"></a>

### SYNC-05 — Policy reconciliation discards its own staged progress on every retry

**Priority:** High  
**Evidence:** Source-confirmed across job, engine, and store

**Pinned source:** [`reconcile.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/reconcile.go) — reconcileByFullEnumeration; [`mail-engine/sync.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/sync.go) — RequestRescan; [`mail-engine/store.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/store.go) — BeginScan.

**Problem and impact.** Every reconciliation attempt calls `RequestRescan`, which invokes `BeginScan` for every mailbox. `BeginScan` explicitly deletes existing scan and seen rows and starts from empty. If the preceding attempt exhausted its page budget, the next attempt discards that durable progress. Large mailboxes can repeatedly re-enumerate their first pages, and mailboxes that already completed are restarted too.

**Implementation — job-generation change.** Associate one rescan generation with a policy job and initialize it idempotently once. A retry resumes that generation; it does not call the destructive reset API again. Completion must remain recorded even when all temporary scan rows have been removed, so “no running scans” is not by itself permission to create another generation.

```sql
CREATE TABLE reconcile_scan_generations (
  account_id text NOT NULL REFERENCES mail_accounts(id) ON DELETE CASCADE,
  policy_version bigint NOT NULL,
  initialized boolean NOT NULL DEFAULT false,
  completed boolean NOT NULL DEFAULT false,
  PRIMARY KEY(account_id, policy_version)
);
```

Initialize the generation and all its mailbox scan rows in a single transaction under the engine's account maintenance lock. This SQL is the admission portion of a new store method; only the winner initializes scan rows. The generation row and scans must commit together.

```sql
INSERT INTO reconcile_scan_generations(account_id,policy_version)
VALUES ($1,$2) ON CONFLICT DO NOTHING;

SELECT initialized,completed FROM reconcile_scan_generations
WHERE account_id=$1 AND policy_version=$2 FOR UPDATE;

-- If completed: return success without new scans.
-- If initialized: resume existing scans without deleting their seen rows.
-- Otherwise create this generation's scans, then:
UPDATE reconcile_scan_generations SET initialized=true
WHERE account_id=$1 AND policy_version=$2;
```

Add a generation identifier to scan rows so completion can be checked against the correct job. Record generation completion transactionally when its last scan finishes. Change `reconcileByFullEnumeration` to call an `EnsureReconcileGeneration(account,version)` store/engine operation, followed by normal `SyncAccount`. Do not implement “ensure” by calling `BeginScan` for an already initialized generation. If a new desired policy supersedes the job, deliberately adopt or replace the generation under the same account lock.

**Regression tests.** More than `MaxPages` pages; multiple mailboxes with different lengths; failure immediately after generation initialization; completion followed by failed product finalization; restart between attempts. Provider page one must not be requested on every retry of the same generation.

<a id="sync-06"></a>

### SYNC-06 — Canceled reconciliation jobs can remain running until the server restarts

**Priority:** High  
**Evidence:** Source-confirmed

**Pinned source:** [`reconcile.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/reconcile.go) — runReconcileJob, finalizeReconcileJob, processReconcileJobs, resetStaleReconcileJobs.

**Problem and impact.** Execution and finalization use the same context. Once the job times out or is canceled, its attempt to change the persisted state fails immediately too. The row remains `running`, while normal processing selects only `pending` or `failed`. Boot-time reset does not recover this while the process continues serving.

**Implementation.** Finalize with a short, detached context and use leases to recover a worker that dies or cannot finalize. Detached finalization must still be part of the application's joined task group; do not introduce an untracked goroutine.

```go
jobErr := a.executeReconcileJob(ctx, job)
finalCtx, cancelFinal := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
finalErr := a.finalizeReconcileJob(finalCtx, job, jobErr)
cancelFinal()
return errors.Join(jobErr, finalErr)
```

The detached context fixes ordinary timeout finalization; a lease also handles process death or database unavailability during that finalization.

```sql
ALTER TABLE account_reconcile_jobs ADD COLUMN lease_until timestamptz;
ALTER TABLE account_reconcile_jobs ADD COLUMN lease_token uuid;

-- Include this recovery path in periodic processing, not only at boot.
UPDATE account_reconcile_jobs
SET state='pending', lease_until=NULL, lease_token=NULL
WHERE state='running' AND lease_until < now();
```

Claims must atomically assign a new token and deadline, renew a live long-running lease, and return the token to the worker. Every final state write must match **account, policy version, and lease token**, so an expired worker cannot finalize a replacement worker's job. Backfill existing running rows during the migration; a NULL legacy lease must not remain unrecoverable forever.

**Regression tests.** Context deadline during provider I/O; cancellation before finalization; database failure during finalization; expired lease reclaimed by a second worker; old worker finishes after reclaim. No job stays running forever or overwrites a newer claim.

<a id="sync-07"></a>

### SYNC-07 — Superseded policy jobs can lose required restoration work or apply stale destructive settings

**Priority:** High  
**Evidence:** Source-confirmed concurrency and replacement windows

**Pinned source:** [`reconcile.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/reconcile.go) — upsertReconcileJobTx, executeReconcileJob, finalizeReconcileJob; [`accounts.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/accounts.go) — retention/backfill transitions.

**Problem and impact.** Job replacement assigns `full_enumeration` from the new change alone. A restoration request can therefore be replaced by a later non-expanding change and lose its still-unfinished enumeration requirement. Execution reads retention/backfill once, then may perform long provider work before applying those old settings. The final version check is too late to prevent stale pruning. Finalization also reads the version without locking and later updates the applied version without a version predicate, allowing an older completion to race a newer one.

**Implementation.** Calculate reconciliation needs from the last **applied** policy, or conservatively preserve an outstanding enumeration requirement when replacing a job. Serialize destructive transitions against policy changes and check the captured version under that same lock. Guard applied-version updates in SQL, not only in a preceding SELECT.

```sql
-- In ON CONFLICT DO UPDATE, preserve unfinished restoration work:
full_enumeration = excluded.full_enumeration OR
  (account_reconcile_jobs.state <> 'complete'
   AND account_reconcile_jobs.full_enumeration)
```

Use a consistent lock order between settings updates, engine maintenance, and reconciliation. Within the transition transaction, lock the account's policy row, reject a superseded job, and apply backfill/retention using the values read under that lock. This requires transaction-aware pruning helpers; reading under a lock and then releasing it before pruning reintroduces the race.

```sql
SELECT policy_version,retention_days,backfill_days
FROM email_accounts WHERE mirror_account_id=$1 FOR UPDATE;

-- After verifying policy_version == the captured job version and applying
-- that policy under the same transition lock:
UPDATE email_accounts SET applied_policy_version=$2
WHERE mirror_account_id=$1 AND policy_version=$2;
```

Check exactly one affected row before marking that job complete, and condition the completion on its version and lease token. Do not stamp a superseding job failed just because an older one detects supersession. Combine this with SYNC-05 so preserving a full-enumeration flag also preserves meaningful progress.

**Regression tests.** Retention expansion immediately followed by a backfill change; new expansion while an old prune is paused; version two completes before version one attempts finalization; newer job replaces an older claimed row. A stale job may stop, but may not delete data according to an obsolete policy or lower an applied version.

<a id="jmap-01"></a>

### JMAP-01 — Referenced body parts missing from bodyValues are accepted as complete content

**Priority:** Medium  
**Evidence:** Source-confirmed

**Pinned source:** [`mail-engine/jmap/adapter.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/jmap/adapter.go) — Body textBody/htmlBody loops; [`mail-engine/sync.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/sync.go) — body caching.

**Problem and impact.** Truncated and encoding-error values are rejected, which is good. But when a referenced part has no `bodyValues` entry, the loop silently skips it and returns success. Missing text or HTML can then be cached as an authoritative complete body. Reopening the message does not necessarily repair that cached success.

**Implementation.** Treat a missing referenced body value as incomplete too; distinguish a genuinely empty message with no renderable parts from a failed fetch of a declared part.

```go
for _, p := range m.TextBody {
    v, ok := m.BodyValues[p.PartID]
    if !ok || v.IsTruncated || v.IsEncodingProblem {
        return nil, fmt.Errorf("jmap: %w: text part %s of %s",
            errIncompleteBody, p.PartID, id)
    }
    body.Text += v.Value
}
for _, p := range m.HTMLBody {
    v, ok := m.BodyValues[p.PartID]
    if !ok || v.IsTruncated || v.IsEncodingProblem {
        return nil, fmt.Errorf("jmap: %w: HTML part %s of %s",
            errIncompleteBody, p.PartID, id)
    }
    body.HTML += v.Value
}
```

An optional bounded raw-message fallback should use the existing MIME parser and return success only after complete parsing. The present error comment should not promise a fallback that no caller implements. Keep failed/incomplete content retryable and never stamp `fetched_at` for it.

**Regression tests.** Missing first part, missing second part, absent HTML value with present text, explicitly truncated value, and a legitimate empty message. Only the last case may be an empty successful result.

<a id="jmap-02"></a>

### JMAP-02 — Discovered JMAP API and download destinations lack an explicit credential-transport trust policy

**Priority:** Medium  
**Evidence:** Source-confirmed validation gap; malicious/misconfigured-provider scenario

**Pinned source:** [`mail-engine/jmap/adapter.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/jmap/adapter.go) — Dial/session metadata, call and download; [`resolver.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/resolver.go) — JMAP connection construction.

**Problem and impact.** Session discovery starts at an HTTPS URL, but the returned API/download URLs are subsequently used with a bearer token without an explicit scheme/origin policy at those credential-adding call sites. A bad discovery document can redirect credentials to an insecure or unapproved destination. A configurable mailbox host intentionally supports operator-selected servers; that is not, by itself, proof of unauthenticated SSRF.

**Implementation.** Require HTTPS for credential-bearing JMAP requests, reject userinfo/fragments, and validate the target before attaching credentials. Support legitimate distinct API/download origins through an explicit trusted configuration or provider policy, not by treating arbitrary returned metadata as an authorization to receive the token. Enforce the same policy on redirects. [RFC 8620 transport-security considerations](https://www.rfc-editor.org/rfc/rfc8620.html#section-8.1) are the protocol reference.

```go
func trustedHTTPSURL(raw string, allowed map[string]bool) (*url.URL, error) {
    u, err := url.Parse(raw)
    if err != nil || u.Scheme != "https" || u.Hostname() == "" ||
        u.User != nil || u.Fragment != "" {
        return nil, errors.New("invalid credential-bearing endpoint")
    }
    // Configure normalized scheme://host[:port] entries explicitly.
    origin := strings.ToLower(u.Scheme + "://" + u.Host)
    if !allowed[origin] { return nil, errors.New("untrusted endpoint origin") }
    return u, nil
}

client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
    if len(via) >= 5 { return errors.New("too many provider redirects") }
    _, err := trustedHTTPSURL(req.URL.String(), trustedOrigins)
    return err
}
```

Validate expanded download templates as well as the session's API URL. Normalize configured default ports consistently so equivalent origins do not create surprising policy decisions. For explicitly supported local test services, use a separate opt-in development transport; never infer permission to send credentials over HTTP from a redirect.

**Regression tests.** HTTP API URL, HTTP download URL, userinfo URL, redirect downgrade, untrusted origin, and an explicitly approved separate HTTPS download origin. Assert that no rejected request reaches a transport with an Authorization header.

<a id="data-02"></a>

### DATA-02 — Unified keyset pagination is not unique across connected accounts

**Priority:** Medium  
**Evidence:** Source-confirmed; isolated ordering reproduction

**Pinned source:** [`classify.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/classify.go) — handleBucket and handleSearch ordering and cursor construction.

**Problem and impact.** Lists order by received time and message ID, and the continuation records those two values. Message IDs are only account-scoped; two account copies may have exactly the same ID and received timestamp. At a page boundary, the next page's strict comparison can skip the second copy. Other handlers correctly require an account to disambiguate IDs, but pagination does not carry that same identity rule.

**Implementation.** Add `account_id` as the final sort key and persist it in a versioned cursor. Update all list/query builders and cursor decoding together. Reject or explicitly restart legacy continuation tokens rather than silently changing their meaning.

```go
type listCursorV2 struct {
    Version    int        `json:"v"`
    ReceivedAt *time.Time `json:"received_at"`
    ID         string     `json:"id"`
    Account    string     `json:"account"`
}
```

For a non-null cursor timestamp, use this predicate (bind the three values at the query's actual parameter offsets):

```sql
AND (
  m.received_at < $3 OR m.received_at IS NULL
  OR (m.received_at = $3 AND (m.id,m.account_id) < ($4,$5))
)
ORDER BY m.received_at DESC NULLS LAST, m.id DESC, m.account_id DESC
```

For a null cursor timestamp:

```sql
AND m.received_at IS NULL AND (m.id,m.account_id) < ($3,$4)
ORDER BY m.received_at DESC NULLS LAST, m.id DESC, m.account_id DESC
```

Build the outgoing cursor from the last **returned** row, including its account. Preserve the existing `limit+1` probe behavior. Add a compatible index after checking query plans; if using `CREATE INDEX CONCURRENTLY`, run it outside a transaction-managed migration.

**Regression tests.** Equal timestamp and message ID in two accounts at the boundary; null timestamps; a page of size one; new arrivals between pages; identical native thread IDs across accounts. Every eligible account/message pair appears once.

<a id="data-03"></a>

### DATA-03 — The legacy default snooze stores NULL instead of a three-day deadline

**Priority:** Medium  
**Evidence:** Source-confirmed

**Pinned source:** [`classify.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/classify.go) — handleMessageAction / parseSnoozeUntil; [`mcp/tools.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mcp/tools.go) — message_action default and schema description.

**Problem and impact.** With `action=set_aside` and neither `until` nor a positive `until_days`, parsing returns a nil value. The caller's integer branch containing the three-day default is not taken, and the final query creates `bucket='set_aside'` with a NULL deadline. It will not wake on the documented three-day schedule. Separately, the MCP description says an empty until means someday, while its implementation sends three days for that case.

**Implementation.** Return the intended legacy integer default from the parser; preserve explicit `until:null` as the distinct someday behavior. Make the MCP description and omitted-value behavior agree.

```go
// At the end of parseSnoozeUntil, after rejecting negative until_days:
return 3, false, "" // omitted fields mean the documented three-day default
```

An explicit parser helper makes the distinction testable without HTTP fixtures:

```go
func legacySnoozeDays(days int) (int, error) {
    if days < 0 || days > 3650 { return 0, errors.New("invalid snooze days") }
    if days == 0 { return 3, nil }
    return days, nil
}
```

In the MCP tool, describe omission as three days and the literal `"null"` as someday, or change the tool argument to preserve omitted versus explicit JSON null and implement a single shared contract. Do not reinterpret already queued absolute snoozes relative to replay time.

**Regression tests.** Omitted fields produce a non-null three-day deadline; explicit null produces `later` and no date; valid absolute timestamps are unchanged; negative and over-limit relative values fail validation; MCP's default agrees with its schema text.

<a id="data-04"></a>

### DATA-04 — Thread reads return unbounded cached message bodies despite a bounded eager-fetch count

**Priority:** Medium  
**Evidence:** Source-confirmed resource gap

**Pinned source:** [`classify.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/classify.go) — handleThread query and accumulated response; [`dashboard/src/app/lib/api.ts`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/lib/api.ts) — JSON response parsing and caching.

**Problem and impact.** The eight-message eager-fetch limit bounds new provider fetches, not the SQL result or already cached bodies. The thread endpoint reads and accumulates every message and body in a thread, then serializes all of them. Large threads or unusually large bodies can consume substantial server and browser memory, and the browser may persist the entire response again.

**Implementation — API pagination.** Paginate thread envelopes and load bodies per message. Keep the account-scoped identity and stable timestamp/ID cursor. Do not silently truncate content while still reporting `body_status='ready'`.

```sql
-- Envelope page; cached body bytes are deliberately not selected here.
SELECT m.id,m.account_id,m.subject,m.received_at,
       EXISTS(SELECT 1 FROM mail_bodies b
              WHERE b.account_id=m.account_id AND b.message_id=m.id) AS cached_body
FROM mail_messages m
JOIN email_accounts ea ON ea.mirror_account_id=m.account_id AND ea.user_id=$1
WHERE m.account_id=$2 AND m.thread_id=$3
ORDER BY m.received_at ASC NULLS LAST,m.id ASC
LIMIT $4; -- bounded pageSize+1; continuation uses the same total order
```

Add an authenticated account/message body endpoint with an explicit maximum decoded response size and a separate raw-download fallback for oversized content. For the existing endpoint during migration, enforce a small page size and return `has_more`/`next_cursor`; update the reader to fetch remaining pages rather than assuming the first response is the whole thread. Give cached bodies an LRU/byte budget, while keeping unsent drafts and mutation intents outside eviction.

```ts
const MAX_BODY_CACHE_BYTES = 32 * 1024 * 1024;
function bodyBytes(text: string, html: string): number {
  const encoder = new TextEncoder();
  return encoder.encode(text).byteLength + encoder.encode(html).byteLength;
}
// Persist each body's measured bytes; evict oldest expendable body-cache
// entries transactionally until the configured budget is met.
```

**Regression tests.** Thousands of messages, one oversized body, mixed cached/missing bodies, and navigation through all pages. Verify bounded resident memory and complete access to content through pagination or explicit download.

<a id="data-05"></a>

### DATA-05 — Creating a connected account is split across two database transactions

**Priority:** Medium  
**Evidence:** Source-confirmed crash window

**Pinned source:** [`accounts.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/accounts.go) — createAccount / store.PutAccount outside the product transaction; [`mail-engine/store.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/store.go) — PutAccount.

**Problem and impact.** The mirror account is committed via the engine store's pool while the product's `email_accounts` transaction remains separate. The deferred cleanup is helpful for ordinary errors but cannot run if the process dies between those commits. That leaves an orphan mirror account. The same pattern should be checked in OAuth account provisioning. A SQL transaction object does not make a separate pool call part of its transaction.

**Implementation.** Create both records on one underlying database transaction. Expose a transaction-aware engine account writer rather than hiding a second commit inside `PutAccount`. Since both schemas live in the same PostgreSQL database, the product can also execute the engine account insert through its own transaction, keeping the store's schema contract explicit.

```go
func putMirrorAccountTx(ctx context.Context, tx *sql.Tx,
    id, provider, email, name string) error {
    _, err := tx.ExecContext(ctx, `
        INSERT INTO mail_accounts(id,provider,email,name,needs_reauth)
        VALUES ($1,$2,$3,$4,false)`, id, provider, email, name)
    return err
}
// Call this through the SAME tx as INSERT INTO email_accounts.
// Commit once, then launch initial sync. Rollback covers both records.
```

Retain provider credential validation before the transaction, with a bounded timeout; do not hold a database transaction during a network login. Treat duplicate-address constraint violations as 409 rather than a generic 500, including when two validated requests race past the preliminary existence check. Migrate or remove already orphaned mirror accounts only after proving they have no owning product account and no active work.

**Regression tests.** Fault injection between the two inserts, transaction rollback, process kill before/after commit, and concurrent duplicate connections. There must be either one fully connected account or no account, never a committed orphan from the failed operation.

<a id="api-03"></a>

### API-03 — Some accepted JSON responses commit headers before Content-Type is set

**Priority:** Low  
**Evidence:** Source-confirmed; HTTP behavior reproduced with httptest

**Pinned source:** [`accounts.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/accounts.go) — accepted backfill/retention responses; [`app.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/app.go) — writeJSON helper.

**Problem and impact.** Calling `WriteHeader(202)` and then `writeJSON` sets the JSON content type after headers have already been committed. Clients cannot reliably identify the response as JSON. Inspecting `ResponseRecorder.Header()` after the handler is misleading; the actual committed headers are in `ResponseRecorder.Result()`.

**Implementation.** Use one helper that serializes before committing headers and accepts a status code; replace late `WriteHeader`/`writeJSON` pairs.

```go
func writeJSONStatus(w http.ResponseWriter, status int, value any) {
    raw, err := json.Marshal(value)
    if err != nil {
        http.Error(w, "response encoding failed", http.StatusInternalServerError)
        return
    }
    w.Header().Set("Content-Type", "application/json; charset=utf-8")
    w.WriteHeader(status)
    _, _ = w.Write(append(raw, '\n'))
}
// Example: writeJSONStatus(w, http.StatusAccepted, result)
```

Keep existing security/cache headers intact. An encoding error must not follow an already committed success status.

**Regression tests.** Assert status and `Result().Header.Get("Content-Type")` for accepted policy changes. The underlying late-header failure and correct ordering were reproduced in the isolated Go harness.

<a id="api-04"></a>

### API-04 — Account-list responses omit reconciliation status even though the response type declares it

**Priority:** Medium  
**Evidence:** Source-confirmed response omission

**Pinned source:** [`accounts.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/accounts.go) — accountJSON.Reconcile and listAccountsJSON; [`reconcile.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/reconcile.go) — reconcileJob.asJSON.

**Problem and impact.** `accountJSON` declares a reconciliation field, but `listAccountsJSON` selects no job fields and never fills it. A client polling the connected-account list cannot distinguish a desired policy from a successfully applied one, or see a failed restoration job through that response. This report does not assume every view only uses that endpoint; the list contract itself is incomplete.

**Implementation.** Load job state together with the account row, avoiding one extra query per account and avoiding a nested query while list rows remain open. Also expose desired and applied policy versions.

```sql
SELECT ea.id,ea.policy_version,ea.applied_policy_version,
       j.state,j.policy_version,j.full_enumeration,j.last_error
FROM email_accounts ea
LEFT JOIN account_reconcile_jobs j ON j.account_id=ea.mirror_account_id
WHERE ea.user_id=$1
ORDER BY ea.created_at;
```

Merge those columns into the existing account-list SELECT, scan nullable joined fields, and populate `Reconcile` only when a job exists. Use the existing `asJSON()` method for a single status shape. Keep internal provider details out of user-facing errors where they could contain secrets.

```go
if jobState.Valid {
    acc.Reconcile = map[string]any{
        "state": jobState.String,
        "policy_version": jobVersion.Int64,
        "full_enumeration": fullEnumeration.Bool,
    }
    if jobError.Valid && jobError.String != "" {
        acc.Reconcile["last_error"] = jobError.String
    }
}
```

**Regression tests.** No job, pending, running, failed, superseded and complete jobs. List and single-account endpoints must agree; a policy version is not presented as applied until its transition completes.

Add `PolicyVersion int64` and `AppliedPolicyVersion int64` to `accountJSON`, tagged `policy_version` and `applied_policy_version`, and fill them from the joined query. This is an additive API change; existing clients can ignore the new fields.


<a id="send-03"></a>

### SEND-03 — The attachment decoder rejects a valid zero-byte attachment

**Priority:** Low  
**Evidence:** Source-confirmed input-validation defect

**Pinned source:** [`sendqueue.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/sendqueue.go) — attachment request validation and base64 decoding.

**Problem and impact.** `decodeAttachments` treats an empty base64 string as missing data. It is also the valid representation of a zero-byte file, so an intentionally empty attachment is rejected even when its metadata is present.

**Implementation.** Change the existing request field to a pointer, preserving its JSON name, and distinguish absence from a present empty string. Replace the decoder with the following version; update existing Go request fixtures to use string pointers.

```go
type sendAttachmentRequest struct {
    Filename    string  `json:"filename"`
    ContentType string  `json:"content_type"`
    DataB64     *string `json:"data_base64"`
}

func decodeAttachments(reqs []sendAttachmentRequest, totalBytes int) ([]mail.Attachment, string) {
    if len(reqs) == 0 { return nil, "" }
    if len(reqs) > 20 { return nil, "at most 20 attachments per message" }
    out := make([]mail.Attachment, 0, len(reqs))
    total := 0
    for i, ra := range reqs {
        if ra.DataB64 == nil {
            return nil, fmt.Sprintf("attachment %d has no data", i+1)
        }
        if len(*ra.DataB64) > base64.StdEncoding.EncodedLen(15<<20) {
            return nil, fmt.Sprintf("attachment %d exceeds the encoded size limit", i+1)
        }
        data, err := base64.StdEncoding.DecodeString(*ra.DataB64)
        if err != nil {
            return nil, fmt.Sprintf("attachment %d is not valid base64", i+1)
        }
        if len(data) > 15<<20 {
            return nil, fmt.Sprintf("attachment %d exceeds 15 MiB", i+1)
        }
        total += len(data)
        if total > totalBytes { return nil, "attachments exceed the total size limit" }
        out = append(out, mail.Attachment{
            Filename: ra.Filename, ContentType: ra.ContentType, Data: data,
        })
    }
    return out, ""
}
```

The wire-size precheck deliberately requires reasonably compact base64. Keep the existing overall 34 MiB request limit, transport-specific validation, and MIME filename/header handling. Rendering must retain the attachment part even when `len(Data)==0`.

**Regression tests.** Missing field fails; present empty string succeeds; malformed base64 fails; existing file/count/aggregate limits still apply; rendered MIME retains the zero-byte attachment part.

<a id="gmail-01"></a>

### GMAIL-01 — Raw-message decoding is less tolerant than the existing Gmail part decoder

**Priority:** Low  
**Evidence:** Source-confirmed inconsistency; padded-payload compatibility case

**Pinned source:** [`mail-engine/gmail/adapter.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/gmail/adapter.go) — Raw and decodeURLBytes.

**Problem and impact.** The raw-message path uses a no-padding-only base64url decoder, while the same adapter already has a bounded decoder supporting both padded and unpadded values. A padded value fails the raw export and causes a lossy mirror fallback. No claim is made that the current Gmail service routinely returns padded raw messages; the defect is an avoidable compatibility inconsistency at an external-data boundary.

**Implementation.** Reuse the existing decoder and avoid an extra byte-slice-to-string copy when returning the stream.

```go
raw, err := decodeURLBytes(m.Raw)
if err != nil { return nil, fmt.Errorf("gmail: decode raw: %w", err) }
return io.NopCloser(bytes.NewReader(raw)), nil
```

This reuses the adapter's encoded-size guard. It does not by itself bound the Google SDK's earlier JSON response allocation; transport-level response budgets remain a separate concern.

**Regression tests.** Padded `YQ==`, unpadded `YQ`, malformed input, and oversized encoded data. Both valid representations decode to the same byte and preserve the provider-original export path.

<a id="ops-01"></a>

### OPS-01 — Byte limits and decode semaphores do not bound slow request-body lifetimes

**Priority:** Medium  
**Evidence:** Source-confirmed timeout/admission gap

**Pinned source:** [`cmd_serve.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/cmd_serve.go) — HTTP server timeout configuration; [`sendqueue.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/sendqueue.go) — acquireDecodeSlot and JSON body decoding; [`app.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/app.go) — request lifecycle.

**Problem and impact.** The server sets a header timeout, but the send decoder can hold one of only two decode slots while reading an arbitrarily slow body. A request context is not a body-read deadline. Two slow authenticated send requests can block other sends; similar long-lived reads can hold account/owner leases and delay deletion. Existing byte and retained-send budgets are valuable but solve a different resource dimension.

**Implementation.** Set an explicit bounded read deadline before admission/decoding, give the operation a context deadline, and distinguish timeout from malformed JSON. Avoid a blanket short write timeout that would break long downloads or event streams.

```go
func withBodyDeadline(next http.HandlerFunc, limit time.Duration) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        ctx, cancel := context.WithTimeout(r.Context(), limit)
        defer cancel()
        controller := http.NewResponseController(w)
        if err := controller.SetReadDeadline(time.Now().Add(limit)); err != nil {
            // In production require supported net/http transport behavior;
            // test wrappers should implement Unwrap or an explicit deadline.
            writeProblem(w, http.StatusServiceUnavailable, "Read Deadline Unavailable",
                "request transport cannot enforce the upload budget")
            return
        }
        defer controller.SetReadDeadline(time.Time{})
        next(w, r.WithContext(ctx))
    }
}
// Apply to body-bearing endpoints, e.g. withBodyDeadline(a.handleSend, 45*time.Second).
```

Place this deadline middleware **outside** the idempotency response recorder so it receives the real transport writer; alternatively make wrappers implement `Unwrap() http.ResponseWriter`. Set `IdleTimeout` for keep-alive connections and preserve `ReadHeaderTimeout`. Release the decode slot as soon as decoding/validation no longer needs its memory budget, rather than retaining it unnecessarily through unrelated provider work; keep the independent accepted-send budget until delivery finishes. Configure upload limits for expected networks and surface a retryable, clear timeout without deleting the draft.

**Regression tests.** Slowly streamed body, client that never finishes JSON, cancellation while waiting for a decode slot, and shutdown while a slot is occupied. Another valid client must regain admission after the bounded timeout.

<a id="ops-02"></a>

### OPS-02 — Some background database work cannot observe the worker context

**Priority:** Medium  
**Evidence:** Source-confirmed cancellation gap

**Pinned source:** [`mail.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail.go) — scheduler Include lookup and purgeExpired database calls; [`lifecycle.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/lifecycle.go) — task draining.

**Problem and impact.** Background paths use context-free database calls even though their worker has a cancellation context. A stalled database query can keep shutdown waiting after the surrounding task was canceled. Merely selecting on context elsewhere in the loop does not interrupt an already running context-free call.

**Implementation.** Pass the worker context to every database operation and apply a per-operation budget. Return failures rather than silently treating a database outage as a disabled account or successful cleanup.

```go
func execMaintenance(ctx context.Context, db *sql.DB, query string, args ...any) error {
    operationCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
    defer cancel()
    _, err := db.ExecContext(operationCtx, query, args...)
    return err
}

// In scheduler inclusion checks:
lookupCtx, cancel := context.WithTimeout(workerCtx, 5*time.Second)
err := a.db.QueryRowContext(lookupCtx,
    `SELECT sync_enabled FROM email_accounts WHERE mirror_account_id=$1`,
    accountID).Scan(&enabled)
cancel()
```

Use the actual callback context where supplied by the scheduler, or a context linked to the service lifetime; do not substitute an unbounded `context.Background()`. Cleanup after cancellation should use a separately bounded finalization context only when that cleanup is genuinely required, as in SYNC-06.

**Regression tests.** A blocked PostgreSQL statement is interrupted by worker cancellation; cleanup errors are logged with the operation name; shutdown drains without closing the pool underneath a still-running query.

<a id="ops-03"></a>

### OPS-03 — Export budgets are per request, while aggregate disk, memory, and canceled work remain unbounded

**Priority:** Medium  
**Evidence:** Source-confirmed resource-control gaps

**Pinned source:** [`export.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/export.go) — handleAccountExport, exportPlan, message loop and budgetedWriter.

**Problem and impact.** The 2 GiB archive cap and 128 MiB raw-message cap are useful, but multiple concurrent exports multiply those budgets. `exportPlan` also materializes message envelopes for every mailbox before writing, including repeated memberships. The message loop does not explicitly stop on request cancellation; a canceled provider read may be treated as a reason to generate a mirror fallback, continuing archive work for a disconnected client.

**Implementation.** Add aggregate admission, a global disk reservation/quota policy, and cancellation checks that are not converted to lossy success. Page envelope plans rather than holding the full account in memory. Add an uncompressed-work budget as well as the compressed ZIP-size cap.

```go
var exportSlots = make(chan struct{}, 1) // Move into App for instance-level configuration.
func acquireExport(ctx context.Context) (func(), error) {
    select {
    case exportSlots <- struct{}{}:
        return func(){ <-exportSlots }, nil
    case <-ctx.Done(): return nil, ctx.Err()
    default: return nil, errors.New("an export is already running")
    }
}

// At handler entry, before exportPlan / provider calls:
releaseExport, err := acquireExport(r.Context())
if err != nil {
    w.Header().Set("Retry-After", "30")
    writeProblem(w, http.StatusTooManyRequests, "Export Busy", "retry the export shortly")
    return
}
defer releaseExport()

// At the start of EVERY mailbox/message iteration:
if err := r.Context().Err(); err != nil { return }
// After a provider read fails:
if errors.Is(rawErr, context.Canceled) || errors.Is(rawErr, context.DeadlineExceeded) {
    return // cancellation is not a successful degraded export
}
```

A provider-specific timeout may justify fallback only while the overall request remains active; distinguish the two contexts. Treat a mirror-body database error differently from a body that was never cached, and record it accurately or fail before committing a download. Keep the existing temporary-file cleanup and restrictive creation permissions. For accounts exceeding a single archive's cap, offer bounded mailbox/date exports instead of making the only export option permanently fail.

**Regression tests.** Parallel exports, cancellation midway through a large account, highly compressible huge content, full disk, mirror read failure, and a failed provider. Provider unavailability may degrade with an explicit manifest; resource/cancellation failures must not masquerade as a complete export.

<a id="web-01"></a>

### WEB-01 — The service worker has no automatic deployment-versioned cache lifecycle

**Priority:** Medium  
**Evidence:** Source-confirmed stale-cache and growth risk

**Pinned source:** [`dashboard/public/service-worker.js`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/public/service-worker.js) — constant VERSION, install asset discovery, generic cache-first branch; [`Dockerfile`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/Dockerfile) — dashboard build.

**Problem and impact.** The cache name is a fixed `lull-shell-v1`, while fingerprinted bundles accumulate and non-fingerprinted assets use a cache-first fallback. A new dashboard build need not change the worker script, so it need not trigger a new install or retire the previous cache. Install-time HTML discovery also does not guarantee that dynamically imported route chunks are available offline. This is a deployment/offline correctness issue, not a claimed auth-response leak: actual auth calls are `/api/auth/...` and are excluded from the worker.

**Implementation.** Inject a content-derived build ID and a complete asset manifest during the dashboard build, then cache only explicitly public shell/static resources. Keep `/api/` network-only. Coordinate activation with open clients rather than unconditionally mixing an old page with a newly claimed worker.

```js
// Generated by the build, not a hand-maintained forever-constant name:
const VERSION = `lull-shell-${BUILD_ID}`;
const PUBLIC_STATIC = new Set(PRECACHE_MANIFEST);

function mayCache(request) {
  const u = new URL(request.url);
  return request.method === 'GET' && u.origin === self.location.origin &&
    !u.pathname.startsWith('/api/') && PUBLIC_STATIC.has(u.pathname);
}
```

A build script can derive `BUILD_ID` from the generated manifest and asset bytes; ensure the injected worker bytes change whenever the shell/asset set changes. Cache immutable hashed assets first, but fetch navigational shell HTML from the network with an offline fallback. Delete old version caches during a coordinated activation. Precache lazy JS/CSS chunks from the build manifest rather than scraping only the root page's script tags.

**Regression tests.** Install version A, open a client, deploy B with unchanged worker source but changed build assets, then reload online/offline. Verify B is installed, obsolete caches retire, navigation and lazy routes work offline, and `/api/auth/status` is never cached.

<a id="web-02"></a>

### WEB-02 — Raw email HTML is parsed before remote-content removal can be guaranteed

**Priority:** Medium  
**Evidence:** Conditional privacy risk; not browser-reproduced in this environment

**Pinned source:** [`dashboard/src/app/reader/Body.tsx`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/reader/Body.tsx) — clean / DOMParser preprocessing and iframe document construction.

**Problem and impact.** The reader first parses untrusted HTML with DOMParser and then removes or rewrites remote resources. The iframe's restrictive CSP applies to the resulting iframe, not necessarily to preliminary parsing. [MDN documents that a parsed inert HTML document can still download resources](https://developer.mozilla.org/en-US/docs/Web/API/DOMParser/parseFromString). Consequently, post-parse removal alone is not a proven “no tracking requests before consent” boundary. This audit could not validate actual network behavior in the target browsers because the available browser environment blocked navigation; this finding is explicitly conditional, not a demonstrated tracking exploit.

**Implementation.** For a minimal fail-safe mode, render the stored plain-text body without first parsing raw HTML. Preserve text through escaping, not HTML interpretation.

```ts
function privatePlainDocument(text: string): string {
  const entities: Record<string, string> = {
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  };
  const escaped = text.replace(/[&<>"']/g, char => entities[char]!);
  return '<!doctype html><meta charset="utf-8">' +
    '<meta http-equiv="Content-Security-Policy" content="default-src \'none\'">' +
    '<pre>' + escaped + '</pre>';
}
```

Use that path when remote content is blocked until the rich path is verified. A permanent rich implementation should sanitize with a network-free parser before any browser DOM parsing (for example a server-side HTML tree parser plus an explicit element/attribute/URL policy), then apply iframe CSP and sandboxing as independent defenses. Do not fix this with a raw HTML regex or by enabling scripts in the iframe. Preserve the current no-script sandbox.

**Regression tests.** Real browsers with a local request recorder: `<img>`, `<iframe>`, CSS URLs/imports, SVG links, srcset, posters, malformed markup and redirects. Before consent, assert zero remote requests during both preprocessing and iframe rendering. Test browser engines separately.

<a id="web-03"></a>

### WEB-03 — Inline cid images have no complete metadata-to-rendering path

**Priority:** Medium  
**Evidence:** Source-confirmed functionality gap

**Pinned source:** [`dashboard/src/app/reader/Body.tsx`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/reader/Body.tsx) — cid URL handling; [`classify.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/classify.go) — attachment response shape and parseAttachments; [`mail-engine/jmap/adapter.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/jmap/adapter.go) — BodyPart ContentID population; [`mail-engine/gmail/adapter.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/gmail/adapter.go) — inline Content-ID metadata.

**Problem and impact.** Keeping a `cid:` image reference in sanitized HTML is not enough to render an embedded image in a browser iframe. The engine knows Content-ID values, but the thread response's attachment shape does not expose a complete inline-part mapping for the renderer. Inline logos and other embedded mail images can remain broken even though their bytes exist at the provider.

**Implementation.** Expose an explicit inline-parts array with account, message, part ID, content ID and MIME type, separate from downloadable attachments. Fetch only referenced, allowed inline image parts through the authenticated attachment endpoint; create local Blob URLs and rewrite normalized exact CID matches. Do not enable arbitrary remote images to repair CID images.

```go
type inlinePartJSON struct {
    PartID    string `json:"part_id"`
    ContentID string `json:"content_id"`
    Type      string `json:"type"`
}
func inlineParts(parts []mail.BodyPart) []inlinePartJSON {
    var out []inlinePartJSON
    for _, p := range parts {
        cid := strings.Trim(strings.TrimSpace(p.ContentID), "<>")
        if cid != "" && (p.Type == "image/png" || p.Type == "image/jpeg" ||
            p.Type == "image/gif" || p.Type == "image/webp") {
            out = append(out, inlinePartJSON{p.PartID, cid, p.Type})
        }
    }
    return out
}
```

```ts
function inlineImageURL(bytes: ArrayBuffer, type: string): string {
  if (!['image/png', 'image/jpeg', 'image/gif', 'image/webp'].includes(type)) {
    throw new Error('Unsupported inline image type');
  }
  return URL.createObjectURL(new Blob([bytes], { type }));
}
// Map cid -> generated URL only after authenticated, size-bounded downloads.
// Revoke every generated URL on message change/unmount, and after failed loads.
```

Preserve owner-generation checks across downloads. Set a small total inline-byte/count budget and ensure the iframe's image CSP permits only the needed generated Blob URLs. Treat duplicate CIDs deterministically and avoid SVG until its separate active-content/security implications are handled.

**Regression tests.** One valid CID image, multiple inline images, missing part, duplicate CID, uppercase/lowercase content identifiers according to the chosen normalization policy, owner change during download, and a remote image alongside a CID image. CID rendering must not imply remote-content consent.

<a id="mcp-01"></a>

### MCP-01 — The MCP client accepts insecure remote origins for bearer-token requests

**Priority:** Medium  
**Evidence:** Source-confirmed configuration/transport hardening gap

**Pinned source:** [`mcp/client.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mcp/client.go) — newClient and default http.Client.

**Problem and impact.** Client construction checks for an absolute URL but does not require HTTPS for a remote service. A mistaken `http://` configuration sends the agent bearer token in cleartext. Default redirect handling also does not express an application-specific same-origin/no-downgrade policy. This requires insecure configuration or a redirect; it is not an unauthenticated server-side exploit.

**Implementation.** Require HTTPS, allowing HTTP only for literal loopback development addresses. Reject userinfo, query and fragment in the configured origin and restrict redirects to the configured origin.

```go
func validateMCPOrigin(raw string) (*url.URL, error) {
    u, err := url.Parse(raw)
    if err != nil || u.Hostname() == "" || u.User != nil ||
        u.RawQuery != "" || u.Fragment != "" {
        return nil, errors.New("LULL_URL must be a clean absolute origin")
    }
    ip := net.ParseIP(u.Hostname())
    loopback := strings.EqualFold(u.Hostname(), "localhost") ||
        (ip != nil && ip.IsLoopback())
    if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
        return nil, errors.New("remote LULL_URL must use HTTPS")
    }
    return u, nil
}

func sameOriginRedirect(base *url.URL) func(*http.Request, []*http.Request) error {
    return func(req *http.Request, via []*http.Request) error {
        if len(via) >= 5 || !strings.EqualFold(req.URL.Scheme, base.Scheme) ||
            !strings.EqualFold(req.URL.Host, base.Host) {
            return errors.New("cross-origin or excessive API redirect rejected")
        }
        return nil
    }
}
// newClient: validate base; set http.Client.CheckRedirect to this policy.
```

Keep the current request timeout and response-size cap. If a reverse proxy intentionally redirects to a different public origin, configure that final origin directly instead of relaxing token forwarding. Treat agent tokens as broad account capabilities in documentation and avoid placing them in command-line arguments or logs.

**Regression tests.** Remote HTTP rejected; HTTPS accepted; literal loopback development supported; cross-origin and downgrade redirects rejected before a credential-bearing request reaches the new destination.

<a id="ui-01"></a>

### UI-01 — Reauthentication retry failures can disappear after the modal has already closed

**Priority:** Medium  
**Evidence:** Source-confirmed UI control-flow issue

**Pinned source:** [`dashboard/src/app/views/SecurityView.tsx`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/views/SecurityView.tsx) — confirmReauth, gated and confirmation modal state; [`reauth.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/reauth.go) — password-only confirmation flow.

**Problem and impact.** Confirmation closes the reauthentication modal before awaiting the deferred sensitive action. If that action fails, the catch stores its error in modal state that is no longer visible. The password-only confirmation UX also sends passwordless users toward signing out and back in instead of offering an inline assertion with an existing passkey. A fresh proof should not require risking unsaved/offline work through logout.

**Implementation.** Separate confirmation failures from action failures, keep the action pending state until the retry completes, and display action errors outside a closed modal. The following control flow adapts to the existing state setters.

```ts
async function confirmThenRetry(
  confirm: () => Promise<unknown>, retry: () => Promise<unknown>,
  close: () => void, showConfirmationError: (s: string) => void,
  showActionError: (s: string) => void,
): Promise<void> {
  try { await confirm(); }
  catch (error) {
    showConfirmationError(error instanceof Error ? error.message : String(error));
    return;
  }
  try {
    await retry();
    close();
  } catch (error) {
    showActionError(error instanceof Error ? error.message : String(error));
    // Keep the pending action recoverable; do not silently forget it.
  }
}
```

Disable duplicate submissions while either stage is pending. For passwordless reauthentication, add a WebAuthn **assertion** against an already enrolled credential, bound to the current session and authentication epoch; successful registration of a new credential is not that proof. Reuse the transaction authorization rules in AUTH-01/AUTH-03. Clearly distinguish any intentionally stricter full-account-deletion freshness policy from ordinary confirmation rather than showing a confirmation flow that cannot satisfy it.

**Regression tests.** Confirmation rejected; confirmation succeeds but token creation/passkey operation fails; passwordless owner; expired ceremony; double click; session revoked between proof and action. Every error remains visible and no sensitive action is duplicated.


## Remediation sequence and integration requirements

### 1. Close authorization and data-loss paths first

Gate passkey management on recent proof of an **existing** credential, reserve TOTP attempts atomically, and authorize credential commits against the captured epoch. Make account leases truthful and account-specific, propagate cancellation, and repair drain semantics before relying on deletion as a security boundary. Fix the owner-delete transaction loop and protect legacy draft migration before shipping another browser-storage migration.

Introduce durable send acceptance and visible outcomes as one feature: database job, encrypted payload/raw data, stable request key and Message-ID, atomic undo/claim, delivery status, and draft recovery. Do not deploy a frontend “sent” status based solely on a 202 response. SMTP's uncertain remote acceptance must remain an explicit state, not an automatic retry loop.

### 2. Repair browser storage and mutation execution as a coordinated contract

Use one owner-generation source of truth, transactional write fences, cross-tab invalidation, and single-authoritative draft state. Fix lock contention and retry scheduling, but also make database mutations atomic with their replay records. A correct client cannot repair a server that commits a mutation without its ledger; a correct server cannot prevent a browser from displaying the previous owner's late response.

Keep draft/outbox data out of opportunistic cache eviction. Before changing namespaces, safely export or migrate unsent content. Version cursors and storage schemas deliberately, with a rollback path that preserves the old data until the new format commits.

### 3. Make synchronization completion a provable boundary

Deploy the Graph phase cursor, bounded invalid-cursor recovery, JMAP query-state/catch-up handling, and durable reconciliation generations together. Do not prune merely because a request had no next page. A valid completion requires the adapter to have verified the scope and consistency of the listing and the engine to have applied all positive/negative evidence.

Take a database backup before deploying changes to retention, membership pruning, cursor identity, or staged-scan state. Validate migrations with both existing unfinished jobs and existing message/product references. Immutable Graph ID migration must preserve all related keys, not just replace the mail_messages primary key.

### 4. Add release gates for the actual failure boundaries

The repository's CI already contains PostgreSQL integration and race-test jobs; preserve these. Add the regression cases from this report to the real handler/engine/browser paths, not only helper-unit tests. The current offline tests test a correct replay planner but miss its caller's dropped timer, and test the lock-manager-absent case without the busy-lock callback case.

The reviewed workflow does not show browser E2E execution or dedicated dependency-vulnerability scanning. Add those as separate release-assurance jobs; this observation is not a claim that any particular dependency has a known vulnerability. Pin third-party workflow actions to reviewed commit SHAs and release images to reviewed digests where reproducibility is required; do not substitute unverified hashes. Retain the existing gate that publishes only after verification and PostgreSQL integration succeed.

A source-complete verification environment should run the following existing project-level checks, with PostgreSQL connection variables taken from the pinned CI configuration rather than guessed:

```sh
set -eu
go test -race -count=1 ./...
go vet ./...
(cd mail-engine && go test -race -count=1 ./... && go vet ./...)
(cd mcp && go test -race -count=1 ./... && go vet ./...)
(cd dashboard && npm ci && npm test && npm run typecheck && npm run build)
```

Verify script names against `dashboard/package.json` before making the shell block a release script. Browser tests additionally need two tabs, controlled time, controllable IndexedDB failures, delayed network responses, and a local request recorder. Provider contract tests should script paginated responses and resets; final acceptance should also include live disposable IMAP/JMAP/Gmail/Graph accounts where supported.

## What this audit did not establish

No successful browser tracking leak, live-account takeover, real-provider message loss, known dependency CVE, leaked production secret, or production compromise is asserted. No test result here is evidence that the full repository is race-free or that the proposed changes compile together.

No arbitrary unauthenticated SSRF claim is made merely because self-hosted mail servers are configurable. No claim is made that the iframe's `allow-same-origin` permission alone permits script execution: the reviewed sandbox omits script permission. No claim is made that JMAP sending is accidentally broken where the product explicitly refuses an unsupported sending transport.

The service worker does **not** cache the authentication endpoint through the suspected path: `authApi` also prefixes `/api`, and the worker excludes `/api/`. A suspected Go `return value, scan(&value)` evaluation problem was tested and excluded. Existing encrypted credential storage, SMTP transport checks, body/attachment size limits, and PostgreSQL/race CI are substantive controls; this report does not pretend they are absent.

## Source coverage and remaining review surface

All links below use the audited commit. “Substantial/full” describes source inspection, not test execution. Large files marked partial were read only in the relevant ranges. Earlier audit reports present in the repository were not used as a substitute for current-code analysis.

| Area | Read during this audit | Coverage / limitation |
|---|---|---|
| Application/authentication | [`README.md`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/README.md), [`app.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/app.go), [`auth.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/auth.go), [`reauth.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/reauth.go), [`password.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/password.go), [`agent.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/agent.go), [`cmd_serve.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/cmd_serve.go), [`lifecycle.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/lifecycle.go) | Substantial/full source review; authentication/lifecycle tests were not exhaustively read or executed. |
| Mail/product data | [`mail.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail.go), [`accounts.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/accounts.go), [`sendqueue.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/sendqueue.go), [`resolver.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/resolver.go), [`schema.sql`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/schema.sql), [`classify.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/classify.go), [`reconcile.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/reconcile.go), [`export.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/export.go), [`idempotency.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/idempotency.go) | Substantial/full source review of the traced handlers and shared state. |
| OAuth | [`oauth.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/oauth.go) | Partial: first 310 lines; no claim of exhaustive token-refresh/callback coverage. |
| Engine core | [`mail-engine/adapter.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/adapter.go), [`mail-engine/store.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/store.go), [`mail-engine/sync.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/sync.go), [`mail-engine/send.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/send.go), [`mail-engine/credential.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/credential.go), [`mail-engine/dialer/dialer.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/dialer/dialer.go) | Substantial/full in reviewed paths; scheduler/service internals and migration runner were not exhaustively inspected. |
| Provider adapters | [`mail-engine/graph/adapter.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/graph/adapter.go), [`mail-engine/jmap/adapter.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/jmap/adapter.go), [`mail-engine/gmail/adapter.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mail-engine/gmail/adapter.go) | Graph and JMAP substantial/full; Gmail through line 650. Low-level IMAP implementation was not exhaustively reviewed. |
| Browser state/reader | [`dashboard/src/app/App.tsx`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/App.tsx), [`dashboard/src/app/Topline.tsx`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/Topline.tsx), [`dashboard/src/app/lib/api.ts`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/lib/api.ts), [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/lib/offline.ts), [`dashboard/src/app/reader/Body.tsx`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/reader/Body.tsx), [`dashboard/public/service-worker.js`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/public/service-worker.js) | Substantial/full source inspection; no successful browser runtime test. |
| Browser actions/UI/tests | [`dashboard/src/app/lib/actions.ts`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/lib/actions.ts), [`dashboard/src/app/lib/store.ts`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/lib/store.ts), [`dashboard/src/app/ui/Compose.tsx`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/ui/Compose.tsx), [`dashboard/src/app/ui/MoreMenu.tsx`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/ui/MoreMenu.tsx), [`dashboard/src/app/views/SecurityView.tsx`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/views/SecurityView.tsx), [`dashboard/src/app/lib/offline.test.ts`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/dashboard/src/app/lib/offline.test.ts) | Partial actions/store/Compose/SecurityView; MoreMenu and offline.test read. Other UI/tests not exhaustively reviewed. |
| Deployment | [`config.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/config.go), [`Dockerfile`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/Dockerfile), [`docker-entrypoint.sh`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/docker-entrypoint.sh), [`.github/workflows/ci.yml`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/.github/workflows/ci.yml) | Source configuration reviewed; no image build, deployed environment, branch rules, or CI job-log verification. |
| MCP | [`mcp/client.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mcp/client.go), [`mcp/tools.go`](https://github.com/lullmail/lullmail/blob/39e251862ef3a744fcbbc9c5541116f81d65a8ce/mcp/tools.go) | Client full; tools through line 310. Remaining tools, SDK behavior and tool annotations were not exhaustively audited. |

**Remaining material review targets:** setup/bootstrap implementation and secret-key lifecycle; root and engine migration runners and downgrade/rollback behavior; low-level IMAP parsing/connection behavior; scheduler/service concurrency; push subscription/network handling; personal export; board/notes/briefing features; the unreviewed OAuth/Gmail/MCP portions; the rest of the dashboard and test suite; and website/distribution assets. These are scope gaps, not declarations that those files are safe or defective.

A repository-level threat-model follow-up should explicitly document whether multiple application instances are supported. In-process owner/account locks are not distributed exclusion, while some database maintenance locks are advisory. Also verify that optional advisory-lock capability probing cannot permanently treat a transient probe error as unsupported or wait for another pool connection while already holding the only available connection. The reviewed store exposes that risk condition, but migration-time priming and all supported backends were not traced sufficiently to rate it as a confirmed deployment bug here.

## Validation appendix — exactly what ran

These harnesses were written outside the repository. They model selected control flow, use the standard Go library and Node's built-in test runner, and deliberately do not import Lull Mail. Passing means the small assertions below passed, not that each application finding was independently reproduced end-to-end. The final JavaScript base64 test demonstrates padded/unpadded encodings; it does not call the Go Gmail adapter.

The executed commands were:

```sh
# In the isolated directory containing go.mod and the two files below:
go test -race -count=1 -v ./...
node --test probes.mjs
```

### Isolated module

```go
module lullmail-audit-probes

go 1.23
```

### Go harness — probes_test.go

```go
package probes

import (
 "context"
 "encoding/json"
 "net/http"
 "net/http/httptest"
 "sync"
 "testing"
 "time"
)

type group struct { mu sync.Mutex; wg sync.WaitGroup; closed bool; done chan struct{} }
func newGroup() *group { return &group{done:make(chan struct{})} }
func (g *group) Go(fn func()) bool { g.mu.Lock(); defer g.mu.Unlock(); if g.closed{return false};g.wg.Add(1);go func(){defer g.wg.Done();fn()}();return true }
func (g *group) Stop(ctx context.Context) bool {
 g.mu.Lock()
 if !g.closed {g.closed=true;go func(){g.wg.Wait();close(g.done)}()}
 g.mu.Unlock()
 select {case <-g.done:return true;case <-ctx.Done():return false}
}
func TestRepeatedStopMustWaitForSameDrain(t *testing.T) {
 g:=newGroup();release:=make(chan struct{});g.Go(func(){<-release})
 for i:=0;i<2;i++ {ctx,cancel:=context.WithTimeout(context.Background(),time.Millisecond);ok:=g.Stop(ctx);cancel();if ok{t.Fatal("reported a drain while worker was blocked")}}
 close(release);ctx,cancel:=context.WithTimeout(context.Background(),time.Second);defer cancel();if !g.Stop(ctx){t.Fatal("worker did not drain")}
 if g.Go(func(){}){t.Fatal("admitted work after closure")}
}
func TestLateContentTypeIsNotTransmitted(t *testing.T) {
 w:=httptest.NewRecorder();w.WriteHeader(http.StatusAccepted);w.Header().Set("Content-Type","application/json");_ = json.NewEncoder(w).Encode(map[string]bool{"ok":true})
 if got:=w.Result().Header.Get("Content-Type");got!=""{t.Fatalf("expected missing committed header, got %q",got)}
 fixed:=httptest.NewRecorder();fixed.Header().Set("Content-Type","application/json");fixed.WriteHeader(http.StatusAccepted);_ = json.NewEncoder(fixed).Encode(map[string]bool{"ok":true})
 if fixed.Result().Header.Get("Content-Type")!="application/json"{t.Fatal("fixed header missing")}
}
func TestJoinedContextObservesLaterAccountCancellation(t *testing.T) {
 account,stopAccount:=context.WithCancel(context.Background());defer stopAccount()
 request,cancel:=context.WithCancel(context.Background());defer cancel()
 stop:=context.AfterFunc(account,cancel);defer stop();stopAccount()
 select{case <-request.Done():case <-time.After(time.Second):t.Fatal("account cancellation was not propagated")}
}
func fill(s *string) error {*s="filled";return nil}
func scannedReturn()(string,error){var s string;return s,fill(&s)}
func TestScanReturnSuspectIsNotABug(t *testing.T){s,e:=scannedReturn();if e!=nil||s!="filled"{t.Fatalf("%q %v",s,e)}}

```

### Go result

```text
=== RUN   TestRepeatedStopMustWaitForSameDrain
--- PASS: TestRepeatedStopMustWaitForSameDrain (0.00s)
=== RUN   TestLateContentTypeIsNotTransmitted
--- PASS: TestLateContentTypeIsNotTransmitted (0.00s)
=== RUN   TestJoinedContextObservesLaterAccountCancellation
--- PASS: TestJoinedContextObservesLaterAccountCancellation (0.00s)
=== RUN   TestScanReturnSuspectIsNotABug
--- PASS: TestScanReturnSuspectIsNotABug (0.00s)
PASS
ok  	lullmail-audit-probes	1.012s

```

### JavaScript harness — probes.mjs

```js
import assert from 'node:assert/strict';
import test from 'node:test';

// These are small reproductions of source control flow, NOT repository tests.
const oldLock = async (locks, run) => {
 if(locks){try{return await locks.request('replay',{ifAvailable:true},run)}catch{}}
 return run();
};
const fixedLock = (locks, run) => locks
 ? locks.request('replay',{ifAvailable:true}, lock => lock ? run() : undefined)
 : run();
test('contended Web Lock must not run a replay pass', async()=>{
 const busy={request:async(_n,_o,cb)=>cb(null)};let n=0;
 await oldLock(busy,async()=>{n++});assert.equal(n,1);
 n=0;await fixedLock(busy,async()=>{n++});assert.equal(n,0);
});
test('worker rejection must not rerun the worker', async()=>{
 const free={request:async(_n,_o,cb)=>cb({name:'replay'})};let n=0;
 await oldLock(free,async()=>{if(++n===1)throw Error('write failed')});assert.equal(n,2);
 n=0;await assert.rejects(fixedLock(free,async()=>{n++;throw Error('write failed')}));assert.equal(n,1);
});
test('persisted retryAt must reach the scheduler',()=>{
 const plan={due:[],retryAt:200};const oldResult={retryAt:undefined};const fixedResult={retryAt:plan.retryAt};
 assert.equal(oldResult.retryAt,undefined);assert.equal(fixedResult.retryAt,200);
});
test('Graph two-page enumeration needs a persisted phase',()=>{
 const old=(cur,next)=>({complete:cur===''&&next===''});
 assert.equal(old('','page2').complete,false);assert.equal(old('page2','').complete,false);
 const phase={enumerating:true};const fixed=(next)=>({complete:phase.enumerating&&next===''});
 assert.equal(fixed('').complete,true);
});
test('offset enumeration plus object deltas is not a snapshot',()=>{
 const before=['a','b','c','d'];const page1=before.slice(0,2);const after=['b','c','d'];const page2=after.slice(2);
 const seen=new Set([...page1,...page2]);assert(seen.has('a'));assert(!seen.has('c'));
 // A changed query-state must invalidate this candidate before destructive pruning.
 const queryStateBefore='q1',queryStateAfter='q2';assert.notEqual(queryStateBefore,queryStateAfter);
});
test('account must be part of a unified-list cursor',()=>{
 const rows=[{time:1,id:'same',account:'b'},{time:1,id:'same',account:'a'}];const cursor=rows[0];
 const old=rows.filter(r=>r.time<cursor.time||(r.time===cursor.time&&r.id<cursor.id));assert.equal(old.length,0);
 const fixed=rows.filter(r=>r.time<cursor.time||(r.time===cursor.time&&(r.id<cursor.id||(r.id===cursor.id&&r.account<cursor.account))));
 assert.equal(fixed.length,1);assert.equal(fixed[0].account,'a');
});
test('migration failure must keep legacy drafts and avoid completion marker',async()=>{
 const old=new Map([['draft','private text']]);old.set('migrated','1');try{throw Error('quota')}catch{}old.delete('draft');assert(!old.has('draft'));
 const fixed=new Map([['draft','private text']]);try{await Promise.reject(Error('quota'));fixed.set('migrated','1');fixed.delete('draft')}catch{}
 assert.equal(fixed.get('draft'),'private text');assert(!fixed.has('migrated'));
});
test('component attachment state does not update its draft-stack seed',()=>{
 const stack={attachments:[]};const component={attachments:['report.pdf']};assert.equal(stack.attachments.length,0);
 stack.attachments=[...component.attachments];const reopened={attachments:[...stack.attachments]};assert.deepEqual(reopened.attachments,['report.pdf']);
});
test('late private responses need a publication fence, not only a cache fence',async()=>{
 let generation=1;const captured=generation;generation=2;
 const old=async()=>{if(captured===generation){/* cache */}return 'private response'};
 assert.equal(await old(),'private response');
 const fixed=async()=>{if(captured!==generation)throw Error('stale owner');return 'private response'};
 await assert.rejects(fixed(),/stale owner/);
});
test('raw Gmail base64 may contain padding',()=>{
 const bytes=Buffer.from('a');assert.equal(bytes.toString('base64url'),'YQ');assert.equal(bytes.toString('base64'),'YQ==');
});

```

### JavaScript result

```text
TAP version 13
# Subtest: contended Web Lock must not run a replay pass
ok 1 - contended Web Lock must not run a replay pass
  ---
  duration_ms: 0.84065
  type: 'test'
  ...
# Subtest: worker rejection must not rerun the worker
ok 2 - worker rejection must not rerun the worker
  ---
  duration_ms: 1.237767
  type: 'test'
  ...
# Subtest: persisted retryAt must reach the scheduler
ok 3 - persisted retryAt must reach the scheduler
  ---
  duration_ms: 0.215072
  type: 'test'
  ...
# Subtest: Graph two-page enumeration needs a persisted phase
ok 4 - Graph two-page enumeration needs a persisted phase
  ---
  duration_ms: 0.136124
  type: 'test'
  ...
# Subtest: offset enumeration plus object deltas is not a snapshot
ok 5 - offset enumeration plus object deltas is not a snapshot
  ---
  duration_ms: 0.244837
  type: 'test'
  ...
# Subtest: account must be part of a unified-list cursor
ok 6 - account must be part of a unified-list cursor
  ---
  duration_ms: 0.133981
  type: 'test'
  ...
# Subtest: migration failure must keep legacy drafts and avoid completion marker
ok 7 - migration failure must keep legacy drafts and avoid completion marker
  ---
  duration_ms: 0.180501
  type: 'test'
  ...
# Subtest: component attachment state does not update its draft-stack seed
ok 8 - component attachment state does not update its draft-stack seed
  ---
  duration_ms: 0.64049
  type: 'test'
  ...
# Subtest: late private responses need a publication fence, not only a cache fence
ok 9 - late private responses need a publication fence, not only a cache fence
  ---
  duration_ms: 0.493019
  type: 'test'
  ...
# Subtest: raw Gmail base64 may contain padding
ok 10 - raw Gmail base64 may contain padding
  ---
  duration_ms: 0.363595
  type: 'test'
  ...
1..10
# tests 10
# suites 0
# pass 10
# fail 0
# cancelled 0
# skipped 0
# todo 0
# duration_ms 56.577991

```

## External primary references

These references support specific protocol/runtime contracts, not an assertion that the repository was executed against every implementation.

- [pgx Rows / connection-use contract](https://pkg.go.dev/github.com/jackc/pgx/v5#Rows): close/consume query rows before reusing their connection.
- [Microsoft Graph immutable identifiers](https://learn.microsoft.com/en-us/graph/outlook-immutable-id): per-request immutable-ID preference, ID lifetime, and migration API.
- [RFC 8620 — JMAP](https://www.rfc-editor.org/rfc/rfc8620.html): object changes versus query state, pagination, and transport security.
- [MDN Web Locks request contract](https://developer.mozilla.org/en-US/docs/Web/API/LockManager/request): `ifAvailable` and the null-lock callback.
- [MDN DOMParser parseFromString](https://developer.mozilla.org/en-US/docs/Web/API/DOMParser/parseFromString): inert parsing is not by itself a guarantee of no resource fetching.

---

**End of report.** Findings apply to the pinned source snapshot. Recheck each affected path after merging fixes; old audit comments and passing helper tests are not substitutes for exercising the complete runtime contract.
