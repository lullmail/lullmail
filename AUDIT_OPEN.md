# Open audit items

Unresolved findings for this repository from the ChatGPT-led audit series (2026-09-09 through 2026-09-11, passes 1-5; register: teploy-neutron-lullmail expanded audit). Every P0/P1 finding has been fixed and verified; the items below are the remaining P2/P3 tail plus one item needing validation. Fields are quoted from the audit register; line references point at the review commits listed per item where recorded.

Open items: 13 P2 (13 total)

## lullmail__lullmail-01 - P2 - Open

**Version the entire offline asset set, not only index.html**

- Kind: Confirmed from source
- Evidence: serveServiceWorker derives its cache version exclusively from raw index.html. The worker uses cache-first handling for other same-origin assets, including unversioned icons and manifest.webmanifest. A deployment changing only those files leaves the worker bytes/cache version unchanged. CSS and navigation use network-first handling and are not subject to the same blanket staleness claim.
- Impact: Previously cached icons/manifests can remain stale after an asset-only deployment, even while the server has newer files.
- Proposed fix: Generate a build manifest covering all precached resources and incorporate its digest into the worker, or fingerprint every immutable asset and use network revalidation for unversioned metadata.
- Acceptance test: Install the worker, change only icon.svg or manifest.webmanifest, deploy with unchanged index.html, and verify the next update serves the changed asset.
- Review commit: `49d159b6654d3dbd27866f87ac783402f19c6cb7` (last reviewed 2026-09-10)

## lullmail__lullmail-02 - P2 - Needs validation

**Check for recursive lifecycle read locks when a deletion is waiting**

- Kind: Risk requiring validation
- Evidence: accountWorkLifecycle holds accountOwnerMu.RLock across its handler. beginAccountUse also takes that read lock and is used by accountResolver. Go RWMutex readers cannot safely acquire recursive read locks when a writer is pending. The route composition needed to prove a nested invocation was not inspected.
- Impact: If a wrapped handler reaches accountResolver while deletion is waiting for the write lock, the request and deletion can deadlock. This is a conditional risk, not a confirmed reachable production deadlock.
- Proposed fix: Trace all mounted handlers, ensure the lifecycle gate is acquired only once per operation, and carry an explicit operation-scoped lease rather than recursively acquiring the same RWMutex.
- Acceptance test: Force an outer handler read lock, queue a deletion writer, then invoke its resolver; a bounded concurrency test must complete rather than deadlock.
- Review commit: `49d159b6654d3dbd27866f87ac783402f19c6cb7` (last reviewed 2026-09-10)

## lullmail__lullmail-03 - P2 - Open improvement

**Add coordinated server shutdown and bounded health database probes**

- Kind: Improvement
- Evidence: serve starts background work then blocks in log.Fatal(srv.ListenAndServe()), with no signal-driven HTTP drain in the reviewed entry point. The health handler passes the request context directly to PingContext without its own short deadline.
- Impact: Shutdown can interrupt requests, and a stalled database probe can make the deliberate liveness endpoint slow or hang until the caller disconnects. No mail-loss outcome was established.
- Proposed fix: Use a signal-derived application context, drain HTTP with a fresh bounded shutdown context, stop/join background workers, close resources, and bound database readiness checks separately from process liveness.
- Acceptance test: Test SIGTERM during a request and a hung database ping; requests drain within policy and liveness remains promptly available.
- Review commit: `49d159b6654d3dbd27866f87ac783402f19c6cb7` (last reviewed 2026-09-10)

## lullmail__lullmail-07 - P2 - Open

**Graph replies use unsupported custom-header names**

- Kind: Source-confirmed
- Evidence: The Graph sendMail JSON branch puts In-Reply-To and References into internetMessageHeaders. Microsoft documents that custom headers supplied when creating a message must have names beginning with x-.
- Impact: The reply payload conflicts with the documented Graph contract and may be rejected rather than sent. This is a source/documentation mismatch; the precise service response has not been reproduced against a live tenant.
- Proposed fix: Use Graph reply/createReply or replyAll workflows with the actual provider message ID, or a documented MIME submission path. Do not rename threading headers to x- variants, since that would not preserve their standard meaning.
- Acceptance test: Exercise a reply and a reply-all with a fake transport contract test and a disposable Microsoft account. Verify successful submission and the intended conversation association.
- Review commit: `49d159b6654d3dbd27866f87ac783402f19c6cb7` (last reviewed 2026-09-10)

## lullmail__lullmail-09 - P2 - Open

**The JSON request cap makes the advertised attachment total unreachable**

- Kind: Source-confirmed
- Evidence: handleSend limits the request to 30 MiB while decodeAttachments permits 25 MiB decoded in total and 15 MiB per file. Two 12-MiB attachments pass those decoded limits but require 32 MiB in base64 before JSON overhead.
- Impact: A valid attachment set under the decoded limits is rejected earlier by the request reader. The practical ceiling is less than 22.5 MiB decoded once envelope/body overhead is included.
- Proposed fix: Choose one documented total limit and size the HTTP envelope for base64 expansion plus bounded message metadata, or switch to streamed multipart/separate uploads. Return a size-specific error rather than a generic malformed-JSON response.
- Acceptance test: Test two 12-MiB files, the precise supported total boundary and one byte above it through the HTTP handler, not just decodeAttachments.
- Review commit: `49d159b6654d3dbd27866f87ac783402f19c6cb7` (last reviewed 2026-09-10)

## lullmail__lullmail-10 - P2 - Open improvement

**Expose a durable send outcome instead of only an undo token**

- Kind: Improvement
- Evidence: enqueue immediately returns queued/undo_seconds. The worker sends its result to a buffered done channel, logs failures and removes the map entry. The inspected product routes expose undo but no delivery-status lookup; no reader of done appears in the inspected queue code.
- Impact: The enqueue response is not a delivery acknowledgment. A later provider failure or restart during the undo window has no durable outcome in this path. The frontend draft lifecycle was not inspected, so draft loss itself is not asserted.
- Proposed fix: Add persistent outbox states and a delivery-result event or status endpoint. Retain/recover draft content until an outcome is known; distinguish accepted, submitting, submitted, failed and ambiguous. Define retry/idempotency behavior rather than blindly retrying sends.
- Acceptance test: Inject a delivery failure after the undo window and restart while a send is pending. Verify that a client can retrieve a meaningful terminal or recoverable state and does not mistake queue acceptance for provider acceptance.
- Review commit: `49d159b6654d3dbd27866f87ac783402f19c6cb7` (last reviewed 2026-09-10)

## lullmail__lullmail-11 - P2 - Open

**A database outage during boot leaves the API permanently disabled**

- Kind: Source-confirmed
- Evidence: connectApp retries database startup under one 15-second context, then returns nil on timeout or migration failure. serve mounts apiUnavailable when app is nil, with no later initialization retry. Its health handler still answers HTTP 200.
- Impact: If the database becomes ready after the initial startup budget, that process does not recover its API by itself. A deployment checking only the 200 liveness endpoint can keep a permanently degraded process running until manual restart.
- Proposed fix: Separate liveness from readiness and either fail startup for supervisor retry or implement a coordinated, bounded reinitialization path. Avoid partially mounting a second API during retries; define readiness around completed migrations and initialized services.
- Acceptance test: Start with an unavailable fake database, make it available after the initial budget, and assert either automatic initialization or a deliberate failing readiness/startup status that causes recovery.
- Review commit: `49d159b6654d3dbd27866f87ac783402f19c6cb7` (last reviewed 2026-09-10)

## lullmail__lullmail-12 - P2 - Open

**Concurrent push dispatches can submit the same notification before deduplication**

- Kind: Source-confirmed
- Evidence: sendPushForUser first selects an unread message with no push_deliveries receipt, then calls the push service, and only afterward inserts a receipt with ON CONFLICT DO NOTHING. The background loop and manual classify route can invoke dispatch separately. Two calls can therefore select the same message before either inserts its receipt.
- Impact: The service can submit duplicate pushes and consume duplicate delivery work. Topic/tag collapse may hide some duplicates in a browser; duplicate visible popups are not asserted for every browser.
- Proposed fix: Claim delivery work atomically before external submission using a recoverable lease/outbox, preferably per message and subscription. Define lease expiry, failures, and uncertain external acknowledgments; do not claim exactly-once external effects without provider support.
- Acceptance test: Hold two dispatches after message selection. Release them together and assert at most one active submission per message/subscription. Then simulate worker failure and verify the uncompleted reservation becomes retryable.
- Review commit: `49d159b6654d3dbd27866f87ac783402f19c6cb7` (last reviewed 2026-09-10)

## lullmail__lullmail-13 - P2 - Open

**A successful push to one device suppresses retry for another failed device**

- Kind: Source-confirmed
- Evidence: The push loop keeps one sent Boolean for all subscriptions. Any 2xx response sets it; the final receipt is keyed by user/account/message rather than subscription. A successful device and a transiently failed device therefore produce the same global delivered record.
- Impact: The failed subscription cannot receive a retry for that message through the normal selection query. Future messages remain eligible. If delivery is intentionally best-effort to any one device, that limitation needs an explicit product contract rather than implied per-device delivery.
- Proposed fix: Track per-subscription delivery outcomes and bounded retries. Mark only successful endpoints complete; remove only expired endpoints; avoid resending to successful devices when another device retries.
- Acceptance test: Return 201 for device A and 503 for device B. On the next eligible dispatch, retry B only, retain A’s receipt, and record B independently after it succeeds.
- Review commit: `49d159b6654d3dbd27866f87ac783402f19c6cb7` (last reviewed 2026-09-10)

## lullmail__lullmail-14 - P2 - Open

**Preserve the mailbox account in exported board-thread references**

- Kind: Source-confirmed
- Evidence: handlePersonalExport selects board id, thread_key, title, note, done_at and created_at, and its cardExport JSON type contains no account field. In contrast, handleBoard reads account_id and uses account plus thread as the identity; the live join also requires both fields. Export projects that account identity away.
- Impact: Two accounts can contain the same provider/thread identifier. After exporting and removing the original database, the exported board card does not say which account’s thread it referenced. A unique card ID identifies the card itself, not the lost mailbox association. Manual non-thread cards are unaffected.
- Proposed fix: Include a nullable account_id in board.json and a stable account mapping/manifest where required for re-import. Preserve the pair (account_id, thread_id); document a format-version migration and keep null/empty references for manual cards.
- Acceptance test: Create pinned cards on two owned accounts with the same thread ID. Export, load only the export, and unambiguously recover each card’s original account/thread pair. Include a manual card and a disconnected-account reference.
- Review commit: `68b2d17a5143fa976ba32fd9596049c5f43d2c53` (last reviewed 2026-09-10)

## lullmail__lullmail-15 - P2 - Open

**Return an HTTP failure when a personal-export ZIP entry cannot be written**

- Kind: Source-confirmed
- Evidence: The archive-entry loop logs and returns when write(name,data) fails, without calling writeProblem. This occurs before ZIP download headers or a response status are written. Neighboring zip-close, seek and stat error paths do produce 500 responses. The entry-write branch instead falls through to the server’s default successful empty response.
- Impact: An export build/storage error can produce HTTP 200 with an empty body, misleading download automation into treating it as successful. This is conditional on an entry write actually failing; not every write error is deferred until ZIP Close. The fixture injects a writer failure rather than filling real storage.
- Proposed fix: Return a structured 500 on the entry-write error path before response headers are committed. Prefer a common archive-builder error return so all build failures receive the same status and cleanup. Ensure ZIP/temp resources are closed and removed on all failures.
- Acceptance test: Inject a ZIP entry-write error before completion and assert non-2xx problem JSON, no successful ZIP headers, and no temporary-file residue. Separately test failures at ZIP Close/Seek/Stat and a normal valid archive.
- Review commit: `68b2d17a5143fa976ba32fd9596049c5f43d2c53` (last reviewed 2026-09-10)

## lullmail__lullmail-17 - P2 - Open

**Restore per-row read state rather than flipping the whole bulk action on undo**

- Kind: Source-supported defect/contract mismatch
- Evidence: markRead registers an undo closure that calls markRead(rows, !read). It does not snapshot each row.read or restrict undo to changed rows. In contrast, nearby markDone already collects previouslyUnread before registering its inverse.
- Impact: For mixed read/unread selections, marking all read then undoing makes every row unread, including messages originally read. An idempotent mark-read can similarly become a destructive unread operation when undone.
- Proposed fix: Snapshot the original read state and identities before mutation. Undo only the rows actually changed, or restore individually to their captured values; use the settled-success set when combined with L-18. Do not infer original state from one inverse boolean.
- Acceptance test: Test mixed [read, unread], all-read and all-unread selections for both actions. After action then undo, every successfully changed row must match its original state, and originally unchanged rows must remain unchanged.
- Review commit: `d294ea76325c3d77d90de1848fa49445113289dc` (last reviewed 2026-09-10)

## lullmail__lullmail-18 - P2 - Open

**Reconcile partial bulk-mutation successes before returning an error**

- Kind: Source-supported defect/contract mismatch
- Evidence: actMany uses Promise.all across independent mutation requests. markRead/markDone/moveTo call afterMutation and register undo only after complete success. A single rejection enters fail; sibling requests can already have succeeded or complete later with no settlement tracking.
- Impact: The UI shows a general error with stale list/count state and no undo for the subset actually changed. Blind retries can repeat mutations or alter the user’s intended recovery. This is an error-path issue distinct from L-17’s successful mixed-state undo.
- Proposed fix: Use per-row settled results (or a transactional batch API) and wait for the complete outcome. Always reconcile displayed state, report failed identities, and register an exact undo for successful changes using captured prior values; retry only failed work.
- Acceptance test: Synchronize two mutations: reject one while holding the other, then let the other succeed after the first failure. The final UI must reflect the successful subset, report the failed subset and permit exact undo of the successful mutation without retrying it.
- Review commit: `d294ea76325c3d77d90de1848fa49445113289dc` (last reviewed 2026-09-10)

