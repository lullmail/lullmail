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
- Pass 7 (2026-09-17, `AUDIT-CHATGPT-3.md`, 50 findings pinned at `f172169`): 30 findings fixed (19 fully, 11 partially with the
  remainder explicitly deferred below) across commits `ec8563d`..`4aef9e2`
  (each message tagged `audit 3-<ID>`); 20 findings deferred against
  standing or newly recorded decisions. No finding was assessed as a
  false positive; several supplied materially new information that
  narrowed or split an existing deferral (noted per entry).
- Round 3 / third pass of the fix series (2026-09-17,
  `AUDIT-CHATGPT-4.md`, 23 new findings F01-F23 plus 26 carried-forward
  risk items R01-R26, pinned at `52fe475`): 17 findings fixed fully and 6
  partially (each remainder explicitly deferred below) across commits
  `e0d58a9`..`9fba00c` (each message tagged `audit 4-<ID>`); the
  carried-forward R-items map one-to-one onto this register's standing
  deferrals and SEND-03, none reopened. No finding was assessed as a
  false positive; the Gmail endpoint/allowlist mismatch (F01), the pool
  hold-and-wait (F08), and the offline replay reordering (F09) each
  reproduced exactly as reported.   F06's server-computed reply defaults
  cover the owner's connected addresses; verified-alias configuration for
  addresses not recorded locally remains future product polish.
- Remainder pass (2026-09-18): the two round-3 register remainders that were
  gated on real-PostgreSQL test infrastructure landed. OPS-07's product-side
  harness (`556e272`) supplies `LULL_TEST_DATABASE_URL`, executes the real
  push candidate SQL, and is wired into the CI postgres job. AUTH-05's
  remainder / AUTH-01's auth-epoch pair (`4fbd4f6`) closed the
  verify-then-mint interleaving through that harness's race runs; that
  entry leaves this register. SYNC-03/04 keep their deferrals but no longer
  wait on the harness precondition.

Open items: 27 (all deferrals; 8 of them are remainders of partial fixes)

Standing decision: **SEND-01 = SEND-03 (pass 6) = lullmail-10 below.** The
durable-outbox finding appears in both later reports; it is the same item
as this register's one recorded product deferral, and the deferral stands.

## F07 (round 3) - Medium - PARTIALLY FIXED (contained guard landed), submission transport DEFERRED (product)

**JMAP accounts have no verified send transport**

- Kind: Product gap surfaced as a defect
- Evidence: Confirmed per the report - `deliveryFor` special-cases Gmail and Graph and sends everything else through `SMTPFor`, which for a JMAP account dials the API host on port 587 with the API token as an SMTP password. The send was accepted into the queue and failed asynchronously after the undo window closed, losing the draft.
- Fixed (round 3, commits tagged `audit 4-F07`): `handleSend` rejects both the fresh-send and reply-parent paths synchronously with 422 "Sending Not Configured" for JMAP accounts, before queue acceptance - the draft stays in the composer instead of silently dying in delivery.
- Deferral (remainder): implementing the real transport is a product feature, not a repair: either JMAP EmailSubmission against the session-advertised capability or a separately verified SMTP submission credential (a transport discriminator plus encrypted SMTP fields on the account). Until one lands, JMAP accounts are read-only for sending by design of the guard; the report's `can_send` accounts-API surface belongs to that same feature.

## F05 (round 3) - Medium - PARTIALLY FIXED (transitional patch landed), storage redesign DEFERRED (offline-v2)

**Draft persistence failed silently on large undo seeds and dropped attachment-only drafts**

- Kind: Storage-layer defect + design remainder
- Evidence: Confirmed per the report - the ring serialized full base64 attachment sets into localStorage and suppressed write errors; a multi-megabyte undo seed blew the quota and dropped the whole ring's save; `restoreDrafts` tested only to/subject/body.
- Fixed (round 3, commits tagged `audit 4-F05`): the ring persists metadata plus a `hasAttachments` marker (payloads stay in IndexedDB keyed by draft id, where Compose already restores them), restore keeps attachment-only and cc/bcc-only drafts, and quota failures surface through a persistent `draftsUnsaved` indicator in the compose ring instead of a swallowed exception.
- Deferral (remainder): one complete draft as a single IndexedDB record with the ordered ring in the same transaction (the report's design) removes the two-engine atomicity gap between the ring and the per-draft attachment records. That is the same storage-layer redesign tracked as the offline-v2 work in WEB-07 below, and it needs the same product decision about what logout/owner change means for drafts.

## lullmail__lullmail-10 - P2 / SEND-03 (pass 6) / SEND-01 (pass 7) High - DEFERRED (decision)

**Expose a durable send outcome instead of only an undo token**

- Kind: Improvement / reliability
- Evidence: enqueue immediately returns queued/undo_seconds. The worker sends its result to a buffered done channel, logs failures and removes the map entry. The inspected product routes expose undo but no delivery-status lookup; no reader of done appears in the inspected queue code.
- Impact: The enqueue response is not a delivery acknowledgment. A later provider failure or restart during the undo window has no durable outcome in this path. The frontend draft lifecycle was not inspected, so draft loss itself is not asserted.
- Proposed fix: Add persistent outbox states and a delivery-result event or status endpoint. Retain/recover draft content until an outcome is known; distinguish accepted, submitting, submitted, failed and ambiguous. Define retry/idempotency behavior rather than blindly retrying sends.
- Deferral: sendqueue.go's design comment is the recorded decision this item argues against — "Five seconds is the whole feature — no queue table, no worker, just a timer map" (SPEC §6.1; the in-process undo window IS the shipped feature). A durable outbox with delivery states, a status endpoint, and retry/idempotency semantics is a product redesign of that surface, not a defect repair; reopening it is a product call, not an audit action. Pass-7 SEND-01 restates it with a full outbox_jobs design; same item, deferral stands. SEND-04 (durable Sent-copy filing state, pass 7) is a requirement on the same replacement and is deferred with it — today a failed Sent-file is logged (never silently dropped) and the delivered message is never re-sent. Partial hardening landed in pass 7: the queue now has an aggregate admission budget and delivery holds an account-use lease, so the volatile window is bounded and fenced (commits tagged `audit 3-SEND-02`, `audit 3-SEND-03`).
- Review commit: `49d159b6654d3dbd27866f87ac783402f19c6cb7` (last reviewed 2026-09-10)

## AUTH-06 (pass 6) / AUTH-02 (pass 7) - Medium - DEFERRED (hardening)

**Long-lived sessions can enroll durable replacement credentials without fresh proof**

- Kind: Hardening; requires an already compromised authenticated session
- Evidence: Confirmed per both reports — factor enrollment, recovery-code regeneration and agent-token creation require only a standing session. Password replacement and full account deletion already require fresh proof (the report credits this).
- Deferral: The fix is a re-authentication ceremony (a dedicated password/passkey verification endpoint stamping `reauthenticated_at` on the session, a 10-minute freshness gate on security operations, and dashboard UI driving it). That is a product feature spanning API and UI, not a defect repair; record it as the security roadmap item it is.

## AUTH-06 (pass 7) - Medium - DEFERRED (hardening, deployment-dependent)

**Standalone TOTP guessing is limited per peer, not by a shared account budget**

- Evidence: Confirmed — replay protection exists (step consumption) and the per-peer limiter is the repaired fail-closed parser, but guesses from many peers against one account share no budget. TOTP is an intentional alternative credential, not a second factor.
- Deferral: A durable per-user fixed-window budget (the report's auth_factor_windows design) plus pruning, monitoring, and an ingress story is a new subsystem with lockout tradeoffs (a shared budget is also a denial-of-service lever against the owner). Exposed deployments should rate-limit TOTP at the ingress now; the account-wide budget belongs to the AUTH-02 re-authentication/security roadmap item above rather than a sweep.

## AUTH-07 (remainder) / AUTH-03 (pass 7) - Medium - DEFERRED (contained fix landed, installation design deferred)

**First-run completion is not a single transactional installation transition**

- Kind: Concurrency gap (remainder)
- Evidence: The pass-7 report proved the pass-6 recheck was insufficient for the no-owner case: two ceremonies with different names create different owner rows, lock different rows, and the global `ownerConfiguredDB` check under a row lock cannot serialize them. FIXED in pass 7: both bootstrap finish paths now take a transaction-scoped installation-wide advisory lock before the configured check, so two concurrent ceremonies can no longer both install a first credential (commit tagged `audit 3-AUTH-03`).
- Deferral (remainder): `ensureUser` can still leave a credential-less second user row behind when two begins race or `LULL_USER_EMAIL` changes between boots, and the report's fuller design (users-single-owner unique index after operator reconciliation, ceremony-bound setup epochs, transaction-aware owner initializer, provisional ceremony input) is a startup/config architecture change. The residual is stray rows with no sign-in capability behind a setup token that retires on first completion.

## AUTH-04 (remainder) - Medium - DEFERRED (contained fix landed, runtime snapshot deferred)

**Setup mutates shared configuration without a consistent synchronization boundary**

- Evidence: Confirmed. FIXED in pass 7: `setOriginForSetup` now validates the candidate on a Config copy and publishes origin + WebAuthn instance together only after construction succeeds, and `handleLoginFinish` reads the instance through the locked accessor (commit tagged `audit 3-AUTH-04`). Bootstrap token retirement already mutates under waMu.
- Deferral (remainder): Other request handlers still read `a.cfg` fields directly rather than one immutable published snapshot, so a reader can mix pre- and post-swap values mid-request. The report's `atomic.Pointer[runtimeAuth]` migration of every reader is a cross-cutting refactor of the auth/config surface; defer as its own pass. The contained fix removed the dangerous case (a failed setup leaving a half-updated live config).

## WEB-04 (pass 6) / WEB-03 (pass 7) / R07 (round 3) - Medium - DEFERRED

**Offline replay is not coordinated across tabs or made idempotent at the server**

- Kind: Concurrency/uncertain-retry gap
- Evidence: Confirmed per both reports — each tab can replay the shared queue and no idempotency key exists server-side. Pass 7 landed the retry-scheduling and rejection-surfacing companions (WEB-04/WEB-05 there), so failures are no longer stranded or invisible; round 3 (F09/F10) additionally made replay strictly order-preserving and self-rescheduling on network-only failures. Duplication under multi-tab replay or a lost acknowledgment remains.
- Deferral: The correctness boundary the reports demand is server-side mutation idempotency (an `api_mutations` table keyed by user+key with request-hash conflict detection, plus Web Locks in the browser). That is a new API contract and table, and per-mutation response persistence across every mutable endpoint — a subsystem, not a repair. Browser-side locks alone were deliberately not added: without the server contract they only narrow the duplicate window while implying a guarantee the system does not make.

## WEB-07 (pass 6) / WEB-01 (pass 7) / R08+F11+F12 remainders (round 3) - Medium - DEFERRED (partial fixes landed)

**Browser caches and drafts need an immutable owner namespace and generation fencing**

- Kind: Isolation gap; exposure depends on logout/reset paths
- Evidence: Confirmed per both reports — persistent owner identity is the (mutable, reusable) email; drafts use global storage keys; in-flight reads are not fenced against an owner change. Pass 7 made the wipe itself atomic and error-propagating (WEB-02 there), which closes the "believed erased but was not" half. Round 3 landed the two contained halves the fourth report isolated: storage preparation failure now suspends offline access fail-closed instead of keep operating on the old namespace (F11, commits tagged `audit 4-F11`), and disconnect/retention purge this device's response snapshots with the reader closed (F12, `audit 4-F12`).
- Deferral (remainder): `installation_id`+`user_id` namespacing, a generation counter fencing every async authenticated read inside the write transaction, abort-on-owner-change, per-mailbox cache records so one account's deletion does not clear every account's snapshots, and IndexedDB store/key migration for existing clients is a storage-layer redesign touching every offline call site (the reports' Scope model). Requires the same product decision as WEB-04 about what logout means for offline data; the single-record drafts design (F05 remainder) lands with it. Tracked as the offline-v2 storage work.

## DATA-06 (pass 6) / DATA-11 (pass 7) - Medium - DEFERRED (partial fix landed)

**Bucket/search result limits have no continuation contract**

- Evidence: Confirmed — buckets cap at 200 threads and search at 60 with no cursor; a client cannot distinguish truncation from completeness. FIXED in pass 7: the deterministic tie-breaker (received_at DESC, id DESC) landed on bucket and search ordering, so equal-timestamp rows no longer reorder between requests (commit tagged `audit 3-DATA-11`).
- Deferral (remainder): Keyset pagination on the outer per-thread result plus `has_more`/`next_cursor` is a versioned API change with dashboard load-more and MCP tool support landing together. Truncation today is silent but bounded.

## DATA-07 (pass 6) / DATA-05 (pass 7) - Medium - DEFERRED

**Relative snooze durations make offline replay and undo change the intended date**

- Evidence: Confirmed — snoozes store server-relative day counts; replay applies them later; undo reconstructs a prior snooze through rounded remaining days. Pass 7 did not change this.
- Deferral: Absolute-UTC snooze timestamps with `until: null` semantics, undo snapshots of the exact prior instant, and dashboard/MCP callers updated together is an API contract migration across server, dashboard and MCP; the offline-queue path only exists for the actions the dashboard already sends, so a server-side acceptance of absolute timestamps alone would change nothing users touch. The queue replay timing itself got fairer in pass 7 (WEB-05 retry scheduling), but the deadline semantics remain relative.

## DATA-04 (pass 7) / R11+F20 remainder (round 3) - Medium - DEFERRED (product design)

**Thread-wide mutations cannot be undone exactly from one summary row**

- Evidence: Confirmed — the action handler mutates every message in the resolved thread while the browser inverse is computed from one list row's flags, so mixed read/bucket state inside a thread is lost on undo. (Per-row snapshots already make single-row read/unread undo exact.) Round 3 removed the adjacent board-pin variant's worst case: re-pins no longer clobber the standing card and the undo deletes only cards the pin created (F20, commits tagged `audit 4-F20`); an edit landing between a pin and its undo is still not conflict-detected.
- Deferral: Exact undo needs server-side preimage capture (row versions, one-use undo records, conflict rejection) — the report's `message_action_undo` design is a schema + API + client change across the same surface as the idempotency work in WEB-04. Deferred as product redesign; the current undo is documented as approximate for mixed threads.

## DATA-08 (pass 7) - Medium - DEFERRED (design)

**Backfill and retention settings can commit before their corresponding data transition succeeds**

- Evidence: Confirmed — the setting update and the pruning/classification/retention that realize it are separate operations; a later failure leaves the new policy committed with an error response and no durable reconciliation status. (DATA-10's fix in pass 7 means post-sync failures are no longer reported as healthy syncs.)
- Deferral: The report's desired-policy vs applied-policy design (`policy_version`, `account_reconcile_jobs`, 202 responses with durable job state) is a new job/queue subsystem sharing machinery with the SYNC-03 staged-scan work below; defer with it.

## SYNC-03 (pass 6) / SYNC-01 (pass 7) - High - DEFERRED

**Destructive cursor reset can erase local filing state before a rebuild succeeds**

- Evidence: Confirmed ordering: an invalid cursor drops local mailbox state before the replacement enumeration exists; if the rebuild then fails, orphan cleanup can read the missing mirror rows as authoritative deletions of user filing state.
- Deferral: The reports' fix is staged, resumable reconciliation — `mirror_scans`/`mirror_scan_seen` engine tables, per-page transactional staging, destructive pruning only at authoritative completion under the account maintenance lock. That is a new engine sync architecture and this is the largest item in the register; it needs its own design pass against real PostgreSQL. The sweep already refuses to run on incomplete listings; the residual is the reset-then-fail window. Pass-7 SYNC-02 (enumeration bookkeeping not durable across MaxPages/restarts; Graph `initial`/`Complete` flags only consistent within one call) folds into this same staged-scan machinery and is deferred with it.

## SYNC-04 (pass 6) / SYNC-04 (pass 7) - Medium - DEFERRED

**Retention and mirror writes can race, leaving expired or orphaned data**

- Evidence: Confirmed — retention deletes mirror rows while sync/body writes run; the mirror has no foreign keys tying bodies and memberships to messages.
- Deferral: NOT VALID→VALIDATED foreign keys on existing production tables, an orphan audit, and a documented account-level lock order shared by retention and sync writeback is a versioned engine migration with deployment sequencing. Deferred with SYNC-03, which owns the same lock-and-stage machinery.

## SYNC-05 (pass 6) / DATA-09 (pass 7) - Medium - DEFERRED

**Increasing retention does not restore older messages removed from the mirror**

- Evidence: Confirmed — an incremental provider will not re-report unchanged old messages after local retention removed them; widening the window changes policy only.
- Deferral: The fix is a durable `reconcile_requested` flag consumed only after a complete rescan — and the complete rescan is exactly the SYNC-03 staged-scan machinery. Deferred with it.

## SYNC-03 (pass 7) - High - PARTIALLY FIXED (ordering), migration DEFERRED with SYNC-03 above

**Identity promotion deletes the old message before its replacement is written**

- Evidence: Confirmed — `apply` called `DeleteMessages` on the old identity before `PutEnvelopes` wrote the new one, so a failed write lost the only readable copy. FIXED in pass 7: the replacement envelope is now written first and the old identity retires only afterwards, so a failure leaves the old record intact and the next sync retries (commit tagged `audit 3-SYNC-03`).
- Deferral (remainder): Product filing state keyed by the old account/message id is still not migrated on promotion (the engine cannot reach product tables; the orphan cleanup eventually drops it), and a truly transactional promotion carrying memberships, body, filing, and receipts across both pools needs the OPS-03 transaction bridge and the staged-migration design. Deferred with the SYNC family above.

## GMAIL-03 - Low - DEFERRED

**Attachment flags are inferred from MIME structure excluded by metadata requests**

- Evidence: Confirmed — envelope requests use format=metadata, which does not return the part tree that `payloadHasAttachment` walks.
- Deferral: The honest fix is a tri-state attachment property (unknown/present/absent) flowing through the envelope model, the mirror schema and the dashboard badge — a model change. Requesting format=full for every envelope would multiply quota cost for every sync; the report itself says not to deploy that without measurement.

## GRAPH-02 / PROVIDER-01 (pass 7) - Medium - DEFERRED

**Graph message identity changes on folder moves because immutable IDs are not requested**

- Evidence: Confirmed per both reports — no `Prefer: IdType="ImmutableId"` header on any request, while Graph default IDs are mutable on moves.
- Deferral: Both reports are explicit that a header-only rollout is wrong: existing mirrors hold default-format IDs, and switching the preference without translating them changes every message's identity overnight (duplicate threads, lost filing state). The mandatory accompaniment is an ID-translation migration of mirror memberships, bodies, product filing and push receipts, plus a real Graph test mailbox regression. That migration touches production data and needs a provider integration environment; it is planned Graph work, not a sweep item.

## OPS-01 (pass 6) / OPS-01 (pass 7) / R19+F14 remainder (round 3) - Medium - DEFERRED (partial hardening exists)

**Request, provider-response and export resource limits are incomplete**

- Evidence: Confirmed scope: many JSON handlers read bodies uncapped; provider reads and export staging allocate before validation. Partial bounds exist (send handler 34 MiB wire cap, push 64 KiB, per-attachment caps, engine-side 90 MB part cap), pass 7 added strict bounded single-document decoding with unknown-field rejection on the account-settings routes (commit tagged `audit 3-DATA-07`) and an aggregate send-queue admission budget, and round 3 made that budget count the retained header fields — subject, recipients, references, attachment metadata — instead of bodies alone (F14, commits tagged `audit 4-F14`).
- Deferral (remainder = F14's pre-decode admission + the rest of R19): a decode semaphore ahead of JSON allocation on the send route, route-specific body bounds across dozens of handlers, a bounded reader on every provider JSON path, export disk budgets and read deadlines is a cross-cutting hardening pass; deferred as its own review with a table of routes and limits rather than piecemeal in a sweep.

## OPS-02 (pass 6) / OPS-02+OPS-03 (pass 7) - Medium - PARTIALLY FIXED, migration runner DEFERRED

**Database connection budgeting and startup migrations are not production-safe by construction**

- Evidence: Confirmed. FIXED in pass 7: the product pool is bounded (16 open / 4 idle, 5-minute idle / 30-minute lifetime caps) and the per-request `last_seen_at` session write is throttled to once per minute with a read fallback that still refuses revoked sessions immediately (commit tagged `audit 3-OPS-02`).
- Deferral (remainder = pass-7 OPS-03): startup still reapplies all schema statements unconditionally with no version ledger, checksums, or installation-wide migration lock. A versioned migration table with advisory-lock serialization replaces the boot-time statement list every existing database converges through; rewriting it can brick upgrades if rushed and needs a tested migration path (engine + product ownership split included). Deferred to its own task.

## OPS-04 (pass 6) / OPS-04 (pass 7) - Medium - DEFERRED

**Shutdown does not join all application-owned background work**

- Evidence: Confirmed per both reports — async sends, initial syncs and post-sync work run on background contexts; pools close after the HTTP drain without joining them. Pass 7 bounded the finishSync detached context (previously bare `context.Background()`), so finalization work now carries its own deadline.
- Deferral: A task group owning every background launch (admission-then-join lifecycle, cancellation propagated into provider I/O) is an app-lifecycle refactor. IMAP-02 (landed) removed the worst hang; the residual is failed writeback noise at shutdown, not lost mail.

## OPS-05 (pass 7) - Medium - DEFERRED (design)

**Deleting one account can wait behind unrelated account work and uncancellable locks**

- Evidence: Confirmed — the lifecycle gate is one global owner RWMutex; a long read on account A delays account B's deletion, and engine/OAuth refresh mutexes do not observe cancellation.
- Deferral: Per-account cancellable admission gates with an active-operation counter (the report's design) plus coalesced manual-sync requests is the same lifecycle refactor as OPS-04 above — it touches every beginAccountUse call site and the deletion transaction. Deferred with it as one app-lifecycle pass; pass 7 at least fenced sends against deletion (SEND-02 fix).

## OPS-05 (pass 6) / OPS-06 (pass 7) - Medium - DEFERRED

**Default port publication and URL configuration permit unintended plaintext exposure**

- Evidence: Confirmed per both reports (deployment hardening, not an unauthenticated-login finding): compose publishes 8080 on all interfaces, HTTP origins away from loopback are accepted, origin detection trusts forwarded headers without a proxy-peer check.
- Deferral: Binding the compose default to loopback silently breaks every existing deployment whose ingress dials the published port, and enforcing HTTPS-only origins plus trusted-proxy forwarded parsing needs the same deployment-policy decision as pass-6 AUTH-01. The dashboard already surfaces a public-exposure warning; the enforcement change belongs to a release-noted deployment-policy update, not a sweep.

## OPS-07 (remainder) - Medium - PARTIALLY FIXED (product harness landed), per-suite isolation and migration-upgrade tests DEFERRED

**CI lacks real SQL integration coverage**

- Evidence: Confirmed — engine integration tests skip without `NEUTRON_MAIL_TEST_DATABASE_URL`. FIXED in pass 7: CI now runs a disposable postgres:17 service job that executes the engine store integration suite and race runs for all three Go modules (commit tagged `audit 3-OPS-07`); the normal green build no longer excludes every database test.
- Fixed (remainder pass, 2026-09-18, `556e272`): `LULL_TEST_DATABASE_URL` gates a product-side integration suite with the same skip-offline contract as the engine's — it resets only product-owned tables, re-runs the real schema migration entry point inside one transaction, and executes the production push candidate SQL (hoisted to `pushCandidateSQL` so the suite runs the shipped statement, not a copy) plus the auth-epoch schema/lookup sync checks. The CI postgres job runs it with `-race` alongside the engine and MCP modules; the harness was verified end-to-end against a live local PostgreSQL (disposable database, created and dropped for the run).
- Deferral (remainder): Isolated per-suite databases for potentially destructive suites and migration-upgrade tests remain CI infrastructure work. The harness precondition this register recorded is now met — AUTH-01's race regression tests landed through it (`4fbd4f6`) — so only SYNC-03/04 still await it.

## OPS-09 (pass 6) / OPS-09+PROVIDER-03-remainder (pass 7) - Medium/Low - DEFERRED

**Outbound URL trust boundaries are implicit, including push endpoints and authenticated continuations**

- Evidence: Confirmed as conditional hardening — push registration accepts arbitrary endpoints; JMAP sends tokens to session-advertised origins. Pass 7 landed the Graph half: continuation URLs are now validated against the configured origin/path before any request (PROVIDER-02 fix), and OAuth account creation joined the owner lifecycle gate with duplicate-address rejection (the contained half of PROVIDER-03).
- Deferral (remainder): Provider-specific HTTPS allowlists, redirect policies, and resolved-address validation for configurable hosts is a deliberate egress-policy design (and user-selected private mail servers are an intentional feature, so a blanket ban is wrong). The cross-pool transactional account creation both reports describe needs the OPS-03 bridge. Deferred with the deployment-policy pass (OPS-05 above).

## OPS-10 - Low - DEFERRED

**Secret-key overrides, local privacy and agent credentials need explicit security policies**

- Evidence: Hardening per the report: SECRET_KEY accepts arbitrary text, agent tokens are broad and unexpiry'd, no key rotation/versioning.
- Deferral: Key IDs + AEAD additional data with re-encryption migration, agent-token scopes/expiry with per-token migration, and documented retention semantics are each their own security-feature tasks; the secure defaults that exist today (generated 32-byte keys, agent route denylist, auth-surface fencing) hold the line meanwhile.
