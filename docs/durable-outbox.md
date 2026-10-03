# Durable outbox candidate

This is an isolated architectural candidate. It is not part of the default
hardening patch. Unit/race checks do not validate PostgreSQL locking, migrations,
or crash recovery. The real-PostgreSQL tests must run before rollout.

## Contract

- `/send` validates the composition and transport, encrypts its recoverable
  composition with the existing SECRET_KEY mechanism, and commits it before
  returning `durable: true`, `queued`, `status`, and `undo_seconds`.
- A client submission key is immutable for one wire request and owner. Retries
  recover the original ID and state; a changed request under that key is 409.
  This survives process replacement. Receipts expire after 30 days.
- Missing-key legacy HTTP clients still receive durable ownership, but cannot
  safely retry a lost acknowledgment. The dashboard persists a key before
  sending. MCP `send_mail` now requires `submission_key` and carries it through
  unchanged; `outbox_list` exposes outcomes. This is an intentional MCP input
  contract change and clients must update.
- `pending` is the only automatically recoverable state. A conditional database
  claim wins against cancellation. A job is claimed only after its undo deadline.
  The normal undo window remains five seconds; a busy worker can start later.
- `submitting` means provider submission may have begun. An interrupted claim
  older than two minutes becomes `ambiguous`, retaining the encrypted payload.
  It is never automatically re-submitted. A late confirmed success from the same
  attempt can still record `submitted`. Transport errors are conservatively
  ambiguous unless failure is known to precede submission.
- `submitted` means provider acceptance, not recipient delivery. Gmail/Graph
  manage their own Sent copies. SMTP acceptance is saved before a separate Sent
  append. Failed/interrupted appends are shown separately; retrying the send is
  never used to repair filing. A saved exact MIME copy can be downloaded.
- `failed` and `cancelled` preserve a recoverable composition. Recovery creates
  a new draft, never an automatic send. Ambiguous recovery shows an explicit
  duplicate-delivery warning and requires deliberate review.

## State transitions

pending -> submitting -> submitted
pending -> cancelled
submitting -> failed (known pre-submission failure)
submitting -> ambiguous (transport error, panic, interrupted claim)
ambiguous -> submitted (late proof for the same attempt only)

SMTP filing: pending -> submitting -> filed or ambiguous. No automatic replay of
an ambiguous APPEND: the server may already have saved that copy.

## Bounds and privacy

Admission is serialized in PostgreSQL and checks at most eight active sends,
4096 retained receipts and a 512 MiB aggregate private-payload reservation.
Reservations cover encrypted composition and conservative MIME expansion before
Sent-copy replacement; replacement also refuses bytes beyond the reservation.
The worker processes one submission at a time per server.
The request still has the existing 34 MiB wire bound, two decoder slots, 25 MiB
attachment total, 15 MiB per file and provider-specific limits.

Terminal outcomes expire after 30 days. Pending work is not silently expired.
Account/owner deletion cascades to outbox rows. The existing personal/mailbox
exports do not include these new saved compositions; backup/recovery must retain
the database and the matching SECRET_KEY. Key rotation/versioning is not added. Explicit removal erases a stopped
job's private payload and Sent bytes while retaining its idempotency receipt;
the UI asks before this irreversible local-copy removal. Secret-key loss makes
saved payloads unrecoverable, as with existing encrypted account credentials.

List responses contain only bounded metadata. Private compositions are loaded
individually and never written to the dashboard's snapshot or memory cache.
Normal mailbox snapshots still work despite the API's generic HTTP no-store
header. Bodies shown for review are rendered as text, not interpreted HTML.

## Recovery and operations

Run the product migration before starting the candidate; migration 8 adds
`outbox_jobs` without modifying historical migrations. Old in-memory sends
cannot be recovered retroactively. Drain the old process and wait for its undo
and transport work before upgrading. Never run an old in-memory writer alongside
the durable writer during cutover.

Conditional PostgreSQL claims support competing outbox workers and process
restarts. This is not a claim of general multi-instance application safety:
account lifecycle and configuration still contain process-local coordination,
and a rolling mixed-version deployment is not supported by this migration.

Do not reset submitting/ambiguous to pending, either manually or in a migration.
Use status/recovery, check Sent and recipient evidence, and create a new draft
only when the user deliberately chooses a new submission.

## Validation and release gates

Authored live-PostgreSQL cases cover same-key concurrent admission/restart,
claim exclusivity/no ambiguous reclaim, owner-scoped undo and account deletion,
SMTP acceptance-before-filing, capacity and replay at capacity, interrupted
claims retaining recoverable payloads, and payload discard retaining receipts.
They are unrun in this environment because PostgreSQL cannot create its socket.

Unit/scripted-driver tests cover persistence errors, immutable-key replay,
capacity errors, encryption/recovery and owner scoping. Component tests cover
cancel/review/restore, attachment preservation, escaped HTML, destructive-action
confirmation, separate Sent-copy state and stopped polling. Run all modules'
race/vet suites and final dashboard tests/typecheck/build after integration with
the hardening patch.

Before rollout also execute process-kill tests at acceptance/claim/provider
success/filing boundaries, database rollback/upgrade tests, and real browser
multi-tab/offline flows. Provider tests must use synthetic/fake endpoints until
separately authorized; none of this work sends real mail.

## Integration boundary

This optional candidate is rebased onto the exact default main-plus-Gmail
review snapshot. The consolidated bundle's optional-outbox-on-default.patch
applies only after DEFAULT_SOURCE_SHA256.json verifies that complete source
snapshot; it must not be applied to the older frozen main, standalone outbox
snapshot or untouched public baseline. A textual patch check alone is not a
substitute for the mandatory full-manifest verification. The account-cancellation, resolver/stream,
owner/account deletion, export, fail-closed admission, cross-tab storage,
composer, scan-repair and Gmail read/cooldown/OAuth fixes are preserved.
Outbox changes remain limited to its explicit feature delta; all unrelated
source files are byte-identical to the new default. Both complete and incremental
default routes, followed by the optional layer, reconstruct the same verified
optional source tree.

This does not qualify the combined candidate for release: the new PostgreSQL
state machine, migration, kill/restart boundaries and actual browser recovery
flows still require execution. The outbox remains a separate architectural
candidate and does not close the product deferral.

The unit quota regression renders synthetic UTF-8 MIME without a network call.
It includes quoted-printable soft-line expansion and encrypted base64 overhead.
New real-PostgreSQL cases for the reserved-byte replacement guard and owner-delete
cascade are authored but unexecuted alongside the existing seven outbox cases.
