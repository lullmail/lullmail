# Lullmail repository audit

**Repository:** [lullmail/lullmail](https://github.com/lullmail/lullmail)  
**Audited revision:** [`f172169b75d673cbd44ae117d530ae5f8c6fbc63`](https://github.com/lullmail/lullmail/commit/f172169b75d673cbd44ae117d530ae5f8c6fbc63)  
**Report date:** September 17, 2026  
**Repository changes:** None. This is a review and remediation report, not an applied patch.

## Scope and interpretation

The source was read through the connected GitHub API at the revision above. The `main` branch resolved to that revision when checked. This report is not an unverified copy of the repository's earlier audit: the earlier report covers `00216d8d7bd0a04dde742deefadabf0540200773`, and many of its findings have since been repaired. The existing `AUDIT_OPEN.md` register was used as a lead and is cross-referenced where appropriate.

A **confirmed static finding** means the behavior or missing invariant is visible in the reviewed code. A **concurrency finding** gives a concrete interleaving; it is not a claim that a production race was reproduced. **Hardening** and **product/design limitations** are explicitly distinguished from exploitable vulnerabilities. Severity is a triage judgment, not a CVSS score: High concerns substantial authentication, mail-loss, or availability consequences; Medium concerns meaningful correctness, reliability, or conditional security risks; Low concerns limited impact and defense in depth. No Critical finding is asserted.

The Go/application, PostgreSQL, provider, browser, npm, and full race test suites were **not executed**. Direct checkout and dependency downloads were unavailable from the container; GitHub source reads worked through the connector. No production service was attacked, no email was sent, and no credentials were accessed. Isolated local checks, where included, are identified separately from application tests.

Implementation blocks below are **proposed, unmerged code**. Small replacements identify their target; larger repairs are explicitly marked integration designs and introduce types, columns, and methods that must be wired into the application. They have not been compiled together as a repository-wide patch. Apply SQL through a reviewed migration, not by concatenating every SQL block in this document. Each finding includes regression requirements.

This is the complete set of findings documented in this review, not a guarantee that every possible defect in the repository has been discovered. The coverage appendix identifies files and limits.

## Executive assessment

The highest-priority remaining problems concern authentication finalization, acceptance of non-durable sends, account-deletion boundaries, and destruction of local state before replacement synchronization succeeds. Several earlier repairs are valuable but incomplete: resolving credentials at delivery time is not the same as holding a deletion lease during delivery; requiring Sent-folder membership in two correspondent queries does not protect a third query; and locking an owner row cannot serialize first-run requests that create different owner rows.

The register includes confirmed defects, reachable concurrency gaps, and explicitly labeled hardening or design limitations. They are not all independent vulnerabilities, and related issues must be repaired together. In particular, authentication epochs, durable outbox state, account maintenance coordination, and browser owner/generation fencing are shared invariants rather than isolated one-line fixes.

**Current register: 50 findings â€” 5 High, 40 Medium, 5 Low.** No Critical finding is asserted.

## Finding register

| ID | Severity | Finding | Classification |
|---|---|---|---|
| [AUTH-01](#auth-01) | High | A login verified against a retired credential can create a new valid session | Concurrency defect |
| [AUTH-02](#auth-02) | Medium | An old authenticated session can enroll lasting replacement credentials | Hardening; compromised-session prerequisite |
| [AUTH-03](#auth-03) | Medium | First-run setup has no installation-wide singleton or transaction boundary | Concurrency defect; valid setup token required |
| [AUTH-04](#auth-04) | Medium | Setup mutates shared configuration without a consistent synchronization boundary | Concurrency defect |
| [AUTH-05](#auth-05) | Medium | Authentication and security status can turn database failures into false sign-outs or false settings | Confirmed static defect |
| [AUTH-06](#auth-06) | Medium | Standalone TOTP guessing is limited by peer, not by a distributed account budget | Security hardening; deployment-dependent |
| [AUTH-07](#auth-07) | Medium | Concurrent setup-token writers can publish a different token than a running process accepts | Conditional startup concurrency defect |
| [SEND-01](#send-01) | High | Queue acceptance is volatile and later delivery failures have no recoverable user-visible record | Confirmed reliability defect; intentionally deferred architecture |
| [SEND-02](#send-02) | High | Delivery-time credential lookup does not fully fence account deletion | Concurrency defect; partial earlier fix |
| [SEND-03](#send-03) | Medium | The send queue has no aggregate memory or concurrency admission limit | Confirmed resource-boundary omission |
| [SEND-04](#send-04) | Medium | A successful submission can permanently lose its Sent copy | Reliability/design gap |
| [DATA-01](#data-01) | Medium | The thread-based correspondent query still trusts spoofable From evidence | Confirmed static defect; partial earlier fix |
| [DATA-02](#data-02) | Medium | Undoing a sender decision reads stale state before acquiring its serialization lock | Concurrency defect |
| [DATA-03](#data-03) | Medium | Screening preference changes and classifier decisions are not one consistent transition | Concurrency and partial-commit defect |
| [DATA-04](#data-04) | Medium | Thread-wide mutations cannot be undone exactly from one summary row | Confirmed state-restoration defect |
| [DATA-05](#data-05) | Medium | Relative snooze requests and rounded undo dates drift on replay | Confirmed semantic defect |
| [DATA-06](#data-06) | Medium | Optional account identity makes duplicate message IDs resolve arbitrarily | Confirmed API ambiguity; not a cross-user authorization bypass |
| [DATA-07](#data-07) | Medium | Account-setting requests cannot distinguish an omitted field from a destructive zero value | Confirmed validation defect |
| [DATA-08](#data-08) | Medium | Backfill and retention settings can commit before their corresponding data transition succeeds | Confirmed partial-commit/design defect |
| [DATA-09](#data-09) | Medium | Increasing retention does not schedule restoration of previously pruned unchanged mail | Confirmed restoration gap for incremental providers |
| [DATA-10](#data-10) | Medium | Post-sync processing errors can be reported as a healthy completed sync | Confirmed error-propagation defect |
| [SYNC-01](#sync-01) | High | Cursor invalidation deletes the live mirror before a replacement is available | Confirmed destructive ordering; failure-dependent impact |
| [SYNC-02](#sync-02) | Medium | Enumeration bookkeeping is not durable across page limits and restarts | Confirmed state-machine gap |
| [SYNC-03](#sync-03) | High | Identity promotion deletes the old message before its replacement and user state are migrated | Confirmed data-loss ordering |
| [SYNC-04](#sync-04) | Medium | Retention, classification, and mirror writes can create orphaned or policy-violating rows | Concurrency/integrity defect |
| [DATA-11](#data-11) | Medium | Bucket results stop at 200 without a continuation contract or stable tie order | Confirmed completeness/usability limitation |
| [DATA-12](#data-12) | Medium | Bounded body prefetch has no explicit missing-body contract | Confirmed API representation gap |
| [DATA-13](#data-13) | Medium | Classifier failures can persist decisions made from incomplete evidence and hold broad locks for large batches | Confirmed error-handling and scalability gap |
| [DATA-14](#data-14) | Low | Attachment downloads build filenames manually and ignore stream errors | Confirmed output-handling weakness |
| [EXPORT-01](#export-01) | Medium | Archive construction can suppress write/manifest failures while reporting planned counts | Confirmed error-handling defect |
| [WEB-01](#web-01) | Medium | Browser caches use a mutable email identity and do not fence in-flight requests | Confirmed isolation/concurrency gap |
| [WEB-02](#web-02) | Medium | Clearing offline data suppresses errors and is not an atomic owner reset | Confirmed privacy/durability defect |
| [WEB-03](#web-03) | Medium | Offline replay can run twice and has no server idempotency contract | Confirmed concurrency/uncertain-retry gap |
| [WEB-04](#web-04) | Medium | Rejected offline actions are retained but not surfaced as structured user-visible failures | Confirmed replay outcome gap |
| [WEB-05](#web-05) | Medium | Transient replay failures can remain stranded while the browser stays online | Confirmed retry-scheduling omission |
| [WEB-06](#web-06) | Medium | Successful mutations delete the entire offline response cache | Confirmed offline-availability defect |
| [WEB-07](#web-07) | Medium | Offline queue acknowledgment is presented through the same success path as a server commit | Confirmed API/UI semantics gap |
| [WEB-08](#web-08) | Low | IndexedDB connection lifecycle can hang during schema upgrades and lacks explicit storage-failure UX | Confirmed robustness gap |
| [OPS-01](#ops-01) | Medium | HTTP and export resource limits are inconsistent and do not bound aggregate work | Confirmed availability/hardening gap |
| [OPS-02](#ops-02) | Medium | The product database pool is unbounded and authenticated reads write the same session row repeatedly | Confirmed scalability/availability gap |
| [OPS-03](#ops-03) | Medium | Startup migrations repeatedly alter live constraints without a version ledger or shared migration lock | Confirmed operational reliability gap |
| [OPS-04](#ops-04) | Medium | Shutdown drains HTTP but does not join all application-owned background work | Confirmed lifecycle defect |
| [OPS-05](#ops-05) | Medium | Deleting one account can wait behind unrelated account work and uncancellable locks | Confirmed availability/design limitation |
| [OPS-06](#ops-06) | Medium | Default network binding and permissive origin detection can expose setup or credentials over HTTP | Deployment-dependent security/hardening gap |
| [OPS-07](#ops-07) | Medium | CI can pass while real database integration tests are skipped | Confirmed test-coverage gap |
| [OPS-08](#ops-08) | Low | Sensitive API responses lack a consistent no-store and safe diagnostic policy | Confirmed defense-in-depth gap |
| [OPS-09](#ops-09) | Low | Public asset requests repeat deterministic hashing work | Confirmed avoidable request cost |
| [PROVIDER-01](#provider-01) | Medium | Graph message identity is move-sensitive because immutable IDs are not requested | Confirmed provider-contract gap |
| [PROVIDER-02](#provider-02) | Low | Graph continuation URLs are not restricted to the configured provider origin | Confirmed egress hardening gap; poisoned-response/cursor prerequisite |
| [PROVIDER-03](#provider-03) | Medium | Connected-account creation spans separate commits, and OAuth creation bypasses the owner lifecycle gate | Confirmed transactional/concurrency gap |

## Detailed findings and implementations

<a id="auth-01"></a>

### AUTH-01 â€” A login verified against a retired credential can create a new valid session

**Severity:** High  
**Classification:** Concurrency defect  
**Evidence:** [`auth.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/auth.go) Â· [`schema.sql`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/schema.sql)

Password login verifies the stored hash and later calls `createSession` without an owner lock or credential revision check. Passkey login similarly separates verification/credential update from session creation. A concurrent password replacement or factor deletion can revoke existing sessions and commit between those steps; the already-verified request then inserts a fresh session after revocation. The TOTP path does lock for claiming the time step, but the secret was read before that lock: its update does not establish that the enabled factor is the same secret that was verified. These are reachable interleavings, not reproduced production incidents. Transactional deletion of old sessions alone does not close the gap.

**Suggested fix.** Introduce an authentication epoch. Capture it with the credential being verified; after expensive verification, lock the owner, require the epoch to match, and insert the session in that transaction. Every credential change increments the epoch under the same lock. Preserve only the current session by explicitly advancing its epoch. Passkey and TOTP snapshots must also identify the exact credential revision.

**Implementation.**

Integration design; new columns and helper replace ordinary `createSession` finalization:

```sql
ALTER TABLE users ADD COLUMN auth_epoch bigint NOT NULL DEFAULT 0;
ALTER TABLE auth_sessions ADD COLUMN auth_epoch bigint NOT NULL DEFAULT 0;
-- Capture a password proof snapshot in one statement:
SELECT u.id, u.auth_epoch, p.hash
FROM users u JOIN auth_passwords p ON p.user_id = u.id
WHERE u.id = $1;
```

```go
// New auth_epoch.go. Imports: context, database/sql, errors, net/http.
type verifiedLogin struct {
    UserID string
    Epoch  int64 // read alongside the credential BEFORE verifying it
    Method string
}

func (a *App) finalizeLogin(ctx context.Context, r *http.Request, p verifiedLogin) (string, error) {
    tx, err := a.db.BeginTx(ctx, nil)
    if err != nil { return "", err }
    defer tx.Rollback()
    var current int64
    if err := tx.QueryRowContext(ctx,
        `SELECT auth_epoch FROM users WHERE id=$1 FOR UPDATE`, p.UserID).Scan(&current); err != nil {
        return "", err
    }
    if current != p.Epoch { return "", errors.New("credential changed; repeat sign-in") }
    raw, err := a.persistSession(ctx, tx, r, p.UserID, p.Method)
    if err != nil { return "", err }
    if _, err := tx.ExecContext(ctx,
        `UPDATE auth_sessions SET auth_epoch=$1 WHERE id_hash=$2`, current, tokenHash(raw)); err != nil {
        return "", err
    }
    if err := tx.Commit(); err != nil { return "", err }
    return raw, nil // set the cookie only AFTER this returns successfully
}

func advanceAuthEpoch(ctx context.Context, tx *sql.Tx, uid, keepSessionHash string) error {
    // Caller has already locked users(uid), and changes the credential in this tx.
    var epoch int64
    if err := tx.QueryRowContext(ctx,
        `UPDATE users SET auth_epoch=auth_epoch+1 WHERE id=$1 RETURNING auth_epoch`, uid).Scan(&epoch); err != nil {
        return err
    }
    if _, err := tx.ExecContext(ctx,
        `DELETE FROM auth_sessions WHERE user_id=$1 AND id_hash<>$2`, uid, keepSessionHash); err != nil {
        return err
    }
    _, err := tx.ExecContext(ctx,
        `UPDATE auth_sessions SET auth_epoch=$1 WHERE user_id=$2 AND id_hash=$3`,
        epoch, uid, keepSessionHash)
    return err
}
```

Session authentication must join `users` and require `s.auth_epoch=u.auth_epoch`. Apply the epoch protocol to password, passkey, recovery-code rotation, and TOTP changes, not only one handler. Registration ceremonies also carry the epoch they started under.

**Regression requirements.** Use barriers to pause each login after verification, rotate/delete its credential in another transaction, then resume. No session may be created. Repeat for TOTP secret replacement, passkey deletion, full account deletion, and concurrent logins. Keep an unchanged credential login working.

**Related work:** Remainder of the earlier AUTH-05 finding; do not remove the existing transactionally checked revocation.

<a id="auth-02"></a>

### AUTH-02 â€” An old authenticated session can enroll lasting replacement credentials

**Severity:** Medium  
**Classification:** Hardening; compromised-session prerequisite  
**Evidence:** [`auth.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/auth.go#L586-L641) Â· [`auth.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/auth.go#L1400-L1545) Â· [`mail.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail.go#L335-L373)

Passkey enrollment, TOTP enrollment, and recovery-code regeneration use the ordinary session gate. A copied session can remain valid for 30 days and can establish a durable alternative login before the owner notices. Password replacement already requires the current password, and full account deletion already requires a recent session; those protections should not be described as absent.

**Suggested fix.** Require recent interactive proof for enrollment, removal, recovery rotation, and agent-token creation. Bind the proof to the current session, intended operation, user, and authentication epoch. Permit a carefully designed recovery flow rather than making recovery sessions impossible to repair.

**Implementation.**

A small first step can reuse the existing recent-sign-in policy; a dedicated, one-use step-up ceremony is stronger:

```go
// Call before sensitive operations; imports context, errors, net/http.
func (a *App) requireRecentSignIn(w http.ResponseWriter, r *http.Request, uid string) bool {
    hash, _ := r.Context().Value(sessionContextKey{}).(string)
    if hash == "" || hash == "bootstrap" {
        writeProblem(w, 428, "Fresh Sign-In Required", "sign in again before changing security settings")
        return false
    }
    var fresh bool
    err := a.db.QueryRowContext(r.Context(), `SELECT EXISTS(
        SELECT 1 FROM auth_sessions
        WHERE id_hash=$1 AND user_id=$2 AND expires_at>now()
          AND created_at>now()-interval '10 minutes')`, hash, uid).Scan(&fresh)
    if err != nil {
        writeProblem(w, 503, "Security Unavailable", "could not verify the session")
        return false
    }
    if !fresh {
        writeProblem(w, 428, "Fresh Sign-In Required", "sign in again before changing security settings")
        return false
    }
    return true
}
```

For a full step-up implementation, store a hashed one-use operation token and consume it in the same transaction as the credential change. Do not update `created_at` or `verified_at` merely because a session accessed an endpoint.

**Regression requirements.** An old session must be denied; a freshly verified session must succeed. Replaying a consumed operation proof, changing its operation, or changing the epoch must fail. Verify that the recovery path can still install a new standing credential.

<a id="auth-03"></a>

### AUTH-03 â€” First-run setup has no installation-wide singleton or transaction boundary

**Severity:** Medium  
**Classification:** Concurrency defect; valid setup token required  
**Evidence:** [`auth.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/auth.go#L388-L577) Â· [`auth.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/auth.go#L1889-L1956) Â· [`schema.sql`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/schema.sql#L7-L16) Â· [`mail.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail.go#L142-L148)

The final bootstrap check locks a user row, but owner creation happens earlier and `users` is unique only by email. Two first-run requests with different names can both observe no owner, create different users, and lock different rows. Their global `ownerConfiguredDB` checks can both run before either credential commits. The application then contains more than one owner despite its single-owner assumptions. Name/origin changes and failed setup attempts can also leave pre-commit state behind. `ensureUser` on a changed environment email is another route to an extra user row.

**Suggested fix.** Use one installation-wide database lock before owner lookup/creation and final credential installation. Enforce a singleton at the database level after inspecting existing rows. Keep name, origin, credential, recovery codes, and session in one transaction; expensive hashing may happen before the lock, but all authoritative reads must be repeated inside it.

**Implementation.**

Migration and transaction integration, not a multi-user migration:

```sql
-- First stop and require operator reconciliation if count(*) > 1.
-- Never silently delete an unexpected owner and its cascaded data.
CREATE UNIQUE INDEX users_single_owner ON users ((true));
```

```go
// At the start of the FINAL bootstrap transaction, before firstUserID/INSERT:
if _, err := tx.ExecContext(ctx,
    `SELECT pg_advisory_xact_lock(684329017521)`); err != nil { return err }
configured, err := ownerConfiguredDB(ctx, tx)
if err != nil { return err }
if configured { return errors.New("installation already configured") }
// Query/create the one owner using tx, not a.db or ensureUser's separate pool call.
// Persist display_name, pinned origin, credential, recovery codes and session here.
// Commit once; publish runtime configuration and retire the token afterwards.
```

Refactor `ensureUser` into a transaction-aware owner initializer. Bootstrap-begin should store provisional ceremony input, not change the authoritative owner profile. Treat a conflicting configured `LULL_USER_EMAIL` as a startup configuration error rather than inserting a second owner.

**Regression requirements.** Run concurrent password/passkey setup using different names and the same valid token; exactly one owner and one successful completion must exist. Inject failures before every write/commit. Test a restart with a changed owner-email override.

**Related work:** The owner-row recheck already present is useful, but not sufficient for the no-owner case.

<a id="auth-04"></a>

### AUTH-04 â€” Setup mutates shared configuration without a consistent synchronization boundary

**Severity:** Medium  
**Classification:** Concurrency defect  
**Evidence:** [`setup.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/setup.go#L212-L315) Â· [`auth.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/auth.go#L199-L279) Â· [`auth.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/auth.go#L742-L785) Â· [`auth.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/auth.go#L388-L577)

`setOriginForSetup` protects writes to `a.wa` and selected `cfg` fields with `waMu`, while other request handlers read those fields without that lock. `handleLoginFinish` uses `a.wa` directly. Setup-token retirement and owner-email changes also mutate state read by other requests. `applyOrigin` changes the live config before WebAuthn construction succeeds, so a failed setup can leave a partially updated configuration.

**Suggested fix.** Build a validated immutable runtime snapshot off to the side, then atomically publish it after the database commit. Read the same snapshot throughout each request. Keep bootstrap token lifetime/state under the same synchronization discipline.

**Implementation.**

Integration pattern; migrate readers instead of mixing atomic and direct access:

```go
// Add sync/atomic and the webauthn import.
type runtimeAuth struct {
    PublicURL string
    RPID string
    Secure bool
    WebAuthn *webauthn.WebAuthn
    SetupToken string
    SetupCreated time.Time
}
// App: authRuntime atomic.Pointer[runtimeAuth]

func buildRuntimeAuth(base Config, previous runtimeAuth, origin string) (*runtimeAuth, error) {
    candidate := base // never mutate the published config while validating
    clean, err := validatedOrigin(origin, false) // OPS-06; explicit override only when configured
    if err != nil { return nil, err }
    if !applyOrigin(&candidate, clean) { return nil, errors.New("invalid origin") }
    wa, err := newWebAuthn(&candidate)
    if err != nil { return nil, err }
    next := previous // preserve bootstrap lifetime/state in this serialized writer
    next.PublicURL, next.RPID = candidate.PublicURL, candidate.RPID
    next.Secure, next.WebAuthn = candidate.SecureAuth, wa
    return &next, nil
}
// After commit: a.authRuntime.Store(next)
// At handler entry: state := a.authRuntime.Load(); use state for the entire request.
```

Serialize snapshot writers, including setup-token retirement, so a concurrent write cannot reintroduce a retired token. A copy alone does not make unsynchronized writes to the original `Config` safe.

**Regression requirements.** Run setup, auth status, cookie creation, login completion, and token retirement concurrently under `go test -race`. Make WebAuthn initialization fail and assert that the previous live origin/instance remains unchanged.

<a id="auth-05"></a>

### AUTH-05 â€” Authentication and security status can turn database failures into false sign-outs or false settings

**Severity:** Medium  
**Classification:** Confirmed static defect  
**Evidence:** [`auth.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/auth.go#L334-L363) Â· [`auth.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/auth.go#L1280-L1411) Â· [`dashboard/src/app/lib/api.ts`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/dashboard/src/app/lib/api.ts#L126-L157)

`handleAuthStatus` treats every error from `authenticateRequest` as an unauthenticated result and returns 200 after the initial configuration query succeeded. An error on the later session lookup therefore causes a false sign-out rather than an unavailable status. Several `handleSecurity` queries discard errors and report absent passwords/TOTP or zero recovery codes. Other auth lookup branches similarly conflate missing rows and infrastructure failure.

**Suggested fix.** Distinguish `sql.ErrNoRows` from database errors at every lookup. Return a retryable unavailable status with a safe public message, log the underlying error, and preserve the browserâ€™s last known authenticated state on that response.

**Implementation.**

Targeted replacements:

```go
uid, session, err := a.authenticateRequest(r)
if err != nil && !errors.Is(err, sql.ErrNoRows) {
    a.log.Error("auth status lookup failed", "err", err)
    writeProblem(w, http.StatusServiceUnavailable, "Status Unavailable",
        "could not check sign-in status; retry shortly")
    return
}
authenticated := err == nil
```

```go
// Do this for EACH security status lookup, instead of `_ = ...Scan(...)`.
if err := a.db.QueryRowContext(r.Context(), `SELECT EXISTS(
    SELECT 1 FROM auth_passwords WHERE user_id=$1)`, uid).Scan(&passwordSet); err != nil {
    a.log.Error("security status failed", "err", err)
    writeProblem(w, 503, "Security Unavailable", "could not read security settings")
    return
}
```

Keep the current frontend distinction between 5xx/network failure and a confirmed 401. Do not return a successful, partially fabricated security object.

**Regression requirements.** Make only the second auth-status query fail, after ownerConfigured succeeds. The response must be 503 and the UI must not switch to the login gate. Fail each security-field query separately and assert no misleading 200 response.

<a id="auth-06"></a>

### AUTH-06 â€” Standalone TOTP guessing is limited by peer, not by a distributed account budget

**Severity:** Medium  
**Classification:** Security hardening; deployment-dependent  
**Evidence:** [`auth.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/auth.go#L78-L136) Â· [`auth.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/auth.go#L1180-L1275) Â· [`README.md`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/README.md#L95-L118)

TOTP is intentionally an alternative login, not a second factor after the password. Replay protection is now present, but failed code guesses use only the per-peer allowance. Requests from different peers do not share a TOTP account budget. Six-digit codes therefore need an additional distributed-guessing control on exposed deployments. The repaired peer parser must not be replaced with untrusted forwarded-header parsing.

**Suggested fix.** Add a canonical-user TOTP failure budget, global admission/backpressure, and monitoring. Make failures expensive enough to slow guessing without imposing an indefinitely renewable account-wide lockout. Clearly label standalone TOTP as an alternative credential, or implement a genuine two-stage MFA flow when that is the desired product policy.

**Implementation.**

A transactional fixed-window budget can be added before TOTP verification:

```sql
CREATE TABLE auth_factor_windows (
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  factor text NOT NULL,
  window_start timestamptz NOT NULL,
  attempts integer NOT NULL CHECK (attempts > 0),
  PRIMARY KEY (user_id, factor, window_start)
);
-- $3 is a server-computed fixed five-minute boundary, not a client value.
INSERT INTO auth_factor_windows(user_id,factor,window_start,attempts)
VALUES ($1,'totp',$3,1)
ON CONFLICT (user_id,factor,window_start)
DO UPDATE SET attempts=auth_factor_windows.attempts+1
RETURNING attempts;
```

```go
// Proposed policy, configurable and accompanied by ingress controls.
const maxTOTPAttemptsPerWindow = 10
if attempts > maxTOTPAttemptsPerWindow {
    w.Header().Set("Retry-After", strconv.Itoa(secondsToNextWindow))
    writeProblem(w, 429, "Too Many Attempts", "retry after the current verification window")
    return
}
```

Prune old windows. Do not extend the fixed deadline on every failed attempt. Keep password/passkey recovery usable and alert on sustained abuse; no account-wide rate limit completely prevents denial-of-service tradeoffs.

**Regression requirements.** Requests from different peer IPs for the same user must share the budget. Test fixed-window expiry, multi-process consistency, successful single-use TOTP, and continued availability of other sign-in methods.

<a id="auth-07"></a>

### AUTH-07 â€” Concurrent setup-token writers can publish a different token than a running process accepts

**Severity:** Medium  
**Classification:** Conditional startup concurrency defect  
**Evidence:** [`setup.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/setup.go#L89-L154)

The generated encryption key now has a proper publish-once winner. Mutable setup tokens do not: concurrent processes can both read an absent/expired token, generate different values, atomically rename their own file, and each return its own value. The final file can disagree with a running process or its printed setup token. The mutable-file helper also does not fsync the directory after rename, leaving a crash-durability gap.

**Suggested fix.** Serialize the entire read/expiry-check/generate/publish sequence across processes, then re-read the winning file under that lock. On supported self-host platforms use an advisory lock file; alternatively store a sealed shared token with a database-coordinated generation. Fsync the directory after mutable publication.

**Implementation.**

Linux/Unix integration example; use a build-tagged platform implementation and do not leave other platforms silently unlocked:

```go
// imports os, path/filepath, syscall
func withSetupFileLock(dir string, fn func() error) error {
    if err := os.MkdirAll(dir, 0700); err != nil { return err }
    f, err := os.OpenFile(filepath.Join(dir, ".setup.lock"), os.O_CREATE|os.O_RDWR, 0600)
    if err != nil { return err }
    defer f.Close()
    if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil { return err }
    defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
    return fn() // includes re-read and validity check, not just the final rename
}

func syncDirectory(dir string) error {
    d, err := os.Open(dir)
    if err != nil { return err }
    defer d.Close()
    return d.Sync()
}
// writePrivateFile: after successful os.Rename(name, path), return syncDirectory(dir).
```

Do not automatically delete an unreadable existing `secret.key`; recover the original from backup or perform an explicit, documented credential reset.

**Regression requirements.** Launch concurrent separate processes, not just goroutines, against an empty and an expired-token data directory. Every successful reader must observe the same token. Inject write/rename/fsync failures without damaging the previous file.

<a id="send-01"></a>

### SEND-01 â€” Queue acceptance is volatile and later delivery failures have no recoverable user-visible record

**Severity:** High  
**Classification:** Confirmed reliability defect; intentionally deferred architecture  
**Evidence:** [`sendqueue.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/sendqueue.go#L405-L473) Â· [`dashboard/src/app/lib/actions.ts`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/dashboard/src/app/lib/actions.ts) Â· [`schema.sql`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/schema.sql)

`enqueue` stores the outgoing message only in a process-local map and a goroutine. It returns a queue ID before delivery; the worker later logs errors and deletes the entry. Its buffered `done` channel has no consumer in the reviewed queue implementation, and there is no durable status endpoint. A restart during the undo window loses accepted work; an SMTP/provider failure after acceptance is not a persistent failure the user can inspect or retry. A lost HTTP acknowledgment also has no idempotency contract. â€œQueuedâ€ must not imply delivered or safely persisted.

**Suggested fix.** Persist the complete composition and its idempotency key before acknowledgment. Implement observable queued/submitting/submitted/failed/ambiguous/cancelled states, atomic undo, stable message identity, and explicit retry/recovery. Preserve a recoverable draft until durable acceptance. Treat a network failure after possible provider acceptance as ambiguous, not automatically retryable.

**Implementation.**

Integration design spanning migration, handlers, worker, and UI:

```sql
CREATE TABLE outbox_jobs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  account_id text NOT NULL REFERENCES email_accounts(mirror_account_id) ON DELETE CASCADE,
  request_key uuid NOT NULL,
  request_hash text NOT NULL,
  payload_ciphertext text NOT NULL,
  state text NOT NULL CHECK (state IN
    ('queued','submitting','submitted','failed','ambiguous','cancelled')),
  not_before timestamptz NOT NULL DEFAULT now()+interval '5 seconds',
  lease_until timestamptz,
  provider_receipt text,
  error_code text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(user_id, request_key)
);
CREATE INDEX outbox_ready ON outbox_jobs(not_before) WHERE state='queued';

-- Enqueue: insert a sealed COMPLETE draft. On a key conflict, read the row
-- and require request_hash to match; otherwise return 409, not a second job.
INSERT INTO outbox_jobs(user_id,account_id,request_key,request_hash,payload_ciphertext,state)
VALUES ($1,$2,$3,$4,$5,'queued')
ON CONFLICT(user_id,request_key) DO NOTHING;

-- Claim one job. Commit this statement before making any network request.
WITH candidate AS (
  SELECT id FROM outbox_jobs
  WHERE state='queued' AND not_before<=now()
  ORDER BY not_before,id FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE outbox_jobs j SET state='submitting',
  lease_until=now()+interval '2 minutes', updated_at=now()
FROM candidate c WHERE j.id=c.id
RETURNING j.*;

-- Undo races safely with claim, and is always owner-scoped.
UPDATE outbox_jobs SET state='cancelled',updated_at=now()
WHERE id=$1 AND user_id=$2 AND state='queued' AND not_before>now()
RETURNING id;

-- A crashed submitting worker might already have sent the message.
UPDATE outbox_jobs SET state='ambiguous',error_code='worker_lost',updated_at=now()
WHERE state='submitting' AND lease_until<now();
```

```ts
// Client: persist requestKey with the draft BEFORE the first request.
const result = await api<{ id: string; state: string }>("/send", {
  body: { ...completeDraft, request_key: draft.requestKey },
});
// Keep the draft's outbox reference; show queued/submitted/failed/ambiguous.
// Poll an owner-scoped GET /outbox/:id or consume authenticated status events.
```

Hash canonical client intent, not newly regenerated dates/Message-IDs. Generate and persist stable submission bytes/identity once. Reusing the same key with different content must fail. A server crash between provider acceptance and database acknowledgment cannot be made exactly-once by SQL alone; show uncertainty and reconcile before retrying.

**Regression requirements.** Restart before the undo deadline and after claim; accepted work must remain inspectable. Lose the HTTP response and retry the same key without a duplicate. Race undo with claim. Simulate provider rejection, successful DATA followed by disconnect, and database failure after provider acceptance. Verify every attachment and reply field survives recovery.

**Related work:** Earlier SEND-03 remains open. SEND-02, SEND-03, SEND-04 below are complementary requirements for the replacement.

<a id="send-02"></a>

### SEND-02 â€” Delivery-time credential lookup does not fully fence account deletion

**Severity:** High  
**Classification:** Concurrency defect; partial earlier fix  
**Evidence:** [`sendqueue.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/sendqueue.go#L317-L405) Â· [`oauth.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/oauth.go#L252-L317) Â· [`app.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/app.go) Â· [`accounts.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/accounts.go)

The current code improves on credential capture at enqueue time by resolving SMTP credentials at delivery time. However, `SMTPFor` and `sendOAuth` do not hold an account-use lease across credential lookup and network submission. A deletion can commit after credentials have been loaded but before submission. The queued work can therefore submit mail for a disconnected/deleted account. The read-only engine API fence does not cover this worker path.

**Suggested fix.** Wrap every outbound transport in an account lifecycle lease acquired before credential resolution and held through submission and state recording. Establish a documented linearization rule: deletion waits for admitted work or cancels it and waits for termination; no new send is admitted after deletion begins. The durable queue must cancel not-yet-admitted jobs for the account.

**Implementation.**

Targeted integration with the existing lifecycle API:

```go
func (a *App) guardDelivery(account mail.AccountID, next deliverFunc) deliverFunc {
    return func(ctx context.Context, out *mail.Outgoing) error {
        release, ok := a.beginAccountUse(account)
        if !ok { return errors.New("account is being deleted") }
        defer release()
        // Avoid a second RLock in fileSent -> accountResolver while a writer waits.
        ctx = context.WithValue(ctx, accountGateKey{}, true)
        if err := ctx.Err(); err != nil { return err }
        return next(ctx, out) // credentials are read INSIDE this closure
    }
}
```

Apply this wrapper to BOTH OAuth and SMTP closures returned by `deliveryFor`. Do not resolve credentials before entering it. Keep the lifecycle context marker internal; do not derive it from request headers. This closes the local-process race, but multi-replica operation requires a database-backed account generation/admission protocol as well.

**Regression requirements.** Pause a fake transport immediately after credential lookup. Start account deletion; it must not report completion while that admitted send remains active. After deletion begins, a second send must fail admission. Test SMTP, Gmail, Graph, and Sent-copy filing without recursive-RWMutex deadlock.

<a id="send-03"></a>

### SEND-03 â€” The send queue has no aggregate memory or concurrency admission limit

**Severity:** Medium  
**Classification:** Confirmed resource-boundary omission  
**Evidence:** [`sendqueue.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/sendqueue.go#L50-L132) Â· [`sendqueue.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/sendqueue.go#L230-L270) Â· [`sendqueue.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/sendqueue.go#L405-L449)

Per-request and per-attachment limits do not bound the total number of simultaneously accepted compositions. Each queued request retains decoded attachment bytes and starts a goroutine. An authenticated client or broadly authorized agent can enqueue many large sends, exhausting memory or provider connections even while every request satisfies the advertised size limits.

**Suggested fix.** Apply a small global and per-owner job limit plus a byte-weighted budget. Admit before decoding large bodies where possible, and release on every completion/undo/error. A durable queue still needs disk quotas and a bounded number of active transport workers.

**Implementation.**

Complete admission helper; choose deployment-specific limits instead of treating these example values as universal:

```go
// imports errors, sync
var errSendCapacity = errors.New("send capacity exhausted")
type sendBudget struct { mu sync.Mutex; jobs int; bytes int64 }
func (b *sendBudget) acquire(n int64) (func(), error) {
    const maxJobs = 8
    const maxBytes int64 = 128 << 20
    if n < 0 || n > maxBytes { return nil, errSendCapacity }
    b.mu.Lock()
    if b.jobs >= maxJobs || b.bytes+n > maxBytes {
        b.mu.Unlock(); return nil, errSendCapacity
    }
    b.jobs++; b.bytes += n
    b.mu.Unlock()
    var once sync.Once
    return func() { once.Do(func() {
        b.mu.Lock(); b.jobs--; b.bytes -= n; b.mu.Unlock()
    }) }, nil
}
```

Use a separate small request-admission semaphore before JSON decoding, then retain the weighted lease until queue removal. Return 429/503 with `Retry-After`; do not accept and then silently drop a job. Count body strings, decoded attachments, and rendering overhead conservatively.

**Regression requirements.** Enqueue many maximum-size requests and assert predictable rejection before memory grows without bound. Undo, panic recovery, malformed requests, and delivery errors must release capacity exactly once. Test shutdown while all slots are occupied.

<a id="send-04"></a>

### SEND-04 â€” A successful submission can permanently lose its Sent copy

**Severity:** Medium  
**Classification:** Reliability/design gap  
**Evidence:** [`sendqueue.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/sendqueue.go#L368-L405)

`fileSent` logs and returns when mailbox lookup, credentials, connection, or Append fails. Delivery has already succeeded, so it is correct not to label this an unsent message; nevertheless there is no durable retry or user-visible filing status. The missing Sent copy also affects the thread and correspondent heuristics that depend on Sent-folder evidence.

**Suggested fix.** Track transport submission and Sent-copy filing as separate durable outcomes. Persist the exact submitted bytes, retain a filing job, retry only filing, and report â€œsent; Sent copy pendingâ€ without sending the original message again.

**Implementation.**

Extend the durable job from SEND-01:

```sql
ALTER TABLE outbox_jobs ADD COLUMN sent_copy_state text NOT NULL DEFAULT 'not_needed'
  CHECK (sent_copy_state IN ('not_needed','pending','filed','failed'));
ALTER TABLE outbox_jobs ADD COLUMN sent_copy_ciphertext text;
ALTER TABLE outbox_jobs ADD COLUMN sent_copy_attempt_at timestamptz;
-- Record the successful transport outcome and retry material together:
UPDATE outbox_jobs SET state='submitted', provider_receipt=$2,
  sent_copy_state='pending',sent_copy_ciphertext=$3,updated_at=now()
WHERE id=$1 AND state='submitting';
```

```go
// Separate worker stage; this stage must NEVER call sender.Send.
if err := appendExactSentCopy(ctx, account, submittedBytes); err != nil {
    return recordSentCopyFailure(ctx, jobID, err) // new durable repository method
}
return markSentCopyFiled(ctx, jobID)
```

The three worker/repository functions above are new integration points. Preserve stable Message-ID or provider receipt information so a lost Append acknowledgment can be reconciled rather than blindly creating duplicate Sent copies.

**Regression requirements.** Make SMTP succeed and Append fail. The UI must show submitted/pending filing, a retry must only append, and the recipient must receive exactly one submission. Exercise lost Append acknowledgment and deleted accounts.

<a id="data-01"></a>

### DATA-01 â€” The thread-based correspondent query still trusts spoofable From evidence

**Severity:** Medium  
**Classification:** Confirmed static defect; partial earlier fix  
**Evidence:** [`classify.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/classify.go#L72-L132)

The recipient and CC correspondent queries now require the owner-authored message to belong to a Sent mailbox. The later `mine`/`other` thread query has no such predicate: it still treats a matching owner From header as proof of outgoing correspondence. Inbound messages can carry that header and matching thread identifiers, so this remaining path can seed trusted correspondents and bypass the Screenerâ€™s intended routing. This is a mail-classification weakness, not a login bypass.

**Suggested fix.** Require genuine Sent membership for `mine` in the thread query too. Centralize the predicate so all outbound-evidence queries share it. Treat thread headers as untrusted grouping hints, not an independent trust source.

**Implementation.**

Add to the thread correspondent query (the alias is `mine`, not `m`):

```sql
AND EXISTS (
  SELECT 1 FROM mail_message_mailboxes mm
  JOIN mail_mailboxes mb
    ON mb.account_id=mm.account_id AND mb.id=mm.mailbox_id
  WHERE mm.account_id=mine.account_id
    AND mm.message_id=mine.id
    AND mb.role='sent'
)
```

```go
// Prefer a fixed, reviewed query constant or a shared CTE:
const sentEvidenceCTE = `WITH outgoing_evidence AS (
  SELECT DISTINCT m.account_id,m.id,m.thread_id
  FROM mail_messages m
  JOIN mail_message_mailboxes mm
    ON mm.account_id=m.account_id AND mm.message_id=m.id
  JOIN mail_mailboxes mb
    ON mb.account_id=mm.account_id AND mb.id=mm.mailbox_id
  WHERE mb.role='sent'
)`
```

Keep explicit user decisions stronger than this heuristic; do not auto-unblock a sender simply because a Sent message exists.

**Regression requirements.** Seed an inbound message with the owner address in From and another message in the same thread: the other sender must remain unknown. Move the genuine owner-authored evidence into Sent and confirm the intended correspondent behavior. Test account scoping and blocked senders.

**Related work:** Earlier DATA-03 was only partially repaired.

<a id="data-02"></a>

### DATA-02 â€” Undoing a sender decision reads stale state before acquiring its serialization lock

**Severity:** Medium  
**Classification:** Concurrency defect  
**Evidence:** [`classify.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/classify.go#L533-L629)

`handleUndecide` reads the previous route/allowed values before opening its transaction and locking the owner. A competing decision can commit between that read and the lock. Undecide then removes the new rule but recalls messages from the old bucket, leaving the new ruleâ€™s effects behind. Adding a lock later in the operation does not validate an earlier read.

**Suggested fix.** Move the previous-rule lookup under the existing owner-row transaction lock. Prefer versioned decisions so an undo can target the precise decision it is reversing rather than whatever rule exists at execution time.

**Implementation.**

Replacement ordering inside `handleUndecide`:

```go
tx, err := a.db.BeginTx(r.Context(), nil)
if err != nil { /* write 500 and return */ return }
defer tx.Rollback()
if err := lockAuthUser(r.Context(), tx, uid); err != nil { return }
var prevRoute string
var prevAllowed bool
err = tx.QueryRowContext(r.Context(),
    `SELECT route,allowed FROM hey_senders WHERE user_id=$1 AND sender_key=$2`,
    uid, req.Sender).Scan(&prevRoute, &prevAllowed)
if errors.Is(err, sql.ErrNoRows) {
    // Already absent: commit/no-op and return the documented response.
} else if err != nil {
    // Report the error; do not proceed with an invented route.
    return
} else {
    // DELETE the rule and reroute using these locked values, through tx.
}
// Check tx.Commit before success.
```

The omitted response branches should use the handler's existing `writeProblem` calls. For exact undo, add a decision version and reject a stale version with 409 rather than deleting a more recent decision.

**Regression requirements.** Pause Undecide after its initial request arrives, commit a new route, then resume. It must either reverse the current locked state or reject the stale version, never remove one rule while undoing another.

<a id="data-03"></a>

### DATA-03 â€” Screening preference changes and classifier decisions are not one consistent transition

**Severity:** Medium  
**Classification:** Concurrency and partial-commit defect  
**Evidence:** [`classify.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/classify.go#L25-L40) Â· [`classify.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/classify.go#L151-L225) Â· [`classify.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/classify.go#L1087-L1165)

`handlePrefs` commits `screening_enabled` and then drains Screener rows in a separate statement. The classifier reads the preference before acquiring its owner lock. A classifier that read â€œoffâ€ can later run after the user re-enabled screening and still drain the waiting room. A failure between the preference write and the drain also leaves a partially applied change despite an error response.

**Suggested fix.** Read screening under the same owner lock as classification and apply preference+drain in one transaction. A common lock must protect both readers of the decision and its associated state changes.

**Implementation.**

Use this transaction in `handlePrefs`:

```go
tx, err := a.db.BeginTx(r.Context(), nil)
if err != nil { return }
defer tx.Rollback()
if err := lockAuthUser(r.Context(), tx, uid); err != nil { return }
if _, err := tx.ExecContext(r.Context(),
    `UPDATE users SET screening_enabled=$2 WHERE id=$1`, uid, enabled); err != nil { return }
if !enabled {
    if _, err := tx.ExecContext(r.Context(),
        `UPDATE hey_messages SET bucket='imbox'
         WHERE user_id=$1 AND bucket='screener'`, uid); err != nil { return }
}
if err := tx.Commit(); err != nil { return }
```

In `classifyUser`, perform `SELECT screening_enabled FROM users WHERE id=$1` through `tx` only after `lockAuthUser`; do not use the earlier pre-lock value. Wire all error branches to an explicit failure response.

**Regression requirements.** Race offâ†’on preference changes against classification. Once the â€œonâ€ change commits, a stale pass must not drain new Screener mail. Fail the drain statement and assert both the preference and routing roll back.

<a id="data-04"></a>

### DATA-04 â€” Thread-wide mutations cannot be undone exactly from one summary row

**Severity:** Medium  
**Classification:** Confirmed state-restoration defect  
**Evidence:** [`classify.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/classify.go#L1000-L1080) Â· [`dashboard/src/app/lib/actions.ts`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/dashboard/src/app/lib/actions.ts#L143-L239) Â· [`classify.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/classify.go#L655-L713)

The message-action handler changes every message in the resolved thread. The browser captures only a list rowâ€™s single read flag, bucket, and optional snooze date. A thread containing a mix of read/unread messages or different buckets loses that per-message state when the inverse action is applied. For example, marking a mixed thread read and undoing can mark all messages unread, including messages that were already read.

**Suggested fix.** Capture exact server-side preimages for all affected rows in the mutation transaction. Return a one-use undo ID and restore each row only if its version still matches the version produced by that mutation. Reject conflicts rather than overwriting later work or newly arrived messages.

**Implementation.**

Schema and mutation pattern:

```sql
ALTER TABLE hey_messages ADD COLUMN version bigint NOT NULL DEFAULT 0;
CREATE TABLE message_action_undo (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz
);
CREATE TABLE message_action_undo_rows (
  undo_id uuid NOT NULL REFERENCES message_action_undo(id) ON DELETE CASCADE,
  account_id text NOT NULL, message_id text NOT NULL,
  bucket text NOT NULL, read_at timestamptz, set_aside_until timestamptz,
  expected_version bigint NOT NULL,
  PRIMARY KEY(undo_id,account_id,message_id)
);
```

```sql
-- In the action transaction: lock target hey_messages rows, save their
-- preimages with expected_version=version+1, then mutate and increment version.
-- In undo: lock the undo record and all target rows; require every version
-- to match before changing any row, then restore:
UPDATE hey_messages h SET bucket=p.bucket,read_at=p.read_at,
  set_aside_until=p.set_aside_until,version=h.version+1
FROM message_action_undo_rows p
WHERE p.undo_id=$1 AND h.user_id=$2
  AND h.account_id=p.account_id AND h.message_id=p.message_id
  AND h.version=p.expected_version;
```

The pre-check and restoration must share one transaction; a partial row-count match is a 409/rollback, not a successful partial undo. The browser sends only the undo ID, not invented replacement state.

**Regression requirements.** Use mixed read flags/buckets/dates within one thread. Action+undo must reproduce every original value. Introduce a later mutation or new thread message and verify undo does not overwrite it. Replaying an undo ID must be rejected or a documented no-op.

<a id="data-05"></a>

### DATA-05 â€” Relative snooze requests and rounded undo dates drift on replay

**Severity:** Medium  
**Classification:** Confirmed semantic defect  
**Evidence:** [`dashboard/src/app/lib/actions.ts`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/dashboard/src/app/lib/actions.ts#L90-L110) Â· [`dashboard/src/app/lib/actions.ts`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/dashboard/src/app/lib/actions.ts#L195-L238) Â· [`classify.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/classify.go#L1040-L1062)

The browser sends `until_days`; the server computes the deadline from its execution time. Offline replay therefore starts the snooze later than the original user action. Undo converts an existing timestamp to a rounded-up number of days and applies it from a new â€œnow,â€ so it cannot restore the original deadline. Daylight-saving and â€œtomorrowâ€ semantics are also unspecified.

**Suggested fix.** Transmit an absolute UTC deadline chosen when the action is created, or an explicit local calendar date plus timezone for calendar-based snoozes. Store and restore the exact timestamp. Define what happens when replay arrives after that deadline.

**Implementation.**

Targeted API change:

```ts
// Queue the intended instant, not a delay that will be recomputed at replay.
await api(path, { body: { action: "set_aside", until: intendedDate.toISOString() } });
// Undo stores the exact previous snooze_until, never ceil(remainingDays).
```

```go
var req struct {
    Action string `json:"action"`
    Until *time.Time `json:"until"`
}
// After bounded decoding:
if req.Action == "set_aside" && req.Until == nil {
    writeProblem(w, 422, "Missing Deadline", "until is required"); return
}
// Policy: an already-expired replay returns mail to imbox rather than
// silently extending the user's chosen deadline.
until := req.Until.UTC()
if until.After(time.Now().AddDate(10, 0, 0)) {
    writeProblem(w, 422, "Invalid Deadline", "deadline is too far in the future"); return
}
// Bind until as a timestamptz parameter in the existing thread-scoped UPDATE.
```

Retain `until_days` only as an explicitly deprecated online compatibility path. New queued commands must use absolute values.

**Regression requirements.** Replay the same command immediately, a day later, and after expiry; its intended deadline must not move. Test exact undo across midnight and daylight-saving changes. Verify â€œtomorrowâ€ uses the documented user timezone.

<a id="data-06"></a>

### DATA-06 â€” Optional account identity makes duplicate message IDs resolve arbitrarily

**Severity:** Medium  
**Classification:** Confirmed API ambiguity; not a cross-user authorization bypass  
**Evidence:** [`classify.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/classify.go#L735-L765) Â· [`classify.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/classify.go#L888-L914) Â· [`classify.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/classify.go#L987-L1012)

Attachment and action endpoints permit a missing account and then select one owner-owned message with `LIMIT 1`; thread reads can combine matching thread IDs across that ownerâ€™s accounts. Provider/native IDs and RFC Message-IDs are not globally unique across connected accounts. Ownership is checked, so this is not evidence of another userâ€™s mail being exposed, but a script can act on or download from the wrong mailbox.

**Suggested fix.** Require account identity on account-scoped message/thread operations and accept one canonical ID form. An endpoint intentionally spanning accounts must return explicit account/message pairs, not silently choose a match.

**Implementation.**

Apply before lookup in the three handlers and document the change for agents/MCP clients:

```go
account := strings.TrimSpace(r.URL.Query().Get("account"))
if account == "" {
    writeProblem(w, http.StatusBadRequest, "Missing Account",
        "account is required for message and thread operations")
    return
}
```

```sql
SELECT m.account_id,m.thread_id
FROM mail_messages m
JOIN email_accounts ea ON ea.mirror_account_id=m.account_id
WHERE ea.user_id=$1 AND m.account_id=$2 AND m.id=$3;
```

Resolve an accepted public account UUID to the canonical mirror ID in a separate owner-scoped helper, rather than scattering `id::text OR mirror_account_id` logic across handlers.

**Regression requirements.** Seed the same message/thread ID in two owned accounts with different content. Missing account must fail; each explicit account must read/mutate only its own copy. Unknown and unowned account IDs must disclose no content.

<a id="data-07"></a>

### DATA-07 â€” Account-setting requests cannot distinguish an omitted field from a destructive zero value

**Severity:** Medium  
**Classification:** Confirmed validation defect  
**Evidence:** [`accounts.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/accounts.go)

The sync/backfill/retention update request shapes use non-pointer bool/int fields. Consequently `{}` can mean â€œdisable synchronizationâ€ or â€œset the window to zero,â€ where zero has meaningful all-history/forever semantics. JSON decoders also generally accept unknown fields and a valid first object followed by extra input. The preferences endpoint already uses a pointer for its required boolean; account updates should do the same.

**Suggested fix.** Represent required values with pointers, reject absence, validate documented ranges, and use a bounded single-document decoder. Preserve explicit zero where it is a supported choice.

**Implementation.**

Targeted request replacements:

```go
var req struct { RetentionDays *int `json:"retention_days"` }
if err := decodeOneJSON(w, r, 16<<10, &req); err != nil {
    writeProblem(w, 400, "Bad Request", err.Error()); return
}
if req.RetentionDays == nil || *req.RetentionDays < 0 {
    writeProblem(w, 422, "Invalid Retention", "retention_days is required and must be nonnegative")
    return
}
days := *req.RetentionDays // explicit 0 remains valid
```

```go
// Use the SAME pattern for BackfillDays *int and SyncEnabled *bool.
// decodeOneJSON is implemented in OPS-01 below.
```

Use the repository's actual JSON field spelling in each replacement; the key rule is presence validation rather than changing the public contract gratuitously.

**Regression requirements.** Test `{}`, `null`, misspelled fields, negative and excessive values, trailing JSON, explicit false, and explicit zero. Only the explicitly supplied valid values may change settings.

<a id="data-08"></a>

### DATA-08 â€” Backfill and retention settings can commit before their corresponding data transition succeeds

**Severity:** Medium  
**Classification:** Confirmed partial-commit/design defect  
**Evidence:** [`accounts.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/accounts.go)

The setting update and subsequent pruning/classification/retention are separate operations. A later failure can leave the new policy committed even though the endpoint reports an error or the local data still reflects the old policy. Concurrent sync and classification are not ordered with a common maintenance operation. The caller has no durable reconciliation status telling it which part succeeded.

**Suggested fix.** Separate desired policy from applied policy and persist a reconciliation job atomically with the desired change. Apply it under account maintenance coordination and report pending/failed/complete. For small purely local changes, one transaction may be sufficient; do not keep a database transaction open while fetching an entire provider mailbox.

**Implementation.**

Integration schema:

```sql
ALTER TABLE email_accounts ADD COLUMN policy_version bigint NOT NULL DEFAULT 0;
ALTER TABLE email_accounts ADD COLUMN applied_policy_version bigint NOT NULL DEFAULT 0;
CREATE TABLE account_reconcile_jobs (
  account_id text PRIMARY KEY REFERENCES email_accounts(mirror_account_id) ON DELETE CASCADE,
  policy_version bigint NOT NULL,
  state text NOT NULL CHECK(state IN ('pending','running','failed','complete')),
  last_error text,
  requested_at timestamptz NOT NULL DEFAULT now()
);
-- In one owner-scoped transaction:
UPDATE email_accounts SET retention_days=$3,policy_version=policy_version+1
WHERE user_id=$1 AND mirror_account_id=$2 RETURNING policy_version;
INSERT INTO account_reconcile_jobs(account_id,policy_version,state)
VALUES ($2,$4,'pending')
ON CONFLICT(account_id) DO UPDATE SET policy_version=excluded.policy_version,
  state='pending',last_error=NULL,requested_at=now();
```

A worker captures the version, reconciles, then advances `applied_policy_version` only if that desired version still matches. Return 202 with the durable job state instead of claiming immediate application.

**Regression requirements.** Fail pruning/classification after policy acceptance, restart the process, and verify reconciliation resumes or shows a persistent failure. Race two policy changes; an older job must not mark a newer version applied.

<a id="data-09"></a>

### DATA-09 â€” Increasing retention does not schedule restoration of previously pruned unchanged mail

**Severity:** Medium  
**Classification:** Confirmed restoration gap for incremental providers  
**Evidence:** [`accounts.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/accounts.go) Â· [`mail-engine/sync.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail-engine/sync.go) Â· [`mail-engine/store.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail-engine/store.go#L635-L695)

Retention deletes local envelopes and bodies but keeps incremental provider cursors. Increasing the retention window later only changes the policy; unchanged old messages may never appear in later deltas. Increasing backfill cannot restore content that has already been physically removed from the mirror. The UI therefore cannot promise that widening a window reconstructs the expected history without a rescan.

**Suggested fix.** Mark a broader-history policy as requiring a staged full enumeration or a provider-supported historical backfill. Preserve current mirror and local product state while rebuilding; do not fix this by invoking the destructive reset described in SYNC-01.

**Implementation.**

Extend the reconciliation job in DATA-08:

```sql
ALTER TABLE account_reconcile_jobs ADD COLUMN full_enumeration boolean NOT NULL DEFAULT false;
-- In the policy-change transaction, compute old/new retention semantics:
-- old > 0 AND (new = 0 OR new > old) means history may need restoring.
UPDATE account_reconcile_jobs SET full_enumeration=true,state='pending'
WHERE account_id=$1;
```

```go
func needsRetentionExpansion(oldDays, newDays int) bool {
    return oldDays > 0 && (newDays == 0 || newDays > oldDays)
}
```

The reconciliation worker uses the staged scan from SYNC-01, filters against the latest policy at commit, and exposes progress. Reuse still-valid cached bodies rather than discarding them.

**Regression requirements.** Prune an old message, widen retention, and supply a provider delta with no changes. The explicit backfill job must restore that message. A failed rebuild must not remove already-available mail or filing state.

<a id="data-10"></a>

### DATA-10 â€” Post-sync processing errors can be reported as a healthy completed sync

**Severity:** Medium  
**Classification:** Confirmed error-propagation defect  
**Evidence:** [`accounts.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/accounts.go) Â· [`mail.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail.go#L124-L142)

`finishSync` records `last_sync_at` and clears `last_error` before mirror-orphan cleanup, retention, and classification complete. Failures in those phases are logged, but the externally visible state can still read as a successful sync. Replacing a cancelled context with an unbounded background context also allows finalization work to outlive shutdown. A provider-success result is not equivalent to a fully usable local view.

**Suggested fix.** Track provider synchronization and local reconciliation separately, or only clear the overall error after required phases succeed. Preserve bounded finalization contexts and emit a state event when an earlier error is cleared even if no messages changed.

**Implementation.**

Refactor finalization to return errors rather than logging them away:

```go
// Integration pattern: these existing phase functions already return errors.
func (a *App) reconcileAfterSync(ctx context.Context, uid string, acct mail.AccountID) error {
    var errs []error
    if err := a.cleanupMirrorOrphans(ctx, acct); err != nil { errs = append(errs, err) }
    // Adapt arguments to the existing per-account retention helper.
    if err := a.classifyUser(ctx, uid); err != nil { errs = append(errs, err) }
    return errors.Join(errs...)
}
```

```sql
-- Prefer separate observable fields:
ALTER TABLE email_accounts ADD COLUMN last_provider_sync_at timestamptz;
ALTER TABLE email_accounts ADD COLUMN last_reconcile_at timestamptz;
ALTER TABLE email_accounts ADD COLUMN reconcile_error text;
```

If cleanup failed because the mirror is mid-rebuild, do not continue destructive orphan cleanup; defer that phase until staged publication. Use a short explicitly bounded shutdown/finalization context where work must survive an individual request, not bare `context.Background()`.

**Regression requirements.** Make provider sync succeed but classification or retention fail. API/events must show degraded reconciliation, not healthy completion. Clear the error on a later zero-change sync and ensure the UI receives the state transition.

<a id="sync-01"></a>

### SYNC-01 â€” Cursor invalidation deletes the live mirror before a replacement is available

**Severity:** High  
**Classification:** Confirmed destructive ordering; failure-dependent impact  
**Evidence:** [`mail-engine/sync.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail-engine/sync.go#L131-L226) Â· [`mail-engine/store.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail-engine/store.go#L659-L695) Â· [`accounts.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/accounts.go)

Both cursor-invalid and provider-reset paths call `ResetMailbox` before a replacement scan succeeds. Reset removes memberships and can delete the message/body when the last membership disappears. The product also removes filing state for messages absent from the mirror. A failed or multi-cycle rebuild can therefore turn temporary cache absence into loss of user filing/snooze state; even without that cleanup, previously readable mail disappears during recovery. Calling the mirror â€œderivedâ€ does not make dependent user-authored state disposable.

**Suggested fix.** Make recovery a non-destructive staged enumeration. Keep the old readable generation and product state until the new scan completes and is reconciled. Only authoritative, completed reconciliation may interpret absence as deletion; do not use absence from a partial cache as proof.

**Implementation.**

Integration schema and commit protocol:

```sql
CREATE TABLE mirror_scans (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id text NOT NULL,
  mailbox_id text NOT NULL,
  continuation text NOT NULL DEFAULT '',
  state text NOT NULL CHECK(state IN ('running','complete','failed')),
  started_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(account_id,mailbox_id)
);
CREATE TABLE mirror_scan_seen (
  scan_id uuid NOT NULL REFERENCES mirror_scans(id) ON DELETE CASCADE,
  message_id text NOT NULL,
  PRIMARY KEY(scan_id,message_id)
);
```

```go
// Replace ResetMailbox-on-invalid with a new staged scan operation.
// New Store methods; every page persists envelopes, seen IDs, and continuation
// together, with no deletion of the old generation on failure.
type ScanStore interface {
    BeginScan(context.Context, AccountID, MailboxID) (string, error)
    ApplyScanPage(context.Context, string, []Envelope, Cursor) error
    FinishScan(context.Context, string, Cursor) error
}
```

```sql
-- Inside FinishScan, after checking scan completion and taking the account
-- maintenance lock, remove only memberships absent from the COMPLETE scan:
DELETE FROM mail_message_mailboxes mm
WHERE mm.account_id=$1 AND mm.mailbox_id=$2
  AND NOT EXISTS (
    SELECT 1 FROM mirror_scan_seen s
    WHERE s.scan_id=$3 AND s.message_id=mm.message_id
  );
-- Publish the terminal provider cursor in this same transaction.
-- Reconcile confirmed deletions/identity migrations explicitly before
-- removing user-authored state; do not run a blanket partial-cache cleanup.
```

The adapter must supply a coherent enumeration/change-feed boundary; staging alone cannot repair an inconsistent provider cursor. Preserve scans across process restarts and clean abandoned scans without touching the live generation.

**Regression requirements.** Invalidate a cursor, fail on page two, restart, and resume. Old mail and user filing must remain accessible until completion. Verify genuine deletions are eventually reconciled, multi-mailbox membership is preserved, and partial scans never trigger orphan-state cleanup.

**Related work:** Earlier SYNC-03 remains open; SYNC-02 and SYNC-03 below share the staging/maintenance design.

<a id="sync-02"></a>

### SYNC-02 â€” Enumeration bookkeeping is not durable across page limits and restarts

**Severity:** Medium  
**Classification:** Confirmed state-machine gap  
**Evidence:** [`mail-engine/sync.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail-engine/sync.go#L151-L226) Â· [`mail-engine/graph/adapter.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail-engine/graph/adapter.go#L202-L245)

`seen`, `enumerating`, and completion flags live only in one `SyncMailbox` call, while the continuation cursor survives calls. A full enumeration crossing `MaxPages` or a restart loses its accumulated membership set and cannot safely finish an absence sweep. The Graph adapter also computes `initial` from an empty cursor on each call and `Complete` from that same flag; a later page of an initial multi-page traversal is no longer marked initial. Thus â€œinitial scan completeâ€ is not represented consistently end to end.

**Suggested fix.** Persist scan identity, full-scan/delta mode, accumulated seen IDs, and continuation. Make adapters distinguish a terminal page from the start of a scan, without requiring `initial` to remain true on every paginated request.

**Implementation.**

A typed cursor envelope can carry the durable mode while preserving the opaque provider token:

```go
type SyncPosition struct {
    Version int    `json:"version"`
    Mode    string `json:"mode"` // "full" or "delta"
    ScanID  string `json:"scan_id,omitempty"`
    Token   string `json:"token"`
}
// Graph: a terminal full page is Mode=="full" && NextLink=="".
// After FinishScan commits, persist Mode="delta" with the terminal DeltaLink.
```

```sql
-- Reuse mirror_scans and mirror_scan_seen from SYNC-01.
-- Update continuation only in the page transaction that records all seen IDs.
UPDATE mirror_scans SET continuation=$2 WHERE id=$1 AND state='running';
```

Do not infer a complete snapshot from â€œthis particular response has no nextLinkâ€ unless the scan mode and starting boundary are known. Legacy opaque cursor values need an explicit migration/compatibility decoder.

**Regression requirements.** Use more than MaxPages, then resume in a new Engine instance. Removed memberships must be reconciled only after all pages, and still-present messages from earlier pages must survive. Test Graph initial scans with two pages and ordinary delta scans separately.

<a id="sync-03"></a>

### SYNC-03 â€” Identity promotion deletes the old message before its replacement and user state are migrated

**Severity:** High  
**Classification:** Confirmed data-loss ordering  
**Evidence:** [`mail-engine/sync.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail-engine/sync.go#L270-L306) Â· [`mail-engine/store.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail-engine/store.go#L486-L514) Â· [`schema.sql`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/schema.sql#L151-L169)

When `UpgradeIdentity` succeeds, `apply` calls `DeleteMessages` on the old ID before `PutEnvelopes` writes the new identity. The old body and memberships are deleted in their own committed transaction. Failure while writing the replacement loses the old readable record; even successful promotion does not migrate product filing state keyed by the old account/message ID. Identity normalization is a migration, not a delete-and-reinsert operation.

**Suggested fix.** Promote an identity transactionally, carrying memberships, body, product state, and references. Handle an already-existing canonical ID with a documented merge policy. Never discard either copy merely because two identifiers map together; keep collision evidence when immutable-content identity is uncertain.

**Implementation.**

Integration contract, implemented by the store/product boundary in one database transaction:

```go
type IdentityMigration struct {
    Account AccountID
    OldID   MessageID
    NewID   MessageID
    Envelope Envelope
}
// New transactional method; replaces DeleteMessages + later PutEnvelopes.
// PromoteIdentity(ctx, migration) must write the canonical envelope first,
// move/merge dependents, and delete the old identity only at the end.
```

```sql
-- Run after canonical envelope insertion, inside the SAME transaction.
INSERT INTO mail_message_mailboxes(account_id,message_id,mailbox_id)
SELECT account_id,$3,mailbox_id FROM mail_message_mailboxes
WHERE account_id=$1 AND message_id=$2
ON CONFLICT DO NOTHING;

INSERT INTO mail_bodies(account_id,message_id,text_body,html_body,parts,fetched_at)
SELECT account_id,$3,text_body,html_body,parts,fetched_at FROM mail_bodies
WHERE account_id=$1 AND message_id=$2
ON CONFLICT(account_id,message_id) DO NOTHING;

-- Move product state only after checking collisions. A collision with
-- different user-authored state must be merged deliberately or abort.
UPDATE hey_messages SET message_id=$3 WHERE account_id=$1 AND message_id=$2;
UPDATE push_deliveries SET message_id=$3 WHERE account_id=$1 AND message_id=$2;
-- Then delete old memberships/body/envelope, and commit once.
```

The two UPDATEs intentionally fail on conflicting unique keys; handle that as a reconciliation case, not by adding `ON CONFLICT DO NOTHING` that silently loses user intent. Include any newly added undo/outbox/identity-alias references in the migration.

**Regression requirements.** Inject failure after each phase and assert the old or fully promoted record remains, never neither. Preserve body, bucket, read state, snooze, and all memberships. Test promotion into an existing canonical ID with both matching and conflicting product state.

<a id="sync-04"></a>

### SYNC-04 â€” Retention, classification, and mirror writes can create orphaned or policy-violating rows

**Severity:** Medium  
**Classification:** Concurrency/integrity defect  
**Evidence:** [`accounts.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/accounts.go) Â· [`classify.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/classify.go#L25-L180) Â· [`mail-engine/store.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail-engine/store.go#L316-L386) Â· [`mail-engine/store.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail-engine/store.go#L573-L590) Â· [`mail-engine/schema.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail-engine/schema.go) Â· [`schema.sql`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/schema.sql)

The mirror tables have no foreign keys to enforce parent membership. `PutBody` can insert a body after retention deletes its message. Retention and synchronization both hold shared account-use access rather than an exclusive maintenance boundary. Classification reads a batch before locking and can later insert product rows after an accountâ€™s mirror rows were removed. Transactions within each operation do not serialize different operations against one another.

**Suggested fix.** Coordinate account writes/retention with one cancellable database maintenance lock and revalidate policy/account existence when applying data. Add safe parent constraints to mirror-owned data after repairing existing orphans. Keep user-authored filing tied to a persistent identity/account, not an ephemeral cache row that may disappear during a rescan.

**Implementation.**

PostgreSQL migration and writer protocol:

```sql
-- Clean orphans deliberately before validating these constraints.
ALTER TABLE mail_bodies ADD CONSTRAINT body_message_fk
  FOREIGN KEY(account_id,message_id) REFERENCES mail_messages(account_id,id)
  ON DELETE CASCADE NOT VALID;
ALTER TABLE mail_message_mailboxes ADD CONSTRAINT membership_message_fk
  FOREIGN KEY(account_id,message_id) REFERENCES mail_messages(account_id,id)
  ON DELETE CASCADE NOT VALID;
ALTER TABLE hey_messages ADD CONSTRAINT filing_account_fk
  FOREIGN KEY(account_id) REFERENCES email_accounts(mirror_account_id)
  ON DELETE CASCADE NOT VALID;
-- Validate after a controlled cleanup, then update engine migration ownership.
```

```sql
-- At the start of EVERY affected write transaction, in a consistent order:
SELECT pg_advisory_xact_lock(hashtextextended('lullmail:account:' || $1, 0));
-- Re-read account existence and the current retention policy under that lock.
-- Apply/retain the envelope, body and cursor consistently with that policy.
```

Use the same lock in product and engine pools; a lock in only one layer is ineffective. Avoid an ON DELETE CASCADE from ephemeral mirror messages directly to irreplaceable filing state. Account deletion is different: deleting that account may intentionally remove its filing.

**Regression requirements.** Pause body fetch, run retention, then resume: no orphan body may be inserted. Pause classifier after its batch read and delete the account: no filing row may survive/reappear. Run synchronization and policy changes from separate database connections/processes and check all invariants.

<a id="data-11"></a>

### DATA-11 â€” Bucket results stop at 200 without a continuation contract or stable tie order

**Severity:** Medium  
**Classification:** Confirmed completeness/usability limitation  
**Evidence:** [`classify.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/classify.go#L641-L714)

`handleBucket` applies a hard `LIMIT 200` and exposes no cursor. Older matching threads can be unreachable through that view. Both the latest-per-thread selection and outer ordering rely on timestamps without a deterministic identity tie-breaker, which can make equal-timestamp rows change order between requests. An honest limit needs a way to continue, not just a larger constant.

**Suggested fix.** Implement keyset pagination on a stable, deterministic thread-summary ordering. Return `items` and `next_cursor`, and update browser/MCP consumers. Normalize null timestamps consistently and cap page size.

**Implementation.**

Query pattern after producing one summary row per account/thread:

```sql
WITH summaries AS (
  SELECT DISTINCT ON (m.account_id,m.thread_id)
    m.account_id,m.thread_id,m.id,
    COALESCE(m.received_at, '-infinity'::timestamp) AS sort_time
  FROM mail_messages m
  JOIN hey_messages h ON h.account_id=m.account_id AND h.message_id=m.id
  WHERE h.user_id=$1 AND h.bucket=ANY($2)
  ORDER BY m.account_id,m.thread_id,m.received_at DESC NULLS LAST,m.id DESC
)
SELECT * FROM summaries
WHERE $3::boolean OR (sort_time,account_id,thread_id,id)<($4,$5,$6,$7)
ORDER BY sort_time DESC,account_id DESC,thread_id DESC,id DESC
LIMIT $8;
```

Fetch `pageSize+1` to determine whether a next cursor exists. Encode the last returned tuple in a versioned base64url JSON cursor; validate its types and scope. The first-page boolean is server-selected after cursor validation, not a SQL fragment from the request. Include the account/bucket filter in the cursor contract.

**Regression requirements.** Load 450 matching threads, including null/equal timestamps. Traverse all pages without omissions or duplicates for an unchanged dataset. Reject malformed or filter-mismatched cursors and oversized page sizes.

<a id="data-12"></a>

### DATA-12 â€” Bounded body prefetch has no explicit missing-body contract

**Severity:** Medium  
**Classification:** Confirmed API representation gap  
**Evidence:** [`classify.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/classify.go#L735-L878) Â· [`mail.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail.go#L383-L394)

The thread endpoint now correctly limits eager upstream body fetches to eight and an overall eight-second context. It still returns empty strings for both genuinely empty messages and bodies that were never fetched or whose fetch failed. There is no body-status field in its response. A consumer cannot reliably know whether blank content is authoritative or should be fetched/retried. Additionally, the query still loads the entire thread and all already-cached bodies before applying the eager-fetch limit.

**Suggested fix.** Return explicit body status, paginate thread messages, and provide a bounded owner/account-scoped single-message body endpoint. Make the UI request the body of the opened message rather than repeatedly asking for every body in a large thread. Cap response bytes as well as provider attempts.

**Implementation.**

Extend the response and use status based on successful storage/fetch, not body length:

```go
type BodyStatus string
const (
    BodyReady BodyStatus = "ready"
    BodyMissing BodyStatus = "missing"
    BodyFailed BodyStatus = "failed"
)
// Add to msgRow:
// BodyStatus BodyStatus `json:"body_status"`
// row.BodyStatus = BodyMissing when !fetched.Valid
// Set BodyReady only for a cached or successfully fetched complete body.
// Set BodyFailed on an attempted fetch failure; return a safe error code.
```

```ts
// Reader integration; new owner-scoped API endpoint must be added server-side.
if (message.body_status !== "ready") {
  const complete = await api<Message>(
    `/messages/${encodeURIComponent(message.id)}/body?account=${encodeURIComponent(message.account)}`,
    { fresh: true, signal: controller.signal },
  );
  // Apply only if the same owner/generation/message is still active.
  replaceMessage(complete);
}
```

The new handler must use the existing ownership and account-work lifecycle checks, the engine's mailbox selection, a deadline, and body-size/admission limits. An empty ready body must not trigger an infinite retry loop.

**Regression requirements.** Open a thread with more than eight uncached messages and then select the ninth. It must become readable through explicit hydration. Distinguish empty, missing, failed, and ready bodies; cancel navigation and owner changes without stale UI writes.

<a id="data-13"></a>

### DATA-13 â€” Classifier failures can persist decisions made from incomplete evidence and hold broad locks for large batches

**Severity:** Medium  
**Classification:** Confirmed error-handling and scalability gap  
**Evidence:** [`classify.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/classify.go#L25-L180)

Correspondent-extraction failures are logged and classification continues with an incomplete map. Those classifications are persisted rather than retried from complete evidence. The entire unclassified set is collected in memory, followed by a sender lookup and insert per message inside a transaction holding the owner row lock. Large imports can therefore produce avoidable query amplification and delay security or sender operations that use the same row lock.

**Suggested fix.** Fail/retry a classification pass if required evidence cannot be read; distinguish optional enrichment from authoritative routing inputs. Batch the operation and use set-based SQL under a bounded transaction, with existence/policy revalidation. Do not retain an open result set while waiting for another connection from a tightly capped pool.

**Implementation.**

Targeted error correction:

```go
if err := collect(recipientsQuery); err != nil {
    return fmt.Errorf("classify: recipient evidence: %w", err)
}
if err := collect(ccQuery); err != nil {
    return fmt.Errorf("classify: CC evidence: %w", err)
}
if err := collect(threadQuery); err != nil {
    return fmt.Errorf("classify: thread evidence: %w", err)
}
```

Set-based batch pattern:

```sql
WITH batch AS (
  SELECT m.account_id,m.id
  FROM mail_messages m
  JOIN email_accounts ea ON ea.mirror_account_id=m.account_id
  WHERE ea.user_id=$1
    AND NOT EXISTS (SELECT 1 FROM hey_messages h WHERE h.user_id=$1
      AND h.account_id=m.account_id AND h.message_id=m.id)
  ORDER BY m.account_id,m.id LIMIT 500
)
SELECT * FROM batch;
```

Resolve sender decisions for the batch with a join, then perform one `INSERT ... SELECT ... ON CONFLICT DO NOTHING` using the existing precedence rules. Keep the transaction short and continue with another bounded batch only while the task context remains active.

**Regression requirements.** Inject an evidence-query failure and assert no potentially incorrect batch is committed. Benchmark a large import, observe bounded transaction duration/query count, and ensure concurrent security operations are not starved. Run with a deliberately small connection pool.

<a id="data-14"></a>

### DATA-14 â€” Attachment downloads build filenames manually and ignore stream errors

**Severity:** Low  
**Classification:** Confirmed output-handling weakness  
**Evidence:** [`classify.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/classify.go#L887-L961) Â· [`export.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/export.go#L210-L226)

Unlike the export endpoint, the attachment endpoint constructs `Content-Disposition` by concatenation and stripping only quotes. Provider filenames can contain separators, backslashes, controls, or non-ASCII text requiring proper encoding. The handler also discards the result of `io.Copy`, so partial downloads are not logged. This is not a claim of HTTP response splitting; Go's HTTP header handling also imposes protections.

**Suggested fix.** Use a sanitized display filename and `mime.FormatMediaType`, set no-store/nosniff, and check copy errors. Never turn a client filename into a server filesystem path.

**Implementation.**

Targeted replacement; imports mime, strings, unicode, io:

```go
func attachmentFilename(s string) string {
    s = strings.ReplaceAll(s, "\\", "/")
    if i := strings.LastIndex(s, "/"); i >= 0 { s = s[i+1:] }
    s = strings.Map(func(r rune) rune {
        if unicode.IsControl(r) { return -1 }; return r
    }, s)
    s = strings.TrimSpace(s)
    if s == "" || s == "." || s == ".." { return "attachment" }
    return s
}

w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment",
    map[string]string{"filename": attachmentFilename(filename)}))
w.Header().Set("Content-Type", "application/octet-stream")
w.Header().Set("Cache-Control", "no-store")
w.Header().Set("X-Content-Type-Options", "nosniff")
if _, err := io.Copy(w, rc); err != nil {
    a.log.Warn("attachment stream interrupted", "account", acct, "message", msgID, "err", err)
}
```

Do not write a JSON error after download headers/body have already been sent.

**Regression requirements.** Exercise quotes, slashes, backslashes, newlines, emoji and non-Latin filenames. Abort the client mid-stream and ensure the event is logged without attempting a second HTTP response.

<a id="export-01"></a>

### EXPORT-01 â€” Archive construction can suppress write/manifest failures while reporting planned counts

**Severity:** Medium  
**Classification:** Confirmed error-handling defect  
**Evidence:** [`export.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/export.go#L139-L200)

The export correctly builds a temporary archive before sending 200, and credential failure now has a mirror fallback. However, entry creation and per-message write failures become warnings and processing continues; manifest creation and encoding errors are ignored. Counts are based on planned messages rather than confirmed successful writes. `zip.Writer.Close` catches many underlying errors, but relying on a later close is not a substitute for handling every construction failure or ensuring the promised manifest exists.

**Suggested fix.** Treat local archive I/O and manifest failure as fatal before committing HTTP headers. Distinguish provider-read degradation (which can legitimately produce a labeled fallback) from an unwritable or structurally incomplete archive. Count messages only after writing them successfully.

**Implementation.**

Replace warning-and-continue branches with checked construction:

```go
entry, err := zw.CreateHeader(h)
if err != nil { return fmt.Errorf("create export entry %q: %w", box.filename, err) }
written := 0
for _, msg := range box.messages {
    // Obtain raw or a clearly disclosed mirror fallback as today.
    if err := writeMboxRD(entry, msg.envelope, raw); err != nil {
        return fmt.Errorf("write export message %s: %w", msg.id, err)
    }
    written++
}
report.Messages = written

manifestEntry, err := zw.Create("export-manifest.json")
if err != nil { return fmt.Errorf("create export manifest: %w", err) }
enc := json.NewEncoder(manifestEntry)
enc.SetIndent("", "  ")
if err := enc.Encode(manifest); err != nil { return fmt.Errorf("encode manifest: %w", err) }
if err := zw.Close(); err != nil { return fmt.Errorf("finalize archive: %w", err) }
```

Extract the archive builder into a function returning `error`; the HTTP handler writes a clean 5xx and removes the temporary file on failure. This snippet belongs in that builder, not a void handler with `return error` pasted into it.

**Regression requirements.** Use a failing writer at entry, body, manifest, and close stages. No 200 or downloadable partial archive may be emitted. Every successful ZIP must contain a parseable manifest with counts matching actual message writes. Verify degraded provider fallback still works.

<a id="web-01"></a>

### WEB-01 â€” Browser caches use a mutable email identity and do not fence in-flight requests

**Severity:** Medium  
**Classification:** Confirmed isolation/concurrency gap  
**Evidence:** [`dashboard/src/app/lib/api.ts`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/dashboard/src/app/lib/api.ts#L44-L157) Â· [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/dashboard/src/app/lib/offline.ts#L66-L87) Â· [`auth.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/auth.go#L324-L363)

Offline ownership is an email string; the memory cache is keyed only by API path. A direct owner change does not necessarily clear that memory cache. More importantly, a request started for the old owner can complete after a change and `cacheResponse` reads the new owner at write time, storing old data under the new namespace. Queue replay likewise does not revalidate ownership before every network operation. Single-owner deployments can still change ownership through deletion/reinitialization or a shared browser.

**Suggested fix.** Return an immutable user UUID plus installation/storage generation from auth status. Capture that scope when a request or queue item is created; use it in memory/IndexedDB keys and check it again before any write or UI update. Abort and invalidate work on owner changes, with cross-tab coordination. The server must reject a queued command whose expected owner/generation differs from its authenticated session.

**Implementation.**

Client integration; this is a new scope model, not an email rename:

```ts
type Scope = Readonly<{ ownerId: string; generation: string }>;
let activeScope: Scope | null = null;
const activeRequests = new Set<AbortController>();

function sameScope(a: Scope | null, b: Scope | null): boolean {
  return !!a && !!b && a.ownerId === b.ownerId && a.generation === b.generation;
}
function cacheKey(s: Scope, path: string): string {
  return JSON.stringify([s.ownerId, s.generation, path]);
}
function switchScope(next: Scope | null): void {
  if (sameScope(activeScope, next)) return;
  for (const c of activeRequests) c.abort();
  activeRequests.clear();
  memoryResponses.clear();
  activeScope = next;
  // Reset account/draft/reader signals and notify other tabs here.
}

async function fetchScoped<T>(path: string): Promise<T> {
  const started = activeScope;
  if (!started) throw new Error("No authenticated owner scope");
  const controller = new AbortController();
  activeRequests.add(controller);
  try {
    const res = await fetch("/api" + path, {
      credentials: "same-origin", signal: controller.signal,
      headers: { "X-Lull-Owner": started.ownerId, "X-Lull-Generation": started.generation },
    });
    if (!res.ok) throw new ApiError(String(res.status), res.status);
    const value = await res.json() as T;
    if (!sameScope(started, activeScope)) throw new DOMException("Owner changed", "AbortError");
    // putScopedCache must check the current scope in the SAME IDB transaction
    // as the write; a preceding asynchronous check is not sufficient.
    await putScopedCache(started, path, value);
    if (!sameScope(started, activeScope)) throw new DOMException("Owner changed", "AbortError");
    return value;
  } finally { activeRequests.delete(controller); }
}
```

Add an IndexedDB `meta` store containing the current scope. `putScopedCache` reads `meta`, compares both values, and writes `responses` in one readwrite transaction; it aborts on mismatch. Update all callers to use this explicit scope, not `offlineOwner()` at completion. Server expected-scope headers are comparisons, not authentication credentials.

**Regression requirements.** Delay an old-owner response until after logout/reset and new-owner login; it must neither render nor enter any new cache. Repeat across two tabs, attachment persistence, a 401, offline replay, and reinstalling an owner with the same email.

**Related work:** Earlier WEB-07 remains open; WEB-02 and WEB-03 are required companions.

<a id="web-02"></a>

### WEB-02 â€” Clearing offline data suppresses errors and is not an atomic owner reset

**Severity:** Medium  
**Classification:** Confirmed privacy/durability defect  
**Evidence:** [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/dashboard/src/app/lib/offline.ts#L151-L168)

`clearOfflineData` clears three stores in separate transactions, catches all errors, then removes the owner marker regardless of success. The caller can believe private data was erased while some cached responses, queued commands, or attachments remain. Partial failure is especially dangerous when the same email namespace is later reused. The newly corrected commit-aware transaction helper does not fix the surrounding swallowed error.

**Suggested fix.** Clear all private stores and advance the storage generation in one transaction. Do not report completion or remove the authoritative ownership marker unless that transaction commits. Handle blocked/version-changed database connections and communicate a failed wipe to the user.

**Implementation.**

Implementation inside offline.ts; create META during an IndexedDB version upgrade:

```ts
const META = "meta";
export async function clearOfflineDataAtomically(): Promise<void> {
  const db = await openDB();
  try {
    await new Promise<void>((resolve, reject) => {
      const tx = db.transaction([CACHE, QUEUE, ATTACHMENTS, META], "readwrite");
      tx.oncomplete = () => resolve();
      tx.onabort = () => reject(tx.error ?? new Error("Private-data reset aborted"));
      tx.onerror = () => { /* abort handler owns rejection */ };
      tx.objectStore(CACHE).clear();
      tx.objectStore(QUEUE).clear();
      tx.objectStore(ATTACHMENTS).clear();
      tx.objectStore(META).put({
        key: "scope", ownerId: "", generation: crypto.randomUUID(),
      });
    });
  } finally { db.close(); }
  // Compatibility hint only; the committed META row is authoritative.
  localStorage.removeItem(OWNER);
}
```

Create META with `keyPath: "key"`. Close old connections on `versionchange`; show a blocked-upgrade message rather than hanging indefinitely. The cache writer in WEB-01 must use the same META store/generation, otherwise a late write can repopulate a successfully cleared database.

**Regression requirements.** Abort one reset transaction and assert that callers receive failure and no partial reset is presented as success. Race a cache write with reset, simulate quota/storage denial, and keep another tab open during the schema upgrade.

<a id="web-03"></a>

### WEB-03 â€” Offline replay can run twice and has no server idempotency contract

**Severity:** Medium  
**Classification:** Confirmed concurrency/uncertain-retry gap  
**Evidence:** [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/dashboard/src/app/lib/offline.ts#L92-L151) Â· [`dashboard/src/app/lib/api.ts`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/dashboard/src/app/lib/api.ts#L73-L104)

Each tab reads the same queued commands and replays independently. Neither an in-tab single-flight guard nor a cross-tab lease prevents duplicates. A request that commits but loses its response can also be queued/retried again. State-setting operations are not universally harmless to repeat: relative snoozes, undo, timestamps, and future commands have non-idempotent consequences.

**Suggested fix.** Serialize replay across tabs and use the persisted queue ID as an end-to-end idempotency key. The server must store the key, request hash, and result in the same transaction as the mutation. A browser lock alone does not handle a lost acknowledgment, process crash, or another device.

**Implementation.**

Browser first line of defense:

```ts
export async function replayOnce(): Promise<number> {
  if (!("locks" in navigator)) {
    throw new Error("Offline replay requires a transactional IDB lease on this browser");
  }
  return navigator.locks.request("lullmail-offline-replay", async () => {
    return replayMutationsWithIdempotency();
  });
}
// Every replay fetch includes:
// "Idempotency-Key": item.id
// "X-Lull-Owner": item.ownerId
// "X-Lull-Generation": item.generation
```

Server integration:

```sql
CREATE TABLE mutation_receipts (
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  request_key text NOT NULL,
  request_hash text NOT NULL,
  http_status integer NOT NULL,
  result jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(user_id,request_key)
);
```

For each command, take a transaction-scoped lock on `(user_id, request_key)`, return a matching stored receipt, reject a mismatched hash, or apply the action and insert its receipt before committing. Avoid generic HTTP middleware that records success after a separately committed handler. Implement an atomic IndexedDB replay lease as the fallback where Web Locks is unavailable, rather than silently running unlocked.

**Regression requirements.** Replay one queue from two tabs simultaneously. Kill the tab after the server commits but before local deletion, then resume. Exactly one logical mutation must result and both retries must observe the same receipt. Reusing a key with different content must return 409.

<a id="web-04"></a>

### WEB-04 â€” Rejected offline actions are retained but not surfaced as structured user-visible failures

**Severity:** Medium  
**Classification:** Confirmed replay outcome gap  
**Evidence:** [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/dashboard/src/app/lib/offline.ts#L111-L149) Â· [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/dashboard/src/app/lib/offline.ts#L198-L224)

The repaired replay path no longer deletes every 4xx as success. It marks a failed record and writes `console.warn`, but returns only a count of committed commands. `startOfflineData` acts only on that count, so a run containing only failures does not surface a structured rejection or reconcile a previously optimistic display. A private IndexedDB record and developer-console warning are not sufficient feedback about lost user intent.

**Suggested fix.** Return and display committed, retrying, reauthentication-needed, and rejected outcomes. Provide an offline activity view with inspect/edit/retry/discard actions. Treat conflicts and preconditions as actionable states rather than asserting every such action can never succeed.

**Implementation.**

Replace the numeric replay result with a typed summary:

```ts
interface ReplayFailure { id: string; path: string; status: number; detail: string }
interface ReplaySummary {
  committed: number;
  rejected: ReplayFailure[];
  retryAt?: number;
  needsSignIn: boolean;
}

async function handleReplaySummary(s: ReplaySummary): Promise<void> {
  if (s.committed > 0) { reload(); await refreshCounts(); }
  if (s.rejected.length > 0) {
    showError(`${s.rejected.length} offline action(s) need attention`);
    // Persist and expose these records in an Activity/Offline queue view.
  }
  if (s.needsSignIn) showError("Sign in to finish queued offline work");
}
```

Preserve a safe error code/detail, not credential-bearing response bodies. A 409 should offer conflict resolution; a 404 may offer discard. Manual retry after editing must receive a new idempotency key, whereas retrying unchanged uncertain work keeps the original key.

**Regression requirements.** A run with zero commits and one 422/409 must show an actionable failure. Verify the record survives reload, can be inspected/discarded, and is not included in successful-action counts. Reconcile optimistic UI state on rejection.

<a id="web-05"></a>

### WEB-05 â€” Transient replay failures can remain stranded while the browser stays online

**Severity:** Medium  
**Classification:** Confirmed retry-scheduling omission  
**Evidence:** [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/dashboard/src/app/lib/offline.ts#L102-L150) Â· [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/dashboard/src/app/lib/offline.ts#L198-L224)

429, selected timeout statuses, and 5xx now correctly stop replay without discarding work. However, replay is started on mount and on an `online` event; a transient failure while connectivity stays online does not itself schedule another attempt. The queue can remain pending until another navigation/mount or network transition, and Retry-After is not consumed.

**Suggested fix.** Persist attempt count and next-attempt time, honor Retry-After, and schedule bounded exponential retry with jitter. Keep replay serialized, pause on 401, and cancel timers on logout/unmount. Do not let a permanent failure block unrelated commands forever without visibility.

**Implementation.**

Reusable delay calculation:

```ts
function retryAfterMs(value: string | null, now = Date.now()): number {
  if (!value) return 0;
  const seconds = Number(value);
  if (Number.isFinite(seconds) && seconds >= 0) return seconds * 1000;
  const date = Date.parse(value);
  return Number.isFinite(date) ? Math.max(0, date - now) : 0;
}
function retryDelay(attempt: number, header: string | null): number {
  const base = Math.min(300_000, 1000 * 2 ** Math.min(attempt, 8));
  const jittered = base * (0.8 + Math.random() * 0.4);
  return Math.max(jittered, retryAfterMs(header));
}
// On a retry response, persist item.attempts+1 and item.nextAttemptAt,
// return retryAt in ReplaySummary, then schedule replayOnce at that time.
// Also inspect persisted nextAttemptAt at startup; a timer is not durable.
```

Use a scheduler that rechecks the current owner and `navigator.onLine` before replay. A long server Retry-After must not be shortened by the client's normal backoff cap.

**Regression requirements.** Return 429 with seconds/date Retry-After and then 200 without changing network connectivity. Work must retry at the permitted time. Restart the tab during backoff and verify the persisted schedule is honored. A 401 must pause rather than loop.

<a id="web-06"></a>

### WEB-06 â€” Successful mutations delete the entire offline response cache

**Severity:** Medium  
**Classification:** Confirmed offline-availability defect  
**Evidence:** [`dashboard/src/app/lib/api.ts`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/dashboard/src/app/lib/api.ts#L105-L121) Â· [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/dashboard/src/app/lib/offline.ts#L153-L156) Â· [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/dashboard/src/app/lib/offline.ts#L198-L218)

The initial-offline-startup wipe has been fixed. A remaining path clears every persisted response after any successful protected mutation, and replay does the same after one committed command. A small note or read-state change can erase unrelated cached threads and attachments metadata, leaving the mailbox unavailable on the next offline visit. Invalidation is being implemented as deletion of the only offline copy.

**Suggested fix.** Invalidate freshness, not offline existence. Clear the short-lived memory cache as needed, but retain persisted snapshots with stale markers and mutation overlays. Refresh affected resources online; reconcile cached views with queued mutations and expose stale/offline status.

**Implementation.**

Change the cache record contract:

```ts
interface Cached {
  key: string; ownerId: string; generation: string;
  savedAt: number; stale: boolean; value: unknown;
}
// On mutation: clearMemoryCache(); markAffectedResponsesStale(scope, affectedTags).
// Do NOT call store.clear() for ordinary state changes.
// On offline read: return the last snapshot + pending local overlays + stale=true.
// On a successful fresh GET: replace the snapshot and set stale=false.
```

```ts
// Minimal safe stopgap in request():
} else if (protectedRoute && res.ok) {
  memoryResponses.clear();
  // Keep persisted offline responses. Refresh active views and indicate that
  // other offline snapshots may be stale until tag-based invalidation lands.
}
```

Move the 204 success branch after mutation invalidation so no-content responses follow the same policy. Ordinary invalidation must remain separate from a deliberate private-data wipe on owner/account deletion.

**Regression requirements.** Cache several threads, mutate an unrelated note/read flag, go offline, and verify the cached mail remains readable with correct stale/pending indicators. Test a successful 204 mutation and replay that commits exactly one queued command.

<a id="web-07"></a>

### WEB-07 â€” Offline queue acknowledgment is presented through the same success path as a server commit

**Severity:** Medium  
**Classification:** Confirmed API/UI semantics gap  
**Evidence:** [`dashboard/src/app/lib/api.ts`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/dashboard/src/app/lib/api.ts#L83-L102) Â· [`dashboard/src/app/lib/actions.ts`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/dashboard/src/app/lib/actions.ts#L90-L174)

On a network failure, a queueable mutation returns `{queued: true}` cast to arbitrary `T`. Callers such as bulk actions treat a fulfilled promise as a completed action and construct normal success/undo feedback. This loses the distinction between persisted local intent and a server-side change; the generic cast can also violate callers' expected response shape. The original request may have committed before its response was lost, further requiring idempotent reconciliation.

**Suggested fix.** Make queued outcomes explicit in the API type and UI. Persist optimistic overlays separately, report â€œqueued offline,â€ and define how undo cancels an unsubmitted command rather than merely adding an inverse command. For uncertain requests, keep the same idempotency key until the server resolves their outcome.

**Implementation.**

Typed replacement for queueable command calls:

```ts
type MutationResult<T> =
  | { state: "committed"; value: T }
  | { state: "queued"; operationId: string };

// queueMutation returns its persisted id instead of Promise<void>.
async function actOn(/* existing arguments */): Promise<MutationResult<{ ok: boolean }>> {
  // Use a dedicated mutation API returning the union, not `as T`.
  return mutateWithOfflineQueue(path, body);
}

function describeMutation<T>(result: MutationResult<T>): string {
  return result.state === "queued" ? "Queued offline" : "Updated";
}
```

The two command functions above are interface changes that require updating callers. A local cancel must atomically remove the unsent command and its overlay. Once replay has started, reconcile with the server receipt before issuing a separate inverse operation.

**Regression requirements.** Test committed, queued, uncertain-acknowledgment, and rejected outcomes in single and bulk actions. Undo before replay must cancel local intent without a later stale command being applied. No caller may assume a queued placeholder has its normal response fields.

<a id="web-08"></a>

### WEB-08 â€” IndexedDB connection lifecycle can hang during schema upgrades and lacks explicit storage-failure UX

**Severity:** Low  
**Classification:** Confirmed robustness gap  
**Evidence:** [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/dashboard/src/app/lib/offline.ts#L25-L65) Â· [`dashboard/src/app/lib/api.ts`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/dashboard/src/app/lib/api.ts#L126-L157)

`openDB` handles success/error/upgrade creation but not a blocked upgrade or `versionchange` closure. With another tab holding a connection, a future schema bump can wait without useful feedback. Local-storage/IndexedDB errors during owner preparation can also fall into the generic auth-refresh failure path and be labeled server unreachability even when the network worked.

**Suggested fix.** Handle blocked upgrades and close connections on versionchange. Separate storage errors from network/auth errors and allow an explicit online-only mode rather than silently claiming offline persistence.

**Implementation.**

Update openDB handlers and error categorization:

```ts
class OfflineStorageError extends Error {}
function openDB(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(DB, VERSION);
    let abandoned = false;
    request.onblocked = () => {
      abandoned = true;
      reject(new OfflineStorageError("Close other Lullmail tabs to update offline storage"));
    };
    request.onupgradeneeded = () => {
      // Existing stores + versioned META migration from WEB-01/WEB-02.
    };
    request.onerror = () => reject(new OfflineStorageError(request.error?.message ?? "Storage unavailable"));
    request.onsuccess = () => {
      const db = request.result;
      if (abandoned) { db.close(); return; }
      db.onversionchange = () => db.close();
      resolve(db);
    };
  });
}
```

Retain the existing store-creation code inside onupgradeneeded; do not replace it with an empty handler. In `refreshAuth`, separate the network fetch from `prepareOfflineOwner` so storage failure does not set `unreachable=true`.

**Regression requirements.** Hold a connection in another tab during an upgrade and assert useful feedback plus eventual safe closure. Disable browser storage while leaving the server available; authentication must remain accurately represented and offline-save failure must be explicit.

<a id="ops-01"></a>

### OPS-01 â€” HTTP and export resource limits are inconsistent and do not bound aggregate work

**Severity:** Medium  
**Classification:** Confirmed availability/hardening gap  
**Evidence:** [`cmd_serve.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/cmd_serve.go) Â· [`accounts.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/accounts.go) Â· [`classify.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/classify.go#L448-L460) Â· [`classify.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/classify.go#L972-L987) Â· [`export.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/export.go#L111-L138) Â· [`sendqueue.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/sendqueue.go#L63-L85)

Some sensitive endpoints have appropriate caps, including the send endpoint's enlarged base64-aware limit. Others decode request bodies without `MaxBytesReader`. The server sets a header timeout but no general idle/body policy, and exports can consume unbounded temporary disk and response-building work. A bounded individual message or fetch count is not an aggregate concurrency, memory, or disk budget.

**Suggested fix.** Use endpoint-specific body limits and strict single-document JSON parsing; apply request admission, deadlines, export byte quotas, and bounded concurrent exports. Keep streaming/SSE exceptions explicit rather than installing a blanket write timeout that breaks them.

**Implementation.**

Reusable decoder; complete helper with standard-library imports:

```go
// imports encoding/json, errors, fmt, io, net/http
func decodeOneJSON(w http.ResponseWriter, r *http.Request, limit int64, dst any) error {
    r.Body = http.MaxBytesReader(w, r.Body, limit)
    dec := json.NewDecoder(r.Body)
    dec.DisallowUnknownFields()
    if err := dec.Decode(dst); err != nil { return err }
    var extra any
    if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
        if err != nil { return err }
        return fmt.Errorf("request must contain exactly one JSON value")
    }
    return nil
}
```

```go
// Preserve the existing send-specific cap; small settings need far less.
srv.IdleTimeout = 60 * time.Second
srv.MaxHeaderBytes = 32 << 10
// Non-streaming JSON routes can also set a bounded body-read deadline:
rc := http.NewResponseController(w)
if err := rc.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
    // Log unsupported deadline control or enforce an equivalent ingress timeout.
}
```

```go
// Wrap the temporary archive writer; limit is an operator-configured quota.
type quotaWriter struct { w io.Writer; remaining int64 }
func (q *quotaWriter) Write(p []byte) (int, error) {
    if int64(len(p)) > q.remaining { return 0, errors.New("export exceeds configured byte quota") }
    n, err := q.w.Write(p); q.remaining -= int64(n); return n, err
}
```

Map `*http.MaxBytesError` to 413, not malformed-JSON 400. A disk quota must include concurrent jobs and available free space. Offer explicit export pagination/jobs rather than silently excluding mail when a quota is reached.

**Regression requirements.** Exercise chunked oversized bodies, slow body readers, trailing JSON, parallel exports, disk-full conditions and cancelled downloads. Memory/disk/connection use must stay bounded. Verify SSE and legitimate large sends still work.

<a id="ops-02"></a>

### OPS-02 â€” The product database pool is unbounded and authenticated reads write the same session row repeatedly

**Severity:** Medium  
**Classification:** Confirmed scalability/availability gap  
**Evidence:** [`mail.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail.go#L34-L84) Â· [`auth.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/auth.go#L202-L236)

The product `database/sql` pool has no configured maximum while the engine uses a separate pool. Request concurrency can therefore consume the database connection budget unexpectedly. Every authenticated request also updates `auth_sessions.last_seen_at`, creating write amplification and contention on the same row for a busy browser. These costs compound with bulk actions and long-running transactions.

**Suggested fix.** Budget both pools together, bound product connections and idle lifetime, and monitor wait time. Separate session validation from a throttled last-seen touch. Avoid an authentication cache that would delay revocation.

**Implementation.**

Startup example, sized against an explicit deployment connection budget:

```go
db.SetMaxOpenConns(16)
db.SetMaxIdleConns(4)
db.SetConnMaxIdleTime(5 * time.Minute)
db.SetConnMaxLifetime(30 * time.Minute)
// Configure the engine pool separately; leave headroom for migrations/admin.
```

```sql
-- Validate the session without changing it on every read (include AUTH-01 epoch).
SELECT s.user_id
FROM auth_sessions s JOIN users u ON u.id=s.user_id
WHERE s.id_hash=$1 AND s.expires_at>now() AND s.auth_epoch=u.auth_epoch;

-- Throttled metadata update; failure is logged, not a fabricated logout.
UPDATE auth_sessions SET last_seen_at=now()
WHERE id_hash=$1 AND last_seen_at<now()-interval '1 minute';
```

The product classifier currently opens additional queries while another result set is active; close/materialize bounded batches first so a smaller pool cannot deadlock on nested connection acquisition. Export and worker admission are still needed even after setting pool limits.

**Regression requirements.** Run concurrent tabs and bulk actions with a small PostgreSQL connection ceiling. Check DB.Stats wait metrics and session-row update frequency. Revoked sessions must fail immediately despite last-seen throttling. Run with low pool sizes to detect nested connection waits.

<a id="ops-03"></a>

### OPS-03 â€” Startup migrations repeatedly alter live constraints without a version ledger or shared migration lock

**Severity:** Medium  
**Classification:** Confirmed operational reliability gap  
**Evidence:** [`mail.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail.go#L57-L81) Â· [`mail.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail.go#L150-L195) Â· [`schema.sql`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/schema.sql#L38-L52) Â· [`schema.sql`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/schema.sql#L106-L121)

Startup executes the product schema statement by statement on every boot, then engine migration, then account-scope migration. Some statements drop/re-add constraints or indexes. There is no application migration ledger/checksum or installation-wide migration lock in this path. Concurrent starts and failure halfway through the sequence can leave partially converged schemas; the same short startup context covers ping plus all migrations.

**Suggested fix.** Use ordered, versioned, checksum-verified migrations with a database lock and explicit rollback/forward-recovery strategy. Separate connection readiness timeout from migration timeout. Test upgrades from historical schemas, not just empty-database creation.

**Implementation.**

Migration infrastructure:

```sql
CREATE TABLE IF NOT EXISTS app_migrations (
  version bigint PRIMARY KEY,
  checksum text NOT NULL,
  applied_at timestamptz NOT NULL DEFAULT now()
);
```

```go
// Use a dedicated *sql.Conn for the entire migration run.
conn, err := db.Conn(migrationCtx)
if err != nil { return err }
defer conn.Close()
if _, err := conn.ExecContext(migrationCtx,
    `SELECT pg_advisory_lock(684329017522)`); err != nil { return err }
defer func() {
    cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    if _, err := conn.ExecContext(cleanup, `SELECT pg_advisory_unlock(684329017522)`); err != nil {
        // Import database/sql/driver: discard, rather than pool, a locked connection.
        _ = conn.Raw(func(any) error { return driver.ErrBadConn })
    }
}()
// For each unapplied version: BeginTx on conn, check ledger/checksum,
// apply its statements, insert ledger row, Commit. Never silently accept a
// changed checksum for an already-applied migration.
```

A pooled connection must not be returned while holding an unreleased session advisory lock; close/discard it on unlock failure. Some DDL needs nontransactional handling; mark such migrations explicitly. Coordinate engine and product schema ownership instead of rewriting engine tables ad hoc on every boot.

**Regression requirements.** Run two startup processes simultaneously, kill one mid-migration, and restart. Compare schema and data with a clean upgrade. Test legacy account-scoped keys, existing recovery/session rows, constraint creation, and a migration taking longer than the connection-ping budget.

<a id="ops-04"></a>

### OPS-04 â€” Shutdown drains HTTP but does not join all application-owned background work

**Severity:** Medium  
**Classification:** Confirmed lifecycle defect  
**Evidence:** [`cmd_serve.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/cmd_serve.go) Â· [`mail.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail.go#L198-L246) Â· [`accounts.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/accounts.go) Â· [`sendqueue.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/sendqueue.go#L405-L449) Â· [`oauth.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/oauth.go#L160-L169)

Scheduler/classification loops and several manual sync, OAuth-sync, push, and send goroutines are launched without a shared completion tracker. Some use `context.Background()`. After HTTP drain, database pools can close while those tasks still execute; timer-based sends can be lost or fail during shutdown. Cancelling a context is not equivalent to waiting for its work to finish.

**Suggested fix.** Own every background task in one lifecycle group, stop admission, cancel work, drain HTTP/streams, join tasks, and only then close pools. Persist accepted sends before relying on shutdown behavior. On drain timeout, close remaining connections and exit explicitly rather than describing the shutdown as graceful.

**Implementation.**

Minimal task group; new calls must go through this object:

```go
// imports context, sync
type taskGroup struct {
    mu sync.Mutex
    closing bool
    ctx context.Context
    cancel context.CancelFunc
    wg sync.WaitGroup
}
func newTaskGroup(parent context.Context) *taskGroup {
    ctx, cancel := context.WithCancel(parent)
    return &taskGroup{ctx:ctx, cancel:cancel}
}
func (g *taskGroup) Go(fn func(context.Context)) bool {
    g.mu.Lock()
    if g.closing { g.mu.Unlock(); return false }
    g.wg.Add(1)
    g.mu.Unlock()
    go func() { defer g.wg.Done(); fn(g.ctx) }()
    return true
}
func (g *taskGroup) Stop(ctx context.Context) error {
    g.mu.Lock(); g.closing=true; g.cancel(); g.mu.Unlock()
    done := make(chan struct{})
    go func() { g.wg.Wait(); close(done) }()
    select { case <-done: return nil; case <-ctx.Done(): return ctx.Err() }
}
```

Replace fire-and-forget `go` calls with `tasks.Go`, and derive per-operation timeouts from its context. Ensure all mutex/network waits also observe cancellation. In main, handle `srv.Shutdown` failure with `srv.Close`; do not close database pools while workers are still legitimately using them.

**Regression requirements.** Send SIGTERM during a queued send, token refresh, sync, body fetch, export, and classification. Accepted durable jobs must be recoverable; no new tasks may be admitted after stop. Assert tasks finish before pool closure and the forced-exit path is bounded.

<a id="ops-05"></a>

### OPS-05 â€” Deleting one account can wait behind unrelated account work and uncancellable locks

**Severity:** Medium  
**Classification:** Confirmed availability/design limitation  
**Evidence:** [`app.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/app.go) Â· [`accounts.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/accounts.go) Â· [`mail-engine/sync.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail-engine/sync.go#L39-L47) Â· [`oauth.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/oauth.go#L19-L25)

The account lifecycle uses a global owner RWMutex for ordinary account work and takes its write lock for deleting one account. A long export or network operation for an unrelated mailbox can therefore delay that deletion and new readers behind it. Engine sync and OAuth refresh locks also wait with plain mutexes, which do not observe request cancellation. This is not a claim that every nested lock currently deadlocks; it is a cross-account blocking and cancellation problem.

**Suggested fix.** Use per-account cancellable admission/maintenance gates, with the global owner lock held only for short registry transitions or full-owner deletion admission. Coalesce duplicate manual sync requests instead of queuing unlimited waiters. Extend the design to database-backed generations before supporting multiple replicas.

**Implementation.**

A context-aware exclusive limiter for sync/refresh work:

```go
// imports context
type contextLock struct { token chan struct{} }
func newContextLock() *contextLock {
    return &contextLock{token: make(chan struct{}, 1)}
}
func (m *contextLock) Lock(ctx context.Context) (func(), error) {
    select {
    case <-ctx.Done(): return nil, ctx.Err()
    case m.token <- struct{}{}:
        if err := ctx.Err(); err != nil { <-m.token; return nil, err }
        return func() { <-m.token }, nil
    }
}
```

For account reads, use an active-operation counter plus `deleting` state and a completion channel: admission checks the flag under a short mutex, increments active count, and releases the mutex; deletion seals admission and waits on the zero-active channel with `select` on context. Full-owner deletion seals the registry before waiting on all gates. Reclaim idle per-account lock entries safely; never delete a lock entry while a waiter can still reference it.

**Regression requirements.** Hold a long read/export on account A and delete B; B must not wait for A. Cancel a queued sync/refresh and verify immediate return without another provider connection. Full-owner deletion must still prevent new work and wait for all admitted operations.

<a id="ops-06"></a>

### OPS-06 â€” Default network binding and permissive origin detection can expose setup or credentials over HTTP

**Severity:** Medium  
**Classification:** Deployment-dependent security/hardening gap  
**Evidence:** [`compose.yaml`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/compose.yaml#L1-L20) Â· [`config.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/config.go#L30-L64) Â· [`setup.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/setup.go#L212-L260) Â· [`auth.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/auth.go#L305-L325)

Compose publishes `8080:8080` on all host interfaces while its instructions tell the operator to open localhost over HTTP. Public exposure is only a warning. Origin detection trusts forwarded host/proto without checking the proxy peer, and origin parsing does not restrict the input to a clean http(s) origin before changing the live configuration. This is not an unauthenticated setup takeover by itselfâ€”the setup token is still requiredâ€”but a remote/plain-HTTP deployment can expose that token and later credentials to the network.

**Suggested fix.** Bind the zero-config example to loopback. Require a validated HTTPS origin for non-loopback deployments unless an explicit dangerous override is selected. Accept forwarded headers only from configured trusted proxies; pin the validated origin transactionally at setup completion.

**Implementation.**

Safer Compose default:

```yaml
services:
  app:
    ports:
      - "127.0.0.1:8080:8080"
```

```go
// imports errors, net/url, strings
func validatedOrigin(raw string, allowInsecure bool) (string, error) {
    u, err := url.Parse(strings.TrimSpace(raw))
    if err != nil || u.Hostname()=="" || u.User!=nil ||
       (u.Scheme!="https" && u.Scheme!="http") ||
       (u.Path!="" && u.Path!="/") || u.RawQuery!="" || u.Fragment!="" || u.Opaque!="" {
        return "", errors.New("expected a clean http(s) origin")
    }
    if u.Scheme=="http" && !isLoopbackHost(u.Hostname()) && !allowInsecure {
        return "", errors.New("HTTPS is required outside loopback")
    }
    return u.Scheme+"://"+u.Host, nil
}
```

Use the socket's host/proto unless RemoteAddr belongs to an explicitly configured proxy CIDR, then parse a single sanitized forwarded origin. Do not blindly reject private IMAP/JMAP servers: application browser-origin policy and mail-server egress policy are different concerns.

**Regression requirements.** Verify the default container is not reachable through a non-loopback host address. Test reverse proxies, forged forwarded headers from an untrusted peer, invalid schemes/userinfo/path/query, IPv6 loopback, and explicit HTTPS configuration.

<a id="ops-07"></a>

### OPS-07 â€” CI can pass while real database integration tests are skipped

**Severity:** Medium  
**Classification:** Confirmed test-coverage gap  
**Evidence:** [`.github/workflows/ci.yml`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/.github/workflows/ci.yml) Â· [`mail-engine/store_integration_test.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail-engine/store_integration_test.go#L13-L43)

The current workflow now runs on pull requests and covers product, mail-engine, MCP, dashboard build/typecheck, and container build. Those are improvements, not missing checks. However, the store integration helper calls `t.Skip` unless `NEUTRON_MAIL_TEST_DATABASE_URL` is supplied, and the workflow supplies neither that variable nor a database service. The normal green build therefore does not exercise that store against PostgreSQL. Race tests and provider contract/integration checks are also not explicitly present in this workflow.

**Suggested fix.** Add a disposable PostgreSQL integration job and explicit race runs. Separate databases/schemas for destructive suites, validate upgrade migrations, and test provider request contracts with fixtures plus opt-in real-provider smoke tests. Do not run destructive testStore against production.

**Implementation.**

Add a dedicated workflow job; merge with the existing workflow rather than replacing its publish logic:

```yaml
  postgres-integration:
    runs-on: ubuntu-latest
    services:
      postgres:
        image: postgres:17
        env:
          POSTGRES_USER: test
          POSTGRES_PASSWORD: test
          POSTGRES_DB: mailtest
        ports: ["5432:5432"]
        options: >-
          --health-cmd "pg_isready -U test -d mailtest"
          --health-interval 2s --health-timeout 5s --health-retries 20
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.26.x"
      - name: Engine integration and race checks
        working-directory: mail-engine
        env:
          NEUTRON_MAIL_TEST_DATABASE_URL: postgres://test:test@localhost:5432/mailtest?sslmode=disable
        run: go test -race -count=1 ./...
      - name: Product race checks
        run: go test -race -count=1 ./...
      - name: MCP race checks
        working-directory: mcp
        run: go test -race -count=1 ./...
```

Add the product's database fixtures using their own verified environment contract; do not assume setting the engine variable enables every product test. Pin workflow actions/base images by reviewed immutable revisions as a separate supply-chain hardening step, with an update process rather than invented digests.

**Regression requirements.** The CI log must show actual database tests, not skips. Deliberately break a store query or migration and confirm the job fails. Add barrier-based regressions for every concurrency finding, and keep destructive suites isolated.

<a id="ops-08"></a>

### OPS-08 â€” Sensitive API responses lack a consistent no-store and safe diagnostic policy

**Severity:** Low  
**Classification:** Confirmed defense-in-depth gap  
**Evidence:** [`mail.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail.go) Â· [`auth.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/auth.go) Â· [`classify.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/classify.go) Â· [`accounts.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/accounts.go)

The shared JSON/problem writers do not set a no-store policy, although individual download handlers do. Many protected errors also send raw database/provider error strings to clients. Authentication reduces exposure, but broadly authorized agents or browser caches need not receive internal query/endpoint details. This finding does not assert a demonstrated cache leak or a credential in every error message.

**Suggested fix.** Apply a no-store policy across auth and product API responses, and separate safe error codes/details from internal diagnostic logs. Use a request ID for correlation; redact secrets and sensitive provider payloads in logs as well.

**Implementation.**

API wrapper and safe error helper:

```go
func privateAPI(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Cache-Control", "no-store")
        w.Header().Set("X-Content-Type-Options", "nosniff")
        next.ServeHTTP(w, r)
    })
}
func (a *App) internalProblem(w http.ResponseWriter, r *http.Request, err error) {
    // requestID is generated by trusted middleware, not accepted verbatim
    // from an arbitrary client header.
    a.log.Error("request failed", "path", r.URL.Path, "err", err)
    writeProblem(w, 500, "Request Failed", "the request could not be completed")
}
```

Wrap the `/api/` dispatch, including public auth responses, without adding arbitrary cache-busting query secrets. Keep actionable validation errors specific; only unexpected/internal details should be generalized.

**Regression requirements.** Check headers on successful and failed auth/product responses and downloads. Inject SQL/provider failures and verify public responses contain no query text, connection strings, token material, or credential-bearing URLs.

<a id="ops-09"></a>

### OPS-09 â€” Public asset requests repeat deterministic hashing work

**Severity:** Low  
**Classification:** Confirmed avoidable request cost  
**Evidence:** [`cmd_serve.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/cmd_serve.go)

Service-worker generation walks and hashes embedded assets for requests even though embedded content does not change during the process lifetime. HTML handling also repeats stylesheet hashing. These public paths needlessly repeat deterministic work and increase unauthenticated request cost. The practical impact depends on asset size and request volume.

**Suggested fix.** Compute asset digests and generated worker/HTML content once at startup, then serve immutable bytes with appropriate cache/version headers. A hash used for correctness should remain content-derived, not become a random per-process cache-buster.

**Implementation.**

Simple lazy-once pattern for the existing builder:

```go
// imports sync
var workerOnce sync.Once
var workerBytes []byte
var workerBuildErr error

func cachedWorker() ([]byte, error) {
    workerOnce.Do(func() {
        workerBytes, workerBuildErr = buildServiceWorkerBytes() // extract existing hash/build logic
    })
    return workerBytes, workerBuildErr
}
```

Prefer eager startup construction so a build failure is detected before serving traffic. Keep the cached byte slice immutable after publication. Use the same precomputed stylesheet digest when constructing HTML.

**Regression requirements.** Issue repeated and concurrent worker/HTML requests and assert the asset walker/hash builder executes once. Changing an embedded asset in a new build must change the derived version; repeated requests within one build must not.

<a id="provider-01"></a>

### PROVIDER-01 â€” Graph message identity is move-sensitive because immutable IDs are not requested

**Severity:** Medium  
**Classification:** Confirmed provider-contract gap  
**Evidence:** [`mail-engine/graph/adapter.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail-engine/graph/adapter.go#L45-L75) Â· [`mail-engine/graph/adapter.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail-engine/graph/adapter.go#L202-L245) Â· [`mail-engine/dialer/dialer.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail-engine/dialer/dialer.go#L103-L114) Â· [`oauth.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/oauth.go#L322-L414)

Graph messages are keyed by returned native IDs, and the reviewed request builders/transport do not set the immutable-ID preference. Microsoft's documented default IDs can change when a message moves folders, whereas the immutable-ID mode is requested per API call. Local filing, bodies, links, and reply-parent references can consequently lose continuity when a message receives a different provider ID. Merely adding a header after existing data has accumulated is not a complete migration.

**Suggested fix.** Adopt immutable IDs consistently across delta, detail, raw/attachment, draft, reply, and send requests. Translate existing IDs in controlled batches and migrate all local references atomically. Preserve alias mappings during rollout; do not mix old and new IDs without a migration plan.

**Implementation.**

Request change, applied to every relevant Graph builder:

```go
req.Header.Set("Prefer", `IdType="ImmutableId"`)
```

Migration request shape (send through the owner-scoped Graph client):

```json
{
  "inputIds": ["existing-provider-id"],
  "sourceIdType": "restId",
  "targetIdType": "restImmutableEntryId"
}
```

```sql
CREATE TABLE provider_message_aliases (
  account_id text NOT NULL,
  provider text NOT NULL,
  old_native_id text NOT NULL,
  new_native_id text NOT NULL,
  PRIMARY KEY(account_id,provider,old_native_id)
);
```

Call Graph's `/me/translateExchangeIds` with documented batch limits. Persist a checkpoint and use the transactional identity migration in SYNC-03 for envelopes, bodies, memberships, filing, push receipts, reply references, and queued jobs. Microsoft's documentation says delta links support both formats; that does not migrate this application's existing keys by itself.

Primary contract: [Microsoft â€” immutable identifiers and migration](https://learn.microsoft.com/en-us/graph/outlook-immutable-id), accessed September 17, 2026. Immutable IDs remain stable within the same mailbox; moving to an archive mailbox or export/reimport can still change identity.

**Regression requirements.** Move a message across folders and verify stable local filing/body/reply references. Test migration restart, existing canonical-ID collisions, queued replies referencing old IDs, and all Graph request paths carrying the preference.

**Related work:** Earlier GRAPH-02 remains open. Header-only adoption is unsafe for an already-populated mirror.

<a id="provider-02"></a>

### PROVIDER-02 â€” Graph continuation URLs are not restricted to the configured provider origin

**Severity:** Low  
**Classification:** Confirmed egress hardening gap; poisoned-response/cursor prerequisite  
**Evidence:** [`mail-engine/graph/adapter.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail-engine/graph/adapter.go#L45-L75) Â· [`mail-engine/graph/adapter.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail-engine/graph/adapter.go#L202-L235) Â· [`mail-engine/dialer/dialer.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/mail-engine/dialer/dialer.go#L115-L162)

The Graph adapter accepts absolute endpoints beginning with â€œhttpâ€ and follows provider continuation URLs/stored cursors without an origin check. This can direct server-side GETs outside the provider if a response/cursor is malicious or corrupted. The bearer transport already strips Authorization outside approved HTTPS hosts; that earlier credential-leak repair is present and must be credited. The remaining issue is unwanted network access, not demonstrated cross-origin bearer leakage.

**Suggested fix.** Validate continuation URLs against the exact configured provider origin and allowed API path before making the request, and restrict redirects too. Apply comparable explicit policies to any other metadata-discovered endpoint, while preserving intentionally configured private mail servers rather than banning all private addresses indiscriminately.

**Implementation.**

Graph-only endpoint validator; imports errors, net/url, strings:

```go
func graphEndpoint(base, endpoint string) (string, error) {
    b, err := url.Parse(base)
    if err != nil || b.Scheme!="https" || b.Hostname()=="" || b.User!=nil {
        return "", errors.New("invalid Graph base origin")
    }
    raw := endpoint
    if strings.HasPrefix(endpoint, "/") { raw = strings.TrimRight(base, "/") + endpoint }
    u, err := url.Parse(raw)
    if err != nil || u.Scheme!="https" || !strings.EqualFold(u.Host,b.Host) ||
       u.User!=nil || u.Fragment!="" ||
       !strings.HasPrefix(u.Path, strings.TrimRight(b.Path,"/")+"/") {
        return "", errors.New("Graph continuation left the approved origin/path")
    }
    return u.String(), nil
}
```

Use this in `get` before `http.NewRequestWithContext`. Configure `CheckRedirect` to reject off-origin/downgrade hops and retain a redirect count limit. Keep the existing bearer host guard as defense in depth. Unit tests can use an HTTPS test server with an explicit test base/client.

**Regression requirements.** Supply a nextLink/deltaLink using HTTP, a different host/port, userinfo, a private host, or an unexpected API path; no outbound request should occur. Verify legitimate paginated/delta URLs and same-origin redirects still work without weakening the bearer guard.

<a id="provider-03"></a>

### PROVIDER-03 â€” Connected-account creation spans separate commits, and OAuth creation bypasses the owner lifecycle gate

**Severity:** Medium  
**Classification:** Confirmed transactional/concurrency gap  
**Evidence:** [`accounts.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/accounts.go) Â· [`oauth.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/oauth.go#L147-L169) Â· [`auth.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/auth.go#L1810-L1886) Â· [`app.go`](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/app.go)

Both connection flows create the engine account separately from the product account record, relying on compensating deletion if a later write fails. A crash or failed cleanup can leave an orphan. The OAuth callback additionally does not participate in the same owner lifecycle gate as regular creation. During full deletion it can insert a mirror account after the deletion transaction enumerated its mirrors; a product row may then be cascaded away while the newly created mirror row was never included in the deletion set.

**Suggested fix.** Use one transaction for engine and product account insertion, with one owner lifecycle/epoch validation protocol shared by OAuth and non-OAuth creation. Verify external credentials before the transaction, then recheck that the same owner/installation generation still exists before publishing the account. Start sync only after a successful commit.

**Implementation.**

Transaction-aware insertion helper; adapt it into the engine/store boundary instead of keeping two independently committed calls:

```go
// Add a shared connectedAccount record containing all validated request fields.
func (a *App) persistConnectedAccount(ctx context.Context, uid string, c connectedAccount) error {
    a.accountOwnerMu.RLock() // existing lock order: owner lifecycle before user row
    defer a.accountOwnerMu.RUnlock()
    tx, err := a.db.BeginTx(ctx, nil)
    if err != nil { return err }
    defer tx.Rollback()
    if err := lockAuthUser(ctx, tx, uid); err != nil { return err }
    if _, err := tx.ExecContext(ctx,
        `INSERT INTO mail_accounts(id,provider,email,name) VALUES($1,$2,$3,$4)`,
        c.MirrorID,c.Provider,c.Address,c.Label); err != nil { return err }
    if _, err := tx.ExecContext(ctx, `INSERT INTO email_accounts
        (user_id,mirror_account_id,provider,address,label,username,host,port,
         smtp_host,smtp_port,cred_ciphertext,backfill_days)
        VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
        uid,c.MirrorID,c.Provider,c.Address,c.Label,c.Username,c.Host,c.Port,
        c.SMTPHost,c.SMTPPort,c.SealedCredential,c.BackfillDays); err != nil { return err }
    return tx.Commit()
}
```

`connectedAccount` is a new validated DTO; define its fields with the corresponding current string/int types. Have both handlers use this helper and map duplicate-address conflicts to an explicit reconnect/409 flow. For a multi-process deployment, replace the in-memory gate with the same database-backed generation/lock protocol used for deletion. The network verification phase must not hold a long database transaction.

**Regression requirements.** Crash/fail after each account insertion and verify both rows commit or neither does. Pause OAuth after consuming state, begin full deletion, then resume: no new mirror account may survive the deleted owner. Test duplicate connections, rollback, and post-commit sync admission.


## Remediation sequence and release gates

### 1. Protect acceptance and authentication invariants first

Implement AUTH-01 and AUTH-03 together with AUTH-02/AUTH-04 so a credential change, first-run completion, or owner transition has one authoritative boundary. Implement SEND-01 through SEND-04 together: a durable queue without a deletion lease can still send after disconnection, and a lease without durability still loses work at restart. Preserve an explicit ambiguous-delivery state; do not advertise exactly-once SMTP delivery.

Release gate: barrier-based tests demonstrate that a retired credential cannot create a session; concurrent setup cannot create multiple owners; accepted sends survive restart; and deletion prevents new send admission. These are proposed gates, not claims that those application tests passed during this review.

### 2. Preserve user-authored state during reconciliation

Implement staged scans, transactional identity promotion, and account maintenance coordination before adding destructive foreign-key cascades or â€œreset and resyncâ€ recovery. Then make retention/backfill transitions durable and implement Graph ID migration. A cached envelope can be rebuilt, but a user's filing, read history, snooze deadline, and accepted draft are not automatically recoverable from the provider.

Release gate: repeated injected failures during reset, identity migration, retention, and account deletion preserve the prior usable state or a complete committed replacement. Restarting reconciliation must not silently erase local intent.

### 3. Make browser state explicit and recoverable

Deploy immutable owner/generation scope, atomic reset, cross-tab replay serialization, and server idempotency as a coordinated protocol. Then add typed queue outcomes, visible failures, timed retries, and stale-but-readable offline snapshots. Exact undo and absolute snooze deadlines should reuse the same operation IDs and row versions.

Release gate: a delayed response or replay cannot cross an owner-generation boundary; two tabs cannot apply the same logical operation twice; and the user can inspect every rejected or ambiguous action.

### 4. Bound and observe the running system

Add endpoint/worker/export admission, database budgets, cancellable waits, a joined task lifecycle, versioned migrations, safe network defaults, and real database/race CI. Expose queue depth/bytes, retry counts, ambiguous sends, reconciliation lag, database wait times, active exports, and cancellation/drain failures. Do not include message bodies, credentials, recovery codes, or raw provider tokens in telemetry.

The dependency order matters. For example, setting a low database pool cap without fixing nested query acquisition can create a new deadlock; adding cascade deletion from ephemeral cache rows to filing state can worsen reset-related loss; and replacing all queue retries with automatic retransmission can duplicate email.

## Implementation integration notes

**Status of code.** The code blocks are remediation implementations and integration patterns, not an applied, repository-wide compiling patch. Blocks labeled â€œintegrationâ€ introduce schema, DTOs, interfaces, response types, or repository methods. Small target replacements still require the stated imports and the surrounding handler's error responses. Do not paste a fragment with `return err` into a void HTTP handler; extract the operation into a helper returning an error and map it at the handler boundary.

**Shared new interfaces and schema.** Implement `connectedAccount`, staged `ScanStore` methods, `putScopedCache`, `mutateWithOfflineQueue`, `replayMutationsWithIdempotency`, durable Sent-copy repository methods, and undo receipt storage before wiring callers. These names identify new responsibilities, not existing repository functions. The `replacement`/`integration` labels distinguish them from existing helpers such as `lockAuthUser`, `persistSession`, and `writeProblem`.

**Version and migration coordination.** Consolidate the SQL examples into a single ordered migration plan. They are not an idempotent script to run as concatenated blocks. Inspect preexisting duplicate owners/orphans/canonical identities before introducing constraints. Abort and require reconciliation rather than silently deleting records to make a constraint pass. Back up both PostgreSQL data and the existing sealing key before a schema/key-related change; a database-only backup is insufficient to restore encrypted credentials.

**Single-process versus multiple replicas.** In-memory lifecycle locks are useful for the current process, but do not establish cross-process guarantees. Supporting several replicas requires database-coordinated owner/account epochs, admissions and outbox claims, shared migration coordination, and distributed-safe token refresh. State this deployment contract explicitly rather than assuming a local mutex protects another process.

**Idempotency and uncertainty.** Store the exact operation identity before its first attempt. Repeated unchanged intent keeps its key; edited intent gets a new key. Persist receipts atomically with state changes. Provider submission requires its own reconciliation policy: successful transport, saved Sent copy, and database acknowledgment are separate facts.

## Validation performed during this review

### Repository/source validation

Actual GitHub content was read at the pinned revision through the connected GitHub API. The branch/revision was checked rather than assuming a historical audit matched current code. Source links in every finding pin that revision so later edits to `main` do not change the evidence. Existing audit documents were treated as leads and historical context, not as proof that every older issue remains present.

The Microsoft immutable-ID contract was also checked against the official documentation cited in PROVIDER-01. No live mailbox was connected, no production requests were sent, no outbound message was delivered, and no secret values were read.

### Isolated helper tests that actually ran

Eight proposed standard-library helpers were extracted into a scratch Go module: the strict JSON decoder, send-budget admission, context-aware lock, task group, origin validator, Graph endpoint validator, filename sanitizer, and quota writer. The module used a small loopback-host helper equivalent to the reviewed resolver helper. The following command ran against that scratch module, **not the Lullmail repository**:

```sh
GOTOOLCHAIN=local GOPROXY=off go test -race -count=1 ./...
```

Observed output:

```text
ok   auditfixes   1.010s
```

The tests checked rejection of unknown/trailing/oversized JSON, send capacity and once-only release, cancelled lock acquisition, stopping/admission of tasks, origin rejection, Graph origin/path restrictions, filename normalization, and export-byte quota enforcement. These results establish only those isolated behaviors. They do not establish that the complete application changes compile together or that any repository/database/browser/provider regression passes.

The exact scratch tests are included below so the tested coverage is inspectable. Place them beside the selected helper definitions in a standalone `auditfixes` package; they are not directly runnable inside `package main` without adaptation.

```go
package auditfixes

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDecode(t *testing.T) {
	for _, tc := range []struct {
		s     string
		limit int64
		good  bool
	}{
		{`{"x":1}`, 100, true}, {`{"x":1} {"x":2}`, 100, false},
		{`{"unknown":1}`, 100, false}, {`{"x":1}`, 3, false},
	} {
		var dst struct {
			X int `json:"x"`
		}
		err := decodeOneJSON(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.s)), tc.limit, &dst)
		if (err == nil) != tc.good {
			t.Fatalf("%s: %v", tc.s, err)
		}
	}
}
func TestBudget(t *testing.T) {
	b := new(sendBudget)
	var releases []func()
	for i := 0; i < 8; i++ {
		rel, err := b.acquire(1)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, rel)
	}
	if _, err := b.acquire(1); !errors.Is(err, errSendCapacity) {
		t.Fatalf("want capacity error: %v", err)
	}
	for _, rel := range releases {
		rel()
		rel()
	}
	if b.jobs != 0 || b.bytes != 0 {
		t.Fatal("budget leak")
	}
}
func TestContextLock(t *testing.T) {
	m := newContextLock()
	release, err := m.Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Lock(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}
func TestTasks(t *testing.T) {
	g := newTaskGroup(context.Background())
	if !g.Go(func(ctx context.Context) { <-ctx.Done() }) {
		t.Fatal("admission")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := g.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if g.Go(func(context.Context) {}) {
		t.Fatal("accepted after shutdown")
	}
}
func TestOrigin(t *testing.T) {
	for _, tc := range []struct {
		s    string
		good bool
	}{
		{"https://mail.example", true}, {"http://localhost:8080", true},
		{"http://mail.example", false}, {"https://u:p@mail.example", false},
		{"https://mail.example/path", false}, {"ftp://mail.example", false},
	} {
		_, err := validatedOrigin(tc.s, false)
		if (err == nil) != tc.good {
			t.Fatalf("%s: %v", tc.s, err)
		}
	}
}
func TestGraphEndpoint(t *testing.T) {
	base := "https://graph.microsoft.com/v1.0"
	for _, tc := range []struct {
		s    string
		good bool
	}{
		{"/me/messages", true}, {base + "/me/messages?$skiptoken=opaque", true},
		{"https://evil.example/v1.0/me", false}, {"http://graph.microsoft.com/v1.0/me", false},
		{"https://graph.microsoft.com:8443/v1.0/me", false},
		{"https://graph.microsoft.com/beta/me", false},
	} {
		_, err := graphEndpoint(base, tc.s)
		if (err == nil) != tc.good {
			t.Fatalf("%s: %v", tc.s, err)
		}
	}
}
func TestFilenameAndQuota(t *testing.T) {
	if got := attachmentFilename("../folder\\rÃ©sumÃ©\n.txt"); got != "rÃ©sumÃ©.txt" {
		t.Fatal(got)
	}
	if got := attachmentFilename(".."); got != "attachment" {
		t.Fatal(got)
	}
	var b bytes.Buffer
	q := &quotaWriter{w: &b, remaining: 3}
	if _, err := q.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Write([]byte("d")); err == nil {
		t.Fatal("quota not enforced")
	}
}
```

### Tests not executed

No full repository `go test`, full repository race run, `go vet`, PostgreSQL migration/integration run, dashboard npm test/typecheck/build, Playwright/browser test, provider smoke test, dependency vulnerability scan, Docker build, or production load test was executed in this review. A source-level interleaving is not a reproduced runtime exploit. PostgreSQL-specific SQL, new schema relationships, and whole-application frontend interfaces require the integration tests described in each finding before deployment.

## Coverage and limitations

### Source examined

The reviewed source included the main authentication/setup/configuration code; account creation, policy changes, deletion and synchronization orchestration; product API routing and classification/actions; send queue and OAuth submission; export construction; the engine's synchronization, relevant store operations and schema; Graph listing/synchronization and the credential-bearing dialer; browser API/offline/action modules; and deployment/CI configuration.

| Area | Reviewed source | Depth/limitations |
|---|---|---|
| Authentication and setup | `auth.go`, `setup.go`, `config.go`, `schema.sql` | Broad static review of auth/session/factor/setup/deletion flows. No real WebAuthn authenticator or password-provider runtime tests. |
| App lifecycle and accounts | `app.go`, `accounts.go`, `mail.go`, `cmd_serve.go`, `resolver.go` | Broad static review of ownership gates, pools, routing, startup, workers, policies and deletion. No live concurrency/load reproduction. |
| Submission and export | `sendqueue.go`, `oauth.go`, initial/account-export and EML paths of `export.go` | Submission/undo/finalization and archive error-path review. The full MIME/mbox implementation was not independently exhaustively tested. |
| Classification and UI actions | Core of `classify.go`; initial/bulk/undo/snooze portion of `dashboard/src/app/lib/actions.ts` | Exact findings concern reviewed paths, not every board/notes/search/view implementation. |
| Engine | Core `mail-engine/sync.go`; relevant `store.go` operations; `schema.go`; `dialer/dialer.go`; initial Graph adapter paths | No claim that every IMAP, Gmail, JMAP, MIME, scheduler, token-source or engine test file was exhaustively re-audited in this pass. |
| Browser persistence/API | `dashboard/src/app/lib/api.ts`, `offline.ts`; relevant `actions.ts` | Static contract/concurrency review. No multi-browser/multi-tab runtime test. Remaining reader, composer, settings, service worker, accessibility, and complete UI behavior are not comprehensively verified. |
| Deployment and tests | `compose.yaml`, `.github/workflows/ci.yml`, beginning of engine store integration tests, repository README/agent guidance and audit register | Workflow/configuration review, not CI execution or a supply-chain inventory. |
| Other repository surfaces | Tree/metadata inspection and historical audit context | MCP runtime, marketing site, push delivery internals, board/notes implementation, remaining protocol adapters, all dependencies, and complete test corpus were not exhaustively reviewed. |

This limitation table is deliberate: reading a repository tree, a README, or an old audit does not equal reviewing every source line. The 50-item register is the complete set of findings established and documented here, not a certification that unlisted files contain no issues.

### Earlier issues not indiscriminately reopened

The current code already contains meaningful repairs. Examples visible in the reviewed source include the read-only raw-engine product mount and ownership fence, strict distinction between database failure and absent authentication in `requireAuth`, TOTP step consumption, the delivery-time credential refresh improvement, bounded thread eager fetching, provider-independent export fallback, IndexedDB acknowledgment after transaction commit, retention of rejected replay records, preserving offline snapshots when replay commits nothing, Graph nested-folder traversal/well-known role resolution, cross-origin bearer-header stripping, pull-request/MCP CI coverage, and publishing logic that does not move `latest` backward for historical version tags.

Those existing protections are not all complete end-to-end solutions: the relevant residuals have their own specific findings above. In particular, AUTH-05 concerns auth-status/security reporting rather than claiming `requireAuth` still returns 401 on every database failure; SEND-02 concerns the remaining lookup-to-submission window; DATA-01 identifies the particular unguarded thread query; and WEB-06 concerns mutation-triggered cache deletion, not the repaired zero-commit startup path.

Historical context: [current audit register](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/AUDIT_OPEN.md) and [earlier, different-revision audit](https://github.com/lullmail/lullmail/blob/f172169b75d673cbd44ae117d530ae5f8c6fbc63/AUDIT-CHATGPT-2.md). Their finding totals should not be added to this report's total: many overlap or describe code already changed.

## Acceptance checklist for the implementing team

Before merging, compile and format all three Go modules and the dashboard; run the database and browser suites in isolated environments; review migration rollback/forward recovery; and implement the concurrency barriers described above. Ensure each new persistent state has an owner/account scope, an explicit retention policy, observable failure states, and a restart story. Review every externally visible acknowledgment to identify exactly what it guarantees: received, persisted, submitted, delivered to a provider, or fully reconciled.

Before deployment, restore-test the backup including the original encryption key, rehearse an upgrade on a copy of an existing installation, test SIGTERM during active work, and verify rollback does not make newer IDs/ciphertext/schema unreadable. Observe the rollout with bounded telemetry and keep a recovery path for accepted drafts and user-authored filing state.

---

**End of audit.** No repository changes were made. No finding is presented as a verified production exploitation event. The report's proposed changes must be integrated and validated before deployment.
