# Open audit items

Unresolved findings for this repository from the ChatGPT-led audit series.

- Passes 1-5 (2026-09-09 through 2026-09-11): every P0/P1 fixed; the P2
  tail was 12-of-13 fixed in commits `83e0dee`..`025a979`, each tagged
  `audit lullmail-NN`.
- Pass 6 (2026-09-17, `AUDIT-CHATGPT-2.md`, 63 findings): 37 findings
  fixed across commits `4948829`..`6252e15` (each message carries the
  report's IDs); 24 findings deferred below against recorded decisions;
  2 findings partially fixed with a remainder deferred. No finding was
  assessed as a false positive — every spot-checked finding reproduced
  against the pinned source.

Open items: 25 (all deferrals; 2 of them are remainders of partial fixes)

Standing decision: **SEND-03 = lullmail-10 below.** The durable-outbox
finding in the second report is the same item as this register's one
recorded deferral; the equivalence is noted on that entry and the
deferral stands unchanged.

## lullmail__lullmail-10 - P2 / SEND-03 High - DEFERRED (decision)

**Expose a durable send outcome instead of only an undo token**

- Kind: Improvement / reliability
- Evidence: enqueue immediately returns queued/undo_seconds. The worker sends its result to a buffered done channel, logs failures and removes the map entry. The inspected product routes expose undo but no delivery-status lookup; no reader of done appears in the inspected queue code.
- Impact: The enqueue response is not a delivery acknowledgment. A later provider failure or restart during the undo window has no durable outcome in this path. The frontend draft lifecycle was not inspected, so draft loss itself is not asserted.
- Proposed fix: Add persistent outbox states and a delivery-result event or status endpoint. Retain/recover draft content until an outcome is known; distinguish accepted, submitting, submitted, failed and ambiguous. Define retry/idempotency behavior rather than blindly retrying sends.
- Deferral: sendqueue.go's design comment is the recorded decision this item argues against — "Five seconds is the whole feature — no queue table, no worker, just a timer map" (SPEC §6.1; the in-process undo window IS the shipped feature). A durable outbox with delivery states, a status endpoint, and retry/idempotency semantics is a product redesign of that surface, not a defect repair; reopening it is a product call, not an audit action. SEND-03 in `AUDIT-CHATGPT-2.md` (2026-09-17) restates this finding with a full outbox-state design; it is the same item, and the deferral stands. The browser-side mitigation the report asked for pending a durable outbox — a complete draft restored on undo (SEND-05) — was fixed.
- Review commit: `49d159b6654d3dbd27866f87ac783402f19c6cb7` (last reviewed 2026-09-10)

## AUTH-05 (remainder) - High - DEFERRED (contained fix landed, epoch design deferred)

**Login can still mint a session from a credential verified before a concurrent credential change committed**

- Kind: Concurrency gap (remainder)
- Evidence: The contained half of AUTH-05 landed (revocation now runs inside the credential-change transaction, errors propagate, TOTP removal revokes, logout reports failed revocation). The remaining gap the report proves is the check/commit interleaving: a login that verifies an old credential, pauses, and inserts its session after the credential-change path deleted sessions leaves a usable stale session. Closing it requires the auth-epoch design (users.auth_epoch + auth_sessions.auth_epoch checked on every lookup, epoch bumped in the same transaction as the credential change, login re-checking the epoch under the user lock at session insert).
- Deferral: An epoch column pair plus a changed session-lookup join is a schema and auth-pipeline change that every session read depends on; it needs its own migration window and race regression tests against real PostgreSQL, not a bolt-on inside an audit sweep. Risk accepted for now: the window requires an in-flight login during the exact change instant, single-operator deployments, and the in-transaction revocation already removes everything not mid-flight.

## AUTH-06 - Medium - DEFERRED (hardening)

**Long-lived sessions can enroll durable replacement credentials without fresh proof**

- Kind: Hardening; requires an already compromised authenticated session
- Evidence: Confirmed per the report — factor enrollment, recovery-code regeneration and agent-token creation require only a standing session.
- Deferral: The fix is a re-authentication ceremony (a dedicated password/passkey verification endpoint stamping `reauthenticated_at` on the session, a 10-minute freshness gate on security operations, and dashboard UI driving it). That is a product feature spanning API and UI, not a defect repair; record it as the security roadmap item it is.

## AUTH-07 (remainder) - Medium - DEFERRED (contained fix landed, installation epoch deferred)

**First-run completion is not a single transactional installation transition**

- Kind: Concurrency gap (remainder)
- Evidence: The contained half landed — both bootstrap finish paths re-check `ownerConfigured` under the owner-row lock inside the credential transaction, so two in-flight ceremonies can no longer both install a first credential.
- Deferral: The report's full design (an `installation_state` singleton with a setup epoch, ceremony-bound epochs, and immutable published config under one mutex) is a startup/config architecture change; the residual exposure after the recheck is mis-sequenced ceremony state during concurrent setup of a not-yet-configured install, which the setup token already gates.

## WEB-04 - Medium - DEFERRED

**Offline replay is not coordinated across tabs or made idempotent at the server**

- Kind: Concurrency/uncertain-retry gap
- Evidence: Confirmed per the report — each tab can replay the shared queue and no idempotency key exists server-side.
- Deferral: The correctness boundary the report demands is server-side mutation idempotency (an `api_mutations` table keyed by user+key with request-hash conflict detection, plus Web Locks in the browser). That is a new API contract and table, and per-mutation response persistence across every mutable endpoint — a subsystem, not a repair. The replay classification fixes that landed (WEB-03) stop the silent data loss; duplication under multi-tab replay remains the known residual.

## WEB-07 - Medium - DEFERRED

**Browser caches and drafts need an immutable owner namespace and generation fencing**

- Kind: Isolation gap; exposure depends on logout/reset paths
- Evidence: Confirmed per the report — persistent owner identity is the (mutable, reusable) email; drafts use global storage keys; in-flight reads are not fenced against an owner change.
- Deferral: `installation_id`+`user_id` namespacing, a generation counter around every async authenticated read, and IndexedDB store/key migration for existing clients is a storage-layer redesign touching every offline call site. Requires the same product decision as WEB-04 about what logout means for offline data. Tracked as the offline-v2 storage work.

## DATA-06 - Medium - DEFERRED

**Bucket/search result limits have no continuation contract**

- Evidence: Confirmed — buckets cap at 200 threads and search at 60 with no cursor; a client cannot distinguish truncation from completeness.
- Deferral: Keyset pagination on the outer per-thread result plus `has_more`/`next_cursor` is a versioned API change with dashboard load-more and MCP tool support landing together. Truncation today is silent but bounded, and the deterministic date+account+thread ordering it needs does not exist yet on every list query.

## DATA-07 - Medium - DEFERRED

**Relative snooze durations make offline replay and undo change the intended date**

- Evidence: Confirmed — snoozes store server-relative day counts; replay applies them later; undo reconstructs a prior snooze through rounded remaining days.
- Deferral: Absolute-UTC snooze timestamps with `until: null` semantics, undo snapshots of the exact prior instant, and dashboard/MCP callers updated together is an API contract migration across server, dashboard and MCP; the offline-queue path only exists for the actions the dashboard already sends, so a server-side acceptance of absolute timestamps alone would change nothing users touch.

## SYNC-03 - High - DEFERRED

**Destructive cursor reset can erase local filing state before a rebuild succeeds**

- Evidence: Confirmed ordering: an invalid cursor drops local mailbox state before the replacement enumeration exists; if the rebuild then fails, orphan cleanup can read the missing mirror rows as authoritative deletions of user filing state.
- Deferral: The report's fix is staged, resumable reconciliation — `mail_scan_runs`/`mail_scan_seen` engine tables, per-page transactional staging, and destructive pruning only at authoritative completion under the account maintenance lock. That is a new engine sync architecture (and SYNC-01, now fixed, was the report's own named amplifier). The sweep already refuses to run on incomplete listings; the residual is the reset-then-fail window. This is the largest item in the register and needs its own design pass against real PostgreSQL.

## SYNC-04 - Medium - DEFERRED

**Retention and mirror writes can race, leaving expired or orphaned data**

- Evidence: Confirmed — retention deletes mirror rows while sync/body writes run; the mirror has no foreign keys tying bodies and memberships to messages.
- Deferral: NOT VALID→VALIDATED foreign keys on existing production tables, an orphan audit, and a documented account-level lock order shared by retention and sync writeback is a versioned engine migration with deployment sequencing. Deferred with SYNC-03, which owns the same lock-and-stage machinery.

## SYNC-05 - Medium - DEFERRED

**Increasing retention does not restore older messages removed from the mirror**

- Evidence: Confirmed — an incremental provider will not re-report unchanged old messages after local retention removed them.
- Deferral: The fix is a durable `reconcile_requested` flag consumed only after a complete rescan — and the complete rescan is exactly the SYNC-03 staged-scan machinery. Deferred with it.

## GMAIL-03 - Low - DEFERRED

**Attachment flags are inferred from MIME structure excluded by metadata requests**

- Evidence: Confirmed — envelope requests use format=metadata, which does not return the part tree that `payloadHasAttachment` walks.
- Deferral: The honest fix is a tri-state attachment property (unknown/present/absent) flowing through the envelope model, the mirror schema and the dashboard badge — a model change. Requesting format=full for every envelope would multiply quota cost for every sync; the report itself says not to deploy that without measurement.

## GRAPH-02 - Medium - DEFERRED

**Graph message identity changes on folder moves because immutable IDs are not requested**

- Evidence: Confirmed — no `Prefer: IdType="ImmutableId"` header on any request, while Graph default IDs are mutable on moves.
- Deferral: The report is explicit that a header-only rollout is wrong: existing mirrors hold default-format IDs, and switching the preference without translating them changes every message's identity overnight (duplicate threads, lost filing state). The mandatory accompaniment is an ID-translation migration of mirror memberships, bodies, product filing and push receipts, plus a real Graph test mailbox regression. That migration touches production data and needs a provider integration environment; it is planned Graph work, not a sweep item.

## OPS-01 - Medium - DEFERRED (partial hardening exists)

**Request, provider-response and export resource limits are incomplete**

- Evidence: Confirmed scope: most JSON handlers read bodies uncapped; provider reads and export staging allocate before validation; the send queue has no aggregate admission budget. Partial bounds exist (send handler 34 MiB wire cap, push 64 KiB, per-attachment caps, engine-side 90 MB part cap added in this sweep).
- Deferral: Route-specific body bounds, a bounded reader across every provider JSON path, export disk budgets and queue admission limits is a cross-cutting hardening pass over dozens of handlers; deferred as its own review with a table of routes and limits rather than piecemeal in this sweep.

## OPS-02 - Medium - DEFERRED

**Database connection budgeting and startup migrations are not production-safe by construction**

- Evidence: Confirmed — no pool maximum on the product side, a separate engine pool, and startup reapplies all schema statements unconditionally.
- Deferral: A versioned migration table with advisory-lock serialization, checksums and resumable phases replaces the boot-time statement list; pool budgets need deployment-specific numbers. Rewriting the migration runner that every existing database converges through is a change that can brick upgrades if rushed; deferred to its own task with a tested migration path.

## OPS-03 - Medium - DEFERRED

**Creating a connected account spans separate engine/product transactions**

- Evidence: Confirmed — mirror insertion (engine pgx pool) and `email_accounts` insertion (product database/sql pool) commit separately with best-effort cleanup.
- Deferral: Atomicity needs a transaction bridge across the two pools (an engine `PutAccountSQLTx` on the product connection, or consolidating on one pool). Both are engine API/deployment changes; the existing best-effort orphan cleanup covers the common failure and the mirror is derived state.

## OPS-04 - Medium - DEFERRED

**Shutdown does not join all application-owned background work**

- Evidence: Confirmed — async sends, initial syncs and post-sync work run on background contexts; pools close after the HTTP drain without joining them.
- Deferral: A task group owning every background launch (admission-then-join lifecycle, cancellation propagated into provider I/O) is an app-lifecycle refactor. IMAP-02 (landed) removed the worst hang (unbounded provider I/O holding account locks past shutdown); the residual is failed writeback noise at shutdown, not lost mail.

## OPS-05 - Medium - DEFERRED

**Default port publication and URL configuration permit unintended plaintext exposure**

- Evidence: Confirmed per the report (deployment hardening, not an unauthenticated-login finding): compose publishes 8080 on all interfaces, and HTTP origins away from loopback are accepted in configuration.
- Deferral: Binding the compose default to loopback silently breaks every existing deployment whose ingress dials the published port, and enforcing HTTPS-only origins needs the same trusted-proxy decision as AUTH-01. The dashboard already surfaces a public-exposure warning; the enforcement change belongs to a release-noted deployment-policy update, not a sweep.

## OPS-07 (remainder) - Medium - DEFERRED (partial fix landed)

**CI lacks real SQL integration coverage**

- Evidence: Confirmed — engine integration tests skip without `NEUTRON_MAIL_TEST_DATABASE_URL`; CI provides no database, so DATA-01-class schema/query mismatches cannot fail a build. The partial fix landed: pull requests now run verification, the MCP module has its own test+vet step, and publication is separated from verification.
- Deferral: The scratch-Postgres service with isolated per-suite databases, a `LULL_TEST_DATABASE_URL` product integration harness executing the real push candidate SQL, and race runs is CI infrastructure work; it is the precondition for regression-testing several deferrals above (AUTH-05, SYNC-03/04), so it should land with them.

## OPS-09 - Medium - DEFERRED

**Outbound URL trust boundaries are implicit, including push endpoints and authenticated continuations**

- Evidence: Confirmed as conditional hardening — push registration accepts arbitrary endpoints; Graph follows absolute continuation URLs; JMAP sends tokens to session-advertised origins.
- Deferral: Provider-specific HTTPS allowlists, redirect policies, and resolved-address validation for configurable hosts is a deliberate egress-policy design (and user-selected private mail servers are an intentional feature, so a blanket ban is wrong). Deferred with the deployment-policy pass (OPS-05).

## OPS-10 - Low - DEFERRED

**Secret-key overrides, local privacy and agent credentials need explicit security policies**

- Evidence: Hardening per the report: SECRET_KEY accepts arbitrary text, agent tokens are broad and unexpiry'd, no key rotation/versioning.
- Deferral: Key IDs + AEAD additional data with re-encryption migration, agent-token scopes/expiry with per-token migration, and documented retention semantics are each their own security-feature tasks; the secure defaults that exist today (generated 32-byte keys, agent route denylist, auth-surface fencing) hold the line meanwhile.
