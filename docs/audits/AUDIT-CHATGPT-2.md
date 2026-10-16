# Lullmail repository audit

**Repository:** [lullmail/lullmail](https://github.com/lullmail/lullmail)  
**Audited revision:** [`00216d8d7bd0a04dde742deefadabf0540200773`](https://github.com/lullmail/lullmail/commit/00216d8d7bd0a04dde742deefadabf0540200773)  
**Audit date:** September 17, 2026  
**Deliverable:** One Markdown report containing findings, implementation guidance, code, regression requirements and local probe evidence.  
**Changes made to the repository:** None.

## Executive assessment

The inspected code contains consequential problems in authentication throttling, SMTP transport policy, send/draft durability, browser offline storage, provider synchronization and local data reconciliation. Several problems combine: spoofable rate-limit identities weaken standalone TOTP login; a queue acknowledgment retires the browser draft even though delivery can fail later; and destructive sync resets can be mistaken for authoritative deletions of user filing state.

This report records **63 grouped findings**: **12 High, 48 Medium and 3 Low** priority. These numbers include explicitly labeled hardening and conditional deployment risks, not 63 independently exploited vulnerabilities. Related sub-defects are grouped where they share a repair. The repository's previous audit notes were considered, but were not treated as evidence that a current implementation is correct.

The fastest high-confidence corrections are the push SQL column, fail-closed SMTP policy, offline-startup invalidation, complete draft restoration, provider result validation and error propagation. The highest-value structural repairs are a durable, observable outbox; transactionally consistent authentication changes; and non-destructive staged mirror reconciliation.

## What was and was not verified

Actual files were fetched from GitHub at the pinned revision, not inferred from the README. Provider-contract questions were checked against primary Microsoft, Google and IETF documentation. Eight isolated local probes reproduced selected parsing/control-flow/transport behaviors; their complete source and output are included in Appendix A. They do not substitute for executing the application.

**The repository's Go, npm, browser, race and PostgreSQL test suites were not run.** The container could not obtain a checkout/dependencies through its network path; source access was through the GitHub connector. The local Go toolchain was 1.23.2 and Node was 22.16.0, while the workflow targets Go 1.26.x. No PostgreSQL server or real Gmail/Graph/JMAP mailbox was used. No production instance was attacked, no external mail was sent, and no secrets were requested or extracted.

The code below consists of three kinds of repair: small targeted edits, complete standalone helpers, and explicitly marked integration/reference designs. **It is not a compiled, application-wide patch set.** Architectural changes require the described schema, handler, worker and UI wiring together. New identifiers/types are introduced deliberately; snippets marked sketches are not claimed to paste into an existing function unchanged. Add the listed imports and update call sites. Apply migrations through the versioned migration mechanism proposed in OPS-02, not blindly by executing every SQL fence in this report.

A source audit cannot establish that every possible problem has been found. Coverage is broad but not every repository file or line was reviewed. The scope below and per-finding confidence labels delimit the conclusions.

### Coverage

| Area | Actual review coverage | Remaining limits |
|---|---|---|
| Product backend | Authentication, passwords, setup, secrets/configuration, accounts, OAuth, send queue, core app/serve/mail setup, classifier/action paths, push, exports, schema and resolver | Some board code was sampled; briefing, events, notes and personal-export internals were not comprehensively reviewed |
| Mail engine | Service/credential handling, send/sync, schema, substantial store paths, IMAP adapter/connection, Gmail, Graph and JMAP adapters | Store tail, low-level IMAP parser, scheduler and other library utilities were not comprehensively reviewed |
| Dashboard | API/offline modules, action/send flow, substantial app/store/composer/HTML-reader paths | Some files were reviewed in ranges; other views, routing, keyboard, service-worker and styling code were not comprehensively reviewed |
| MCP | HTTP client and initial tool definitions, alongside server-side agent authorization | Remaining tool definitions and SDK internals were not comprehensively reviewed |
| Tests and deployment | Workflow, Dockerfile, Compose, entrypoint, push mocks and integration-test setup | No full test-suite execution, release artifact reproduction, dependency CVE scan, organizational permission or ruleset audit |
| Marketing site | Repository tree awareness only | `site/` was not audited |

### Severity and evidence

**High** means substantial account-security exposure, potential irreversible local work loss, plaintext transport exposure or process/lifecycle impact under the stated conditions. **Medium** means significant correctness, consistency, availability or conditional security risk. **Low** means a limited fidelity issue or defense-in-depth improvement. These are triage judgments, not CVSS scores or issued CVEs. â€œConfirmed staticâ€ means a code/schema/contract discrepancy is visible in the inspected source; it does not mean a production exploit was executed. Concurrency findings describe a reachable interleaving that still needs a controlled regression test.

## Finding register

| ID | Priority | Finding | Evidence category |
|---|---|---|---|
| [AUTH-01](#auth-01) | High | Authentication throttling trusts client-supplied forwarding headers | Confirmed static defect |
| [AUTH-02](#auth-02) | High | Password verification has unbounded expensive concurrency and attacker-keyed state | Confirmed static defect |
| [AUTH-03](#auth-03) | High | Standalone TOTP login permits reuse of an already accepted code | Confirmed static defect; standalone login is an explicit design choice |
| [AUTH-04](#auth-04) | Medium | Concurrent TOTP enrollment can overwrite or disable a newly enabled factor | Confirmed concurrency defect |
| [AUTH-05](#auth-05) | High | Credential changes do not guarantee revocation of old sessions | Confirmed static defect and concurrency gap |
| [AUTH-06](#auth-06) | Medium | Long-lived sessions can enroll durable replacement credentials without fresh proof | Hardening; requires an already compromised authenticated session |
| [AUTH-07](#auth-07) | Medium | First-run completion is not a single atomic transition | Confirmed check/commit concurrency gap |
| [AUTH-08](#auth-08) | High | Generated secret/config files can be overwritten or truncated during concurrent startup | Confirmed file-persistence defect under concurrent startup or crash |
| [SEND-01](#send-01) | High | SMTP can downgrade to plaintext, cannot use implicit TLS on port 465, and misreports QUIT failures | Confirmed static defect; plaintext branch reproduced locally |
| [SEND-02](#send-02) | Medium | Outgoing UTF-8 body parts omit transfer encoding and can exceed transport line limits | Confirmed MIME construction defect |
| [SEND-03](#send-03) | High | Accepted sends are volatile and later failures are invisible to the user | Confirmed reliability defect; durable outbox was already deferred |
| [SEND-04](#send-04) | High | A queued SMTP message can be sent after its account has been deleted | Confirmed lifecycle race |
| [SEND-05](#send-05) | Medium | Undo-send reconstructs an incomplete draft | Confirmed user-data loss defect |
| [SEND-06](#send-06) | Medium | Graph attachment limits are enforced after queue acceptance rather than before it | Confirmed validation-order defect |
| [SEND-07](#send-07) | Medium | The exposed raw mail-engine API bypasses product send and sync semantics | Confirmed API-consistency defect; raw routes are owner-only |
| [WEB-01](#web-01) | High | Offline startup deletes the cache it is supposed to read | Confirmed static defect; control flow reproduced locally |
| [WEB-02](#web-02) | Medium | IndexedDB operations report success before the transaction commits | Confirmed static defect; promise timing reproduced locally |
| [WEB-03](#web-03) | Medium | Offline replay discards failed 4xx mutations as though they succeeded | Confirmed retry/data-loss defect |
| [WEB-04](#web-04) | Medium | Offline replay is not coordinated across tabs or made idempotent at the server | Confirmed concurrency/uncertain-retry gap |
| [WEB-05](#web-05) | Medium | Competing draft stores can resurrect stale text during tab changes | Confirmed persistence-order defect |
| [WEB-06](#web-06) | Medium | Attachment restoration and concurrent file additions can lose attachments | Confirmed asynchronous UI race |
| [WEB-07](#web-07) | Medium | Browser caches and drafts need an immutable owner namespace and generation fencing | Confirmed isolation gap; some exposure depends on logout/reset paths |
| [WEB-08](#web-08) | Medium | Generic link unwrapping rewrites legitimate email URLs | Confirmed transformation defect; URL behavior reproduced locally |
| [WEB-09](#web-09) | Low | Discarding the document head loses legitimate email styling | Confirmed rendering limitation |
| [DATA-01](#data-01) | Medium | Push notification selection references a nonexistent SQL column | Confirmed static defect against the actual schema |
| [DATA-02](#data-02) | Medium | Sender decisions and classification can race into hidden Screener mail | Confirmed concurrency defect |
| [DATA-03](#data-03) | Medium | Correspondent trust is inferred from an untrusted From header | Confirmed trust-heuristic weakness, not an authentication bypass |
| [DATA-04](#data-04) | Medium | Undated messages can be silently excluded, including valid space-padded IMAP dates | Confirmed SQL/parser defects; date parsing reproduced locally |
| [SYNC-01](#sync-01) | Medium | Partial mailbox sync failures are returned as overall success | Confirmed error-propagation defect |
| [SYNC-02](#sync-02) | Medium | Body prefetch ignores account-wide authentication and throttling failures | Confirmed retry/backpressure defect |
| [SYNC-03](#sync-03) | High | Destructive cursor reset can erase local filing state before a rebuild succeeds | Confirmed destructive ordering with a reachable failure chain |
| [SYNC-04](#sync-04) | Medium | Retention and mirror writes can race, leaving expired or orphaned data | Confirmed integrity gap under concurrent operations |
| [SYNC-05](#sync-05) | Medium | Increasing retention does not restore older messages removed from the mirror | Confirmed restoration gap for incremental providers |
| [DATA-05](#data-05) | Medium | Thread reads can trigger unbounded provider work | Confirmed resource-boundary omission |
| [DATA-06](#data-06) | Medium | Bucket/search result limits have no continuation contract | Confirmed completeness/usability limitation |
| [DATA-07](#data-07) | Medium | Relative snooze durations make offline replay and undo change the intended date | Confirmed semantic/idempotency defect |
| [EXPORT-01](#export-01) | Medium | Account export aborts on credential failure instead of using its promised mirror fallback | Confirmed fallback-order defect |
| [EXPORT-02](#export-02) | Medium | Export filename disambiguation can create duplicate ZIP entries | Confirmed algorithm defect; reproduced locally |
| [IMAP-01](#imap-01) | Medium | CONDSTORE-only sync never reconciles expunged messages | Confirmed protocol/state defect |
| [IMAP-02](#imap-02) | High | IMAP greeting, cancellation and logout can block past the caller lifetime | Confirmed deadline/cancellation omission |
| [IMAP-03](#imap-03) | Medium | IMAP mutations lack a reliable selected-mailbox contract and capability/input checks | Confirmed library/raw-API correctness gaps |
| [JMAP-01](#jmap-01) | Medium | JMAP Email/set treats per-message failures as successful mutations | Confirmed response-contract defect |
| [JMAP-02](#jmap-02) | Medium | Malformed JMAP responses can panic background sync | Confirmed bounds/validation defect |
| [JMAP-03](#jmap-03) | High | Initial JMAP pagination can advance past changes it never observed | Confirmed cursor-design defect |
| [JMAP-04](#jmap-04) | Medium | JMAP truncated body values are cached as complete content | Confirmed response-field omission; requires a provider returning truncation flags |
| [GMAIL-01](#gmail-01) | Medium | Gmail address and date parsing rejects valid mail headers | Confirmed parsing defects; reproduced locally |
| [GMAIL-02](#gmail-02) | Medium | Gmail body and attachment handling assumes only one legal data representation | Confirmed data-path defect |
| [GMAIL-03](#gmail-03) | Low | Attachment flags are inferred from MIME structure excluded by metadata requests | Confirmed request/response mismatch |
| [GMAIL-04](#gmail-04) | Medium | Initial Gmail listing omits the explicit Spam/Trash inclusion flag | Confirmed query omission; provider integration regression required |
| [GRAPH-01](#graph-01) | Medium | Graph folder discovery misses nested folders and relies on a nonexistent v1.0 role field | Confirmed against code and current official API contract |
| [GRAPH-02](#graph-02) | Medium | Graph message identity changes on folder moves because immutable IDs are not requested | Confirmed identity-contract mismatch |
| [GRAPH-03](#graph-03) | Medium | Graph attachment-list errors and pagination are silently lost in cached bodies | Confirmed incomplete-result handling defect |
| [GRAPH-04](#graph-04) | Medium | Adding or removing one Graph custom keyword overwrites unrelated categories | Confirmed destructive mutation defect |
| [OPS-01](#ops-01) | Medium | Request, provider-response and export resource limits are incomplete | Confirmed boundary omissions; impact depends on reachable input size/concurrency |
| [OPS-02](#ops-02) | Medium | Database connection budgeting and startup migrations are not production-safe by construction | Confirmed operational gaps |
| [OPS-03](#ops-03) | Medium | Creating a connected account spans separate engine/product transactions | Confirmed atomicity gap |
| [OPS-04](#ops-04) | Medium | Shutdown does not join all application-owned background work | Confirmed lifecycle gap |
| [OPS-05](#ops-05) | Medium | Default port publication and URL configuration permit unintended plaintext exposure | Deployment hardening; not an unauthenticated-login finding |
| [OPS-06](#ops-06) | Medium | The container entrypoint ignores the configurable data directory | Confirmed deployment configuration defect |
| [OPS-07](#ops-07) | Medium | CI skips real SQL integration coverage and excludes pull requests and the MCP module | Confirmed test/workflow gaps |
| [OPS-08](#ops-08) | Medium | Any v-prefixed tag can republish an older image as latest | Confirmed release-policy hazard; supply-chain hardening |
| [OPS-09](#ops-09) | Medium | Outbound URL trust boundaries are implicit, including push endpoints and authenticated continuations | Conditional SSRF/credential-forwarding risk; hardening |
| [OPS-10](#ops-10) | Low | Secret-key overrides, local privacy and agent credentials need explicit security policies | Hardening; secure defaults already exist in several paths |

## Detailed findings


<a id="auth-01"></a>

### AUTH-01 â€” Authentication throttling trusts client-supplied forwarding headers

**Priority:** High  
**Evidence:** Confirmed static defect  
**Pinned source:** [`auth.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/auth.go)

**Evidence and impact.** `clientHost` takes the first `X-Forwarded-For` value without establishing that `RemoteAddr` is a trusted reverse proxy. Public login/recovery/TOTP throttles use that result. A directly connected client can choose a new limiter key on every request. The host-map cleanup also removes only old entries, so rotating fresh keys can grow the map beyond the nominal threshold. This is not proof of an account compromise; it removes a principal protection against online guessing, especially for the standalone six-digit TOTP login.

**Implementation.** The safest default is to ignore forwarding headers. Replace `clientHost` with the following and configure the ingress to enforce its own limits. Where proxy-derived identities are necessary, use an explicit trusted-proxy CIDR list and parse the chain from the trusted end, not the first value supplied by the client.

```go
// auth.go; imports net, net/netip
func clientHost(r *http.Request) string {
    host, _, err := net.SplitHostPort(r.RemoteAddr)
    if err != nil { host = r.RemoteAddr }
    ip, err := netip.ParseAddr(host)
    if err != nil { return "invalid-peer" }
    return ip.Unmap().String()
}
```

Bound limiter storage as well as individual buckets: expire entries on a timer and reject new keys when the configured capacity is reached rather than admitting an unbounded collection. Keep an independent global admission limit and a canonical account limit; changing an IP must not reset the latter. Do not introduce a permanent account lockout that lets an attacker indefinitely lock out the owner.

**Regression.** Two requests with the same `RemoteAddr` and different forwarding headers must consume the same bucket. Exercise IPv4-mapped IPv6, invalid `RemoteAddr`, a full limiter table, and an actual trusted ingress configuration. A header-rotation test belongs in the authentication handler tests, not only the helper tests.


<a id="auth-02"></a>

### AUTH-02 â€” Password verification has unbounded expensive concurrency and attacker-keyed state

**Priority:** High  
**Evidence:** Confirmed static defect  
**Pinned source:** [`auth.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/auth.go); [`password.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/password.go)

**Evidence and impact.** Password checks use Argon2 with approximately 64 MiB per verification. There is no process-wide KDF admission limit. `pwFails` is keyed by the submitted identifier rather than consistently by the resolved user, and low-count unknown identifiers are retained. Together with AUTH-01, many concurrent unknown-user requests can consume substantial memory/CPU and grow bookkeeping. The password-delete verification path also lacks the same attempt protection used by password setting. Identifier aliases should not multiply the account's guessing allowance.

**Implementation.** Put both real and dummy password verifications behind the same small semaphore. Add bounded, expiring attempt storage; use resolved user IDs for account limits, a fixed dummy account bucket for nonexistent users, and a trusted peer bucket before lookup. Preserve comparable unknown-user timing without allowing unlimited KDF work.

```go
// auth_work.go; imports context, errors
var errAuthBusy = errors.New("authentication is busy")
var passwordWork = make(chan struct{}, 2) // configure against the memory budget

func withPasswordWork(ctx context.Context, verify func() bool) (bool, error) {
    select {
    case passwordWork <- struct{}{}:
        defer func() { <-passwordWork }()
    case <-ctx.Done():
        return false, ctx.Err()
    default:
        return false, errAuthBusy
    }
    if err := ctx.Err(); err != nil { return false, err }
    return verify(), nil
}
```

Integrate this around **every** password hash/verification entry point, including setup, change, delete, login and the dummy hash. Translate admission rejection into `429` with `Retry-After`, not a wrong-password result. Argon2 itself cannot be interrupted mid-computation; admission is what bounds its memory use. Choose hash parameters for the deployment rather than increasing semaphore capacity indiscriminately.

**Regression.** Submit more concurrent valid-length requests than capacity and assert at most two KDF calls run. Check aliases share an account limit, unknown names cannot grow storage indefinitely, and deleting a password is throttled.


<a id="auth-03"></a>

### AUTH-03 â€” Standalone TOTP login permits reuse of an already accepted code

**Priority:** High  
**Evidence:** Confirmed static defect; standalone login is an explicit design choice  
**Pinned source:** [`auth.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/auth.go); [`schema.sql`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/schema.sql); [`AUDIT_OPEN.md`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/AUDIT_OPEN.md)

**Evidence and impact.** TOTP is intentionally an alternative login, not a mandatory second factor. Validation accepts codes around the current time window without recording an accepted counter. A captured successful code can be reused while valid. The spoofable peer limiter in AUTH-01 makes the low-entropy standalone login particularly important. Merely calling this â€œmissing MFAâ€ would misrepresent the documented design; the concrete defect is absent replay consumption, and the weaker alternative-login policy also warrants an explicit product decision.

**Implementation.** Store the last accepted time step and advance it atomically as part of creating the session. Return the matching counter from verification instead of only a Boolean. Use the same configured period/skew as the existing verifier.

```sql
ALTER TABLE auth_totp
  ADD COLUMN last_used_step bigint NOT NULL DEFAULT -1;

-- Execute after cryptographic validation, in the session-creation transaction.
UPDATE auth_totp
SET last_used_step = $2
WHERE user_id = $1 AND enabled_at IS NOT NULL
  AND last_used_step < $2
RETURNING user_id;
```

An update returning no row is a replay/disabled-factor failure. Do not mint the session before committing this update. If several skew counters could match, choose the highest matching acceptable counter and advance monotonically. Enrollment confirmation should also consume its accepted step. Add an account-level short-lived attempt budget independent of IP. For stronger protection, offer password/passkey-first TOTP verification and clearly distinguish it from standalone authenticator login.

**Regression.** Concurrent submissions of one valid code yield one session. A previous accepted step is rejected, a later step succeeds, a transaction failure does not create a session, and factor deletion/re-enrollment resets the counter only as part of a deliberate factor replacement.


<a id="auth-04"></a>

### AUTH-04 â€” Concurrent TOTP enrollment can overwrite or disable a newly enabled factor

**Priority:** Medium  
**Evidence:** Confirmed concurrency defect  
**Pinned source:** [`auth.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/auth.go); [`schema.sql`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/schema.sql)

**Evidence and interleaving.** The begin handler checks whether TOTP is enabled separately from an upsert that replaces the secret and resets `enabled_at`. A competing confirm can enable the factor between those operations. A stale begin then overwrites it. Conversely, confirmation can validate one secret while a competing begin replaces that secret before the enabling update.

**Implementation.** Keep pending enrollment separate from the active secret and bind confirmation to a revision. A minimal compatible repair is to serialize begin and confirm on the owner row, re-read the TOTP row inside that transaction, and make replacement conditional on it still being pending.

```sql
BEGIN;
SELECT id FROM users WHERE id = $1 FOR UPDATE;
-- Re-check enabled_at under this lock before accepting a begin operation.
INSERT INTO auth_totp (user_id, secret_ciphertext, enabled_at)
VALUES ($1, $2, NULL)
ON CONFLICT (user_id) DO UPDATE
SET secret_ciphertext = EXCLUDED.secret_ciphertext
WHERE auth_totp.enabled_at IS NULL
RETURNING user_id;
COMMIT;
```

Prefer a new `pending_secret_ciphertext`, `pending_expires_at`, and random `pending_revision` so retrying begin never touches an active factor. Confirm must validate the secret read under the lock and condition its update on that same revision; commit factor enablement, replay-counter initialization and session revocation together.

**Regression.** Pause begin after its preliminary lookup; complete confirm; resume begin. The active factor must remain unchanged. Simultaneous begin/confirm against different revisions must not enable a secret the user never confirmed.


<a id="auth-05"></a>

### AUTH-05 â€” Credential changes do not guarantee revocation of old sessions

**Priority:** High  
**Evidence:** Confirmed static defect and concurrency gap  
**Pinned source:** [`auth.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/auth.go); [`schema.sql`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/schema.sql)

**Evidence and impact.** `revokeOtherSessions` discards database errors and is called after credential changes have committed. Removing TOTP does not perform the same revocation. There is also a check/commit race: a login can verify an old credential, pause, and create a new session after the credential-change path has deleted existing sessions. Reporting a successful security change must not silently leave such sessions usable. `handleLogout` likewise discards the session-deletion error before returning success: clearing this browser cookie does not revoke another copy of that token when the database deletion failed.

**Implementation.** Use an authentication epoch, and perform credential mutation and epoch increment in one transaction. Login captures the epoch with the credential and creates its session only if that epoch remains current. A current session retained after a credential change must be updated explicitly; all other epochs become invalid without relying on best-effort deletion.

```sql
ALTER TABLE users ADD COLUMN auth_epoch bigint NOT NULL DEFAULT 0;
ALTER TABLE auth_sessions ADD COLUMN auth_epoch bigint NOT NULL DEFAULT 0;

-- Within the credential-changing transaction, after locking the user:
UPDATE users SET auth_epoch = auth_epoch + 1 WHERE id = $1
RETURNING auth_epoch;
-- Update only the retained, authenticated current session to the returned epoch.
UPDATE auth_sessions SET auth_epoch = $2 WHERE id_hash = $3 AND user_id = $1;

-- Add this equality to every session lookup:
SELECT s.user_id
FROM auth_sessions s JOIN users u ON u.id = s.user_id
WHERE s.id_hash = $1 AND s.expires_at > now()
  AND s.auth_epoch = u.auth_epoch;
```

For login, lock `users` with `FOR UPDATE` in the short session-insert transaction, compare the epoch to the value read before verification, and reject/retry if changed. Security mutation uses the same user lock. Do not hold a database transaction open during Argon2. Apply this to password/passkey/TOTP changes and recovery/reset flows. If initially retaining deletion-based revocation, at least execute it in the same transaction and propagate its error; that alone does not close the stale-login race.

For logout, check the result of the server-side session deletion and return a retryable failure when revocation cannot be confirmed; do not report successful global token invalidation merely because a local cookie was cleared.

**Regression.** Inject revocation failure and require an atomic rollback. Pause a login between verification and session insertion, change the credential, and verify the stale login cannot mint a usable session. Check TOTP removal too.


<a id="auth-06"></a>

### AUTH-06 â€” Long-lived sessions can enroll durable replacement credentials without fresh proof

**Priority:** Medium  
**Evidence:** Hardening; requires an already compromised authenticated session  
**Pinned source:** [`auth.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/auth.go)

**Evidence and impact.** Several security-sensitive operations rely on an authenticated session without demanding fresh credential verification. A stolen long-lived session can become durable access by adding a passkey, changing recovery material or issuing agent credentials. This is not an unauthenticated bypass; it is a persistence risk after session theft. Password-changing and full-account deletion already have additional checks in some paths, so apply the rule consistently rather than weakening those checks.

**Implementation.** Record a distinct successful reauthentication timestamp, not merely session creation time or recent activity.

```sql
ALTER TABLE auth_sessions ADD COLUMN reauthenticated_at timestamptz;
```
```go
// Call after session validation, before beginning a security ceremony.
func freshProof(at sql.NullTime, now time.Time) bool {
    return at.Valid && !at.Time.After(now) && now.Sub(at.Time) <= 10*time.Minute
}
```

A dedicated password/passkey verification endpoint should update this field only after actual verification, use AUTH-02 admission control, and bind the ensuing security operation to that session. Require fresh proof for factor enrollment/removal, recovery-code regeneration and agent-token creation. A first-run bootstrap or explicitly verified recovery flow needs its own tightly scoped, one-use authorization instead of pretending to be an ordinary existing session.

**Regression.** A valid but old session cannot add credentials; ordinary activity does not refresh proof; a fresh proof expires; proof obtained in one browser session does not authorize another.


<a id="auth-07"></a>

### AUTH-07 â€” First-run completion is not a single atomic transition

**Priority:** Medium  
**Evidence:** Confirmed check/commit concurrency gap  
**Pinned source:** [`auth.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/auth.go); [`setup.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/setup.go); [`config.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/config.go)

**Evidence and impact.** `handleBootstrapBegin` checks `ownerConfigured`, but `handleBootstrapFinish` checks only `bootstrapAuthorized` before opening its transaction. `finishRegistration` locks the user before insertion, without rechecking installation completion under that lock. Two finish requests that pass token authorization before the first retires the token can therefore both insert distinct credentials and replace recovery material. The password path also checks configuration before its transaction, so passkey/password completion needs the same guard. Concurrent in-flight first-run ceremonies require a final transactional re-check; a previously issued setup ceremony must not remain an authorization to install credentials after another has completed setup. Configuration/origin/authenticator fields are also mutated during setup while serving requests, and their reads need one consistent synchronization policy. The bootstrap token is required; this is not a claim that anonymous strangers can simply initialize a configured installation.

**Implementation.** Introduce an installation row and consume setup in the same transaction as the first credential. Store the ceremony's setup epoch and reject stale completion.

```sql
CREATE TABLE installation_state (
  singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
  setup_epoch bigint NOT NULL DEFAULT 0,
  configured_at timestamptz
);
INSERT INTO installation_state(singleton) VALUES (true) ON CONFLICT DO NOTHING;

-- Finish-handler transaction:
SELECT setup_epoch, configured_at FROM installation_state
WHERE singleton = true FOR UPDATE;
-- Reject if configured_at IS NOT NULL or the ceremony's epoch differs.
-- Insert the first credential and recovery material in this same transaction.
UPDATE installation_state
SET configured_at = now(), setup_epoch = setup_epoch + 1
WHERE singleton = true AND configured_at IS NULL;
```

Publish a fully constructed immutable authentication/origin configuration after commit under one mutex or `atomic.Pointer`; do not mutate fields of an object concurrent readers already hold. Reinitialize from durable state on restart. Require explicit authenticated recovery, rather than a second bootstrap completion, to change an initialized owner.

**Regression.** Two preissued ceremonies race to finish; exactly one initializes the installation and the other's transaction changes nothing. Run those handlers and configuration reads under `go test -race`.


<a id="auth-08"></a>

### AUTH-08 â€” Generated secret/config files can be overwritten or truncated during concurrent startup

**Priority:** High  
**Evidence:** Confirmed file-persistence defect under concurrent startup or crash  
**Pinned source:** [`setup.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/setup.go); [`secretbox.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/secretbox.go)

**Evidence and impact.** The private-file helper uses `os.WriteFile` after generating values. The mode protects a newly created file but does not make publication exclusive or atomic. Two starts can generate different encryption keys, then overwrite the same path; a process may keep one key in memory while a different one survives on disk. Interruption during truncation/write can also destroy the only persisted key. Losing that key loses the ability to decrypt provider credentials.

**Implementation.** For a generated immutable key, publish a complete private temporary file using an exclusive hard-link operation on the same filesystem. A loser reads the existing key instead of replacing it. Fsync both content and directory. Mutable JSON/config files need same-directory temporary-write plus atomic rename and serialized writers instead.

```go
// setup_atomic.go; imports errors, io/fs, os, path/filepath
func publishPrivateOnce(path string, data []byte) ([]byte, error) {
    dir := filepath.Dir(path)
    if err := os.MkdirAll(dir, 0700); err != nil { return nil, err }
    f, err := os.CreateTemp(dir, ".lullmail-secret-*")
    if err != nil { return nil, err }
    name := f.Name()
    defer os.Remove(name)
    defer f.Close()
    if err = f.Chmod(0600); err != nil { return nil, err }
    if _, err = f.Write(data); err != nil { return nil, err }
    if err = f.Sync(); err != nil { return nil, err }
    if err = f.Close(); err != nil { return nil, err }
    if err = os.Link(name, path); err != nil {
        if errors.Is(err, fs.ErrExist) { return os.ReadFile(path) }
        return nil, err
    }
    d, err := os.Open(dir)
    if err != nil { return nil, err }
    defer d.Close()
    if err = d.Sync(); err != nil { return nil, err }
    return append([]byte(nil), data...), nil
}
```

This implementation targets the Linux deployment and a filesystem supporting same-filesystem hard links; use an equivalent exclusive atomic-publish primitive elsewhere. Parse/validate the winning file, require an appropriate owner/private directory, and use its returned value in memory. Do not delete/recreate an unreadable encryption key automatically. Back up the database **and** the encryption key together, securely.

**Regression.** Start many publishers with different candidate keys: all must read the same winner. Simulate crashes before/after publication, verify restrictive permissions, and confirm restart decrypts an already stored credential.


<a id="send-01"></a>

### SEND-01 â€” SMTP can downgrade to plaintext, cannot use implicit TLS on port 465, and misreports QUIT failures

**Priority:** High  
**Evidence:** Confirmed static defect; plaintext branch reproduced locally  
**Pinned source:** [`mail-engine/send.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/send.go); [`app.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/app.go)

**Evidence and impact.** `Sender.submit` starts with a plaintext TCP connection and upgrades only when the peer advertises STARTTLS. It also skips authentication when AUTH is not advertised. A peer advertising neither can receive the message in plaintext even when `Plaintext` is false. The local loopback probe reproduced this exact decision path without contacting an external mail server. This does **not** establish password disclosure: Go's `PlainAuth` has additional non-TLS checks. The disclosed material here is the SMTP envelope and message. Port 465 also needs TLS before the SMTP greeting, which the current implementation does not perform. Finally, returning `c.Quit()` after a successful DATA acknowledgment incorrectly treats a cleanup failure as a failed delivery.

**Implementation â€” replacement for `Sender.submit`.** Imports required: `context`, `crypto/tls`, `fmt`, `net`, `net/smtp`, `strconv`, `time`. Keep the existing `SMTPConfig` and `Sender` types. This supports the configured default 587 and implicit TLS on 465, requires advertised STARTTLS otherwise, requires authentication when configured, and treats the DATA acknowledgment as acceptance.

```go
func (s *Sender) submit(ctx context.Context, from string, rcpts []string, body []byte) error {
    ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
    defer cancel()
    addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
    tlsCfg := &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}
    dialer := &net.Dialer{Timeout: 10*time.Second}
    var conn net.Conn
    var err error
    implicitTLS := !s.cfg.Plaintext && s.cfg.Port == 465
    if implicitTLS {
        conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsCfg}).DialContext(ctx, "tcp", addr)
    } else {
        conn, err = dialer.DialContext(ctx, "tcp", addr)
    }
    if err != nil { return fmt.Errorf("smtp dial: %w", err) }
    defer conn.Close()
    if s.cfg.Plaintext {
        peer, ok := conn.RemoteAddr().(*net.TCPAddr)
        if !ok || !peer.IP.IsLoopback() {
            return fmt.Errorf("plaintext SMTP is restricted to loopback")
        }
    }
    stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
    defer stop()
    deadline, _ := ctx.Deadline()
    if err := conn.SetDeadline(deadline); err != nil { return err }
    c, err := smtp.NewClient(conn, s.cfg.Host)
    if err != nil { return err }
    defer c.Close()
    if err := c.Hello("localhost"); err != nil { return err }
    if !s.cfg.Plaintext && !implicitTLS {
        if ok, _ := c.Extension("STARTTLS"); !ok {
            return fmt.Errorf("SMTP server does not offer required STARTTLS")
        }
        if err := c.StartTLS(tlsCfg); err != nil { return err }
    }
    if s.cfg.Username != "" {
        if ok, _ := c.Extension("AUTH"); !ok {
            return fmt.Errorf("SMTP server does not offer required authentication")
        }
        if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
            return err
        }
    }
    if err := c.Mail(from); err != nil { return err }
    for _, recipient := range rcpts {
        if err := c.Rcpt(recipient); err != nil { return err }
    }
    w, err := c.Data()
    if err != nil { return err }
    if _, err := w.Write(body); err != nil { return err }
    if err := w.Close(); err != nil { return err } // acknowledgment matters
    quitDeadline := time.Now().Add(2*time.Second)
    if deadline.Before(quitDeadline) { quitDeadline = deadline }
    _ = conn.SetDeadline(quitDeadline)
    _ = c.Quit() // accepted mail must not become failed because QUIT failed
    return nil
}
```

An explicit transport-mode field is preferable for nonstandard ports. A lost DATA acknowledgment remains **ambiguous**, not safely retryable with exactly-once guarantees; SEND-03 addresses that state.

**Regression.** Refuse a server advertising neither STARTTLS nor AUTH before MAIL/DATA; complete an implicit-TLS 465-style connection; reject an invalid certificate; honor cancellation; and report accepted delivery when the server acknowledges DATA then closes before QUIT.


<a id="send-02"></a>

### SEND-02 â€” Outgoing UTF-8 body parts omit transfer encoding and can exceed transport line limits

**Priority:** Medium  
**Evidence:** Confirmed MIME construction defect  
**Pinned source:** [`mail-engine/send.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/send.go); [`export.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/export.go)

**Evidence and impact.** `renderBodyPart` writes raw UTF-8 text/HTML without `Content-Transfer-Encoding`. The implicit MIME encoding is 7bit, and long unwrapped text/HTML lines are emitted directly. This can produce invalid or damaged messages on stricter transports. The fallback exporter declares 8bit but still deserves line/encoding normalization. The concern is message correctness, not an inferred script-execution vulnerability.

**Implementation.** Encode each textual MIME leaf as quoted-printable. This helper is a complete replacement for leaf construction; use it for text-only, HTML-only, and each child of the multipart/alternative branch while retaining the existing random boundary generation.

```go
// mail-engine/send.go; imports fmt, mime/quotedprintable, strings
func encodedTextPart(mediaType, text string) (string, error) {
    if mediaType != "text/plain" && mediaType != "text/html" {
        return "", fmt.Errorf("unsupported text MIME type %q", mediaType)
    }
    var b strings.Builder
    b.WriteString("Content-Type: " + mediaType + "; charset=utf-8\r\n")
    b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
    enc := quotedprintable.NewWriter(&b)
    if _, err := enc.Write([]byte(text)); err != nil { return "", err }
    if err := enc.Close(); err != nil { return "", err }
    b.WriteString("\r\n")
    return b.String(), nil
}
```

For a single text part, return `encodedTextPart("text/plain", msg.Text)`; for HTML use `"text/html"`. In multipart construction, append the helper's returned part after each boundary and propagate errors. Use `mime.FormatMediaType` for non-ASCII attachment filenames instead of byte truncation and ad-hoc quoting. Validate all non-address headers at the composition boundary, including library callers' `InReplyTo`, rather than relying only on the product HTTP handler.

**Regression.** Round-trip emoji, non-Latin text, trailing spaces, mixed newlines and a 10,000-character line through `net/mail` plus a MIME decoder; assert equal decoded body and legal encoded line lengths. Add Unicode attachment-name tests.


<a id="send-03"></a>

### SEND-03 â€” Accepted sends are volatile and later failures are invisible to the user

**Priority:** High  
**Evidence:** Confirmed reliability defect; durable outbox was already deferred  
**Pinned source:** [`sendqueue.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/sendqueue.go); [`dashboard/src/app/lib/actions.ts`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/lib/actions.ts); [`dashboard/src/app/ui/Compose.tsx`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/ui/Compose.tsx); [`AUDIT_OPEN.md`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/AUDIT_OPEN.md)

**Evidence and impact.** The server acknowledges an in-memory queued send, then delivers later in a background context. Failure removes the entry and logs an error; the completion channel is not a durable status API. Restart loses pending jobs. The composer deletes the draft and attachment storage after the queue acknowledgment, before provider acceptance. A transient SMTP/OAuth error can therefore leave neither delivery nor a recoverable draft. This is stronger than merely saying the documented in-memory queue is not crash durable.

**Implementation â€” durable state contract.** Persist the complete encrypted payload and a stable message identity **before** returning `202`. Keep drafts until a terminal accepted/cancelled result has been reconciled. The following migration and claim/cancel SQL are the central implementation; the existing delivery functions become worker callbacks. The worker, API, status events and composer integration are required together, not a drop-in single-function fix.

```sql
CREATE TABLE outgoing_jobs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  account_id uuid NOT NULL REFERENCES email_accounts(id) ON DELETE CASCADE,
  idempotency_key text NOT NULL,
  payload_ciphertext text NOT NULL,
  payload_bytes bigint NOT NULL CHECK (payload_bytes BETWEEN 0 AND 35651584),
  message_id text NOT NULL,
  state text NOT NULL DEFAULT 'pending'
    CHECK (state IN ('pending','delivering','accepted','failed','unknown','cancelled')),
  not_before timestamptz NOT NULL DEFAULT now() + interval '5 seconds',
  lease_until timestamptz,
  lease_token uuid,
  attempts integer NOT NULL DEFAULT 0,
  error_code text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (user_id, idempotency_key)
);
CREATE INDEX outgoing_jobs_ready ON outgoing_jobs(not_before)
  WHERE state = 'pending';

-- Claim one job in a short transaction. Do not hold this transaction over I/O.
WITH picked AS (
  SELECT id FROM outgoing_jobs
  WHERE state = 'pending' AND not_before <= now()
  ORDER BY not_before, id
  FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE outgoing_jobs j
SET state = 'delivering', lease_until = now() + interval '90 seconds',
    lease_token = gen_random_uuid(), attempts = attempts + 1, updated_at = now()
FROM picked WHERE j.id = picked.id
RETURNING j.*;

-- Undo succeeds only while delivery has not claimed the job.
UPDATE outgoing_jobs
SET state = 'cancelled', updated_at = now()
WHERE id = $1 AND user_id = $2 AND state = 'pending'
RETURNING payload_ciphertext;

-- Do not automatically resend a job whose worker may already have sent it.
UPDATE outgoing_jobs SET state = 'unknown', updated_at = now()
WHERE state = 'delivering' AND lease_until < now();
```

A worker completion update must include `WHERE id=$1 AND lease_token=$2 AND state='delivering'` so an expired worker cannot overwrite a newer decision. Render and persist the stable Message-ID/raw MIME once for SMTP. Enforce per-owner outstanding-byte and job-count quotas inside a serialized transaction. Expose `GET /api/outbox/{id}` with owner authorization, safe error codes and delivery state; emit changes through the existing event mechanism. Persist a request-body digest and reject reuse of an idempotency key with different content. Do not claim that a stable Message-ID itself prevents duplicates.

**Failure semantics.** Distinguish known pre-acceptance failure, accepted delivery, unknown acceptance, and failed Sent-copy archival. A failed Sent-copy must not trigger another SMTP send. A crash after provider acceptance but before database completion produces `unknown`; reconcile with provider evidence when possible and otherwise require an explicit informed retry. Do not implement blind automatic retries as an â€œexactly onceâ€ guarantee.

**Regression.** Restart during the undo window, fail DNS/auth/provider requests, crash after DATA acknowledgment, duplicate the API request, expire a worker lease, and fail Sent-copy archival. In every case show a recoverable payload and truthful status. Test quotas and cancellation races against real PostgreSQL.


<a id="send-04"></a>

### SEND-04 â€” A queued SMTP message can be sent after its account has been deleted

**Priority:** High  
**Evidence:** Confirmed lifecycle race  
**Pinned source:** [`sendqueue.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/sendqueue.go); [`accounts.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/accounts.go); [`app.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/app.go); [`auth.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/auth.go)

**Evidence and interleaving.** SMTP delivery captures a sender with credentials before the timer. Actual submission does not hold the account-use gate. Delete the account after the queue response but before the timer: the captured sender still has credentials and can send. The Sent-copy gate runs too late to stop delivery. Full-account deletion has the same concern for outstanding in-memory jobs.

**Implementation.** Acquire the account's lifecycle lease at **actual delivery time**, re-read credentials inside that lease, and retain the lease through submission and Sent-copy handling. Cancel pending jobs during deletion. For cross-process correctness, use the account row as the barrier; this helper is a concrete integration pattern. Deletion must lock that same row `FOR UPDATE` **before any cleanup**, and every sender must use this helper.

```go
// Imports context, database/sql.
func withDeliveryAccount(ctx context.Context, db *sql.DB, userID, accountID string,
    deliver func(context.Context, *sql.Tx) error) error {
    tx, err := db.BeginTx(ctx, nil)
    if err != nil { return err }
    defer tx.Rollback()
    var id string
    err = tx.QueryRowContext(ctx, `
        SELECT id::text FROM email_accounts
        WHERE id=$1 AND user_id=$2 FOR SHARE`, accountID, userID).Scan(&id)
    if err != nil { return err } // deleted/disabled account: do not send
    if err := deliver(ctx, tx); err != nil { return err }
    return tx.Commit()
}
```

This intentionally holds a row lock during bounded delivery; keep worker concurrency small and set a hard delivery deadline. Read credentials through `tx` so the lease and credential belong to the same row. Existing single-process account gates can provide a lighter implementation only when deployment is explicitly single-process. Do not hold the job-claim row lock while acquiring the account lock, or deletion and workers can deadlock through reversed lock order.

**Regression.** Queue, delete, then advance the fake clock: no SMTP connection occurs. Pause an in-flight send and start deletion: deletion must either cancel and join it or wait for its bounded completion, and no send may start after deletion returns successfully.


<a id="send-05"></a>

### SEND-05 â€” Undo-send reconstructs an incomplete draft

**Priority:** Medium  
**Evidence:** Confirmed user-data loss defect  
**Pinned source:** [`dashboard/src/app/lib/actions.ts`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/lib/actions.ts); [`dashboard/src/app/ui/Compose.tsx`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/ui/Compose.tsx); [`dashboard/src/app/lib/store.ts`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/lib/store.ts)

**Evidence and impact.** The undo callback reopens only selected fields; it omits Cc, Bcc and attachments. The composer also initializes Cc/Bcc from saved storage or empty strings rather than the supplied seed. Fixing only the callback would therefore still lose fields. SEND-03's early draft retirement amplifies the problem.

**Implementation.** Treat a draft as one complete versioned snapshot and restore that snapshot, not a hand-selected subset. A suitable shared type is:

```ts
export interface DraftSnapshot {
  id: string;
  revision: number;
  accountId: string;
  to: string;
  cc: string;
  bcc: string;
  subject: string;
  text: string;
  html: string;
  replyToId?: string;
  attachments: Array<{
    filename: string;
    contentType: string;
    dataBase64: string;
  }>;
}
export function copyDraft(draft: DraftSnapshot): DraftSnapshot {
  return structuredClone(draft);
}
```

Capture this full snapshot before enqueue; associate it with the returned job ID in IndexedDB. After a successful cancel, reopen that snapshot and retain it until a later successful send or explicit discard. Set initial Cc and Bcc to `saved?.cc ?? seed.cc ?? ""` and `saved?.bcc ?? seed.bcc ?? ""`, respectively. Initialize attachment state from the restored snapshot, or await its IndexedDB restoration before enabling Send. Do not duplicate attachment bodies into synchronous `localStorage`.

**Regression.** Compose with To/Cc/Bcc, HTML, multiple attachments, a selected sending account and a reply parent; send then undo. Compare the complete restored snapshot byte-for-byte, including attachment contents and recipient fields.


<a id="send-06"></a>

### SEND-06 â€” Graph attachment limits are enforced after queue acceptance rather than before it

**Priority:** Medium  
**Evidence:** Confirmed validation-order defect  
**Pinned source:** [`sendqueue.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/sendqueue.go); [`oauth.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/oauth.go); [`dashboard/src/app/ui/Compose.tsx`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/ui/Compose.tsx)

**Evidence and impact.** The generic send handler accepts substantially larger attachments than the Graph direct-upload implementation supports. `graphAttachments` rejects those later, inside actual OAuth delivery. Consequently a request can be accepted and its browser draft retired even though the chosen transport is guaranteed to reject it locally.

**Implementation.** After resolving the owner-authorized account and decoding attachments, run the transport-specific validator before adding the job. Reuse the existing validator rather than maintaining a different client-only limit.

```go
// In handleSend, before queue insertion. provider and outgoing are the
// account/provider and assembled mail.Outgoing already resolved by the handler.
if provider == "graph" {
    if _, err := graphAttachments(outgoing.Attachments); err != nil {
        writeProblem(w, http.StatusUnprocessableEntity, "Invalid Attachment", err.Error())
        return
    }
}
```

Expose transport capabilities to the composer so it can explain the limit before upload. Supporting larger Graph files requires its upload-session flow, not merely increasing this constant. Retain the server-side validation even after adding client checks.

**Regression.** Test the exact boundary around the Graph helper's existing limit and a file accepted by the generic limit but rejected by Graph. The latter must return a synchronous validation error and leave the draft untouched.


<a id="send-07"></a>

### SEND-07 â€” The exposed raw mail-engine API bypasses product send and sync semantics

**Priority:** Medium  
**Evidence:** Confirmed API-consistency defect; raw routes are owner-only  
**Pinned source:** [`mail.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail.go); [`mail-engine/service.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/service.go); [`app.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/app.go); [`agent.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/agent.go); [`mail-engine/credential.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/credential.go)

**Evidence and impact.** The raw `/api/mail/` surface provides alternate mutation/sync/send paths. Its sending path uses SMTP independently of the product's OAuth, undo/status and Sent-copy behavior. Reply construction does not preserve Cc/Bcc consistently. Raw sync also bypasses the product's normal completion work. Nonzero request credentials can replace the stored resolver credential for an existing mirror, allowing a different mailbox to be synced under the original mirror identity. Agents are explicitly blocked from this surface; this is **not** an agent-to-owner authorization bypass, nor proof that a caller can redirect stored secrets using only a host header.

**Implementation.** Prefer not mounting the mutable library service in the product. Route supported operations through the owner-scoped product handlers and reject `X-Mail-*` credential overrides there. The smallest safe product restriction is to deny raw mutations rather than trying to keep two send pipelines equivalent:

```go
func readOnlyEngine(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodGet && r.Method != http.MethodHead {
            writeProblem(w, http.StatusMethodNotAllowed, "Use Product API",
                "mail mutations must use the owner-scoped product endpoints")
            return
        }
        next.ServeHTTP(w, r)
    })
}
```

Removing the mount altogether is cleaner if no supported client needs it. Any retained route that contacts a provider must load credentials from the authorized account, not caller-supplied overrides. In the reusable library's reply branch, assign Cc and Bcc explicitly from the request in addition to To/Subject/HTML, and test them separately.

**Regression.** Compare supported send/reply behavior across entry points; verify OAuth accounts never fall back to SMTP password handling; verify raw credential overrides cannot contaminate an existing mirror. Preserve the existing agent denial tests.


<a id="web-01"></a>

### WEB-01 â€” Offline startup deletes the cache it is supposed to read

**Priority:** High  
**Evidence:** Confirmed static defect; control flow reproduced locally  
**Pinned source:** [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/lib/offline.ts); [`dashboard/src/app/App.tsx`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/App.tsx); [`dashboard/src/app/lib/api.ts`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/lib/api.ts)

**Evidence and impact.** `startOfflineData` invokes replay on startup and unconditionally clears response storage in its completion handler. Replay returns zero when offline or when there is nothing to replay. Thus an offline launch clears previously saved mail instead of preserving it. Startup order makes this compete with authentication and cached reads, so apparent success can depend on timing. The standalone control-flow probe reproduced clearing after a zero-result replay.

**Implementation.** Do not invalidate anything when replay made no committed changes. Invalidate before, not after, loading fresh server responses. A minimal replacement for the completion chain is:

```ts
void replayMutations().then(async (committed) => {
  if (committed === 0) return;
  await clearResponseCache(); // later narrow this to affected resource keys
  const { reload, refreshCounts } = await import("./actions");
  await Promise.all([reload(), refreshCounts()]);
}).catch((error) => {
  console.error("Offline replay failed", error);
  // Preserve queued work and cached mail; surface a retryable sync indicator.
});
```

WEB-03 must first make the returned count mean successful committed mutations, not deleted error responses. The broad cache clear in the generic mutation path should likewise become invalidation by resource/account, otherwise an unrelated online action removes all offline thread bodies. Keep cache invalidation separate from deleting locally authored drafts.

**Regression.** Seed cached authentication metadata, a bucket and thread; launch with `navigator.onLine=false`; all remain available. Repeat with an empty queue online and with a failed replay. After a successful replay, freshly fetched responses must remain cached.


<a id="web-02"></a>

### WEB-02 â€” IndexedDB operations report success before the transaction commits

**Priority:** Medium  
**Evidence:** Confirmed static defect; promise timing reproduced locally  
**Pinned source:** [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/lib/offline.ts)

**Evidence and impact.** The transaction helper resolves on a request's `onsuccess`. A successful `put` request does not establish transaction commit; the transaction can still abort, including on quota or another operation's failure. This permits the UI to report a saved draft/attachment or queued mutation which was never durable.

**Implementation â€” replacement helper contract.** Resolve only from `transaction.oncomplete`, and reject abort/error paths. Adapt the existing helper's callers to this signature.

```ts
export function committedRequest<T>(
  db: IDBDatabase,
  storeName: string,
  mode: IDBTransactionMode,
  operation: (store: IDBObjectStore) => IDBRequest<T>,
): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const tx = db.transaction(storeName, mode);
    let value!: T;
    let requestError: DOMException | null = null;
    tx.oncomplete = () => resolve(value);
    tx.onabort = () => reject(tx.error ?? requestError ?? new Error("Storage transaction aborted"));
    tx.onerror = () => { requestError = tx.error; };
    try {
      const req = operation(tx.objectStore(storeName));
      req.onsuccess = () => { value = req.result; };
      req.onerror = () => { requestError = req.error; };
    } catch (error) {
      try { tx.abort(); } catch { /* already inactive */ }
      reject(error);
    }
  });
}
```

Do not catch these failures and silently call the operation saved. Show a persistent storage error and retain the in-memory draft so the owner can copy or retry it. An unavailable database or missing owner namespace must not be reported as successful persistence either.

**Regression.** A request succeeds, then its transaction aborts: the promise must reject. Test quota exhaustion, database-unavailable/private-storage behavior, synchronous operation exceptions and a normal committed read/write in a real browser.


<a id="web-03"></a>

### WEB-03 â€” Offline replay discards failed 4xx mutations as though they succeeded

**Priority:** Medium  
**Evidence:** Confirmed retry/data-loss defect  
**Pinned source:** [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/lib/offline.ts)

**Evidence and impact.** Replay deletes queued work on broad 4xx responses and counts it as replayed. A `429` is a retry request, not successful application; conflicts and validation failures also need a visible outcome. Silently removing those actions loses intended changes and can trigger cache invalidation/reloads that conceal the failure.

**Implementation.** Classify results explicitly, delete only confirmed successes, and persist a retry/conflict/failed state for the rest. Respect `Retry-After` and cap exponential backoff with jitter.

```ts
type ReplayDecision = "committed" | "reauth" | "retry" | "conflict" | "failed";
export function replayDecision(status: number): ReplayDecision {
  if (status >= 200 && status < 300) return "committed";
  if (status === 401) return "reauth";
  if (status === 409 || status === 412) return "conflict";
  if ([408, 425, 429].includes(status) || status >= 500) return "retry";
  return "failed";
}
export function retryAt(header: string | null, attempt: number, now = Date.now()): number {
  if (header) {
    const seconds = Number(header);
    const absolute = Number.isFinite(seconds) ? now + Math.max(0, seconds) * 1000 : Date.parse(header);
    if (Number.isFinite(absolute)) return Math.max(now, absolute);
  }
  const delay = Math.min(300_000, 1000 * 2 ** Math.min(attempt, 8));
  return now + delay + Math.floor(Math.random() * 1000);
}
```

For a permanent 404/422, show a failed-action entry with the original intent and a discard/revise control instead of claiming success. Count only `committed` results. A network exception must preserve the queued action; WEB-04 addresses uncertain server acceptance and duplicates.

**Regression.** Inject 429, 408, 409, 412, 422, 500, 401 and a connection failure after server commit. Assert queue state and user messaging, not only the number of fetches.


<a id="web-04"></a>

### WEB-04 â€” Offline replay is not coordinated across tabs or made idempotent at the server

**Priority:** Medium  
**Evidence:** Confirmed concurrency/uncertain-retry gap  
**Pinned source:** [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/lib/offline.ts); [`dashboard/src/app/lib/api.ts`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/lib/api.ts); [`dashboard/src/app/App.tsx`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/App.tsx)

**Evidence and impact.** Each page instance can replay the same persisted queue. There is no cross-tab claim or server idempotency key. Even with a single tab, the response can be lost after the server applied a request, leaving the client to replay it. Repeated non-idempotent operations and relative-time snoozes can produce duplicates or changed results. Replay starts before current authentication is established, which also makes owner changes a boundary that must be explicitly guarded.

**Implementation.** Start replay only after verifying the authenticated owner matches the queue namespace. Use a Web Lock as a browser optimization, but make server idempotency the correctness boundary. Preserve a UUID idempotency key across every retry.

```ts
async function replayForOwner(owner: string, replay: () => Promise<number>): Promise<number> {
  if (!navigator.locks) return replay(); // server idempotency remains mandatory
  return navigator.locks.request(`lullmail:replay:${owner}`, async () => replay());
}
// When constructing the fetch for a stored action:
// headers: {"Content-Type":"application/json", "Idempotency-Key": mutation.id}
```
```sql
CREATE TABLE api_mutations (
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  key uuid NOT NULL,
  request_hash text NOT NULL,
  response_status integer NOT NULL,
  response_json jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, key)
);
```

For a database-only product mutation, lock the owner row, check `(user_id,key)`, reject different request hashes with 409, execute the mutation and save its response **in the same transaction**. Return the saved response on repetition. Outgoing network side effects use the durable outbox, not a transaction wrapped around an arbitrary external POST. Define retention longer than the supported offline queue lifetime.

**Regression.** Two tabs replay one queue simultaneously; only one logical mutation occurs. Lose the first response after server commit and retry. Switch owners while replay is paused; no remaining action may be sent using the new owner's session.


<a id="web-05"></a>

### WEB-05 â€” Competing draft stores can resurrect stale text during tab changes

**Priority:** Medium  
**Evidence:** Confirmed persistence-order defect  
**Pinned source:** [`dashboard/src/app/ui/Compose.tsx`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/ui/Compose.tsx); [`dashboard/src/app/lib/store.ts`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/lib/store.ts)

**Evidence and impact.** The composer has a debounced per-draft saved snapshot in addition to the debounced global draft stack. Remount initialization prefers the per-draft saved value. Clearing a pending composer timer on unmount can leave that snapshot older than the stack, then switching back restores the older text. Different timers and storage locations have no shared revision ordering.

**Implementation.** Make one versioned snapshot authoritative, preferably IndexedDB, and do not overwrite a newer revision with a delayed write. This complete persistence helper uses one transaction for compare-and-put; create a `drafts` object store during the next database version upgrade.

```ts
export function saveDraftRevision(
  db: IDBDatabase, key: string, snapshot: DraftSnapshot,
): Promise<void> {
  return new Promise((resolve, reject) => {
    const tx = db.transaction("drafts", "readwrite");
    tx.oncomplete = () => resolve();
    tx.onabort = () => reject(tx.error ?? new Error("Draft save aborted"));
    tx.onerror = () => { /* onabort owns failure completion */ };
    const store = tx.objectStore("drafts");
    const read = store.get(key);
    read.onsuccess = () => {
      const previous = read.result as DraftSnapshot | undefined;
      if (!previous || previous.revision < snapshot.revision) {
        store.put(structuredClone(snapshot), key);
      }
    };
  });
}
```

Increment the revision on each logical edit, flush the latest snapshot before switching/parking a compose tab, and initialize from the highest revision only. Remove the duplicate per-draft `localStorage` authority. For concurrent editing of one draft across tabs, use a revision compare-and-swap with conflict presentation, not independent per-tab counters that can both write revision 10. Namespace the key by immutable installation/user identity as in WEB-07.

**Regression.** Type and switch drafts before 250 ms; switch back; the final keystrokes remain. Deliver old persistence callbacks after newer ones; the newer revision wins. Test reload/pagehide and quota failure without deleting the in-memory draft.


<a id="web-06"></a>

### WEB-06 â€” Attachment restoration and concurrent file additions can lose attachments

**Priority:** Medium  
**Evidence:** Confirmed asynchronous UI race  
**Pinned source:** [`dashboard/src/app/ui/Compose.tsx`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/ui/Compose.tsx); [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/lib/offline.ts)

**Evidence and impact.** The async restore closure observes the initial empty attachment list, so it can overwrite attachments selected while loading. Send is enabled before restoration finishes, making an apparently parked draft send without its saved attachments. File-reading loops also build results from a captured attachment array, allowing concurrent selections to overwrite one another.

**Implementation.** Give each local attachment a stable ID, merge restored/current entries by that ID, and use functional updates. Do not enable Send or persist an empty replacement list until restoration succeeds. A failed load must show a recoverable error, not silently become an empty attachment list.

```ts
type LocalAttachment = SendAttachment & { id: string };
function mergeAttachments(saved: LocalAttachment[], current: LocalAttachment[]): LocalAttachment[] {
  return [...new Map([...saved, ...current].map(item => [item.id, item])).values()];
}
// In Compose's restore effect:
useEffect(() => {
  let disposed = false;
  setAttachmentsReady(false);
  void loadDraftAttachments(draftID).then(saved => {
    if (disposed) return;
    setAttachments(current => mergeAttachments(saved as LocalAttachment[], current));
    setAttachmentsReady(true);
  }).catch(error => {
    if (!disposed) setAttachmentError(String(error));
  });
  return () => { disposed = true; };
}, [draftID]);
// For newly decoded files, use:
// setAttachments(current => mergeAttachments(current, decodedFiles));
// Disable Send while !attachmentsReady, while file decoding, or on a storage error.
```

Add the referenced ready/error state in the component and migrate older attachment records by assigning IDs once and persisting them. Serialize persistence or combine it with draft revisions so delayed writes cannot replace the latest list. Check count and aggregate-byte limits against the **new combined list**, including concurrent selections.

**Regression.** Delay restore, select another file, then finish restore: both survive. Click Send before restoration: submission is blocked. Resolve two file selections in reverse order and verify no file disappears.


<a id="web-07"></a>

### WEB-07 â€” Browser caches and drafts need an immutable owner namespace and generation fencing

**Priority:** Medium  
**Evidence:** Confirmed isolation gap; some exposure depends on logout/reset paths  
**Pinned source:** [`dashboard/src/app/lib/api.ts`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/lib/api.ts); [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/lib/offline.ts); [`dashboard/src/app/lib/store.ts`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/lib/store.ts); [`dashboard/src/app/App.tsx`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/App.tsx)

**Evidence and impact.** Persistent owner identity uses email, draft text uses global storage keys, and in-flight reads are not fenced against an owner change. Email is mutable/reusable and cannot distinguish a recreated installation owner. A late response from an old session can be written after the current namespace changes. Draft restoration also happens before authentication. These are local/shared-browser isolation risks, not a demonstrated cross-account server authorization bypass. The exact exposure depends on the logout/reset route taken; the storage layer should enforce isolation itself instead of relying on every UI caller to remember cleanup.

**Implementation.** Return immutable `installation_id` and `user_id` after authentication, and key drafts/responses/attachments/queue entries by both. Capture a generation and owner at request start; never persist an old response using the current owner at completion.

```ts
let authGeneration = 0;
let ownerKey: string | null = null;
export function installOwner(installationID: string, userID: string): void {
  const next = `${installationID}:${userID}`;
  if (ownerKey !== next) { ownerKey = next; authGeneration++; }
}
export function forgetOwner(): void { ownerKey = null; authGeneration++; }
export function requestIdentity() {
  return { owner: ownerKey, generation: authGeneration };
}
export function stillCurrent(snapshot: ReturnType<typeof requestIdentity>): boolean {
  return snapshot.owner !== null && snapshot.owner === ownerKey &&
    snapshot.generation === authGeneration;
}
```

Use `stillCurrent` before updating memory or IndexedDB after **every** asynchronous authenticated read. On confirmed 401/logout, cancel replay and requests and clear or lock that owner's local data according to an explicit offline policy. Do not treat a network outage as a confirmed logout and wipe valuable offline mail. Never restore one owner's draft under another owner or replay actions before owner verification.

**Regression.** Pause a read, log out, log into another owner, then complete the old read. Recreate an owner with the same email. Confirm old drafts are neither displayed nor sent, and offline access works only under the documented local-data policy.


<a id="web-08"></a>

### WEB-08 â€” Generic link unwrapping rewrites legitimate email URLs

**Priority:** Medium  
**Evidence:** Confirmed transformation defect; URL behavior reproduced locally  
**Pinned source:** [`dashboard/src/app/reader/Body.tsx`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/reader/Body.tsx)

**Evidence and impact.** Tracking-link unwrapping treats generic query parameters such as `to`, `url` and `target` as redirects on arbitrary hosts and resolves relative values. A legitimate link such as `https://example.invalid/report?to=alice` can become `https://example.invalid/alice`. Signed/action links can stop working or point somewhere unintended. The browser URL-resolution part was reproduced locally; this is not a claim of script execution.

**Implementation.** The safe default is not to infer redirect semantics at all. Preserve the original HTTP(S) destination. Only add a tracker-specific rule after testing the exact hostname, path, parameter and allowed absolute target scheme.

```ts
export function preservedMailLink(raw: string): string | null {
  try {
    const parsed = new URL(raw);
    if (!['https:', 'http:', 'mailto:'].includes(parsed.protocol)) return null;
    return parsed.href;
  } catch {
    return null;
  }
}
```

Replace the generic recursive unwrapping call with this helper at the sanitized anchor boundary. Continue enforcing the iframe sandbox, CSP, safe target and `rel` policy. Do not broaden protocols to `javascript:` or accept an untrusted `<base>` element to make relative links â€œwork.â€

**Regression.** Preserve signed URLs and ordinary `to`, `url`, `u` and `target` query parameters. Reject script/data schemes. A supported tracker-specific transformation must have positive and negative hostname/path tests.


<a id="web-09"></a>

### WEB-09 â€” Discarding the document head loses legitimate email styling

**Priority:** Low  
**Evidence:** Confirmed rendering limitation  
**Pinned source:** [`dashboard/src/app/reader/Body.tsx`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/reader/Body.tsx)

**Evidence and impact.** Returning only the sanitized `body.innerHTML` discards safe `<style>` rules originally in the head and body-level presentation. Many messages therefore lose layout or readable color combinations. This is a fidelity problem; the reader's sandbox/CSP already provides meaningful security defenses and should not be relaxed as a workaround.

**Implementation.** Preserve vetted inline stylesheet nodes in the isolated document and move body contents into a wrapper carrying only explicitly allowed presentation. Do this within the existing sanitization pipeline, after removing executable nodes, links, bases and event attributes.

```ts
function preserveInlinePresentation(doc: Document): void {
  const styles = [...doc.head.querySelectorAll('style')].map(node => node.cloneNode(true));
  const wrapper = doc.createElement('div');
  wrapper.style.cssText = doc.body.style.cssText;
  while (doc.body.firstChild) wrapper.appendChild(doc.body.firstChild);
  doc.body.appendChild(wrapper);
  for (const style of styles) doc.body.prepend(style);
}
```

Keep styles subject to the existing iframe CSP and remote-resource policy. Do not import external stylesheets or allow script execution to improve fidelity. Limit input/stylesheet size and test CSS resource loading while remote images are disabled.

**Regression.** Head-defined classes, dark text on a light body and simple responsive email layouts should survive, while scripts, `@import` requests and image tracking remain blocked under the no-remote-resources mode.


<a id="data-01"></a>

### DATA-01 â€” Push notification selection references a nonexistent SQL column

**Priority:** Medium  
**Evidence:** Confirmed static defect against the actual schema  
**Pinned source:** [`push.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/push.go); [`mail-engine/schema.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/schema.go); [`push_test.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/push_test.go)

**Evidence and impact.** `sendPushForUser` compares `p.message_id=m.message_id`, but `mail_messages` defines `id`, not `message_id`. The candidate query therefore errors before dispatch; the error path simply returns. This explains how push can remain completely nonfunctional while its mock-database tests pass. No real PostgreSQL execution was available in this audit; the schema/query mismatch itself is unambiguous.

**Implementation â€” exact one-token SQL correction and error handling.**

```diff
- p.message_id=m.message_id
+ p.message_id=m.id
```
```go
// After the candidate QueryRowContext(...).Scan(...):
if errors.Is(err, sql.ErrNoRows) { return }
if err != nil {
    a.log.Error("select push candidate", "err", err)
    return
}
```

Add `errors`/`database/sql` imports if absent. Extract the SQL into a named constant used by production and a real-PostgreSQL integration test. Tests that return invented rows for any query cannot catch missing columns, types, operators or constraints.

**Regression.** Migrate both schemas in a scratch database, seed one user/account/unread Imbox message/subscription, execute the production query and assert it finds the message. Add already-delivered, active-lease, failed-device retry and legacy-receipt cases. Never point schema-dropping tests at a real mailbox database.


<a id="data-02"></a>

### DATA-02 â€” Sender decisions and classification can race into hidden Screener mail

**Priority:** Medium  
**Evidence:** Confirmed concurrency defect  
**Pinned source:** [`classify.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/classify.go); [`schema.sql`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/schema.sql)

**Evidence and interleaving.** Classification and sender-decision updates run in separate transactions without serializing on the user. Classification can insert an uncommitted Screener row while a decision update sees no such row; classification's own â€œself-healâ€ query can simultaneously miss the uncommitted decision. Both commit and leave the message in Screener despite a decided sender. The sender list excludes decided senders, making those messages hard to reach. A later pass with no new classification batch returns before repair. Screening preference changes also update the setting and existing rows separately.

**Implementation.** Serialize classification, decide/undecide and screening preference changes on one owner row, with all relevant reads and writes inside the serialized transaction. This reusable wrapper provides the transaction boundary:

```go
// Imports context, database/sql.
func withOwnerMutation(ctx context.Context, db *sql.DB, uid string,
    apply func(*sql.Tx) error) error {
    tx, err := db.BeginTx(ctx, nil)
    if err != nil { return err }
    defer tx.Rollback()
    var locked string
    if err := tx.QueryRowContext(ctx, `SELECT id::text FROM users WHERE id=$1 FOR UPDATE`, uid).
        Scan(&locked); err != nil { return err }
    if err := apply(tx); err != nil { return err }
    return tx.Commit()
}
```

Do provider I/O before acquiring this lock. Re-read the current sender rules/preferences after locking, and execute the batch classification and reroute SQL through `tx`, not `a.db`. Run repair even when there are no new rows. Record filing provenance (`automatic`, `manual`, rule revision) instead of inferring manual changes from whether a bucket happens to equal the rule's route; otherwise undo can undo a manual move indistinguishable from an automatic one.

**Regression.** Use two real transactions and synchronization barriers to reproduce both commit orders. Every decided sender's automatically classified messages must converge to the decision's bucket, including when no new mail arrives. Toggle screening concurrently with classification and verify there is no stranded batch.


<a id="data-03"></a>

### DATA-03 â€” Correspondent trust is inferred from an untrusted From header

**Priority:** Medium  
**Evidence:** Confirmed trust-heuristic weakness, not an authentication bypass  
**Pinned source:** [`classify.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/classify.go); [`mail-engine/schema.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/schema.go)

**Evidence and impact.** Correspondent collection treats a message whose From address equals the owner's address as evidence the owner wrote to its To/Cc recipients. From headers in incoming mail are not proof of an actual owner submission. A forged self-sender message can therefore seed a misleading correspondent and affect Screener routing. Thread-header linkage is likewise not cryptographic evidence. This affects inbox organization; it does not authenticate an attacker to Lullmail.

**Implementation.** Prefer recipients of **accepted outbound jobs** recorded by SEND-03. For historical imports, require genuine Sent-folder membership in addition to an owned From address, and treat this as a heuristic rather than strong identity verification. Add the following membership condition to the existing outbound-correspondent selection:

```sql
AND EXISTS (
  SELECT 1
  FROM mail_message_mailboxes mm
  JOIN mail_mailboxes mb
    ON mb.account_id=mm.account_id AND mb.id=mm.mailbox_id
  WHERE mm.account_id=m.account_id AND mm.message_id=m.id
    AND mb.role='sent'
)
```

Do not automatically trust client-supplied `Authentication-Results` headers either; an application would need to know which provider-added result is authoritative. Keep explicit owner decisions separate from inferred correspondents so changing a heuristic can be repaired without losing manual choices.

**Regression.** An incoming message forged as From=self must not grant correspondent status to an arbitrary To/Cc address. A successfully submitted outgoing message should. Exercise aliases and localized provider Sent folders.


<a id="data-04"></a>

### DATA-04 â€” Undated messages can be silently excluded, including valid space-padded IMAP dates

**Priority:** Medium  
**Evidence:** Confirmed SQL/parser defects; date parsing reproduced locally  
**Pinned source:** [`classify.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/classify.go); [`mail-engine/imap/adapter.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/imap/adapter.go)

**Evidence and impact.** The backfill predicate excludes NULL `received_at` values despite code comments intending unknown dates to remain visible. IMAP INTERNALDATE parsing uses a zero-padded day format, which rejects a legal space-padded single-digit day such as ` 1-Jan-2026`. This turns a valid provider date into a zero value and can then keep the message out of product classification.

**Implementation.** Accept the space-padded day format and make unknown-date policy explicit in SQL.

```diff
- time.Parse("02-Jan-2006 15:04:05 -0700", internal.text)
+ time.Parse("_2-Jan-2006 15:04:05 -0700", internal.text)
```
```sql
-- Include this branch in the existing backfill-window predicate:
(backfill_days = 0 OR m.received_at IS NULL
 OR m.received_at > /* the existing correctly bound UTC cutoff */ $2)
```

Integrate the NULL branch without changing the existing query's placeholder numbering or user/account ownership clauses. Either include unknown-date messages with a visible unknown-date marker or place them in an explicit review bucket; silently hiding them is the failure. Log a bounded parse diagnostic rather than silently assuming malformed provider dates are normal.

**Regression.** Test `01-Jan`, ` 1-Jan` and invalid dates, and classify an envelope with NULL received time under a finite backfill window. The local Go probe confirmed `_2-Jan` accepts the space-padded example rejected by `02-Jan`.


<a id="sync-01"></a>

### SYNC-01 â€” Partial mailbox sync failures are returned as overall success

**Priority:** Medium  
**Evidence:** Confirmed error-propagation defect  
**Pinned source:** [`mail-engine/sync.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/sync.go); [`accounts.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/accounts.go)

**Evidence and impact.** `SyncAccount` logs ordinary folder errors and continues, but returns a nil overall error. The product completion path then updates last-success status and clears the account error. A database failure, network failure or cancellation in one or more folders can therefore look like a healthy completed sync. Continuing independent folders is reasonable; suppressing the final partial-failure status is not.

**Implementation.** Accumulate nonfatal folder failures while preserving successful reports, and return `errors.Join` at the end. Keep the existing special handling for reauthentication/rate limits; include context cancellation instead of continuing a dead context.

```go
// Inside SyncAccount, alongside its existing reports collection:
var folderErrors []error

// Replace the ordinary log-and-continue error branch:
if err != nil {
    folderErrors = append(folderErrors, fmt.Errorf("mailbox %s: %w", box.ID, err))
    if ctx.Err() != nil { break }
    continue
}

// At the end, return successful reports plus the aggregate:
return reports, errors.Join(folderErrors...)
```

Adapt the local loop variable names to the existing function and add the `errors` import. In `finishSync`, distinguish `last_attempt_at` from `last_success_at`; do not erase `last_error` for a partial result. Successful folders can still be classified, but destructive orphan cleanup must wait for authoritative completed reconciliation (SYNC-03).

**Regression.** One of two folders fails: the result includes the successful folder and a non-nil error identifying the failed one. An all-failure or cancelled run must not update the last-success timestamp.


<a id="sync-02"></a>

### SYNC-02 â€” Body prefetch ignores account-wide authentication and throttling failures

**Priority:** Medium  
**Evidence:** Confirmed retry/backpressure defect  
**Pinned source:** [`mail-engine/sync.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/sync.go)

**Evidence and impact.** Prefetch logs body-fetch errors and continues across the batch. That includes errors which should stop account work, such as reauthentication, rate limiting and cancellation. A provider already throttling can receive many more requests, while the account is not correctly marked as needing reauthentication for this path.

**Implementation.** Preserve best-effort behavior only for message-local failures. Route account-wide failures through the same classification used by envelope sync.

```go
func stopPrefetch(err error) bool {
    return errors.Is(err, ErrReauthRequired) ||
        errors.Is(err, ErrRateLimited) ||
        errors.Is(err, context.Canceled) ||
        errors.Is(err, context.DeadlineExceeded)
}
// In the body loop: if stopPrefetch(err) { return report, err }
```

Use the package's existing report return shape. Persist the reauthentication state and apply scheduler backoff/Retry-After for throttling. A corrupt or vanished single message should be recorded as a per-message failure and not abort unrelated folders unnecessarily.

**Regression.** A body fetch returns 401-equivalent or 429-equivalent on the first item; assert no remaining body requests are made and the account status/backoff changes. A single not-found message should not freeze healthy subsequent sync work.


<a id="sync-03"></a>

### SYNC-03 â€” Destructive cursor reset can erase local filing state before a rebuild succeeds

**Priority:** High  
**Evidence:** Confirmed destructive ordering with a reachable failure chain  
**Pinned source:** [`mail-engine/sync.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/sync.go); [`mail-engine/store.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/store.go); [`mail.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail.go); [`accounts.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/accounts.go); [`classify.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/classify.go)

**Evidence and impact.** Resetting an invalid cursor drops local mailbox state before a replacement enumeration is known to succeed. If rebuilding then fails, missing mirror rows can be treated as genuine deletions by product orphan cleanup, erasing user-authored bucket/read/snooze state. SYNC-01 makes the chain especially reachable by presenting partial failure as success. A rebuildable provider mirror is not the same thing as rebuildable user filing decisions.

**Implementation.** Rebuild non-destructively and reconcile only after a complete authoritative scan. Persist scan identity/seen IDs across page budgets and restarts instead of retaining them only in one function call.

```sql
-- Mail-engine migration, not a product-layer alteration of engine tables.
CREATE TABLE mail_scan_runs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id text NOT NULL,
  mailbox_id text NOT NULL,
  baseline_cursor text NOT NULL DEFAULT '',
  next_cursor text NOT NULL DEFAULT '',
  started_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (account_id, mailbox_id)
);
CREATE TABLE mail_scan_seen (
  run_id uuid NOT NULL REFERENCES mail_scan_runs(id) ON DELETE CASCADE,
  message_id text NOT NULL,
  PRIMARY KEY (run_id, message_id)
);

-- Insert each successfully staged ID in the same transaction as page progress.
INSERT INTO mail_scan_seen(run_id,message_id) VALUES ($1,$2)
ON CONFLICT DO NOTHING;

-- Only at authoritative completion, under the account maintenance lock:
DELETE FROM mail_message_mailboxes mm
WHERE mm.account_id=$1 AND mm.mailbox_id=$2
  AND NOT EXISTS (
    SELECT 1 FROM mail_scan_seen ss
    WHERE ss.run_id=$3 AND ss.message_id=mm.message_id
  );
```

Stage new envelopes/cursor progress transactionally; retain the previous visible generation until completion. Then prune orphan mirror rows and advance the final cursor in one transaction. Preserve product filing keyed by stable identity throughout. A failed or timed-out page must not trigger the deletion statement. During UIDVALIDITY changes, mark old positional identities stale and avoid using their obsolete UIDs for provider writes while still allowing already cached content to be read.

**Regression.** Seed manual filing state, return cursor-invalid, fail the replacement page, and run ordinary background cleanup: the filing state remains. Restart or stop after a page budget; resume the same scan and reconcile only after the last page. Test labels shared by multiple folders so losing one membership does not delete a message still held elsewhere.


<a id="sync-04"></a>

### SYNC-04 â€” Retention and mirror writes can race, leaving expired or orphaned data

**Priority:** Medium  
**Evidence:** Confirmed integrity gap under concurrent operations  
**Pinned source:** [`accounts.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/accounts.go); [`mail-engine/store.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/store.go); [`mail-engine/schema.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/schema.go); [`app.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/app.go)

**Evidence and impact.** Retention deletes mirror rows while independent sync/body writes can run; account-use locks chiefly protect account deletion, not retention against all writers. The mirror has no foreign keys tying bodies and memberships to their messages. A body fetched before expiration can be inserted after its parent was removed. A later sync can also reinsert expired metadata unless retention is enforced at ingestion.

**Implementation.** Serialize retention/cutover against account writes and add engine-owned referential constraints after cleaning existing orphan rows. Do not add cascading mirror foreign keys to user-authored product filing indiscriminately, because that would worsen SYNC-03.

```sql
-- Run an orphan audit/cleanup first; deploy as a versioned engine migration.
ALTER TABLE mail_bodies ADD CONSTRAINT mail_bodies_message_fk
  FOREIGN KEY (account_id,message_id)
  REFERENCES mail_messages(account_id,id) ON DELETE CASCADE NOT VALID;
ALTER TABLE mail_message_mailboxes ADD CONSTRAINT mail_membership_message_fk
  FOREIGN KEY (account_id,message_id)
  REFERENCES mail_messages(account_id,id) ON DELETE CASCADE NOT VALID;
ALTER TABLE mail_bodies VALIDATE CONSTRAINT mail_bodies_message_fk;
ALTER TABLE mail_message_mailboxes VALIDATE CONSTRAINT mail_membership_message_fk;
```

A `NOT VALID` foreign key still checks new writes; arrange the rollout accordingly. Take an account-level database advisory/row lock in sync writeback and retention using one documented lock order. Check the effective retention cutoff before storing envelopes and bodies; a body whose parent has expired must not be reinserted. Prefer a set-based expired-ID snapshot rather than repeatedly computing slightly different deletion sets.

**Regression.** Pause a body fetch before writeback, expire/delete its message, then resume: no orphan body appears. Race retention with sync and verify the final database satisfies the retention policy and foreign keys.


<a id="sync-05"></a>

### SYNC-05 â€” Increasing retention does not restore older messages removed from the mirror

**Priority:** Medium  
**Evidence:** Confirmed restoration gap for incremental providers  
**Pinned source:** [`accounts.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/accounts.go); [`mail-engine/sync.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/sync.go)

**Evidence and impact.** After local retention removes older unchanged messages, increasing/disabling retention does not by itself make an incremental provider report them again. Leaving the existing delta cursor in place can therefore make a larger retention window appear ineffective indefinitely.

**Implementation.** Record a durable reconciliation request when the retention window grows or becomes unlimited. Use SYNC-03's non-destructive full scan rather than deleting the mirror again.

```sql
ALTER TABLE email_accounts ADD COLUMN reconcile_requested boolean NOT NULL DEFAULT false;

UPDATE email_accounts
SET reconcile_requested = reconcile_requested OR
      ($2 = 0 AND retention_days <> 0) OR
      ($2 > retention_days AND retention_days <> 0),
    retention_days = $2
WHERE id = $1 AND user_id = $3;
```

The scheduler must consume `reconcile_requested` only after a successful complete scan under the new policy. Display â€œrebuilding retained historyâ€ until then. Coordinate this with `backfill_days`, which is a separate product-classification window: restoring the mirror should not automatically override manual filing or silently expand a deliberately smaller organizational window.

**Regression.** Retain seven days, sync, expire an unchanged month-old message, then change to 90 days and to unlimited. It returns without requiring a provider-side modification. An interrupted rebuild keeps the request pending.


<a id="data-05"></a>

### DATA-05 â€” Thread reads can trigger unbounded provider work

**Priority:** Medium  
**Evidence:** Confirmed resource-boundary omission  
**Pinned source:** [`classify.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/classify.go); [`mail-engine/sync.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/sync.go); [`mail-engine/imap/adapter.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/imap/adapter.go)

**Evidence and impact.** The thread handler enumerates a thread and fetches missing bodies across its messages without a meaningful count/time budget, despite a comment describing bounded work. On IMAP, a body/attachment read may retrieve and parse the entire message. A very long thread or large mail can therefore hold request/account resources and consume substantial memory. Mail content and thread headers can be sender-influenced; authenticated read access is still required to trigger the handler.

**Implementation.** Bound eager body fetching, return explicit `body_state` values, and fetch remaining bodies lazily through an owner-authorized per-message endpoint. This helper is a concrete budgeting primitive to wrap the current loop:

```go
func prefetchThread(ctx context.Context, ids []string,
    fetch func(context.Context, string) error) map[string]error {
    ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
    defer cancel()
    const eagerLimit = 8
    outcomes := make(map[string]error)
    for i, id := range ids {
        if i >= eagerLimit || ctx.Err() != nil { break }
        outcomes[id] = fetch(ctx, id)
    }
    return outcomes
}
```

Apply byte limits before raw MIME parsing, not only after `io.ReadAll` has allocated the data. Have adapters stream large attachments and cap eager preview/body extraction independently from explicit downloads. Exports need their own disk/size/concurrency budget rather than sharing a small interactive-body limit.

**Regression.** Open a thread with thousands of uncached messages and a very large attachment. Interactive completion respects the budget, omitted bodies are marked deferred rather than empty/successful, and explicit downloads still work within their documented limits.


<a id="data-06"></a>

### DATA-06 â€” Bucket/search result limits have no continuation contract

**Priority:** Medium  
**Evidence:** Confirmed completeness/usability limitation  
**Pinned source:** [`classify.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/classify.go); [`dashboard/src/app/lib/api.ts`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/lib/api.ts); [`mcp/tools.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mcp/tools.go)

**Evidence and impact.** Bucket and search handlers cap results but expose no pagination/continuation mechanism. Older matching work is not reachable by traversing the list, and consumers cannot distinguish a complete result from a truncated one. Some ordering also lacks a stable identity tie-breaker.

**Implementation.** Fetch `limit+1`, return `has_more`/`next_cursor`, and use keyset pagination on the **outer, per-thread result**, preserving the existing owner/account/filter clauses. The cursor must include date, account and thread identity, including an explicit unknown-date representation.

```go
type Page[T any] struct {
    Items []T `json:"items"`
    NextCursor string `json:"next_cursor,omitempty"`
    HasMore bool `json:"has_more"`
}
type ThreadCursor struct {
    ReceivedAt *time.Time `json:"received_at"`
    Account string `json:"account"`
    Thread string `json:"thread"`
}
```
```sql
-- Add after selecting the latest eligible row per thread, not before grouping:
WHERE (COALESCE(received_at, '-infinity'::timestamp), account_id, thread_id)
    < (COALESCE($cursor_time::timestamp, '-infinity'::timestamp), $cursor_account, $cursor_thread)
ORDER BY COALESCE(received_at, '-infinity'::timestamp) DESC, account_id DESC, thread_id DESC
LIMIT $page_size_plus_one;
```

The `$cursor_*` names above describe separately bound parameters; convert to the existing PostgreSQL `$n` indices during integration. Validate/decode a size-bounded cursor instead of concatenating it into SQL. Preserve old clients with a versioned endpoint or deliberate contract migration. Add load-more support to dashboard and MCP tools.

**Regression.** More than 200 bucket threads and more than 60 search matches remain traversable without duplicates, including equal timestamps, multiple accounts and NULL dates. Distinguish snapshot consistency from live updates when new mail arrives during pagination.


<a id="data-07"></a>

### DATA-07 â€” Relative snooze durations make offline replay and undo change the intended date

**Priority:** Medium  
**Evidence:** Confirmed semantic/idempotency defect  
**Pinned source:** [`classify.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/classify.go); [`dashboard/src/app/lib/actions.ts`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/lib/actions.ts); [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/dashboard/src/app/lib/offline.ts); [`mcp/tools.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mcp/tools.go)

**Evidence and impact.** Snoozes are expressed as days relative to the server's time of application. Offline replay applies them later. Undo reconstructs a prior snooze through rounded remaining days rather than restoring the exact prior timestamp. These operations can move the promised return time instead of reproducing the user's original action.

**Implementation.** Accept an absolute UTC timestamp, with explicit null for an undated set-aside, and preserve it unchanged through replay/undo. Keep the old relative field only as a compatibility input converted once at request creation.

```go
// Imports encoding/json, fmt, time.
func parseSnoozeAt(raw json.RawMessage, now time.Time) (*time.Time, error) {
    if string(raw) == "null" { return nil, nil }
    var at time.Time
    if err := json.Unmarshal(raw, &at); err != nil { return nil, err }
    at = at.UTC()
    if at.IsZero() || at.After(now.AddDate(10, 0, 0)) {
        return nil, fmt.Errorf("invalid snooze timestamp")
    }
    return &at, nil
}
```

Distinguish missing `until_at` from explicit JSON null by using `json.RawMessage`. Parameterize the timestamp update directly. Store the previous exact bucket, read state and snooze timestamp for undo; do not recalculate a duration. A timestamp already passed at replay should result in an immediately due snooze, not a fresh multi-day deferral. Combine with mutation idempotency and optional row-version checks so an old offline edit does not silently overwrite a newer deliberate change.

**Regression.** Queue a three-day snooze, replay it two days later, and verify its original date. Undo after fractional days and across daylight-saving transitions must restore the exact original instant.


<a id="export-01"></a>

### EXPORT-01 â€” Account export aborts on credential failure instead of using its promised mirror fallback

**Priority:** Medium  
**Evidence:** Confirmed fallback-order defect  
**Pinned source:** [`export.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/export.go); [`oauth.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/oauth.go); [`app.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/app.go)

**Evidence and impact.** Account ZIP export returns an error immediately when `a.Token` fails, before it reaches the provider-unavailable fallback. OAuth refresh can require network access; an expired token while offline can therefore prevent exporting the local mirror. Single-message EML export already treats credential failure more gracefully. The lossiness of a mirror-only export is documented and is not itself the bug.

**Implementation.** Treat token/decryption/refresh unavailability as a provider-source failure once the owner and local export plan are safely resolved. Reuse the same fallback path as resolver failure.

```go
cred, providerErr := a.Token(r.Context(), nmail.AccountID(mirrorID))
var adapter nmail.Adapter
var release func()
if providerErr == nil {
    adapter, release, providerErr = newResolver()(r.Context(), nmail.AccountID(mirrorID), cred)
}
if release != nil { defer release() }
// Build the archive even when providerErr != nil, using mirrorEML and a
// manifest warning. Database/plan/write failures must still abort export.
```

Do not include raw tokens or credentials in the warning; use a safe reason code and separately logged diagnostics. Keep `X-Lullmail-Export-Source`/manifest warnings truthful, especially for missing original headers and attachment bytes.

**Regression.** Expired OAuth token plus unreachable token endpoint, revoked grant and unreadable provider credential each still permit a clearly marked local-mirror export. Failure to read the local database or finish writing the ZIP must remain a clean error, not a claimed successful backup.


<a id="export-02"></a>

### EXPORT-02 â€” Export filename disambiguation can create duplicate ZIP entries

**Priority:** Medium  
**Evidence:** Confirmed algorithm defect; reproduced locally  
**Pinned source:** [`export.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/export.go)

**Evidence and impact.** `exportPlan` counts collisions by the sanitized base name, not by the final filename. Bases `a`, `a`, `a-2` produce `a.mbox`, `a-2.mbox`, `a-2.mbox`. Distinct provider folder names can sanitize to the same base. Extraction tools may overwrite one file, turning an apparently complete backup into missing mailbox data.

**Implementation â€” replace the filename-allocation loop.** Track actual allocated filenames, case-insensitively for portability, and keep trying until one is free. The input `base` must still come from `safeExportName`.

```go
func uniqueMboxName(base string, used map[string]bool) string {
    for suffix := 1; ; suffix++ {
        name := base + ".mbox"
        if suffix > 1 { name = fmt.Sprintf("%s-%d.mbox", base, suffix) }
        key := strings.ToLower(name)
        if !used[key] {
            used[key] = true
            return name
        }
    }
}
```

Create `used := map[string]bool{}` once per export and call this function for every mailbox. Consider adding a stable short mailbox-ID suffix for Windows-reserved basenames and Unicode-normalization collisions, and retain the exact original folder name in the manifest.

**Regression.** Cover `a/a/a-2`, sanitization collisions, case-only differences and already-suffixed names. Open the produced ZIP and assert every filename is unique. The local probe changed the third example to `a-2-2.mbox` with the fixed allocator.


<a id="imap-01"></a>

### IMAP-01 â€” CONDSTORE-only sync never reconciles expunged messages

**Priority:** Medium  
**Evidence:** Confirmed protocol/state defect  
**Pinned source:** [`mail-engine/imap/adapter.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/imap/adapter.go); [`mail-engine/imap/conn.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/imap/conn.go)

**Evidence and impact.** A previous modification sequence selects incremental sync whenever CONDSTORE is available. When QRESYNC is absent, that path does not perform the UID-set reconciliation described by its comments, so expunged messages can remain indefinitely in the mirror. CONDSTORE flags/metadata deltas alone are not an expunge feed. See [RFC 7162](https://www.rfc-editor.org/rfc/rfc7162.html).

**Implementation.** For a CONDSTORE-only server, pair CHANGEDSINCE with a complete `UID SEARCH ALL` set and remove stored membership for absent UIDs, using the same selected mailbox/UIDVALIDITY. A simple correctness-first fallback is to use the existing full scan unless both capabilities are actually usable:

```go
// At the incremental-vs-full decision in Adapter.Sync:
canUseVanished := a.conn.Supports("QRESYNC") && a.conn.Supports("CONDSTORE")
if prev.ModSeq > 0 && canUseVanished {
    // retain the existing CHANGEDSINCE/VANISHED incremental branch
} else {
    // retain the existing complete full-scan branch and reconciliation flags
}
```

The local variable/connection field names must match the current adapter. This conservative fallback may do extra work and should be replaced with UID reconciliation for large mailboxes. The current connection code **does issue ENABLE QRESYNC** in `Select`; this report is not claiming that command is absent. Track actual enablement success rather than assuming advertising a capability alone guarantees its use. QRESYNC-only advertisement can be optimized according to the protocol once tested.

**Regression.** A fake server advertises CONDSTORE but not QRESYNC, then expunges one message. The next sync removes the correct local membership. Include ordinary flag changes and a UIDVALIDITY reset.


<a id="imap-02"></a>

### IMAP-02 â€” IMAP greeting, cancellation and logout can block past the caller lifetime

**Priority:** High  
**Evidence:** Confirmed deadline/cancellation omission  
**Pinned source:** [`mail-engine/imap/conn.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/imap/conn.go); [`app.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/app.go); [`accounts.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/accounts.go)

**Evidence and impact.** The TLS dial uses `tls.DialWithDialer` rather than caller-context dialing. Greeting reads happen before an I/O deadline is established. Commands use a deadline only when the context already has one, and cancellation without a deadline does not actively interrupt network I/O. `Close` attempts LOGOUT with `context.Background()` after deadlines have been reset. A stalled server can therefore hold goroutines and account-use locks, including during cleanup/deletion.

**Implementation.** Use `tls.Dialer.DialContext`, set a bounded setup deadline through greeting/authentication, and wrap each command/literal exchange in a default per-operation timeout that also closes the socket on cancellation. Add `ioTimeout time.Duration` to `Conn`, initialize it from `Config.Timeout`, and use this wrapper around the existing command body:

```go
func (c *Conn) boundedIO(ctx context.Context, operation func(context.Context) error) error {
    timeout := c.ioTimeout
    if timeout <= 0 { timeout = 30*time.Second }
    ctx, cancel := context.WithTimeout(ctx, timeout)
    deadline, _ := ctx.Deadline()
    if err := c.raw.SetDeadline(deadline); err != nil { cancel(); return err }
    stop := context.AfterFunc(ctx, func() { _ = c.raw.Close() })
    defer func() {
        stop()
        cancel()
        _ = c.raw.SetDeadline(time.Time{})
    }()
    return operation(ctx)
}

// Cleanup must always be bounded. Closing without LOGOUT is safe for this
// cleanup contract; optionally attempt LOGOUT with a separate short timeout.
func (c *Conn) Close() error {
    if c.raw == nil { return nil }
    return c.raw.Close()
}
```

Use the wrapper for greeting, commands and APPEND; do not nest it in a way that resets an outer deadline prematurely. An interrupted IMAP command leaves the connection unusable; do not return it to any connection pool. For a configurable non-993 connection, explicitly model implicit TLS versus STARTTLS rather than treating every secure port as implicit TLS.

**Regression.** Stall before the greeting, mid-literal, before command completion and during LOGOUT. Cancel a context with no preexisting deadline. Each operation and account deletion must finish within a deterministic bound without leaked goroutines.


<a id="imap-03"></a>

### IMAP-03 â€” IMAP mutations lack a reliable selected-mailbox contract and capability/input checks

**Priority:** Medium  
**Evidence:** Confirmed library/raw-API correctness gaps  
**Pinned source:** [`mail-engine/sync.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/sync.go); [`mail-engine/service.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/service.go); [`mail-engine/imap/adapter.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/imap/adapter.go)

**Evidence and impact.** `Engine.Apply` resolves a fresh adapter without the body path's mailbox-location step. IMAP mutations need a selected mailbox; a fresh adapter can fail immediately. Within an already-used adapter, resolving IDs can switch selections after the initial writable SELECT, so a batch spanning mailboxes has no safe single UID namespace. Delete uses UID EXPUNGE without first proving UIDPLUS support. Arbitrary custom keyword text is interpolated into the protocol command rather than being validated as an atom. These concerns are in the reusable engine/owner raw surface; the product's local bucket actions are not evidence that an unauthenticated sender can invoke them.

**Implementation.** Make source mailbox explicit for IMAP mutation batches. Resolve and verify all IDs against that mailbox/UIDVALIDITY, then perform writable SELECT **after** resolution and before STORE/MOVE. Split multi-mailbox operations into independently reported source-scoped operations. Fail unsupported capability checks before changing any flags. Never substitute global EXPUNGE, which could delete unrelated messages.

```go
// Before the first mutating command:
if op.Kind == mail.OpDelete && !a.conn.Supports("UIDPLUS") {
    return fmt.Errorf("imap: targeted deletion requires UIDPLUS")
}

// Replace raw custom-keyword interpolation with a conservative atom validator.
func keywordAtom(s string) (string, error) {
    if len(s) == 0 || len(s) > 64 { return "", fmt.Errorf("invalid IMAP keyword length") }
    for _, c := range []byte(s) {
        if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
            c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
            return "", fmt.Errorf("invalid IMAP keyword")
        }
    }
    return s, nil
}
```

Preserve the existing explicit mapping for standard system flags (`seen`, `flagged`, etc.); run only custom keywords through this validator. If broader legal atoms are needed, implement the full protocol grammar rather than permitting CR/LF or parentheses. Cache located UIDs by `(mailbox, UIDVALIDITY, messageID)` after a scan so repeated lazy reads do not repeatedly enumerate an entire mailbox, and invalidate that cache whenever UIDVALIDITY changes.

**Regression.** Apply a flag immediately after a fresh connection; mutate two messages in different folders; test a read-only prior selection; reject a CR/LF-bearing custom keyword; and verify a server without UIDPLUS receives no STORE/EXPUNGE from a rejected delete.


<a id="jmap-01"></a>

### JMAP-01 â€” JMAP Email/set treats per-message failures as successful mutations

**Priority:** Medium  
**Evidence:** Confirmed response-contract defect  
**Pinned source:** [`mail-engine/jmap/adapter.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/jmap/adapter.go)

**Evidence and impact.** `Apply` discards the `Email/set` payload and returns only the transport/method-level error. A successful method response can contain `notUpdated` or `notDestroyed`, so the application can claim success when some or all requested changes failed. Custom-keyword patch paths also need JSON Pointer escaping. See [JMAP core set responses](https://www.rfc-editor.org/rfc/rfc8620.html#section-5.3).

**Implementation.** Decode and verify each requested ID. This helper can be called after `a.call` returns the validated Email/set response; preserve successful IDs in a richer partial-result contract if callers need safe retries.

```go
type setResult struct {
    Updated map[string]json.RawMessage `json:"updated"`
    Destroyed []string `json:"destroyed"`
    NotUpdated map[string]json.RawMessage `json:"notUpdated"`
    NotDestroyed map[string]json.RawMessage `json:"notDestroyed"`
}
func checkSetResult(raw json.RawMessage, ids []string, destroy bool) error {
    var result setResult
    if err := json.Unmarshal(raw, &result); err != nil { return err }
    deleted := make(map[string]bool, len(result.Destroyed))
    for _, id := range result.Destroyed { deleted[id] = true }
    var failures []error
    for _, id := range ids {
        if destroy {
            if _, failed := result.NotDestroyed[id]; failed || !deleted[id] {
                failures = append(failures, fmt.Errorf("JMAP destroy failed for %q", id))
            }
        } else {
            _, updated := result.Updated[id]
            _, failed := result.NotUpdated[id]
            if failed || !updated {
                failures = append(failures, fmt.Errorf("JMAP update failed for %q", id))
            }
        }
    }
    return errors.Join(failures...)
}
func pointerToken(value string) string {
    return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}
```

Use `"keywords/" + pointerToken(jmapKeyword(op.Keyword))` when constructing a patch. Add the `errors` import. Map structured per-ID error types into retryable/permanent failures without exposing sensitive raw provider payloads.

**Regression.** Mixed `updated`/`notUpdated`, entirely failed destroy, a missing requested result and a keyword containing `/` or `~`. An HTTP 200 response alone must not cause the mutation to be reported complete.


<a id="jmap-02"></a>

### JMAP-02 â€” Malformed JMAP responses can panic background sync

**Priority:** Medium  
**Evidence:** Confirmed bounds/validation defect  
**Pinned source:** [`mail-engine/jmap/adapter.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/jmap/adapter.go); [`mail-engine/sync.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/sync.go)

**Evidence and impact.** `call` returns whatever number/order of method response payloads arrived, while callers index `res[0]` or `res[1]` without checking. A 200 response with an empty/missing/short `methodResponses` array can panic. In a background goroutine, an unrecovered panic can terminate the process. This needs a malformed upstream response or test/misconfigured provider, not merely a malicious message body.

**Implementation.** Validate response tuples and correlate them with request call IDs and method names before exposing the positional result array. This helper replaces the raw append-in-arrival-order logic:

```go
func checkedResponses(raw []byte, calls [][3]any) ([]json.RawMessage, error) {
    var wire struct { Responses [][]json.RawMessage `json:"methodResponses"` }
    if err := json.Unmarshal(raw, &wire); err != nil { return nil, err }
    indexes := map[string]int{}
    methods := make([]string, len(calls))
    for i, call := range calls {
        method, mok := call[0].(string)
        tag, tok := call[2].(string)
        if !mok || !tok || tag == "" { return nil, fmt.Errorf("invalid JMAP call") }
        if _, duplicate := indexes[tag]; duplicate { return nil, fmt.Errorf("duplicate JMAP call id") }
        indexes[tag], methods[i] = i, method
    }
    out := make([]json.RawMessage, len(calls))
    for _, tuple := range wire.Responses {
        if len(tuple) != 3 { return nil, fmt.Errorf("invalid JMAP response tuple") }
        var method, tag string
        if err := json.Unmarshal(tuple[0], &method); err != nil { return nil, err }
        if err := json.Unmarshal(tuple[2], &tag); err != nil { return nil, err }
        i, wanted := indexes[tag]
        if !wanted { continue }
        if method == "error" { return nil, fmt.Errorf("JMAP method %s failed", methods[i]) }
        if method != methods[i] { continue } // unrelated implicit response
        if out[i] != nil { return nil, fmt.Errorf("duplicate JMAP result") }
        out[i] = tuple[1]
    }
    for i := range out {
        if out[i] == nil { return nil, fmt.Errorf("missing JMAP result for %s", methods[i]) }
    }
    return out, nil
}
```

In production, retain the existing `methodError` mapping when `method == "error"` instead of flattening reauthentication/rate-limit/reset errors into a generic string. Read a size-bounded body before decoding (OPS-01), and validate required result fields such as nonempty state/IDs. Do not use panic recovery as the primary protocol parser.

**Regression.** Empty/missing arrays, one result for two calls, out-of-order IDs, duplicate results, error tuples and unrelated implicit responses produce controlled errors or correctly ordered results, never a panic.


<a id="jmap-03"></a>

### JMAP-03 â€” Initial JMAP pagination can advance past changes it never observed

**Priority:** High  
**Evidence:** Confirmed cursor-design defect  
**Pinned source:** [`mail-engine/jmap/adapter.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/jmap/adapter.go); [`mail-engine/gmail/adapter.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/gmail/adapter.go); [`mail-engine/sync.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/sync.go)

**Evidence and interleaving.** The initial cursor remembers only a position. The final page switches to the state from that page's Email/get. Modify or delete a message already fetched on an earlier page, then finish the scan: the final state can lie after that change even though the mirror contains the old object. Positional pagination can also shift when membership/order changes. The Gmail adapter's capture of a pre-enumeration history baseline is a useful existing counterexample.

**Implementation.** Capture an account Email state before enumeration, carry it through every cursor, track query consistency, and replay changes from that **baseline**, not the last page's get state. Persist reconciliation generation as in SYNC-03. Replace the numeric-only initial cursor with a versioned object:

```go
type initialCursor struct {
    Version int `json:"v"`
    Position int `json:"position"`
    Baseline string `json:"baseline"`
    QueryState string `json:"query_state"`
}
func encodeInitial(c initialCursor) (mail.Cursor, error) {
    if c.Version != 1 || c.Position < 0 || c.Baseline == "" {
        return "", fmt.Errorf("invalid initial JMAP cursor")
    }
    raw, err := json.Marshal(c)
    if err != nil { return "", err }
    return mail.Cursor("jmap-initial-v1:" + base64.RawURLEncoding.EncodeToString(raw)), nil
}
```

Obtain the baseline with `Email/get` for an empty ID list before the first query, validate its state, and reuse it unchanged. Compare the query state on later pages; if the listing changed in a way the client cannot reconcile, restart the staged enumeration without deleting the visible mirror. At completion set the incremental cursor to `Baseline`; then run Email/changes to catch up. Decode the versioned cursor with size/range checks, and reset old numeric cursors non-destructively.

**Regression.** More than one page, then update/delete an early-page message between requests. Insert an item that shifts query positions, crash/resume between pages, and expire the baseline. The final mirror must converge without skipping or prematurely deleting mail.


<a id="jmap-04"></a>

### JMAP-04 â€” JMAP truncated body values are cached as complete content

**Priority:** Medium  
**Evidence:** Confirmed response-field omission; requires a provider returning truncation flags  
**Pinned source:** [`mail-engine/jmap/adapter.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/jmap/adapter.go); [`mail-engine/store.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/store.go)

**Evidence and impact.** The body-value structure reads `value` but not `isTruncated`/`isEncodingProblem`. A provider response that explicitly reports truncation can be assembled and cached as if it were the complete message. Subsequent local reads then have no indication that content is missing.

**Implementation.** Decode completeness metadata and refuse to commit a partial body as complete. Fetch the original blob through the adapter's Raw/download path for a bounded MIME parse, or return a typed incomplete-body result the UI can explain.

```go
type bodyValue struct {
    Value string `json:"value"`
    IsTruncated bool `json:"isTruncated"`
    IsEncodingProblem bool `json:"isEncodingProblem"`
}
var errIncompleteBody = errors.New("provider returned incomplete body content")

func completeBodyValue(v bodyValue) (string, error) {
    if v.IsTruncated || v.IsEncodingProblem { return "", errIncompleteBody }
    return v.Value, nil
}
```

Apply this to each referenced text/html part, not just an arbitrary map entry. Do not convert this error into an empty successful body in the engine. A partial-preview cache needs a separate explicit status from a complete body cache.

**Regression.** A body value with `isTruncated=true` must trigger fallback or a visible incomplete state and must not permanently replace the full-body cache. Cover encoding problems and multiple text/html parts.


<a id="gmail-01"></a>

### GMAIL-01 â€” Gmail address and date parsing rejects valid mail headers

**Priority:** Medium  
**Evidence:** Confirmed parsing defects; reproduced locally  
**Pinned source:** [`mail-engine/gmail/adapter.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/gmail/adapter.go)

**Evidence and impact.** `parseAddrs` splits on commas, so a valid quoted display name such as `"Doe, Jane" <jane@example.invalid>` becomes two apparent addresses, one invalid. This can affect the first sender used by screening, display and replies. Date parsing accepts only `time.RFC1123Z`, rejecting valid mail dates with a named zone or other permitted forms. The local probes reproduced both failures.

**Implementation.** Use Go's mail parser and MIME word decoder rather than manually splitting headers. Keep parse failure visible in bounded diagnostics instead of inventing an invalid email address.

```go
// mail-engine/gmail/adapter.go; imports mime and stdmail "net/mail".
func parseAddrs(header string) []mail.Address {
    parser := stdmail.AddressParser{WordDecoder: &mime.WordDecoder{}}
    addresses, err := parser.ParseList(header)
    if err != nil { return nil }
    out := make([]mail.Address, 0, len(addresses))
    for _, address := range addresses {
        out = append(out, mail.Address{Name: address.Name, Email: address.Address})
    }
    return out
}
// In the Date header branch:
// if t, err := stdmail.ParseDate(h.Value); err == nil { env.SentAt = t }
```

Configure `WordDecoder.CharsetReader` for legacy encoded-word charsets where needed; ordinary UTF-8/ASCII should not depend on that extension. Do not let an absent/invalid sender crash downstream classification, and retain the raw header for diagnostics or export where appropriate.

**Regression.** Quoted commas, multiple recipients, encoded display names, groups supported by `net/mail`, malformed headers and valid dates using GMT or a one-digit day. Assert the same canonical email flows through screening and reply addressing.


<a id="gmail-02"></a>

### GMAIL-02 â€” Gmail body and attachment handling assumes only one legal data representation

**Priority:** Medium  
**Evidence:** Confirmed data-path defect  
**Pinned source:** [`mail-engine/gmail/adapter.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/gmail/adapter.go)

**Evidence and impact.** Text/html body collection reads only inline `Body.Data`; an attachment-backed text part with `AttachmentId` and no inline data becomes blank. Conversely, a named attachment can contain inline `Data` without an attachment ID, but the download path rejects it because it searches only for an attachment ID. Decode failures are also converted to empty text, and charset handling is absent. These are alternate representations the existing part model itself exposes.

**Implementation.** Centralize part-byte resolution and use it for both body collection and attachment downloads. Make body collection context-aware and propagate errors. Find the **part object**, not only its attachment ID.

```go
func decodeURLBytes(value string) ([]byte, error) {
    if len(value) > 90<<20 { return nil, fmt.Errorf("encoded part exceeds configured limit") }
    if strings.HasSuffix(value, "=") { return base64.URLEncoding.DecodeString(value) }
    return base64.RawURLEncoding.DecodeString(value)
}
func (a *Adapter) partBytes(ctx context.Context, messageID string, p *gmail.MessagePart) ([]byte, error) {
    if p == nil || p.Body == nil { return nil, fmt.Errorf("missing MIME part body") }
    if p.Body.Data != "" { return decodeURLBytes(p.Body.Data) }
    if p.Body.AttachmentId != "" {
        result, err := a.svc.Users.Messages.Attachments.
            Get(a.user, messageID, p.Body.AttachmentId).Context(ctx).Do()
        if err != nil { return nil, classify(err) }
        return decodeURLBytes(result.Data)
    }
    if p.Body.Size == 0 { return []byte{}, nil }
    return nil, fmt.Errorf("nonempty MIME part has no data source")
}
```

Choose configurable encoded/decoded limits appropriate to interactive bodies versus explicit downloads; the example cap is a safety bound, not a provider limit. Decode the part's declared charset before treating bytes as UTF-8, using a maintained MIME/charset decoder. Accept padded and unpadded base64url. Ensure a successful fallback body is cached only after all required parts are resolved.

**Regression.** Text via `AttachmentId`, a named file via inline `Data`, empty attachments, padded/unpadded base64url, invalid encoding and non-UTF-8 text. Attachment IDs and message IDs must remain owner-scoped.


<a id="gmail-03"></a>

### GMAIL-03 â€” Attachment flags are inferred from MIME structure excluded by metadata requests

**Priority:** Low  
**Evidence:** Confirmed request/response mismatch  
**Pinned source:** [`mail-engine/gmail/adapter.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/gmail/adapter.go)

**Evidence and impact.** Envelope requests use `format=metadata`, while `toEnvelope` determines attachment presence by walking the MIME parts. Metadata mode does not provide the complete MIME body structure required for that inference. Consequently attachment badges can falsely say there are no attachments. See Google's [Gmail format definition](https://developers.google.com/workspace/gmail/api/reference/rest/v1/Format).

**Implementation.** A correctness-first patch is to request the full format when attachment presence must be known:

```diff
- Format("metadata").
+ Format("full").
```

This costs additional bandwidth and can defeat lightweight envelope sync, so do not deploy it without body-size limits and measurement. A better longer-term model is a tri-state attachment property (`unknown`, `present`, `absent`) populated when MIME structure/body is actually fetched; update envelope metadata in the same transaction as `PutBody`. Render â€œunknownâ€ honestly rather than converting it to false. Reuse full responses for prefetch instead of downloading them twice.

**Regression.** Fixture metadata responses omit parts while the corresponding full response contains an attachment. The UI must not assert absence based on metadata-only data, and a later full/body fetch must update the indicator.


<a id="gmail-04"></a>

### GMAIL-04 â€” Initial Gmail listing omits the explicit Spam/Trash inclusion flag

**Priority:** Medium  
**Evidence:** Confirmed query omission; provider integration regression required  
**Pinned source:** [`mail-engine/gmail/adapter.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/gmail/adapter.go)

**Evidence and impact.** Mailbox discovery includes SPAM/TRASH, but initial `Messages.List` requests do not set `includeSpamTrash`. The API exposes that flag specifically to include those messages. The adapter should not assume a label filter removes the need for it. See the official [messages.list contract](https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.messages/list).

**Implementation.** Explicitly enable inclusion for the initial scan; label filtering continues to select the intended mailbox.

```go
call := a.svc.Users.Messages.List(a.user).
    LabelIds(string(box)).
    IncludeSpamTrash(true).
    MaxResults(500)
```

Keep pagination/history baseline behavior intact; it is already stronger here than in the JMAP initial cursor. Also add a completeness test for archived messages with **no ordinary labels**: a design that enumerates only label folders needs an account-wide/all-messages discovery path. If that fixture is not covered, implement a deliberate synthetic all-messages mailbox or account-level sync contract and ensure unlabeled messages remain exportable rather than assuming Gmail exposes an ordinary All Mail label through this list.

**Regression.** Start from an empty mirror with messages solely in Spam, solely in Trash and archived without labels. Verify each is either synchronized/exported according to explicit policy or reported as intentionally excluded; never silently claim a complete account mirror.


<a id="graph-01"></a>

### GRAPH-01 â€” Graph folder discovery misses nested folders and relies on a nonexistent v1.0 role field

**Priority:** Medium  
**Evidence:** Confirmed against code and current official API contract  
**Pinned source:** [`mail-engine/graph/adapter.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/graph/adapter.go); [`mail-engine/store.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/store.go)

**Evidence and impact.** `Mailboxes` only pages `/me/mailFolders`, which returns the root's direct children; it never traverses `childFolders`. It also reads `wellKnownName` as though it were a normal v1.0 property, while the documented resource properties do not include it. Default folders can thus lose their semantic roles. Because `PutMailboxes` treats discovery as authoritative, an incomplete listing can also cause existing nested-folder state to be pruned. References: [root-folder listing](https://learn.microsoft.com/en-us/graph/api/user-list-mailfolders?view=graph-rest-1.0), [mailFolder properties and well-known aliases](https://learn.microsoft.com/en-us/graph/api/resources/mailfolder?view=graph-rest-1.0).

**Implementation.** Traverse every child collection, including pagination, before returning an authoritative result. On any traversal failure, return an error rather than a partial successful folder list. Resolve role-to-ID separately through well-known folder aliases.

```go
// Queue-based traversal sketch to integrate into Mailboxes; page contains
// Value (id/displayName/parentFolderId/childFolderCount) and NextLink.
queue := []string{"/me/mailFolders?$top=200"}
visited := map[string]bool{}
for len(queue) > 0 {
    endpoint := queue[0]
    queue = queue[1:]
    for endpoint != "" {
        var page folderPage // define with the fields described above
        if err := a.get(ctx, endpoint, &page); err != nil { return nil, err }
        for _, folder := range page.Value {
            if visited[folder.ID] { continue }
            visited[folder.ID] = true
            // Append the mailbox, assigning role from the separately resolved ID map.
            if folder.ChildFolderCount > 0 {
                queue = append(queue, "/me/mailFolders/" + url.PathEscape(folder.ID) + "/childFolders?$top=200")
            }
        }
        endpoint = page.NextLink
    }
}
```

This is an integration sketch, not a standalone replacement: define `folderPage`, append the existing `mail.Mailbox` values, and build the role map by GETs for `inbox`, `sentitems`, `drafts`, `deleteditems`, `junkemail`, and `archive` using `$select=id`. A missing optional folder is not a transport failure; classify it explicitly. Validate nextLink origins and bound cycles/page counts. Choose an explicit policy for hidden/search folders.

**Regression.** Nested folders deeper than one level, child pagination, localized names and a failure halfway through traversal. A partial result must never prune folders. Well-known aliases must identify roles without matching localized display names.


<a id="graph-02"></a>

### GRAPH-02 â€” Graph message identity changes on folder moves because immutable IDs are not requested

**Priority:** Medium  
**Evidence:** Confirmed identity-contract mismatch  
**Pinned source:** [`mail-engine/graph/adapter.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/graph/adapter.go); [`oauth.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/oauth.go)

**Evidence and impact.** Graph requests do not request immutable IDs, yet provider-native message IDs are used as persistent mirror/product identities. Default Graph message IDs can change on moves. A message can therefore return as a different identity and lose local state tied to the old ID. Microsoft's [immutable-ID documentation](https://learn.microsoft.com/en-us/graph/outlook-immutable-id) requires the preference consistently and describes migration of existing IDs.

**Implementation.** Apply the preference to **every** Graph request: reads, delta, raw/attachment reads and mutations, including product OAuth sending/reply helpers. A transport wrapper avoids missing individual endpoints:

```go
type immutableGraphTransport struct { Base http.RoundTripper }
func (t immutableGraphTransport) RoundTrip(r *http.Request) (*http.Response, error) {
    copy := r.Clone(r.Context())
    copy.Header = r.Header.Clone()
    if copy.URL.Scheme != "https" || copy.URL.Hostname() != "graph.microsoft.com" {
        return nil, fmt.Errorf("unexpected Graph request origin")
    }
    copy.Header.Set("Prefer", `IdType="ImmutableId"`)
    base := t.Base
    if base == nil { base = http.DefaultTransport }
    return base.RoundTrip(copy)
}
```

Merge this preference with other required `Prefer` values rather than overwriting them if later features add any. Wrap the authenticated transport at a point that validates the destination **before** credentials are added. Support other legitimate Graph clouds only through explicit configured origins.

**Migration is mandatory.** Translate existing IDs using the provider's ID translation facility; stage an old-to-new mapping and migrate mirror memberships/bodies/product filing/push receipts transactionally, handling collisions. Do not simply switch the header and delete/reimport all local state. Keep stable thread/account identity and reconcile old delta state under the documented provider contract.

**Regression.** Move a message between folders in a real Graph test mailbox; it retains identity and manual filing. Test upgrade from an existing default-ID mirror and verify all associated rows survive.


<a id="graph-03"></a>

### GRAPH-03 â€” Graph attachment-list errors and pagination are silently lost in cached bodies

**Priority:** Medium  
**Evidence:** Confirmed incomplete-result handling defect  
**Pinned source:** [`mail-engine/graph/adapter.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/graph/adapter.go); [`mail-engine/store.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/store.go)

**Evidence and impact.** `Body` ignores errors fetching attachment metadata and returns a successful body anyway. It also reads only one attachment-list page. That incomplete `Parts` list can then be cached permanently, making attachments absent even after a transient provider failure has cleared.

**Implementation.** Follow `@odata.nextLink` and fail/mark incomplete on attachment-list errors before committing a full-body cache. Extend the attachment page structure with `NextLink` and move the existing part-conversion loop inside this pagination loop:

```go
for endpoint := attEndpoint; endpoint != ""; {
    var page attachmentPage // existing attachment fields plus NextLink
    if err := a.get(ctx, endpoint, &page); err != nil {
        return nil, fmt.Errorf("graph: attachment metadata incomplete: %w", err)
    }
    for _, at := range page.Value {
        disposition := "attachment"
        if at.IsInline { disposition = "inline" }
        body.Parts = append(body.Parts, mail.BodyPart{
            PartID: at.ID, Type: at.ContentType, Filename: at.Name,
            Size: at.Size, ContentID: at.ContentID, Disposition: disposition,
        })
    }
    endpoint = page.NextLink
}
```

Define the page type by lifting the current anonymous struct and adding `NextLink string` with JSON tag `@odata.nextLink`. If product UX needs text immediately despite an attachment failure, introduce an explicit partial-body status and retry the missing metadata; do not store it as a complete body. Apply destination/page-cycle limits to continuation URLs.

**Regression.** Fail attachment metadata once then retry successfully; attachments must eventually appear. Return two pages and ensure every part is available. A 401/429 follows SYNC-02 instead of becoming an empty successful attachment list.


<a id="graph-04"></a>

### GRAPH-04 â€” Adding or removing one Graph custom keyword overwrites unrelated categories

**Priority:** Medium  
**Evidence:** Confirmed destructive mutation defect  
**Pinned source:** [`mail-engine/graph/adapter.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/graph/adapter.go)

**Evidence and impact.** `keywordPatch` maps an arbitrary added keyword to `categories: [keyword]` and a removed keyword to `categories: []`. Both replace the entire category collection. Removing one category therefore removes every unrelated category, including categories set by another client.

**Implementation.** The safe minimal repair is to reject unsupported custom-keyword operations before performing any network request, while retaining the supported seen/flagged mappings:

```go
if op.Kind == mail.OpAddKeyword || op.Kind == mail.OpRemoveKeyword {
    switch strings.ToLower(op.Keyword) {
    case "seen", "flagged":
        // Existing supported mapping.
    default:
        return fmt.Errorf("graph: arbitrary keyword mutation is unsupported")
    }
}
```

To support categories fully, GET the existing set and version/ETag, compute a set union/difference for the requested category, then PATCH with `If-Match`; retry a bounded number of 412 conflicts after re-reading. Do not implement an unguarded read-modify-write, which would still overwrite concurrent external changes. Expose adapter capabilities so callers do not assume identical keyword support everywhere.

**Regression.** Start with categories A and B; remove A; B remains. Add C; A/B remain. Inject an external category update between read and PATCH and verify it is preserved or a conflict is reported.


<a id="ops-01"></a>

### OPS-01 â€” Request, provider-response and export resource limits are incomplete

**Priority:** Medium  
**Evidence:** Confirmed boundary omissions; impact depends on reachable input size/concurrency  
**Pinned source:** [`cmd_serve.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/cmd_serve.go); [`accounts.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/accounts.go); [`auth.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/auth.go); [`mail-engine/jmap/adapter.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/jmap/adapter.go); [`mail-engine/graph/adapter.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/graph/adapter.go); [`export.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/export.go); [`sendqueue.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/sendqueue.go)

**Evidence and impact.** The server principally sets a header-read timeout. Several JSON handlers lack body limits; provider JSON/MIME reads and export staging can allocate large responses; whole-account exports can consume substantial temporary disk; the in-memory send queue has no aggregate byte/job admission budget. Existing per-attachment/send limits are useful but do not cover simultaneous requests or a huge received message.

**Implementation.** Apply route-specific body bounds, a request-body read timeout and idle timeout, separate small interactive work from bounded exports, and limit upstream reads before allocation. Do not impose a blanket short write timeout on SSE/streaming downloads.

```go
func decodeOneJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, target any) error {
    r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
    decoder := json.NewDecoder(r.Body)
    decoder.DisallowUnknownFields()
    if err := decoder.Decode(target); err != nil { return err }
    var extra any
    if err := decoder.Decode(&extra); err != io.EOF {
        if err == nil { return fmt.Errorf("multiple JSON values are not allowed") }
        return err
    }
    return nil
}
func readBounded(reader io.Reader, maxBytes int64) ([]byte, error) {
    if maxBytes < 0 { return nil, fmt.Errorf("invalid byte limit") }
    data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
    if err != nil { return nil, err }
    if int64(len(data)) > maxBytes { return nil, fmt.Errorf("response exceeds byte limit") }
    return data, nil
}
```

For ordinary small product JSON use, for example, 64â€“256 KiB; preserve the send handler's deliberate larger bound and use the WebAuthn library's schema handling for its own responses. Translate `*http.MaxBytesError` to 413. Set `ReadTimeout`/`IdleTimeout` in `http.Server` based on the documented upload policy. For exports, reserve a semaphore/disk budget, use streaming MIME where possible, check context between messages, and clean stale temporary exports on startup. Admission failure should return 429/503 without dropping accepted jobs.

**Regression.** Oversized JSON, slow bodies, huge provider literals, many parallel sends/exports and a full temporary filesystem fail predictably without crashing the process or leaving apparently successful but truncated artifacts.


<a id="ops-02"></a>

### OPS-02 â€” Database connection budgeting and startup migrations are not production-safe by construction

**Priority:** Medium  
**Evidence:** Confirmed operational gaps  
**Pinned source:** [`mail.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail.go); [`schema.sql`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/schema.sql); [`mail-engine/schema.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/schema.go); [`mail-engine/store.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/store.go)

**Evidence and impact.** The product `database/sql` pool has no explicit maximum; the engine has a separate pool. Total database use is therefore not budgeted across components. Startup reapplies schema statements, including constraint/index changes, instead of a versioned, serialized migration transaction. Concurrent starts and interrupted DDL can lead to lock contention and partially upgraded state. The engine does use transactions in many store operations; this is not a blanket claim that persistence lacks transactions.

**Implementation.** Set and monitor a total connection budget, reserving capacity for the engine, migrations and delivery leases. Example product pool settings:

```go
db.SetMaxOpenConns(20)
db.SetMaxIdleConns(5)
db.SetConnMaxIdleTime(5*time.Minute)
db.SetConnMaxLifetime(30*time.Minute)
```

Use deployment-specific values, not these numbers blindly. Introduce a migration table and take a PostgreSQL advisory transaction lock before examining/applying unapplied versions:

```sql
BEGIN;
SELECT pg_advisory_xact_lock(1280658508);
CREATE TABLE IF NOT EXISTS product_schema_migrations (
  version bigint PRIMARY KEY,
  checksum text NOT NULL,
  applied_at timestamptz NOT NULL DEFAULT now()
);
-- Under this lock, apply only versions absent from the table, then record
-- their checked-in checksums in the same transaction.
COMMIT;
```

Implement the transaction in the migration runner, not as an uncoordinated list of separate `db.Exec` calls. Respect engine/product schema ownership. Operations that cannot run in a transaction, such as concurrent index creation, need explicit resumable phases and validation before recording completion. Remove unconditional drop/re-add DDL from every normal boot after a versioned migration has performed it.

**Regression.** Run two initializers concurrently, interrupt between migration steps and restart, migrate a real old-schema fixture, and fail on a changed checksum. Monitor both pools under provider/export load and verify neither starves the other.


<a id="ops-03"></a>

### OPS-03 â€” Creating a connected account spans separate engine/product transactions

**Priority:** Medium  
**Evidence:** Confirmed atomicity gap  
**Pinned source:** [`accounts.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/accounts.go); [`oauth.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/oauth.go); [`mail-engine/store.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/store.go)

**Evidence and impact.** Mirror-account creation and credential/product-account insertion are separate operations, with best-effort cleanup on later failure. Cancellation, a crash or failed cleanup can leave an orphan mirror account. OAuth connection follows the same pattern. Provider validation should happen before a short local commit, and all local ownership records should become visible atomically.

**Implementation.** Extend the engine API with a transaction-aware account insertion that uses the product transaction without transferring schema ownership to the product layer. A concrete `database/sql` bridge can live in the engine package:

```go
// mail-engine: imports context and database/sql.
func (s *PgStore) PutAccountSQLTx(ctx context.Context, tx *sql.Tx, account *Account) error {
    _, err := tx.ExecContext(ctx, `
        INSERT INTO mail_accounts(id,provider,email,name) VALUES ($1,$2,$3,$4)`,
        account.ID, account.Provider, account.Email, account.Name)
    return err
}
```

In both connection handlers, start `a.db.BeginTx`, call this method, execute the existing `INSERT INTO email_accounts` through the same `tx`, and commit. Roll back on **every** error; do not launch sync before commit succeeds. If keeping pgx throughout instead, expose a pgx unit-of-work API and execute both inserts through that one transaction. Do not acquire one transaction on each pool and call that atomic.

**Regression.** Inject credential-insert failure and cancellation immediately after mirror insertion: neither row persists. A duplicate address must leave the existing account intact and create no second mirror. A reconnect flow should preserve the existing mirror identity and filing state rather than forcing delete/recreate.


<a id="ops-04"></a>

### OPS-04 â€” Shutdown does not join all application-owned background work

**Priority:** Medium  
**Evidence:** Confirmed lifecycle gap  
**Pinned source:** [`cmd_serve.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/cmd_serve.go); [`mail.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail.go); [`accounts.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/accounts.go); [`sendqueue.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/sendqueue.go); [`app.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/app.go)

**Evidence and impact.** HTTP shutdown and database close are not a complete lifecycle boundary for asynchronous sends, initial sync and post-sync/classification work. Some work uses background contexts and is not joined. Closing database resources while those goroutines still run creates failed writeback, misleading status and nondeterministic shutdown. Persistent send state is still necessary; waiting longer alone does not make an in-memory queue durable.

**Implementation.** Track all owned work in a task group which stops admitting jobs before waiting. Replace untracked `go` launches with `Go`, propagate its context and handle admission rejection.

```go
type taskGroup struct {
    mu sync.Mutex
    wg sync.WaitGroup
    ctx context.Context
    cancel context.CancelFunc
    closed bool
}
func newTaskGroup(parent context.Context) *taskGroup {
    ctx, cancel := context.WithCancel(parent)
    return &taskGroup{ctx: ctx, cancel: cancel}
}
func (g *taskGroup) Go(work func(context.Context)) bool {
    g.mu.Lock()
    defer g.mu.Unlock()
    if g.closed { return false }
    g.wg.Add(1)
    go func() { defer g.wg.Done(); work(g.ctx) }()
    return true
}
func (g *taskGroup) Stop(ctx context.Context) error {
    g.mu.Lock()
    g.closed = true
    g.cancel()
    g.mu.Unlock()
    done := make(chan struct{})
    go func() { g.wg.Wait(); close(done) }()
    select {
    case <-done: return nil
    case <-ctx.Done(): return ctx.Err()
    }
}
```

Shut down request admission, stop schedulers/new claims, cancel/join tasks, then close pools. A drain timeout must be reported as such; durable jobs whose outcome is uncertain remain recoverable/unknown. All provider I/O must honor cancellation (IMAP-02).

**Deployment boundary.** Existing per-account mutexes, OAuth-refresh locks and maps are process-local. Do not silently support multiple replicas with them. Either enforce a single active process using a dedicated database advisory-lock connection, or make job claims, credential refresh, retention and lifecycle coordination database-backed. Document that the encryption key must be shared consistently across any supported replicas.

**Regression.** Shutdown while syncing, classifying, waiting in an undo window and transmitting. No task starts after stop, all bounded tasks finish/cancel before pool close, and restart reconciles outstanding job states.


<a id="ops-05"></a>

### OPS-05 â€” Default port publication and URL configuration permit unintended plaintext exposure

**Priority:** Medium  
**Evidence:** Deployment hardening; not an unauthenticated-login finding  
**Pinned source:** [`compose.yaml`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/compose.yaml); [`config.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/config.go); [`setup.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/setup.go); [`mcp/client.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mcp/client.go)

**Evidence and impact.** Compose publishes `8080:8080` on all host interfaces while the default access story is localhost HTTP. URL configuration and the MCP client accept remote HTTP origins without requiring an explicit insecure mode. A deployment can unintentionally expose sessions, agent tokens or mail over plaintext. Strong bootstrap tokens and existing authentication still matter; this is not a claim that the published port is automatically unauthenticated.

**Implementation.** Bind the default host publication to loopback and make internet exposure an explicit TLS-proxy configuration:

```yaml
ports:
  - "127.0.0.1:8080:8080"
```
```go
func validateOrigin(raw string) (*url.URL, error) {
    u, err := url.Parse(raw)
    if err != nil || u.Hostname() == "" || u.User != nil ||
        u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
        return nil, fmt.Errorf("expected an absolute HTTP(S) origin")
    }
    if u.Scheme == "https" { return u, nil }
    ip := net.ParseIP(u.Hostname())
    loopback := u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
    if u.Scheme == "http" && loopback { return u, nil }
    return nil, fmt.Errorf("HTTPS is required except for loopback development")
}
```

Use equivalent validation for `PUBLIC_URL` and MCP `LULL_URL`, with a deliberate override only for controlled development/private-network deployments. Reject malformed ports as well. Configure the public HTTPS origin behind a reverse proxy so cookie security is not inferred from a plaintext backend hop. Combine with explicit trusted-proxy handling (AUTH-01), not blind trust in forwarding headers.

**Regression.** Default Compose is not publicly published; remote HTTP config fails clearly; local HTTP and valid HTTPS work. Test TLS-terminating proxy deployment and verify Secure/HttpOnly/SameSite cookie properties.


<a id="ops-06"></a>

### OPS-06 â€” The container entrypoint ignores the configurable data directory

**Priority:** Medium  
**Evidence:** Confirmed deployment configuration defect  
**Pinned source:** [`docker-entrypoint.sh`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/docker-entrypoint.sh); [`Dockerfile`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/Dockerfile); [`config.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/config.go)

**Evidence and impact.** The application supports `DATA_DIR`, but the entrypoint prepares/chowns a hardcoded `/app/data`. An alternate mounted data directory can remain unwritable to the dropped-privilege application user and fail at runtime. The current entrypoint **does** drop privileges with `su-exec`; claiming that the normal server simply runs as root would be incorrect.

**Implementation.** Prepare the configured dedicated directory, validate it, and preserve privilege dropping. Avoid recursively changing ownership on an arbitrary environment-supplied filesystem tree.

```sh
#!/bin/sh
set -eu
data_dir="${DATA_DIR:-/app/data}"
case "$data_dir" in /*) ;; *) echo 'DATA_DIR must be absolute' >&2; exit 1 ;; esac
mkdir -p -- "$data_dir"
data_dir="$(readlink -f "$data_dir")"
case "$data_dir" in
  /|/app|/etc|/usr|/var|/home) echo 'DATA_DIR must be a dedicated data directory' >&2; exit 1 ;;
esac
chown lull:lull "$data_dir"
chmod 0700 "$data_dir"
export DATA_DIR="$data_dir"
exec su-exec lull "$@"
```

Retain any other existing entrypoint setup when integrating this fragment. Existing files in a moved volume must already have the application's ownership; provide a deliberate one-time migration/check rather than blindly `chown -R`ing an arbitrary path on every boot. Consider rejecting symlinked data roots under the deployment policy.

**Regression.** Run with the default volume, an alternate dedicated volume, an unwritable volume, existing private files and an invalid/root directory. Verify the final server UID is non-root.


<a id="ops-07"></a>

### OPS-07 â€” CI skips real SQL integration coverage and excludes pull requests and the MCP module

**Priority:** Medium  
**Evidence:** Confirmed test/workflow gaps  
**Pinned source:** [`.github/workflows/ci.yml`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/.github/workflows/ci.yml); [`mail-engine/store_integration_test.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/store_integration_test.go); [`push_test.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/push_test.go); [`mcp/client_test.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mcp/client_test.go)

**Evidence and impact.** Existing engine integration tests explicitly skip unless `NEUTRON_MAIL_TEST_DATABASE_URL` is set; CI provides neither that variable nor a database. The push test uses a recording driver, so it cannot catch DATA-01. CI runs product, engine and dashboard checks, but has no pull-request trigger and no separate MCP-module test step. It also omits race testing. This report does **not** claim the mail-engine module is entirely omitted; it is explicitly tested, just without its real-database path.

**Implementation.** Extend the existing verify job with a scratch Postgres service, isolated per-suite databases, PR execution and MCP/race checks. The versions below deliberately follow the audited workflow's toolchain rather than asserting they are the newest available releases.

```yaml
on:
  pull_request:
  push:
    branches: [main]
    tags: ["v*"]
permissions:
  contents: read
jobs:
  verify:
    runs-on: ubuntu-latest
    services:
      postgres:
        image: postgres:17-alpine
        env:
          POSTGRES_USER: lull_test
          POSTGRES_PASSWORD: local-ci-only
          POSTGRES_DB: engine_test
        ports: ["5432:5432"]
        options: >-
          --health-cmd "pg_isready -U lull_test -d engine_test"
          --health-interval 5s --health-timeout 5s --health-retries 12
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.26.x"
          cache-dependency-path: |
            go.sum
            mail-engine/go.sum
            mcp/go.sum
      - uses: actions/setup-node@v4
        with:
          node-version: "22"
          cache: npm
          cache-dependency-path: dashboard/package-lock.json
      - name: Create isolated product test database
        run: >-
          docker exec "${{ job.services.postgres.id }}"
          psql -U lull_test -d postgres -c 'CREATE DATABASE product_test;'
      - name: Product tests
        env:
          LULL_TEST_DATABASE_URL: postgres://lull_test:local-ci-only@localhost:5432/product_test?sslmode=disable
        run: go test -race ./... && go vet ./...
      - name: Engine real-database tests
        working-directory: mail-engine
        env:
          NEUTRON_MAIL_TEST_DATABASE_URL: postgres://lull_test:local-ci-only@localhost:5432/engine_test?sslmode=disable
        run: go test -race -p 1 ./... && go vet ./...
      - name: MCP tests
        working-directory: mcp
        run: go test -race ./... && go vet ./...
      - name: Dashboard
        working-directory: dashboard
        run: npm ci && npm test && npm run typecheck && npm run build
```

Retain binary/container build verification and move publication to a separate push-only job (OPS-08). `LULL_TEST_DATABASE_URL` is a **new** environment contract for the product integration test in Appendix B; setting it alone does not make the existing mock tests execute SQL. Keep destructive store tests isolated and nonparallel within a shared scratch database. Add browser tests for IndexedDB transactions/offline lifecycle; unit mocks do not model all browser commit/race behavior.

**Regression.** CI logs must show actual database tests executed, not skipped. The uncorrected push query fails the integration test. PRs run read-only verification, the MCP module compiles, and race tests exercise authentication/setup/worker interleavings.


<a id="ops-08"></a>

### OPS-08 â€” Any v-prefixed tag can republish an older image as latest

**Priority:** Medium  
**Evidence:** Confirmed release-policy hazard; supply-chain hardening  
**Pinned source:** [`.github/workflows/ci.yml`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/.github/workflows/ci.yml); [`Dockerfile`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/Dockerfile)

**Evidence and impact.** The same workflow publishes `latest` for both main pushes and any `v*` tag. Creating a matching tag on an older commit can overwrite `latest` with that older code. Build jobs also receive package-write permission globally, and actions/base images use movable tags. These do not prove repository compromise or missing branch rules; branch/ruleset administration and account security were not audited.

**Implementation.** Separate read-only verification from publication. Restrict package-write permission to the publish job; publish main as a deliberately chosen channel, and version tags under their own immutable version/commit tags. For example:

```yaml
publish:
  needs: verify
  if: github.event_name == 'push'
  permissions:
    contents: read
    packages: write
  runs-on: ubuntu-latest
  steps:
    - uses: actions/checkout@v4
    - name: Choose tags without rolling latest back from a historical tag
      id: tags
      shell: bash
      run: |
        image=ghcr.io/lullmail/lullmail
        {
          echo 'value<<EOF'
          echo "$image:${GITHUB_SHA}"
          if [[ "$GITHUB_REF" == refs/heads/main ]]; then
            echo "$image:latest"
          elif [[ "$GITHUB_REF" == refs/tags/v* ]]; then
            echo "$image:${GITHUB_REF_NAME}"
          fi
          echo EOF
        } >> "$GITHUB_OUTPUT"
```

Use the output for the existing build/push action and retain authenticated registry setup. Validate version-tag syntax and uniqueness; protect production publication with release rules/environment approvals appropriate to the project. Pin actions to reviewed full commit SHAs and base images to recorded digests through a maintained update processâ€”do not paste invented SHAs from an audit. Generate an SBOM/provenance and run pinned dependency/vulnerability tooling for each module and both npm applications before releases.

**Regression.** Tag an old commit in a test repository: only its version/commit tags publish, never `latest`. Fork pull requests cannot publish packages. Rebuilding a pinned source/toolchain records the inputs needed to explain differences.


<a id="ops-09"></a>

### OPS-09 â€” Outbound URL trust boundaries are implicit, including push endpoints and authenticated continuations

**Priority:** Medium  
**Evidence:** Conditional SSRF/credential-forwarding risk; hardening  
**Pinned source:** [`push.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/push.go); [`mail-engine/graph/adapter.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/graph/adapter.go); [`mail-engine/jmap/adapter.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mail-engine/jmap/adapter.go); [`accounts.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/accounts.go); [`mcp/client.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/mcp/client.go)

**Evidence and impact.** Push registration accepts supplied endpoints with little destination validation. Graph accepts absolute continuation URLs and uses an authenticated client; JMAP follows session-advertised API/download URLs and sends a token to them. These paths need deliberate scheme/origin/redirect policies. Attacker-controlled continuation data generally requires a malicious/compromised provider or altered state; push registration requires an authorized user. User-selected IMAP/JMAP hosts are also an intentional feature, so â€œban all private hosts everywhereâ€ is not an appropriate blanket fix.

**Implementation.** Use a provider-specific HTTPS hostname allowlist for push/Graph, reject credentials/fragments in URLs, and disallow redirects unless their destinations pass the same policy. JMAP needs an explicitly authorized set of session/API/download origins; do not automatically trust any origin solely because it appears in a response.

```go
func allowedHTTPS(raw string, allowed map[string]bool) (*url.URL, error) {
    u, err := url.Parse(raw)
    if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" {
        return nil, fmt.Errorf("HTTPS endpoint required")
    }
    if !allowed[strings.ToLower(u.Hostname())] || (u.Port() != "" && u.Port() != "443") {
        return nil, fmt.Errorf("endpoint origin is not authorized")
    }
    return u, nil
}
// Safe default for provider clients that need no redirects:
// client.CheckRedirect = func(*http.Request, []*http.Request) error {
//     return http.ErrUseLastResponse
// }
```

A hostname check alone is **not** a complete DNS-rebinding defense. For untrusted configurable hosts, validate resolved addresses, reject loopback/private/link-local/shared-address ranges unless explicitly authorized, and dial the checked address so validation and connection cannot diverge. An egress proxy/network policy can enforce this centrally. Make private-mail-server access an explicit account/deployment capability. Validate destinations before OAuth transports add Authorization headers; redirect header stripping by `http.Client` is not sufficient when the transport itself reattaches tokens.

**Regression.** Reject HTTP, loopback/link-local redirects, mixed public/private DNS answers and unexpected authenticated continuation origins; allow the deployment's explicitly approved private mail server and legitimate provider/CDN origins without forwarding credentials to others.


<a id="ops-10"></a>

### OPS-10 â€” Secret-key overrides, local privacy and agent credentials need explicit security policies

**Priority:** Low  
**Evidence:** Hardening; secure defaults already exist in several paths  
**Pinned source:** [`config.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/config.go); [`secretbox.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/secretbox.go); [`agent.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/agent.go); [`auth.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/auth.go); [`cmd_serve.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/cmd_serve.go); [`board.go`](https://github.com/lullmail/lullmail/blob/00216d8d7bd0a04dde742deefadabf0540200773/board.go)

**Evidence and tradeoffs.** Automatically generated keys are strong and secret storage uses authenticated encryption. A manually configured `SECRET_KEY` can nevertheless be a low-entropy password because arbitrary text is hashed into the encryption key. Key rotation/versioning and binding ciphertext to its owner/purpose are not modeled. Agent tokens have a useful auth-surface denylist but broad, long-lived capabilities rather than per-token scopes/expiry. Browser/private API cache policy and the deliberately retained board-pin titles also deserve explicit user-facing retention semantics. None of these observations proves a cryptographic break or an unauthenticated route bypass.

**Implementation.** Validate newly supplied key material as a generated 32-byte value, while keeping a safe legacy-read/rotation path instead of invalidating existing encrypted data:

```go
func validateNewSecretKey(value string) error {
    decoded, err := hex.DecodeString(value)
    if err != nil || len(decoded) != 32 {
        return fmt.Errorf("new SECRET_KEY values must contain 32 random bytes encoded as 64 hex characters")
    }
    allZero := true
    for _, b := range decoded { allZero = allZero && b == 0 }
    if allZero { return fmt.Errorf("SECRET_KEY must be randomly generated") }
    return nil
}
```

Format checks cannot prove randomness. Preserve the current key derivation for existing ciphertext during migration. A new version can carry a key ID and use authenticated additional data such as `json.Marshal([3]string{userID, accountID, purpose})`; keep old-key decryption until every row is re-encrypted and verified. Document encrypted backup/key recovery.

For new agent credentials, add scopes and expiry, retain the existing route denylist as a second layer, and compare **exact** scopes rather than path-prefix guesses:

```sql
ALTER TABLE agent_tokens ADD COLUMN scopes text[];
ALTER TABLE agent_tokens ADD COLUMN expires_at timestamptz;
-- New tokens require explicit scopes and expiry in the creation handler.
-- Migrate legacy tokens deliberately; do not silently broaden privileges.
```
```go
func hasScope(scopes []string, required string) bool {
    for _, scope := range scopes { if scope == required { return true } }
    return false
}
```

Define separate read, send, mail-write and account-delete scopes. Require recent proof to create/escalate tokens. Add `Cache-Control: private, no-store` to sensitive API/auth responses while retaining deliberate owner-scoped IndexedDB caching; these are different storage layers. State clearly that board pins are copied personal notes that can survive mailbox disconnect, and offer an explicit purge option rather than silently changing that intentional behavior.

**Regression.** Weak/invalid new key configuration fails without destroying legacy decryptability; rotation survives restart; expired/read-only tokens cannot send or delete accounts; security responses are not intermediary-cacheable; disconnect/purge behavior matches what the UI promises.


## Remediation order and acceptance gates

### 1. Stop avoidable exposure and silent loss

Fix AUTH-01/02/03, SEND-01/03/04, WEB-01/02/05/06 and DATA-01 first. Until durable delivery exists, preserve a complete browser draft after queue acceptance and surface later failures; do not call that interim mitigation crash-safe. Keep public deployment behind a deliberately configured TLS boundary. Back up the database and its encryption key before migration work.

The gate is behavioral: a spoofed forwarding header cannot create a fresh authentication allowance; a server without required TLS receives no mail; an accepted pending send survives restart or remains recoverable; a deleted account cannot start a send; offline startup retains cached content; and the production push candidate query executes against PostgreSQL.

### 2. Establish transactional ownership and truthful synchronization

Implement authentication epoch/ceremony serialization, owner-scoped mutation serialization, staged reconciliation and retention write barriers. Then fix IMAP/JMAP/Gmail/Graph contracts with protocol fixtures and provider integration mailboxes. Preserve stable IDs and local filing through migrations; treat Graph immutable-ID adoption as a migration, not a header-only rollout.

The gate includes fault injection between network reads and database writes, between pages, and between credential verification and session creation. A partial result must remain partial, and only an authoritative completed scan may drive destructive reconciliation.

### 3. Make the repaired behavior continuously testable

Run real-database, race, browser and MCP checks on pull requests; separate publishing permissions and channels; bound request/provider/export resources; and define single-instance versus multi-instance support explicitly. Add dependency scanning with pinned tools, but report actual results rather than assuming an absence of known CVEs from a successful build.

## Checks that should not be misreported as new vulnerabilities

The raw mail-engine API is explicitly denied to agent tokens; findings about that API concern the authenticated owner/library surface. The normal container entrypoint drops privileges. Account ownership and the `(user_id, lower(address))` uniqueness constraint exist. OAuth refresh handling already has important locking/CAS safeguards, and Gmail's initial history baseline is deliberately captured before enumeration. The HTML reader has substantial sandbox/CSP defenses; this audit did not establish stored XSS. The IMAP connection issues `ENABLE QRESYNC`; the defect is the CONDSTORE-only expunge path, not absence of ENABLE. Board-pin titles surviving disconnect and standalone TOTP login are documented choices, with the separate risks stated in their findings. A suspected Go return-expression/Scan ordering issue was checked and discarded rather than included as a bug.

These are scoped observations, not a claim that those entire subsystems are secure or complete.

## Appendix A â€” Local probe evidence

These programs use only local processes and a loopback SMTP fixture. No connection is made to an external mail provider. They recreate the relevant algorithms/decision branches; they do **not** import or execute the pinned repository as an application. The localhost SMTP server intentionally advertises neither STARTTLS nor AUTH, and accepts only the synthetic test message.

### Go probe

Run with `go run probes.go` in an isolated directory. The original control-flow probes were executed with Go 1.23.2.

```go
package main
import (
 "bufio"
 "fmt"
 "net"
 "net/mail"
 "net/smtp"
 "strings"
 "time"
)
func oldAddrs(header string) []string {
 var out []string
 for _, raw := range strings.Split(header, ",") {
  raw = strings.TrimSpace(raw); if raw=="" { continue }
  if open:=strings.LastIndex(raw,"<"); open>=0 {
   if close:=strings.Index(raw[open:],">");close>=0 {out=append(out,raw[open+1:open+close]);continue}
  };out=append(out,raw)
 };return out
}
func oldNames(bases []string) []string {
 used:=map[string]int{};var out []string
 for _,base:=range bases {used[base]++;name:=base+".mbox";if used[base]>1 {name=fmt.Sprintf("%s-%d.mbox",base,used[base])};out=append(out,name)}
 return out
}
func freshName(base string, used map[string]bool) string {
 for n:=1;;n++ {name:=base+".mbox";if n>1{name=fmt.Sprintf("%s-%d.mbox",base,n)};key:=strings.ToLower(name);if !used[key]{used[key]=true;return name}}
}
func smtpProbe() (bool,error) {
 ln,err:=net.Listen("tcp","127.0.0.1:0");if err!=nil{return false,err};defer ln.Close()
 received:=make(chan bool,1)
 go func(){
  c,err:=ln.Accept();if err!=nil{received<-false;return};defer c.Close();_ = c.SetDeadline(time.Now().Add(3*time.Second))
  fmt.Fprint(c,"220 local test ESMTP\r\n");r:=bufio.NewReader(c)
  for {line,err:=r.ReadString('\n');if err!=nil{received<-false;return}
   switch {
   case strings.HasPrefix(line,"EHLO"):fmt.Fprint(c,"250-local test\r\n250 SIZE 100000\r\n")
   case strings.HasPrefix(line,"MAIL FROM"),strings.HasPrefix(line,"RCPT TO"):fmt.Fprint(c,"250 OK\r\n")
   case strings.HasPrefix(line,"DATA"):
    fmt.Fprint(c,"354 send\r\n");for {s,e:=r.ReadString('\n');if e!=nil{received<-false;return};if s==".\r\n"{break}};fmt.Fprint(c,"250 accepted\r\n")
   case strings.HasPrefix(line,"QUIT"):fmt.Fprint(c,"221 bye\r\n");received<-true;return
   default:fmt.Fprint(c,"500 unexpected\r\n")
   }
  }
 }()
 conn,err:=net.DialTimeout("tcp",ln.Addr().String(),time.Second);if err!=nil{return false,err};_ = conn.SetDeadline(time.Now().Add(3*time.Second))
 c,err:=smtp.NewClient(conn,"mail.example.invalid");if err!=nil{return false,err};defer c.Close()
 if err=c.Hello("localhost");err!=nil{return false,err}
 // The audited branches skip TLS and authentication when not advertised.
 if ok,_:=c.Extension("STARTTLS");ok {return false,fmt.Errorf("unexpected STARTTLS")}
 if ok,_:=c.Extension("AUTH");ok{return false,fmt.Errorf("unexpected AUTH")}
 if err=c.Mail("sender@example.invalid");err!=nil{return false,err}
 if err=c.Rcpt("recipient@example.invalid");err!=nil{return false,err}
 w,err:=c.Data();if err!=nil{return false,err};_,err=w.Write([]byte("Subject: local probe\r\n\r\nNo real mail.\r\n"));if err!=nil{return false,err};if err=w.Close();err!=nil{return false,err}
 if err=c.Quit();err!=nil{return false,err};return <-received,nil
}
func main(){
 h:=`"Doe, Jane" <jane@example.invalid>`
 parsed,err:=mail.ParseAddressList(h)
 fmt.Printf("P1 comma split: %q; RFC parser count=%d first=%q error=%v\n",oldAddrs(h),len(parsed),parsed[0].Address,err)
 bases:=[]string{"a","a","a-2"};fixed:=[]string{};used:=map[string]bool{};for _,b:=range bases{fixed=append(fixed,freshName(b,used))}
 fmt.Printf("P2 ZIP names: old=%q fixed=%q\n",oldNames(bases),fixed)
 _,old:=time.Parse("02-Jan-2006 15:04:05 -0700"," 1-Jan-2026 09:00:00 +0000")
 _,newErr:=time.Parse("_2-Jan-2006 15:04:05 -0700"," 1-Jan-2026 09:00:00 +0000")
 fmt.Printf("P3 IMAP date: old rejects=%t replacement accepts=%t\n",old!=nil,newErr==nil)
 _,strict:=time.Parse(time.RFC1123Z,"Thu, 17 Sep 2026 09:00:00 GMT");_,flexible:=mail.ParseDate("Thu, 17 Sep 2026 09:00:00 GMT")
 fmt.Printf("P4 mail date: old rejects=%t RFC parser accepts=%t\n",strict!=nil,flexible==nil)
 accepted,e:=smtpProbe();fmt.Printf("P5 SMTP plaintext without AUTH or STARTTLS accepted=%t error=%v\n",accepted,e)
 if !accepted||e!=nil{panic("SMTP probe did not reproduce")}
}
```

Observed output:

```text
P1 comma split: ["\"Doe" "jane@example.invalid"]; RFC parser count=1 first="jane@example.invalid" error=<nil>
P2 ZIP names: old=["a.mbox" "a-2.mbox" "a-2.mbox"] fixed=["a.mbox" "a-2.mbox" "a-2-2.mbox"]
P3 IMAP date: old rejects=true replacement accepts=true
P4 mail date: old rejects=true RFC parser accepts=true
P5 SMTP plaintext without AUTH or STARTTLS accepted=true error=<nil>
```


### JavaScript probe

Run with `node probes.mjs`. This control-flow mock does not simulate the full IndexedDB browser implementation; the browser regression tests in WEB-02 remain necessary.

```js
import assert from 'node:assert/strict';
const original = new URL('https://example.invalid/report?to=alice');
const changed = new URL(original.searchParams.get('to'), original);
assert.equal(changed.href,'https://example.invalid/alice');
console.log(`P6 unwrapping a relative to parameter: ${original.href} -> ${changed.href}`);
const cache = new Map([['/threads/t?account=a', 'saved offline mail']]);
async function replayMutations() { return 0; }
async function clearResponseCache() { cache.clear(); }
await replayMutations().then(async n => { if(n>0) throw new Error('not this path'); return clearResponseCache(); });
assert.equal(cache.size,0);
console.log('P7 startup with zero replayed mutations clears cached mail: true');
const pending=[];
function currentTransactionResult(req){return new Promise((resolve,reject)=>{req.onsuccess=()=>resolve(req.result);req.onerror=()=>reject(req.error);});}
const req={result:'saved'};const result=currentTransactionResult(req).then(v=>pending.push(v));
req.onsuccess();await result; // A later transaction abort cannot unresolve this promise.
assert.deepEqual(pending,['saved']);
console.log('P8 resolving on IDB request success reports saved before a later transaction abort: true');
```

Observed output:

```text
P6 unwrapping a relative to parameter: https://example.invalid/report?to=alice -> https://example.invalid/alice
P7 startup with zero replayed mutations clears cached mail: true
P8 resolving on IDB request success reports saved before a later transaction abort: true
```


## Appendix B â€” Database and concurrency test implementation

The highest-yield missing test is a **real execution of production SQL**. Extract the push candidate statement into `pushCandidateSQL` and use that same constant from `sendPushForUser` and the test below. Run only against the isolated scratch `LULL_TEST_DATABASE_URL` from OPS-07. The test's schema initializer must run the application's real migration entry point before fixture creation; do not replace production schema with hand-invented tables that make a broken query pass.

The complete test body below is a proposed integration test, not an executed result. `openProductTestDB` is the deliberately factored project-specific migration fixture described next; wiring it to the existing initializer is necessary.

```go
func TestPushCandidateSQLIntegration(t *testing.T) {
    db := openProductTestDB(t)
    ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
    defer cancel()
    tx, err := db.BeginTx(ctx, nil)
    if err != nil { t.Fatal(err) }
    defer tx.Rollback()

    var uid, accountID string
    if err := tx.QueryRowContext(ctx, `
        INSERT INTO users(email) VALUES ('push-' || gen_random_uuid()::text || '@example.invalid')
        RETURNING id::text`).Scan(&uid); err != nil { t.Fatal(err) }
    if err := tx.QueryRowContext(ctx, `SELECT gen_random_uuid()::text`).Scan(&accountID); err != nil { t.Fatal(err) }
    mirrorID := "audit-mirror-" + accountID
    if _, err := tx.ExecContext(ctx, `
        INSERT INTO mail_accounts(id,provider,email,name)
        VALUES ($1,'imap','owner@example.invalid','audit fixture')`, mirrorID); err != nil { t.Fatal(err) }
    if _, err := tx.ExecContext(ctx, `
        INSERT INTO email_accounts(id,user_id,mirror_account_id,provider,address,cred_ciphertext)
        VALUES ($1,$2,$3,'imap','owner@example.invalid','test-only-not-a-real-secret')`,
        accountID, uid, mirrorID); err != nil { t.Fatal(err) }
    if _, err := tx.ExecContext(ctx, `
        INSERT INTO mail_messages(account_id,id,thread_id,received_at)
        VALUES ($1,'audit-message','audit-thread',now() AT TIME ZONE 'UTC')`, mirrorID); err != nil { t.Fatal(err) }
    if _, err := tx.ExecContext(ctx, `
        INSERT INTO hey_messages(user_id,account_id,message_id,bucket)
        VALUES ($1,$2,'audit-message','imbox')`, uid, mirrorID); err != nil { t.Fatal(err) }
    if _, err := tx.ExecContext(ctx, `
        INSERT INTO push_subscriptions(endpoint_hash,user_id,subscription_ciphertext)
        VALUES ($1,$2,'test-only-no-network-subscription')`, "audit-sub-"+accountID, uid); err != nil { t.Fatal(err) }

    var gotAccount, gotMessage, gotThread string
    err = tx.QueryRowContext(ctx, pushCandidateSQL, uid).Scan(&gotAccount, &gotMessage, &gotThread)
    if err != nil { t.Fatalf("production push candidate query failed: %v", err) }
    if gotAccount != mirrorID || gotMessage != "audit-message" || gotThread != "audit-thread" {
        t.Fatalf("unexpected candidate: %q %q %q", gotAccount, gotMessage, gotThread)
    }
}
```

`openProductTestDB(t)` should: require the explicitly named scratch database URL (skip locally if absent, fail the CI job if required integration coverage is absent), create/open a dedicated test schema/database, run engine and product migrations in their real order, register cleanup, and return `*sql.DB`. Keep fixtures transaction-local as above. The engine's existing schema-dropping integration helper must use a **different database**. Do not run either helper against production.

For concurrency regressions, use channels/barriers and separate SQL transactions to force the reported interleavings; avoid tests that merely sleep and hope a race occurs. Track the observed side effect: number of SMTP DATA acceptances, usable sessions, committed mutation IDs, retained draft bytes, or final mirror memberships. Assertions only about HTTP status codes or mocked callback counts will miss several of these defects.

## Appendix C â€” Primary provider/protocol references

These references were consulted for contract verification, not as substitutes for reading repository code. The detailed findings link to their relevant references directly.

| Reference | Relevance |
|---|---|
| [Microsoft: list mail folders](https://learn.microsoft.com/en-us/graph/api/user-list-mailfolders?view=graph-rest-1.0) | Root listing is not recursive; child traversal is needed |
| [Microsoft: mailFolder resource](https://learn.microsoft.com/en-us/graph/api/resources/mailfolder?view=graph-rest-1.0) | Documented properties and well-known aliases |
| [Microsoft: immutable Outlook IDs](https://learn.microsoft.com/en-us/graph/outlook-immutable-id) | Persistent identity, request preference and migration |
| [Google: Gmail message formats](https://developers.google.com/workspace/gmail/api/reference/rest/v1/Format) | Metadata versus full message structure |
| [Google: messages.list](https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.messages/list) | Spam/Trash inclusion and pagination |
| [IETF RFC 7162](https://www.rfc-editor.org/rfc/rfc7162.html) | CONDSTORE/QRESYNC and expunge reconciliation |
| [IETF RFC 8620](https://www.rfc-editor.org/rfc/rfc8620.html) | JMAP method responses and per-object set failures |

## Final limitation

This is an evidence-backed worklist for the pinned code, with code-level repair guidance and explicit verification requirements. It is not a certification, a claim of complete repository coverage, a live penetration-test result or a claim that the suggested application-wide changes have already compiled and passed tests. Re-audit the resulting diff and run the stated regression gates before declaring the issues fixed.
