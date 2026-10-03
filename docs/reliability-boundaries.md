# Reliability boundaries

## Mutation transactions

The ten routes wrapped by `withIdempotency` perform all local reads and writes
through `mutationDB` or `beginMutation`. Their domain writes, response body, and
idempotency record commit in one PostgreSQL transaction. A handler must never
acquire another connection or perform provider/network work inside this wrapper.

The lock order is owner row, idempotency record, then domain rows. Taking the
owner lock before inserting the ledger row avoids upgrading two foreign-key
`KEY SHARE` locks to `FOR UPDATE` concurrently. This deliberately serializes
keyed local mutations for one owner. These are short database-only operations;
throughput optimization must preserve lock ordering and atomicity.

A savepoint isolates handler writes from the key claim. Deterministic rejections
are recorded after rolling back their writes. HTTP 408, 429, and 5xx outcomes
roll back both writes and claim, allowing the same immutable request/key to retry.
A panic or ledger-write failure rolls back everything. Ambiguous commit results
are resolved by retrying the same key.

Migration 7 adds response metadata without modifying historical migrations.
Replays preserve Content-Type, Location, Retry-After, and Cache-Control. Cookies,
authentication headers, and transport-specific headers are never replayed.
Previously recorded outcomes retain their existing retention policy.

## Send acceptance is still volatile

`enqueue` atomically checks the submission key, reserves capacity, publishes the
entry, and admits the worker. A replay neither reserves another job nor creates
another undo token. Rejected worker admission is not published as an acceptance.
Dashboard drafts persist one key for unchanged composition; edits and new or
undo-restored compositions get a new submission identity.

After the worker exits, the queue retains at most 4096 small completion receipts
for 24 hours from completion; retries never extend expiry. The oldest receipt is
evicted at capacity. Receipts contain only an ID, request hash, outcome, and
expiry, never composition or attachment content. A submitted receipt replays the
original token with zero undo time. Transport errors and panics remain ambiguous
and refuse same-key resubmission; cancelled receipts do likewise. Undo-restored
compositions use a new key.

This does not guarantee deduplication after server restart, expiry, or capacity
eviction. Queue acceptance is not delivery confirmation. No automatic provider
retry should be added to conceal uncertainty.

### Proposed durable outbox, separate change

A future outbox should persist composition and attachment references alongside a
unique `(owner, submission_key)` and immutable request hash, before acknowledging
acceptance. Keep explicit states:

- `pending`: durable and undoable until a persisted deadline
- `submitting`: one leased worker has begun submission
- `submitted`: provider acceptance is known, with provider identifier when available
- `failed`: a definite failure, retaining a recoverable draft
- `ambiguous`: submission may have succeeded; do not blindly retry
- `cancelled`: undo won before submission began

Undo and worker claim must use conditional state transitions in the same
database. Restart recovery may safely re-claim `pending`, but an expired
`submitting` lease must become `ambiguous` unless the transport supplies a
verifiable idempotent submission contract. SMTP connection loss after DATA is
not proof of non-delivery. A stable Message-ID helps investigation, but does not
make SMTP exactly-once.

Expose an owner-scoped status endpoint and event updates. Retire the browser's
recoverable composition only after durable server ownership is confirmed, and
surface failed/ambiguous outcomes. Sent-copy filing needs its own durable status
so filing retries never resend the message. Define attachment quotas, encryption,
retention, export/deletion, and migration before implementation.

Tradeoff: this adds persistent private-message storage and a worker lifecycle,
but makes accepted work recoverable. The bounded in-memory receipt cache reduces repeats after completion; it cannot
resolve restart or SMTP ambiguity.

## Reconciliation completion

PgStore implements the additive `FinalScanPageStore` extension. Final envelopes,
seen IDs, destroyed IDs, absent-membership pruning, terminal cursor, generation
completion marker, and scan removal commit atomically. Optional body prefetch
runs afterward; failure cannot strand a running scan at an incremental cursor.
Cursor-invalid and explicit-reset paths retain the policy generation.

External ScanStore implementations remain compatible, but need the extension
for crash-atomic completion. Existing scans already stranded by the old code
have lost their terminal evidence and must be restarted as fresh non-destructive
scans; they must not be finalized by guessing. The vendored engine change must
be matched upstream and re-vendored according to `mail-engine/VENDOR.md` before
publication.

## Browser owner and account boundaries

Account responses expose both product and mirror identities. Snapshot cleanup
removes both account lenses and unified snapshots. Older servers without the
mapping cause conservative cleanup of the current owner's snapshots. A snapshot
generation fences late authenticated reads and IndexedDB writes.

Session teardown clears live draft/private state and pending autosaves before
persistent storage is erased. Draft hydration, field saves, deletes, and send
acknowledgments are fenced to their originating owner/generation. The expanded storage pass uses an IndexedDB metadata store for transactional
owner/generation/snapshot authority; localStorage markers are invalidation and
display hints, not sufficient permission to read or write private records.
Cache, draft and replay operations validate authority in their own transaction.
Draft writes additionally compare revisions, and retirement persists tombstones
so stale tabs cannot resurrect a sent or discarded draft. Conflicts do not
silently merge; the editor must reload. Owner changes, failed cleanup retries
and obsolete same-tab preparations remain fenced before UI admission.

Draft attachment reads reserve per-file, per-message, count and estimated
cross-draft memory budgets before allocating buffers; successful files survive
another file's failure. The estimated memory budget is not a browser heap-size
guarantee. Pending reads and in-flight submissions block incompatible sends and
edits across parking/remounting.

IndexedDB v4 upgrades retain valid v3 data, and legacy migration commits with its
metadata marker. Failed persistence is surfaced and the local session stays
suspended rather than claiming erasure. Physical deletion cannot be promised if
both IndexedDB cleanup and the localStorage invalidation hint fail; a full
reload cannot recover cleanup intent that no persistence layer recorded.
Native-browser durability, crash/upgrade/quota and multi-tab acceptance remain
separate verification requirements from fake-IndexedDB tests.

A failed account-cache purge leaves a unique owner/generation-scoped recovery
hint. Matching hints suspend snapshot reads; fresh preparation clears snapshots
only, preserving drafts and pending actions, before acknowledging the hints it
actually recovered. Stale owners cannot publish invalidation for the new owner.
Replay attempts abort network or error-body stalls after 30 seconds and retain
the same queued mutation identity with backoff measured from failure. If Web
Locks are unavailable, separate tabs may replay concurrently; server-side
idempotency remains essential. A browser storage transaction cannot make an
already-transmitted HTTP request atomic with a session-cookie change.

## Authentication, lifecycle and export boundaries

Passkey registration consumes/verifies a one-use ceremony before acquiring its
credential-write transaction, avoiding a nested pool dependency. Installation
and owner state are still rechecked while writing. All WebAuthn finish requests
are bounded to 16 KiB including trailing bytes before library parsing.

Provider work joins caller deadlines and account cancellation. Resolver wrappers
carry the same lifetime into later methods and returned raw/attachment streams,
without pretending every provider supports mailbox selection or Sent append.
The owner lock covers request admission rather than its entire network lifetime;
a single-account deletion keeps its seal/drain/delete serialized with owner
removal. No lease or synthetic context test replaces actual database deletion
and provider cancellation testing.

An export may use a disclosed mirror fallback after a provider-local timeout,
but actual request/account cancellation stops the export. Historical staged-scan
repair is an exact-ID operator action, not automatic startup behavior; see
`scan-repair.md`.

## Verification gates

Before merging, run dashboard tests, typecheck, and production build; product,
mail-engine, and MCP tests/vet; and real PostgreSQL integration/race tests. CI uses
Go 1.26.x and PostgreSQL 17. Integration URLs must point only to disposable test
databases: `LULL_TEST_DATABASE_URL` for the product and
`NEUTRON_MAIL_TEST_DATABASE_URL` for the engine. The harness drops test schemas.
Run database suites sequentially when sharing a scratch database.

Regressions cover concurrent send keys/undo/shutdown/capacity, one-connection
mutation routes, rollback of domain writes on ledger failure or panic, transient
retry and rejection metadata, terminal-scan interruption/restart, identity-aware
cache deletion, and private draft teardown/retry flows. A production mailbox and
an actual SMTP/provider submission are outside these synthetic test fixtures.


The CI `dashboard-selection` job installs test Chromium and runs the existing
synthetic selection fixture, preserving its JSON result, console log and
screenshots. It adds no account credentials and intercepts API requests. Its
configuration and script syntax were checked locally, but execution is pending;
it does not change publication dependencies until proven reliable. Local native
browser acceptance remains blocked by socket/file-URL policy restrictions.
