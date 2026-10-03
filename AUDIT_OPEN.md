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
- Staged reconciliation pass (2026-09-18, later): the mail-state
  reconciliation program landed against the same harness. SYNC-03/SYNC-01
  (staged resumable scans), SYNC-04 (mirror FKs + account lock order),
  SYNC-05/DATA-09 (retention-widening restoration), DATA-08 (versioned
  policy + durable reconcile jobs), and the OPS-02/OPS-03 migration-runner
  remainder are all fixed and leave this register; the regression tests
  run under both harnesses, including kill-between-pages at every phase.
  The SYNC-03 (pass 7) identity-promotion remainder stays deferred below —
  it needs the OPS-03 cross-pool transaction bridge, which stays deferred
  with it (not built, noted).
- Idempotency + offline-v2 + API-contract pass (2026-09-18, final): the
  four biggest deferrals closed together because they share one contract
  surface. WEB-04/WEB-03/R07 closed server-side (`0b30f84` api_mutations)
  and client-side (`6f99201` Web Locks replay + idempotency keys);
  WEB-07/WEB-01/R08+F11+F12 and the F05 remainder closed as the ratified
  offline-v2 storage (`9e52433`, plus the fresh-device preparation fix
  `0f3b8cc` caught by the new fake-indexeddb tests); DATA-06/DATA-11
  closed with keyset pagination and the dashboard load-more + MCP halves
  (`3df8501`); DATA-07/DATA-05 closed with absolute-UTC snooze and exact
  undo snapshots across server, dashboard and MCP (`0e294ef`); OPS-01
  closed with route body bounds, decode admission, provider read caps and
  export budgets, registered in `docs/route-limits.md` (`f281466`).
- Lifecycle + re-auth pass (2026-09-18, later still): the two standing
  app-lifecycle deferrals and the two re-authentication deferrals closed
  together. OPS-04/OPS-05 landed as one task-group + per-account-gate
  pass (`c341e02`); AUTH-02 (pass 7)/AUTH-06 (pass 6) landed as the
  re-authentication ceremony and AUTH-06 (pass 7) as the durable per-user
  TOTP budget (`b9c1027` server, `b889d95` dashboard) — all three leave
  this register.
- Round 5 / audit 5 (2026-09-19, `AUDIT-CHATGPT-5.md`, 42 findings — 20
  High, 19 Medium, 3 Low — pinned at `39e2518`, the first audit of the
  offline-v2/lifecycle/re-auth/IMAP-threading surface): 34 findings fixed
  across commits `806ace7`..`f4c44b8` (each message tagged `audit 5-<ID>`);
  8 deferred below against standing or newly recorded architectural
  decisions. No finding was assessed as a false positive — every
  spot-checked finding reproduced against the pinned source; the TOTP
  non-atomicity, the Graph multi-page completion loss, the offset-shift
  JMAP enumeration hole, the destructive rescan retry, and the owner-delete
  busy-connection failure all reproduced exactly as reported.

## GMAIL-READ-01 / GMAIL-AUTH-01 - High - LOCAL PROPOSED PATCH; upstream and live-provider gates remain (2026-10-02)

A deterministic fake transport reproduces initial-page starvation after a
metadata throttle. Gmail reads now have bounded read-only retries and shared
local pacing; 100-ID initial pages reduce the pacing overhead before a durable
checkpoint. Exhausted attempts return no partial page and preserve staged
continuation, generation and seen evidence. Repeated-label metadata remains
fresh; no cached-ID shortcut changes labels or flags.

A real scheduler with fake HTTP/SQL reproduces two distinct credential bugs:
401 mid-run permanently paused a refreshable account, while invalid_grant at
admission did not pause it. Stored-account sync resolvers now perform one
origin-restricted GET-only refresh/replay through the application CAS path;
invalid refresh grants are typed and scheduler admission records reauth.
Sends and label mutations are not retried. Cancellation covers waits and refresh
lock acquisition. See `docs/gmail-read-reliability.md` for the contract, measured
synthetic budget, quota references and outstanding boundaries. The default review
candidate now includes these Gmail fixes with the frozen main hardening. The
optional outbox layer remains separate. Neutron upstream backflow/re-vendor is
required before release; no live mailbox or deployment was touched.

## Current verification status (2026-10-02)

This register distinguishes local implementations, executed checks, and release
requirements. Product, engine and MCP `go test -race -count=1 ./...` plus `go vet
./...` now pass with Go 1.26.6. PostgreSQL-dependent tests skip in this authoring
environment because server socket creation is restricted; their SQL and
concurrency guarantees are **not** marked verified. Real-browser interaction,
live-provider submission, upstream backflow and deployment remain separate gates.
See the per-item statuses below rather than the historical open-item count;
several report IDs are aliases for the same underlying issue.

## UI-SELECT-01 - FIXED locally (selection and navigation pass, 2026-10-02)

Message modifier clicks now support anchored ranges, individual toggles and additive ranges without opening mail. Focus and selected membership follow account-qualified identities across list changes; hidden readers, native controls, menus and composition are fenced from unrelated shortcuts. Route/search/account changes reveal their destination, and returning to the same list is the only navigation that restores its saved scroll offset. Today/Board show their existing keyboard selection state. See `docs/mail-selection.md` for the precise behavior contract and official Roundcube references.

The adjacent paging correction fences late first/continuation responses on account/query changes, refresh and unmount, prevents duplicate same-tick load-more requests, and stabilizes published row identity. Actual component/hook regression tests cover these boundaries. Chromium launch remains blocked by the authoring runtime's temporary-socket restriction; the separately available supported browser also rejects the self-contained fixture's file URL before page load. No native-browser or live-mailbox verification is claimed. A synthetic CI browser job is prepared with JSON/log/screenshot evidence and unchanged publication dependencies.

## UI-SELECT-02 - Medium - IMPLEMENTED locally; PostgreSQL validation pending

Confirmed and reproduced: Board pinned-card conversion fabricated `read: false` and `bucket: "imbox"`. Keyboard Done/Undo unread a read pin; moving/undo filed a non-Inbox pin into Inbox; snooze/undo lost the original bucket and exact date. The four original component regressions fail against the prior candidate and pass with this correction.

The Board response now includes owner/account/message-scoped read, bucket and snooze state without a schema change. Missing product state remains absent, and deadlines preserve microseconds. Only cards with complete, supported undo snapshots join the keyboard mutation list; stale/disconnected, dropped and incomplete dated-snooze cards retain their card controls but cannot invent mail state. Rendered cursor indices match that filtered list, and refresh prunes unavailable selected rows. Derived unread Inbox cards keep their known contract. Board remains count/Clear-only: card Done checks off a card, while keyboard e marks mail read.

Frontend action/undo and keyboard regressions pass, as do Go handler serialization/scoping tests under the race detector. The real-PostgreSQL regression is authored but skipped in this environment; actual SQL execution remains required before this item is closed. This corrects the fabricated representative-row snapshot only; the separate mixed-thread state/undo limitation remains under DATA-04 (pass 7).

## AUTH-POOL-01 / AUTH-BOUND-01 - High / Medium - FIXED locally (2026-10-02)

Passkey enrollment previously opened a credential transaction before consuming
its ceremony and loading the WebAuthn user through the pool. A one-connection
pool deadlocked until the request expired. Verification now consumes the one-use
ceremony and validates the proof before reserving the credential transaction;
bootstrap still rechecks installation state under its advisory lock and owner
lock before writing. Rejected proofs and failed inserts do not restore a
consumed challenge. Synthetic one-connection driver regressions fail on the old
code and pass under `-race` on both register and bootstrap routes.

WebAuthn finish parsing now reads through the documented 16 KiB bound, including
trailing bytes, before calling the library decoder. Exact-boundary and one-byte
over-limit regressions pass for login, registration and bootstrap; oversized
responses receive 413 and the original body is closed. This is not real hardware
passkey/browser verification.

## LIFE-CANCEL-01 / LIFE-DELETE-01 - High - FIXED locally; PostgreSQL lifecycle gate remains (2026-10-02)

An account lease previously counted operations without consistently propagating
its cancellation. Account deletion could wait behind a provider request that
never received cancellation; full-owner deletion also held the owner write-lock
behind request-long read locks. Admitted request/sync/send/export work now joins
caller and account contexts, and resolver-returned adapters bind later provider
methods and live streams to that lifetime while preserving optional interfaces.
The owner read-lock is released after request admission. Single-account deletion
holds the owner read-lock through its seal/drain/delete so a full-owner deletion
cannot mistake an in-progress seal for an already retired account.

A later independent review found admission lookup errors could fall through
without a lease after the owner lock was released. Admission now distinguishes
real missing rows from database/query-scan failure and rejects transient failure
with retryable 503 before entering the handler. A first-query-fails/later-query-
succeeds regression reproduces the bypass and passes with this guard.

Synthetic provider/context regressions, cancellation during dial and streams,
nested-lease preservation, unrelated-account behavior, failed-delete recovery,
and overlapping account/owner deletion pass under `-race`. Existing real-SQL
lifecycle suites remain skipped here. No mailbox or owner was deleted.

## EXPORT-TIMEOUT-01 - Medium - FIXED locally (2026-10-02)

A provider-local timeout was mistaken for cancellation of the whole export.
Fallback now depends on the actual export request context: a live request can
use the mirror with its disclosure, while request/account cancellation stops
the export. Provider deadline and caller-cancellation cases pass in unit tests.

## SYNC-REPAIR-01 - High - IMPLEMENTED locally; PostgreSQL and upstream gates remain (2026-10-02)

Atomic terminal-page completion prevents new stranded staged scans. Historical
scans lack completion evidence and cannot safely be finalized by inference.
`repair-scans` defaults to inspection; mutation requires one exact `--scan` ID
plus `--apply`. The targeted transaction restarts enumeration without deleting
live envelopes, bodies, cursor or filing state, retains policy generation, and
rejects retired late page/finalizer IDs. The CLI guard passes; preservation,
rollback and late-writer tests require PostgreSQL and remain unrun. This tool has
not been run against production. See `docs/scan-repair.md` and the required
Neutron upstream/re-vendor boundary.

## Reliability follow-up (2026-10-02)

Contained fixes and regression coverage are prepared for these confirmed gaps:

- SEND-02: atomic same-key check/admission/publication at enqueue, including concurrent retries, full capacity, worker refusal, and undo; dashboard submissions retain an unchanged draft's key across ambiguous retries. Completed, ambiguous, and cancelled outcomes retain bounded in-process receipts (24 hours / 4096 keys) without message payloads; restart, expiry, and capacity eviction still require the durable-outbox contract below.
- SYNC terminal completion: PgStore commits the final page, pruning, cursor, and generation completion together before body prefetch; cursor resets preserve policy generation. Old already-stranded scans require a fresh non-destructive scan because completion evidence was lost. External stores need the additive FinalScanPageStore interface for the same atomic guarantee. The vendored fix requires matching upstream backflow before merge under mail-engine/VENDOR.md.
- Offline account cleanup: account responses expose mirror_account_id; dashboard cleanup purges both product/mirror identity lenses plus unified snapshots and fences late response writes. A missing identity mapping triggers conservative current-owner snapshot cleanup.
- Offline logout: synchronous private-state teardown clears live drafts, undo closures, and autosaves before storage erasure; asynchronous draft work is owner/generation-fenced. The expanded OFF-02/DRAFT-02 storage implementation is recorded below; native-browser durability validation remains open.

Product, engine and MCP compilation, non-PostgreSQL tests/race checks and vet now pass. PostgreSQL 17 integration execution remains a verification gate. See [reliability boundaries and outbox proposal](docs/reliability-boundaries.md) for contract limits and required checks.

## Isolated durable-outbox candidate review (2026-10-02)

The separately prepared architectural candidate remains outside the default
hardening patch and does not close the standing SEND-01/SEND-03 deferral.
Its review corrected three reproduced candidate defects: encrypted Sent-copy
MIME could exceed its admission reservation after quoted-printable line folding;
cancellation receipt lookup errors incorrectly answered Too Late; and removed
failed/cancelled/ambiguous payloads incorrectly claimed provider acceptance.
The quota now reserves encoding headroom and enforces the reserved-byte bound on
Sent replacement. Regression tests also cover missing/changed encryption keys.

These offline/unit checks do not validate the new PostgreSQL migration, locks,
claims, cascade teardown or crash recovery. The candidate has been reconciled onto a separate frozen copy of the newer
lifecycle/offline hardening. Real PostgreSQL, process-kill and browser recovery
checks remain required before any rollout. See
`docs/durable-outbox.md` for its separate contract and release gates.

Standing decision: **SEND-01 = SEND-03 (pass 6) = lullmail-10 below.** The
durable-outbox finding appears in both later reports; it is the same item
as this register's one recorded product deferral, and the deferral stands.

## F07 (round 3) - Medium - PARTIALLY FIXED (contained guard landed), submission transport DEFERRED (product)

**JMAP accounts have no verified send transport**

- Kind: Product gap surfaced as a defect
- Evidence: Confirmed per the report - `deliveryFor` special-cases Gmail and Graph and sends everything else through `SMTPFor`, which for a JMAP account dials the API host on port 587 with the API token as an SMTP password. The send was accepted into the queue and failed asynchronously after the undo window closed, losing the draft.
- Fixed (round 3, commits tagged `audit 4-F07`): `handleSend` rejects both the fresh-send and reply-parent paths synchronously with 422 "Sending Not Configured" for JMAP accounts, before queue acceptance - the draft stays in the composer instead of silently dying in delivery.
- Deferral (remainder): implementing the real transport is a product feature, not a repair: either JMAP EmailSubmission against the session-advertised capability or a separately verified SMTP submission credential (a transport discriminator plus encrypted SMTP fields on the account). Until one lands, JMAP accounts are read-only for sending by design of the guard; the report's `can_send` accounts-API surface belongs to that same feature.

## F05 (round 3) - Medium - FIXED (offline-v2 storage landed)

**Draft persistence failed silently on large undo seeds and dropped attachment-only drafts**

- Kind: Storage-layer defect + design remainder
- Evidence: Confirmed per the report - the ring serialized full base64 attachment sets into localStorage and suppressed write errors; a multi-megabyte undo seed blew the quota and dropped the whole ring's save; `restoreDrafts` tested only to/subject/body.
- Fixed (round 3, commits tagged `audit 4-F05`): the transitional patch kept the ring in localStorage with payloads in IndexedDB, surfaced quota failures, and kept attachment-only drafts restorable.
- Fixed (remainder, offline-v2 `9e52433`): one draft is ONE IndexedDB record — fields and attachment payloads in the same row, the draft ring those rows in seq order — so the two-engine atomicity gap and the localStorage quota ceiling are both gone. Regression (fake-indexeddb): field edits merge into the single record without clobbering its attachments (`offline-storage.test.ts`).

## lullmail__lullmail-10 - P2 / SEND-03 (pass 6) / SEND-01 (pass 7) High - DEFERRED (decision)

**Expose a durable send outcome instead of only an undo token**

- Kind: Improvement / reliability
- Evidence: enqueue immediately returns queued/undo_seconds. The worker sends its result to a buffered done channel, logs failures and removes the map entry. The inspected product routes expose undo but no delivery-status lookup; no reader of done appears in the inspected queue code.
- Impact: The enqueue response is not a delivery acknowledgment. A later provider failure or restart during the undo window has no durable outcome in this path. The frontend draft lifecycle was not inspected, so draft loss itself is not asserted.
- Proposed fix: Add persistent outbox states and a delivery-result event or status endpoint. Retain/recover draft content until an outcome is known; distinguish accepted, submitting, submitted, failed and ambiguous. Define retry/idempotency behavior rather than blindly retrying sends.
- Deferral: the original sendqueue.go design comment recorded the decision this item argues against — "Five seconds is the whole feature — no queue table, no worker, just a timer map" (SPEC §6.1; the in-process undo window IS the shipped feature). A durable outbox with delivery states, a status endpoint, and retry/idempotency semantics is a product redesign of that surface, not a defect repair; reopening it is a product call, not an audit action. Pass-7 SEND-01 restates it with a full outbox_jobs design; same item, deferral stands. SEND-04 (durable Sent-copy filing state, pass 7) is a requirement on the same replacement and is deferred with it — today a failed Sent-file is logged (never silently dropped) and the delivered message is never re-sent. Partial hardening landed in pass 7: the queue now has an aggregate admission budget and delivery holds an account-use lease, so the volatile window is bounded and fenced (commits tagged `audit 3-SEND-02`, `audit 3-SEND-03`).
- Review commit: `49d159b6654d3dbd27866f87ac783402f19c6cb7` (last reviewed 2026-09-10)

## AUTH-02 (pass 7) / AUTH-06 (pass 6) - Medium - FIXED (lifecycle + re-auth pass, 2026-09-18)

**Long-lived sessions can enroll durable replacement credentials without fresh proof**

- Kind: Hardening; requires an already compromised authenticated session
- Evidence: Confirmed per both reports — factor enrollment, recovery-code regeneration and agent-token creation require only a standing session. Password replacement and full account deletion already require fresh proof (the report credits this).
- Fixed (`b9c1027` + `b889d95`): the re-authentication ceremony landed as the roadmap item it was. `POST /security/reauthenticate` verifies the standing password with the same KDF admission, failure counting, and account lockout as sign-in, then stamps `reauthenticated_at` on the confirming session (epoch-matched; migration 5). Every credential-enrolling mutation — TOTP begin/confirm/delete, password set/delete, recovery-code regeneration, agent-token creation — refuses with 428 unless the session is younger than ten minutes or was confirmed within ten minutes; a fresh sign-in satisfies the gate by construction (the same rule full-account deletion applies), the setup token passes (it retires with the first credential), and passkey ceremonies stay outside the gate because WebAuthn user verification IS the fresh proof. The dashboard turns a 428 into an inline password-confirmation dialog that retries the parked action; a wrong confirmation keeps it parked with the server's message. Regressions on the live-PostgreSQL harness: stale-session 428s across the gated surface, fresh-session and confirmed-session passes, stamp expiry, wrong-password counting into the account lockout (`TestIntegrationReauthFreshnessGate`, `TestIntegrationReauthenticateWrongPasswordAndLockout`).

## AUTH-06 (pass 7) - Medium - FIXED (lifecycle + re-auth pass, 2026-09-18)

**Standalone TOTP guessing is limited per peer, not by a shared account budget**

- Evidence: Confirmed — replay protection exists (step consumption) and the per-peer limiter is the repaired fail-closed parser, but guesses from many peers against one account share no budget. TOTP is an intentional alternative credential, not a second factor.
- Fixed (`b9c1027`): the report's `auth_factor_windows` design landed with product migration 5 — a durable per-user fixed five-minute window (server-computed boundaries, never client values) counts every failed standalone-TOTP guess through an atomic upsert; ten failures from any set of peers exhaust the window and further attempts — including the correct code — answer 429 with `Retry-After` to the boundary. The fixed deadline never extends on new failures, spent windows prune after a day in the housekeeping purge, the per-peer limiter stays in front, and the password/passkey/recovery paths are untouched (a shared budget is still a denial-of-service lever against the owner — the window is short and bounded for exactly that reason; ingress rate limiting for exposed deployments remains the deployment-policy item it was under OPS-05/OPS-06 below). Regressions: distributed exhaustion across distinct peers, correct-code refusal, rollover after the window passes, per-user isolation (`TestIntegrationTOTPPerUserBudgetExhaustion`, `TestIntegrationTOTPBudgetIsPerUser`).

## AUTH-07 (remainder) / AUTH-03 (pass 7) - Medium - DEFERRED (contained fix landed, installation design deferred)

**First-run completion is not a single transactional installation transition**

- Kind: Concurrency gap (remainder)
- Evidence: The pass-7 report proved the pass-6 recheck was insufficient for the no-owner case: two ceremonies with different names create different owner rows, lock different rows, and the global `ownerConfiguredDB` check under a row lock cannot serialize them. FIXED in pass 7: both bootstrap finish paths now take a transaction-scoped installation-wide advisory lock before the configured check, so two concurrent ceremonies can no longer both install a first credential (commit tagged `audit 3-AUTH-03`).
- Deferral (remainder): `ensureUser` can still leave a credential-less second user row behind when two begins race or `LULL_USER_EMAIL` changes between boots, and the report's fuller design (users-single-owner unique index after operator reconciliation, ceremony-bound setup epochs, transaction-aware owner initializer, provisional ceremony input) is a startup/config architecture change. The residual is stray rows with no sign-in capability behind a setup token that retires on first completion.

## AUTH-04 (remainder) - Medium - DEFERRED (contained fix landed, runtime snapshot deferred)

**Setup mutates shared configuration without a consistent synchronization boundary**

- Evidence: Confirmed. FIXED in pass 7: `setOriginForSetup` now validates the candidate on a Config copy and publishes origin + WebAuthn instance together only after construction succeeds, and `handleLoginFinish` reads the instance through the locked accessor (commit tagged `audit 3-AUTH-04`). Bootstrap token retirement already mutates under waMu.
- Deferral (remainder): Other request handlers still read `a.cfg` fields directly rather than one immutable published snapshot, so a reader can mix pre- and post-swap values mid-request. The report's `atomic.Pointer[runtimeAuth]` migration of every reader is a cross-cutting refactor of the auth/config surface; defer as its own pass. The contained fix removed the dangerous case (a failed setup leaving a half-updated live config).

## WEB-04 (pass 6) / WEB-03 (pass 7) / R07 (round 3) - Medium - FIXED (idempotency + offline-v2 pass, 2026-09-18)

**Offline replay is not coordinated across tabs or made idempotent at the server**

- Kind: Concurrency/uncertain-retry gap
- Evidence: Confirmed per both reports — each tab can replay the shared queue and no idempotency key exists server-side. Pass 7 landed the retry-scheduling and rejection-surfacing companions (WEB-04/WEB-05 there), so failures are no longer stranded or invisible; round 3 (F09/F10) additionally made replay strictly order-preserving and self-rescheduling on network-only failures. Duplication under multi-tab replay or a lost acknowledgment remained.
- Fixed (server, `0b30f84`): the `api_mutations` table (product migration 4, versioned runner from the OPS-02 work) keys one recorded answer per (user, Idempotency-Key ≤128 chars) with request-hash conflict detection (409 on reuse with a different request); the recorded answer — success or handler error — replays verbatim on a lost acknowledgment, recorded answers prune after 30 days, and the buffered idempotency copy of the request body is capped at 1 MiB. Every mutable product endpoint takes the key.
- Fixed (client, `6f99201`): every queueable mutation mints its key before the first fetch attempt, queued rows carry it, replay sends it, and the replay pass runs under a Web Locks held lock (uncoordinated fallback only where `navigator.locks` is absent — safe now that the server contract exists).
- Regressions: `TestIntegrationConcurrentSameKeyMutationAppliesOnce` (parallel same-key racers apply exactly once), `TestIntegrationLostAcknowledgmentReplaysRecordedResponse`, `TestIntegrationIdempotencyKeyConflictRejectsDifferentRequest`, `TestIntegrationIdempotentHandlerErrorIsReplayed`, `TestIntegrationIdempotencyKeyLengthAndBodyBound`; dashboard-side key minting/transport tests.

## WEB-07 (pass 6) / WEB-01 (pass 7) / R08+F11+F12 remainders (round 3) - Medium - FIXED (offline-v2 pass, 2026-09-18)

**Browser caches and drafts need an immutable owner namespace and generation fencing**

- Kind: Isolation gap; exposure depends on logout/reset paths
- Evidence: Confirmed per both reports — persistent owner identity is the (mutable, reusable) email; drafts use global storage keys; in-flight reads are not fenced against an owner change. Pass 7 made the wipe itself atomic and error-propagating (WEB-02 there). Round 3 landed the contained halves: fail-closed suspension on storage-preparation failure (F11) and disconnect/retention snapshot purge (F12).
- Fixed (remainder, offline-v2 `9e52433`): every offline store is namespaced `installation_id/user_id` (the server mints the installation id into `app_settings`; both ids ride `/auth/status`); a generation counter bumped on every owner/account switch and wipe fences every async authenticated read — a stale-generation write is discarded inside the write transaction, never published; cache records are per-mailbox so one account's deletion keeps other mailboxes' snapshots; a one-time migration carries the SAME owner's parked drafts and pending queue from the v1 engine and wipes a different owner's remnants. Logout semantics are the founder-ratified ones: logout/owner switch clears that owner's caches, queue, and drafts (no survivorship) — implemented as the one-transaction wipe with the generation counter deliberately never removed.
- Fixed (follow-up, `0f3b8cc`): the v1-remnant wipe inside the one-time migration stripped the just-written namespace on a fresh device, silently no-op'ing every offline store; the migration now runs before the markers are written. Caught by the first real-IndexedDB tests (fake-indexeddb): the logout wipe clears caches/queue/attachments/drafts, drops the namespace marker, and keeps the generation counting.

## DATA-06 (pass 6) / DATA-11 (pass 7) - Medium - FIXED (idempotency + offline-v2 pass, 2026-09-18)

**Bucket/search result limits have no continuation contract**

- Evidence: Confirmed — buckets cap at 200 threads and search at 60 with no cursor; a client cannot distinguish truncation from completeness. FIXED in pass 7: the deterministic tie-breaker (received_at DESC, id DESC) landed on bucket and search ordering, so equal-timestamp rows no longer reorder between requests (commit tagged `audit 3-DATA-11`).
- Fixed (remainder, `3df8501`): buckets and search answer `{rows, has_more, next_cursor}` and accept `?cursor=` + `?limit=` (1-200, clamped). The cursor pins the last row's (received_at DESC NULLS LAST, id DESC) key, so rows arriving between pages never duplicate or skip what earlier pages delivered; received_at comparisons go through `AT TIME ZONE 'UTC'` because the mirror stores UTC-naive timestamps. Dashboard buckets and search page at 50 with a load-more tail (board and calendar unwrap page one); MCP `list_bucket` and `search_mail` expose limit/cursor and document the iteration. Regressions: `TestIntegrationKeysetPaginationStableUnderInsertion` (walk to exhaustion while inserting), malformed-cursor 400, limit clamping.

## DATA-07 (pass 6) / DATA-05 (pass 7) - Medium - FIXED (idempotency + offline-v2 pass, 2026-09-18)

**Relative snooze durations make offline replay and undo change the intended date**

- Evidence: Confirmed — snoozes store server-relative day counts; replay applies them later; undo reconstructs a prior snooze through rounded remaining days. Pass 7 did not change this.
- Fixed (`0e294ef`): `set_aside` takes `until` — an absolute RFC3339 instant captured at user-intent time and stored verbatim, so offline replay applies the exact intended deadline no matter when it runs; `until: null` parks the thread as someday (the later bucket, no date); `until_days` stays as deprecated server-relative compat. The response echoes the applied bucket and `snooze_until` so undo snapshots are exact: the dashboard captures the row's prior instant verbatim and restores it (the old path re-derived rounded day counts from remaining time), and the SnoozeMenu deadline is computed at click time. MCP `message_action` takes `until` (or null) with the legacy days form still accepted. Regressions: `TestIntegrationSnoozeUntilAbsoluteAndUndoExactness` (microsecond equality through move-and-restore), `TestIntegrationSnoozeUntilNullIsSomeday`, `TestIntegrationSnoozeValidationAndLegacyPath` (garbage-until 422, legacy bounds).

## DATA-04 (pass 7) / R11+F20 remainder (round 3) - Medium - DEFERRED (product design)

**Thread-wide mutations cannot be undone exactly from one summary row**

- Evidence: Confirmed — the action handler mutates every message in the resolved thread while the browser inverse is computed from one list row's flags, so mixed read/bucket state inside a thread is lost on undo. (Per-row snapshots already make single-row read/unread undo exact.) Round 3 removed the adjacent board-pin variant's worst case: re-pins no longer clobber the standing card and the undo deletes only cards the pin created (F20, commits tagged `audit 4-F20`); an edit landing between a pin and its undo is still not conflict-detected.
- Deferral: Exact undo needs server-side preimage capture (row versions, one-use undo records, conflict rejection) — the report's `message_action_undo` design is a schema + API + client change across the same surface as the idempotency work in WEB-04. Deferred as product redesign; the current undo is documented as approximate for mixed threads.

## DATA-08 (pass 7) - Medium - FIXED (staged reconciliation pass, 2026-09-18)

**Backfill and retention settings can commit before their corresponding data transition succeeds**

- Evidence: Confirmed — the setting update and the pruning/classification/retention that realize it are separate operations; a later failure leaves the new policy committed with an error response and no durable reconciliation status. (DATA-10's fix in pass 7 means post-sync failures are no longer reported as healthy syncs.)
- Fixed: the report's design landed as committed. `email_accounts.policy_version`/`applied_policy_version` plus `account_reconcile_jobs` (product migration 3); the setting update and the durable job commit in ONE transaction; the endpoints answer 202 with the job state (`full_enumeration` true exactly when the window widened); a worker consumes the job and advances `applied_policy_version` only when the desired version still matches — an older job can never mark a newer version applied (regression: `TestIntegrationOlderReconcileJobNeverMarksNewerVersionApplied`). The job is durable across restarts ('running' rows reset to pending at boot; interrupted jobs resume staged scans). The account item API exposes the job state as `reconcile` so a UI can show "rebuilding retained history" until completion.

## SYNC-03 (pass 6) / SYNC-01 (pass 7) - High - FIXED (staged reconciliation pass, 2026-09-18)

**Destructive cursor reset can erase local filing state before a rebuild succeeds**

- Evidence: Confirmed ordering: an invalid cursor drops local mailbox state before the replacement enumeration exists; if the rebuild then fails, orphan cleanup can read the missing mirror rows as authoritative deletions of user filing state.
- Fixed: staged, resumable reconciliation is the engine's only recovery path. `mirror_scans`/`mirror_scan_seen` (engine migration 2) hold one durable scan per mailbox: every page commits envelopes + seen IDs + continuation in one transaction (`ApplyScanPage`), and the ONLY deletion in the recovery path is `FinishScan`'s single completion transaction (prune memberships absent from the complete seen set, delete membership-less messages, publish the terminal cursor, drop the scan rows). Cursor invalidation, provider `Reset`, and every full enumeration now enter scans (the pass-7 SYNC-02 durable-bookkeeping fold-in), so MaxPages cuts and process restarts resume from the stored continuation instead of losing the seen set. Product orphan cleanup can no longer observe a mid-rebuild mirror because the mirror is never removed mid-rebuild. Regressions at every interruption phase (offline matrix `TestStagedScanInterruptionAtEveryPhaseNeverPrunes`, real-PG restart proof `TestIntegrationStagedScanSurvivesKillBetweenPages`, end-to-end `TestIntegrationReconcileInterruptionKeepsStateAndResumes`); multi-mailbox membership survives the prune (`...FinishScanKeepsOtherMailboxMembership`). Stores that do not implement `ScanStore` keep the legacy destructive path — PgStore, the only production store, implements it.

## SYNC-04 (pass 6) / SYNC-04 (pass 7) - Medium - FIXED (staged reconciliation pass, 2026-09-18)

**Retention and mirror writes can race, leaving expired or orphaned data**

- Evidence: Confirmed — retention deletes mirror rows while sync/body writes run; the mirror has no foreign keys tying bodies and memberships to messages.
- Fixed: engine migration 3 runs the orphan audit first, then lands `mail_bodies_message_fk` and `mail_membership_message_fk` as NOT VALID → VALIDATED `ON DELETE CASCADE` constraints, so a body or membership whose parent expired can never be written (the mid-race body write is refused by the FK and surfaces as the prefetch path's best-effort warn). Every transaction that writes or deletes mirror rows — engine writeback and scan staging/completion on the pgx pool, product retention, orphan cleanup, and account deletion on the database/sql pool — takes `pg_advisory_xact_lock(mail.AccountLockKey(account))` as its FIRST statement; one lock per transaction is the entire, deadlock-free lock order (documented in `mail-engine/schema.go`). Envelope re-insertion after expiry converges through the post-sync retention sweep rather than ingestion-time filtering — the report's preferred shape, but the FK plus sweep satisfies the regression's finish line (final state honors policy, zero orphans) under `-race` (`TestIntegrationProductRetentionRacingEngineSync`, engine-side `TestIntegrationConcurrentRetentionAndSyncLeaveNoOrphans`).

## SYNC-05 (pass 6) / DATA-09 (pass 7) - Medium - FIXED (staged reconciliation pass, 2026-09-18)

**Increasing retention does not restore older messages removed from the mirror**

- Evidence: Confirmed — an incremental provider will not re-report unchanged old messages after local retention removed them; widening the window changes policy only.
- Fixed: `needsRetentionExpansion(old, new)` gates a `full_enumeration` reconcile job (old bounded AND (new unbounded OR wider)); the worker drives `Engine.RequestRescan` — staged scans for every mailbox, the same non-destructive machinery as SYNC-03, never the destructive reset — and the job is consumed (state complete, `applied_policy_version` advanced) only after every scan finished under the new policy. An interrupted rebuild leaves the request pending with its staged progress durable. Regression: `TestIntegrationRetentionIncreaseRestoresOlderMessages` (prune an unchanged month-old message at 7 days, widen to 90, provider reports no delta — the message returns), plus the interruption/resume case.

## SYNC-03 (pass 7) - High - PARTIALLY FIXED (ordering), cross-pool promotion migration DEFERRED (needs OPS-03 bridge)

**Identity promotion deletes the old message before its replacement is written**

- Evidence: Confirmed — `apply` called `DeleteMessages` on the old identity before `PutEnvelopes` wrote the new one, so a failed write lost the only readable copy. FIXED in pass 7: the replacement envelope is now written first and the old identity retires only afterwards, so a failure leaves the old record intact and the next sync retries (commit tagged `audit 3-SYNC-03`). The staged-scan pass preserves this ordering inside scans (promotions retire after `ApplyScanPage` staged the replacement).
- Deferral (remainder): Product filing state keyed by the old account/message id is still not migrated on promotion (the engine cannot reach product tables; the orphan cleanup eventually drops it), and a truly transactional promotion carrying memberships, body, filing, and receipts across both pools needs the OPS-03 cross-pool transaction bridge — one transaction spanning the engine's pgx pool and the product's database/sql pool. The bridge remains deferred as its own infrastructure item (it is also the prerequisite for transactional cross-pool account creation, see OPS-09); the identity-carrying migration lands with it.

## GMAIL-IMPORT-01 - High - FIXED locally with GMAIL-READ-01 / GMAIL-AUTH-01; upstream and provider gates remain

The report in contributor [PR #1](https://github.com/lullmail/lullmail/pull/1)
led to current-source synthetic regressions. The frozen source reproduced the
metadata-throttle page loss, refreshable 401 becoming permanent reauth, and
invalid_grant admission retry defects. The independently implemented fixes are
now part of the default hardening-plus-Gmail candidate; see GMAIL-READ-01 /
GMAIL-AUTH-01 above and `docs/gmail-read-reliability.md` for precise behavior.
The contributor patch was not applied or accepted, and its live performance
figures are not claimed as verified.

100-ID pages, bounded paced read retries and shared process-local cooldowns
preserve staged continuation, generation and seen evidence. Metadata is still
refetched across labels; no cached-ID dedup shortcut changes flags or membership.
One origin-restricted GET-only credential recovery uses existing encrypted
storage, a cancellable refresh lock and CAS. No send or label-mutation retry was
introduced, and the body-prefetch default is unchanged. Google quota profiles
can differ for grandfathered projects. PostgreSQL, live-provider and Neutron
upstream/re-vendor requirements remain separate release gates.

## GMAIL-03 - Low - DEFERRED

**Attachment flags are inferred from MIME structure excluded by metadata requests**

- Evidence: Confirmed — envelope requests use format=metadata, which does not return the part tree that `payloadHasAttachment` walks.
- Deferral: The honest fix is a tri-state attachment property (unknown/present/absent) flowing through the envelope model, the mirror schema and the dashboard badge — a model change. Requesting format=full for every envelope would multiply quota cost for every sync; the report itself says not to deploy that without measurement.

## GRAPH-02 / PROVIDER-01 (pass 7) - Medium - DEFERRED

**Graph message identity changes on folder moves because immutable IDs are not requested**

- Evidence: Confirmed per both reports — no `Prefer: IdType="ImmutableId"` header on any request, while Graph default IDs are mutable on moves.
- Deferral: Both reports are explicit that a header-only rollout is wrong: existing mirrors hold default-format IDs, and switching the preference without translating them changes every message's identity overnight (duplicate threads, lost filing state). The mandatory accompaniment is an ID-translation migration of mirror memberships, bodies, product filing and push receipts, plus a real Graph test mailbox regression. That migration touches production data and needs a provider integration environment; it is planned Graph work, not a sweep item.

## OPS-01 (pass 6) / OPS-01 (pass 7) / R19+F14 remainder (round 3) - Medium - FIXED (idempotency + offline-v2 pass, 2026-09-18)

**Request, provider-response and export resource limits are incomplete**

- Evidence: Confirmed scope: many JSON handlers read bodies uncapped; provider reads and export staging allocate before validation. Partial bounds existed earlier (send handler 34 MiB wire cap, push 64 KiB, per-attachment caps, engine-side 90 MB part cap; pass 7 added strict bounded single-document decoding with unknown-field rejection on the account-settings routes and an aggregate send-queue admission budget; round 3 made that budget count retained header fields, F14).
- Fixed (remainder, `f281466`): every request-body decode is bounded ahead of allocation with `http.MaxBytesReader` per route (`decodeJSON`/`decodeJSONLimit`, 64 KiB default; oversized bodies answer 413, not malformed-JSON 400); the send route takes one of 2 global decode slots BEFORE its 34 MiB reader allocates (queue at admission, 503 on give-up, ~68 MiB peak decode memory); every provider JSON path decodes through a bounded reader (OAuth identity/graphCall 4 MiB, engine Graph/JMAP method responses 64 MiB, JMAP session 4 MiB, token callback 1 MiB, error bodies 2 KiB); export builds write through a 2 GiB `budgetedWriter`, cap each provider raw-message read at 128 MiB (mirror fallback beyond), and sweep stale temp archives synchronously ahead of ListenAndServe. `docs/route-limits.md` is the route/bound register this deferral required. Offline tests cover the budgeted writer, slot admission, and the 413-vs-400 handler contract.

## OPS-02 (pass 6) / OPS-02+OPS-03 (pass 7) - Medium - FIXED (staged reconciliation pass, 2026-09-18)

**Database connection budgeting and startup migrations are not production-safe by construction**

- Evidence: Confirmed. Fixed in pass 7: the product pool is bounded (16 open / 4 idle, 5-minute idle / 30-minute lifetime caps) and the per-request `last_seen_at` session write is throttled to once per minute with a read fallback that still refuses revoked sessions immediately (commit tagged `audit 3-OPS-02`).
- Fixed (remainder = pass-7 OPS-03): both schema owners now converge through versioned migration runners with a ledger, checksums, and real advisory-lock serialization — the engine (`mail_migrations`, `mail-engine/migrate.go`) and the product (`app_migrations`, `migrate.go`). Version 1 on each side is the old boot-time statement list verbatim, so every existing database converges by recording it and moving on; nothing is rewritten in place, and a modified already-applied migration refuses to boot. `pg_advisory_lock` serializes concurrent runners on a dedicated connection (bootstrap inside the lock — two concurrent `CREATE TABLE IF NOT EXISTS` race on the catalog), with the pooled connection discarded rather than returned if the unlock fails. The ledger bootstrap and every migration application run in one transaction per version. Boot order is fixed everywhere (engine first, then product: product v2 reads `mail_messages`) so the two runners can never deadlock. Regressions: convergence of a simulated pre-ledger database with live data on both sides, two concurrent runners recording each version exactly once, and the direct advisory-lock proof (a runner waits while another session holds the key, then proceeds). Nucleus backends without `pg_advisory_lock` still migrate sequentially (probed once; the ledger's primary key remains the backstop) — lullmail deployments run real PostgreSQL. Per-suite database isolation for potentially destructive harness suites remains CI infrastructure work tracked under OPS-07 below.

## OPS-04 (pass 6) / OPS-04 (pass 7) - Medium - FIXED (lifecycle + re-auth pass, 2026-09-18)

**Shutdown does not join all application-owned background work**

- Evidence: Confirmed per both reports — async sends, initial syncs and post-sync work run on background contexts; pools close after the HTTP drain without joining them. Pass 7 bounded the finishSync detached context (previously bare `context.Background()`), so finalization work now carries its own deadline.
- Fixed (`c341e02`): one `backgroundTasks` group owns every background launch — scheduler, housekeeping loop, initial/manual/OAuth-callback syncs, reconcile jobs, push dispatches, and send-delivery workers — with admission refused once the drain begins. `stopBackground` cancels the group root and joins in-flight work (bounded 30 s) after the HTTP drain and BEFORE the pools close; account-gate contexts derive from the same root, so the cancellation reaches provider I/O. The writeback-noise residual is gone both ways: the join lets finalization land while the pools are open, and a sync canceled by the drain owes no finalization (no spurious `last_error`, no failed-record logs). Regressions: `TestIntegrationShutdownDrainsInFlightSync` (the join outlasts the provider call's post-cancel cleanup; cancellation reached the adapter; `last_error` stays clean; post-drain admission refused).
- The deferral's IMAP-02 note is superseded: the residual it recorded (failed writeback noise at shutdown) is what this pass removed.

## OPS-05 (pass 7) - Medium - FIXED (lifecycle + re-auth pass, 2026-09-18)

**Deleting one account can wait behind unrelated account work and uncancellable locks**

- Evidence: Confirmed — the lifecycle gate was one global owner RWMutex; a long read on account A delays account B's deletion, and engine/OAuth refresh mutexes do not observe cancellation.
- Fixed (`c341e02`): per-account admission gates replace the global lock on the work path — an active-operation counter sealed by deletion plus a per-account context derived from the background root. Deletion seals the gate (new admissions fail), CANCELS the account context so in-flight provider I/O aborts, and waits for the count to drain bounded by the request context plus a hard cap; an uncommitted deletion resurrects the gate. The global owner lock survives only around full-owner deletion (which enumerates, seals, and drains every account while blocking creation and gated requests), account creation, and the middleware that holds a request's per-account lease. Duplicate manual syncs coalesce on a context-aware per-account lock: a queued waiter returns immediately on cancellation without another provider connection (the engine's own per-account mutex is unchanged — its critical sections are ctx-aware provider calls). Regressions: `TestIntegrationAccountDeletionIndependentAndCancelling` (B deletes while A's sync is parked; A's deletion cancels A's provider call and drains), `TestIntegrationDuplicateSyncCoalescesOnContextGate`, plus the offline gate-matrix in `accounts_test.go`.

## OPS-05 (pass 6) / OPS-06 (pass 7) - Medium - DEFERRED

**Default port publication and URL configuration permit unintended plaintext exposure**

- Evidence: Confirmed per both reports (deployment hardening, not an unauthenticated-login finding): compose publishes 8080 on all interfaces, HTTP origins away from loopback are accepted, origin detection trusts forwarded headers without a proxy-peer check.
- Deferral: Binding the compose default to loopback silently breaks every existing deployment whose ingress dials the published port, and enforcing HTTPS-only origins plus trusted-proxy forwarded parsing needs the same deployment-policy decision as pass-6 AUTH-01. The dashboard already surfaces a public-exposure warning; the enforcement change belongs to a release-noted deployment-policy update, not a sweep.

## OPS-07 (remainder) - Medium - PARTIALLY FIXED (product harness and migration tests landed), per-suite isolation DEFERRED

**CI lacks real SQL integration coverage**

- Evidence: Confirmed — engine integration tests skip without `NEUTRON_MAIL_TEST_DATABASE_URL`. FIXED in pass 7: CI now runs a disposable postgres:17 service job that executes the engine store integration suite and race runs for all three Go modules (commit tagged `audit 3-OPS-07`); the normal green build no longer excludes every database test.
- Fixed (remainder pass, 2026-09-18, `556e272`): `LULL_TEST_DATABASE_URL` gates a product-side integration suite with the same skip-offline contract as the engine's — it resets only product-owned tables, re-runs the real schema migration entry point inside one transaction, and executes the production push candidate SQL (hoisted to `pushCandidateSQL` so the suite runs the shipped statement, not a copy) plus the auth-epoch schema/lookup sync checks. The CI postgres job runs it with `-race` alongside the engine and MCP modules; the harness was verified end-to-end against a live local PostgreSQL (disposable database, created and dropped for the run).
- Deferral (remainder): Isolated per-suite databases for potentially destructive suites remain CI infrastructure work. Migration-convergence, migration-advisory-lock and concurrent-runner regressions already exist in both modules; they are not missing implementation, but their PostgreSQL execution is unavailable in this environment. The harness precondition this register recorded was met — AUTH-01's race regression tests landed through it (`4fbd4f6`) and the staged reconciliation pass (SYNC-03/04/05, DATA-08/09, OPS-02) landed through it with kill-between-pages, concurrency, and migration-convergence regressions on both suites — so no open finding still waits on the harness.

## OPS-09 (pass 6) / OPS-09+PROVIDER-03-remainder (pass 7) - Medium/Low - DEFERRED

**Outbound URL trust boundaries are implicit, including push endpoints and authenticated continuations**

- Evidence: Confirmed as conditional hardening — push registration accepts arbitrary endpoints; JMAP sends tokens to session-advertised origins. Pass 7 landed the Graph half: continuation URLs are now validated against the configured origin/path before any request (PROVIDER-02 fix), and OAuth account creation joined the owner lifecycle gate with duplicate-address rejection (the contained half of PROVIDER-03).
- Deferral (remainder): Provider-specific HTTPS allowlists, redirect policies, and resolved-address validation for configurable hosts is a deliberate egress-policy design (and user-selected private mail servers are an intentional feature, so a blanket ban is wrong). The cross-pool transactional account creation both reports describe needs the OPS-03 bridge. Deferred with the deployment-policy pass (OPS-05 above).

## OPS-10 - Low - DEFERRED

**Secret-key overrides, local privacy and agent credentials need explicit security policies**

- Evidence: Hardening per the report: SECRET_KEY accepts arbitrary text, agent tokens are broad and unexpiry'd, no key rotation/versioning.
- Deferral: Key IDs + AEAD additional data with re-encryption migration, agent-token scopes/expiry with per-token migration, and documented retention semantics are each their own security-feature tasks; the secure defaults that exist today (generated 32-byte keys, agent route denylist, auth-surface fencing) hold the line meanwhile.

## SEND-01 (audit 5) - High - DEFERRED (standing lullmail-10 product decision)

**Accepted sends and their only recoverable draft can disappear**

- Evidence: Confirmed per the report — the queue is in-process, worker errors are logged, the composer retires the draft on the queued response, and a restart or provider failure inside the window loses both. This is the same finding as SEND-01/SEND-03 (pass 7)/lullmail-10 above, restated with the full durable-outbox design; the recorded product deferral stands.
- Contained hardening (audit 5 SEND-02/LIFE-03, extended in the reliability follow-up): acceptance claims are atomic, and completion/ambiguous/cancelled receipts retain the request hash and original token for up to 24 hours, bounded to 4096 keys. A different body under a retained key is 409. Shutdown-refused work unrolls reservations and answers 503; accepted work cancelled before submission retains a cancelled receipt. The dashboard carries stable submission keys and does not offer undo after a replayed zero-second window. Restart, receipt expiry, and capacity eviction remain outside the guarantee and require the durable outbox the deferral describes.

## SYNC-02 (audit 5) - High - DEFERRED (standing GRAPH-02/PROVIDER-01 decision)

**Graph message identities are not made stable across folder moves**

- Evidence: Confirmed; identical to GRAPH-02/PROVIDER-01 (pass 7) above — the `Prefer: IdType="ImmutableId"` header cannot roll out without translating every stored default-format ID across envelopes, bodies, memberships, product filing, and push receipts. The standing deferral and its provider-integration prerequisite are unchanged.

## API-01 (audit 5) - High / API-02 (audit 5) - Medium - IMPLEMENTED, PostgreSQL validation pending

**The mutation ledger and its local domain writes must commit together**

- Prior evidence: the wrapper held a ledger transaction while handlers committed independently. A crash could retain the domain write but lose its key; concurrent requests could exhaust the pool while each held an outer connection and requested an inner one.
- Implementation: all ten wrapped board, note, screener, and message-action routes share the ledger transaction through mutationDB/beginMutation. Owner-row locking precedes ledger insertion to prevent foreign-key lock-upgrade deadlocks. This intentionally serializes short, database-only keyed mutations per owner.
- Error contract: a savepoint rolls back partial handler writes before deterministic rejections are recorded. HTTP 408/429/5xx roll back the claim too, allowing same-key retry. Migration 7 stores bounded response metadata (Content-Type, Location, Retry-After, Cache-Control); cookies/authentication/transport headers are not replayed.
- Regressions prepared: all wrapped routes on a one-connection pool; ledger-write failure and handler panic roll back domain state; transient failures retry safely; deterministic rejections preserve metadata without side effects; sixteen distinct-key screener decisions do not deadlock.
- Executed: product compilation, non-PostgreSQL race tests and vet. Remaining gate: real PostgreSQL race/integration execution, including the one-connection route matrix and rollback/deadlock tests. The durable-send/outbox contract is separate and remains open.

## OFF-02 (audit 5) - High - IMPLEMENTED locally; native-browser validation pending

**Owner changes must fence storage, replay and UI publication atomically**

The earlier response/cache/queue-generation fixes now have a v4 IndexedDB
metadata authority. Cache, draft and queue operations include `meta` in their
transaction, so missing/delayed cross-tab events cannot authorize stale data.
LocalStorage remains an invalidation/display mirror. Admitted identities and
same-tab transition versions fence obsolete preparations, logout and suspension;
failed wipe retry intent remains bound to its original owner/generation. A
newer same-owner invalidation is conservatively erased before fresh admission.

Account-cache purge recovery uses unique owner/generation-scoped persisted
hints. Pending matching hints disable snapshot reads; fresh admission clears
cache only and acknowledges only the recovered hints. Query/open/transaction
failures, unrelated successful purges, delayed marker publication, v3 upgrade,
and newer-owner preservation have synthetic regressions. Drafts and queued
mutations are preserved by cache-only recovery. If both persistence layers fail,
physical deletion and cross-reload recovery cannot be promised; failures are
surfaced and the current tab remains suspended.

Replay checks authority per item, preserves mutation keys/order/backoff, skips
contended Web Locks, propagates lock failures without a second fallback pass,
and bounds network/error-body attempts to 30 seconds before persisting retry
backoff. Without Web Locks, replay is uncoordinated across tabs and still relies
on server-side idempotency. The four storage/reliability suites pass 90 tests with fake IndexedDB.
Native browser upgrade/crash/quota/cross-tab checks remain unrun; this is not a
claim that IndexedDB transactions stop an already-transmitted server request.

## DRAFT-02 (audit 5) - Medium - IMPLEMENTED locally; native-browser validation pending

**Async reads and delayed saves must not outlive or overwrite their draft**

Draft records use monotonic revision comparison and durable retirement
tombstones in the same IndexedDB transaction. An unchanged hydration write does
not advance revision; background reads do not silently authorize a stale editor.
Stale save/discard conflicts are surfaced and require reload, rather than losing
a newer edit. Legacy migration and its marker commit together; valid v3 records
survive the v4 upgrade and failed localStorage cleanup cannot remigrate over a
newer committed draft.

The shared draft ring owns attachment fields, pending reads and submission
state across parking/remounting. Per-file 15 MiB, per-message 25 MiB/20-file, and
estimated cross-draft 128 MiB reservations are taken before file reads. Partial
file failure retains other valid attachments. Retirement/session invalidation
fences late reads and saves; sending waits for admitted reads and prevents
incompatible edits/discard. Unit/component tests pass; these estimates do not
claim a strict bound on browser heap overhead or native filesystem durability.

## DATA-04 (audit 5) - Medium - DEFERRED (API pagination redesign)

**Thread reads return unbounded cached message bodies despite a bounded eager-fetch count**

- Evidence: Confirmed — the thread endpoint selects and serializes every message and cached body in a thread with no envelope-page bound; the eight-message eager-fetch limit bounds only NEW provider fetches.
- Deferral: the fix is a response-contract change (paginated envelopes + per-message body endpoint + has_more semantics) across the server route, the dashboard reader, and the MCP read_thread tool — a redesign of the thread surface, not a repair. Single-operator installs hold bounded bodies per the mirror's retention; the reader already renders lazily per message. Deferred as its own API pass.

## WEB-01 (audit 5) - Medium - RECONCILED (existing fix present at reviewed baseline)

**Deployment-versioned service-worker cache lifecycle**

- The earlier register entry inspected the source placeholder but missed the production serving path. `shellRenderer.worker` in `cmd_serve.go` already hashes the built shell and replaces that placeholder in the served worker; activation deletes older Lull Mail and legacy shell caches. This behavior is present in baseline `7e3ba75`, not a new fix in this pass.
- `TestServiceWorkerVersionCoversAllShellAssets` passes under the race detector, including icon-only and manifest-only changes. The browser reinstall/activation/offline lifecycle still needs real-browser execution; synthetic code tests do not claim that end-to-end result.

## WEB-02 (audit 5) - Medium - DEFERRED (conditional; verified sanitization pipeline needed)

**Raw email HTML is parsed before remote-content removal can be guaranteed**

- Evidence: Conditional per the report itself — the reader sanitizes by parsing with DOMParser and stripping/removing remote sources afterward, and MDN documents that a parsed inert document can still request resources; the report explicitly did not browser-verify any actual tracking request, and neither can this pass.
- Deferral: the fail-safe fix (render stored plain text only, never parsing raw HTML, until consent) visibly degrades the default reading experience for every HTML message — a product decision about the privacy/UX trade, and the permanent fix (network-free server-side sanitizer before any browser parse) is its own pipeline. The iframe CSP keeps scripts and (pre-consent) images fenced; what is NOT proven is zero network requests during preprocessing.

## WEB-03 (audit 5) - Medium - DEFERRED (cross-layer feature)

**Inline cid images have no complete metadata-to-rendering path**

- Evidence: Confirmed — the engine records Content-ID values and the sanitizer preserves `cid:` references, but the thread response exposes no inline-part mapping and the renderer has no path from a cid to authenticated bytes, so embedded images render broken.
- Deferral: a cross-layer feature (engine inline-parts array + authenticated part endpoint + Blob-URL renderer with revoke discipline + CSP allowances), explicitly scoped in the report as new functionality rather than a defect repair; tracked as reader work.
