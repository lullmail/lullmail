# Durable outbox

A message the user sends is stored durably in PostgreSQL before any provider is
contacted. A crash, restart or closed browser cannot lose it, and cannot send it
twice without an explicit user action. When the outcome of a provider call is
uncertain, the send is recorded as `ambiguous` and is never resent
automatically.

What is and is not proven is stated in [Validation](#validation) and
[Duplicate-send windows that remain](#duplicate-send-windows-that-remain).

## Contract

- `POST /api/send` validates the composition and transport, encrypts the
  recoverable composition, and commits it before returning `durable: true`,
  `queued`, `status` and `undo_seconds`. An acknowledgment is never sent for an
  uncommitted row.
- A client submission key (`Idempotency-Key`) is immutable for one wire request
  and owner. A retry recovers the original id and state; a different request
  under the same key is 409. Receipts are kept in PostgreSQL, so this survives
  restarts and process replacement.
- A request with no key still receives durable ownership but is given a random
  key, so a lost acknowledgment cannot be retried safely. The dashboard persists
  a key before sending. MCP `send_mail` requires `submission_key` and carries it
  unchanged; `outbox_list` exposes outcomes (an intentional MCP contract
  change).
- `pending` is the only state that is resumed automatically. A conditional
  database claim wins against cancellation; a job is claimed only after its undo
  deadline (five seconds). Cancellation and claim are decided by one predicate on
  one row, so exactly one wins and the user is told which.
- `submitting` means provider submission may have begun. A claim older than two
  minutes becomes `ambiguous`, keeping the payload. A late confirmed success from
  the same attempt can still record `submitted`; an outcome from any other
  attempt is refused.
- `failed` means the provider provably never accepted the message: a refused
  connection, bad credentials, a rejected recipient or DATA command, an explicit
  4xx/5xx reply to the end of data, a 4xx from Gmail or Graph, a missing
  credential, or the account being gone. `ambiguous` means it may have been
  accepted: the message body was in flight, the reply to it was lost, the
  process died, or a 5xx/transport error came from an API that may have acted.
  Both keep the composition and neither is ever retried; the UI reserves the
  duplicate-delivery warning for `ambiguous`.
- `submitted` means provider acceptance, not recipient delivery. Gmail and Graph
  keep their own Sent copies. For SMTP, acceptance is committed first with the
  exact submitted bytes, then the Sent-folder IMAP APPEND runs as a separate
  step (`filing_status`). An interrupted or uncertain APPEND ends the filing
  `ambiguous`, never retried, because the server may already hold the copy; the
  saved copy can be downloaded.
- Recovery from `failed`, `cancelled` or `ambiguous` creates a new draft from the
  saved composition. It never sends. A deliberate re-send is a new submission
  with a new key.

## State transitions

```
pending -> submitting -> submitted
pending -> cancelled                      (only while undo_until > now())
submitting -> failed                      (provider provably did not accept)
submitting -> ambiguous                   (uncertain, panic, interrupted claim)
ambiguous  -> submitted                   (late proof from the same attempt only)
```

SMTP filing: `pending -> submitting -> filed | ambiguous`.

No transition ever moves `submitting`, `ambiguous` or `failed` back to
`pending`. Do not do it by hand or in a migration either; see
[Operations](#operations).

## Retention and bounds

Admission is serialized by a PostgreSQL advisory lock and checks:

| Bound | Value | Scope |
| --- | --- | --- |
| active sends (`pending` + `submitting`) | 8 | process-wide |
| private-payload reservation | 512 MiB | process-wide |
| private-payload reservation | 256 MiB | per owner |
| retained receipts | 50,000 | per owner |

The reservation covers the encrypted composition and the MIME Sent copy
(5x the retained input plus 64 KiB of headers); replacing the composition with
the Sent copy is refused if it would exceed the reservation. Cancelled and failed
sends release the Sent-copy part of their reservation immediately. The request
keeps its 34 MiB wire bound, two decode slots, 25 MiB attachment total, 15 MiB
per file and provider limits. The worker handles one submission at a time per
server.

Two clocks run from a row's last state change once it has settled (no
submission or filing in flight):

- **30 days**: the saved composition and the saved Sent copy are cleared
  (`payload_bytes` returns to zero). This is the window in which a person can
  recover or download them.
- **90 days**: the idempotency receipt (key, request hash, outcome, no content)
  is deleted. It outlives the payload on purpose: it is what turns a late retry
  of a lost acknowledgment into a replay instead of a second send.

Pending work is never expired. Receipts and byte shares are per owner, so one
owner reaching a cap cannot stop anyone else from sending. When an owner is at
the receipt cap, receipts past 90 days are reclaimed inside the admission
transaction before refusing, so a healthy account is never refused for rows
retention has already retired; only more than 50,000 sends inside 90 days is
refused (429, `Retry-After: 3600`). Pruning runs in bounded batches from the
worker and is indexed.

`Remove saved composition` clears the payload and Sent copy immediately and
keeps the receipt.

## Encryption

Saved compositions and Sent copies are sealed as

```
"v2:" + base64( 0x02 | keyID(4) | nonce(12) | AES-256-GCM( plaintext ) )
```

with the purpose (`payload` or `sent`), owner id and row id as additional data.
A ciphertext copied into another row, owner or column fails authentication.
`keyID` is a fingerprint of `SECRET_KEY` (never the key), so a missing or
changed key is distinguishable from damage. Data in the earlier unversioned,
unbound format is still read, without the binding check.

If the key is missing or has changed, the send ends `failed` with
`payload_key_unavailable` and keeps its ciphertext. Nothing is sent, nothing is
retried, and nothing panics. Restoring the original `SECRET_KEY` makes the entry
readable again (Review, download, export). `payload_corrupt` means the key is
right but the data is damaged or bound to a different row. The key is the same
`SECRET_KEY` that seals account credentials, so keep it with database backups. A
backup is not recoverable without both.

There is no key rotation yet. The key id in the format is what a keyring would
use; until one exists, changing `SECRET_KEY` strands saved compositions in the
state above (and account credentials, as before).

## Export and agent scope

Compositions that were never sent, or whose outcome is unknown, exist nowhere
else, so they are part of the owner's data and travel in the **personal export**
(`GET /api/personal/export`): `outbox/<id>/message.txt`, `composition.json`,
`attachments/…`, plus `submitted-message.eml` for a message whose Sent filing
did not complete. Messages that were filed are in the provider's Sent folder and
travel in the mailbox export. An entry that cannot be decrypted does not fail the
archive; the manifest's `outbox.unreadable` names it and the reason.

Agent tokens never receive compositions: the personal export omits them for an
agent request (the manifest says `included: false`), and `/api/outbox/{id}`
(decrypted composition, Sent copy, removal, cancellation) is session-only. The
agent surface keeps only `GET /api/outbox`, the outcome list the MCP tool uses.

## Single-writer fence

Ownership of outbox rows follows the schema version in `app_migrations`. A
build may admit or claim only while no migration newer than the last one it
knows has been applied. A newer build raises the ledger when it migrates, which
silences every older build that is still running at its next statement:

- admission answers 503 (`Retry-After: 5`, "retry with the same submission key");
- the worker neither claims, sweeps nor files;
- retries of already-accepted keys are still answered (read-only).

The check is part of the claim statement itself, so it is atomic with the claim.
Two builds that both understand the outbox can never both own it.

**What the fence cannot do.** A build from before the outbox (6e561ac and
earlier) never reads `outbox_jobs`, so no code in this version can stop it from
sending. That case is closed by deployment rule alone; see below.

## Deployment

Lullmail on infra-home deploys with Teploy `ingress: host`
(`/Users/tyler/Documents/infra/lullmail/teploy.yml`): the container publishes a
fixed host port, so Teploy recreates it instead of running blue/green. It stops
the old container (SIGTERM, `stop_timeout: 30`), then starts the new one on the
same port. The new container's `serve` runs the engine and product migrations
before it listens. There is therefore no moment at which the pre-outbox build and
this one both run, and the fence is not needed for the first deployment. Keep it
that way:

1. Before deploying, check nothing is mid-flight (it is harmless for `pending`
   rows to wait; they are sent after the restart):
   `SELECT count(*) FROM outbox_jobs WHERE state='submitting' OR filing_state='submitting';`
   A graceful stop aborts an in-flight submission: if its body was already on
   the wire the send becomes `ambiguous` (the user is told), otherwise `failed`.
2. Deploy the new image once. Do not run a second copy of any build alongside
   it, and do not put a proxy in front that can route to two versions. If you
   ever move to Caddy blue/green, the new build must pass its health gate only
   after the old one has stopped serving `/api/send`; the fence protects only
   between outbox-aware builds.
3. **Never roll back to a pre-outbox image** (including `teploy rollback`, or
   Teploy restoring the displaced container after a failed health gate) while
   any row is `pending`, `submitting` or has `filing_state='submitting'`: the
   old build neither sends nor sees those rows, and a client that retries a key
   the new build already accepted will be sent again by the old build. If a
   rollback is unavoidable: stop the app, review those rows, mark any `pending`
   row you do not want sent later as `cancelled`
   (`UPDATE outbox_jobs SET state='cancelled', updated_at=now() WHERE state='pending';`),
   then roll back. The table is harmless to the old build and may stay.
4. To remove the schema as well (rarely needed): stop the app, then
   `DROP TABLE outbox_jobs; DELETE FROM app_migrations WHERE version=8;`.
   Migration 8 creates only the outbox table and its indexes, so this restores
   schema 7 exactly; migrating again re-applies it. All unsent compositions are
   lost, so export them first.
5. A database **restore** to an earlier point re-creates `pending` rows whose
   messages were already sent. Before starting the app on a restored database,
   cancel every `pending` and `submitting` row that predates the restore point
   (or all of them) and tell users to check Sent. The same applies to promoting
   a replica that was behind.

Migration 8 is not yet released; its statements were revised during review. A
database that applied the earlier draft of migration 8 (only test databases)
fails the checksum and must be reset or dropped/re-applied as in step 4.

## Operations

- Do not reset `submitting`, `ambiguous` or `failed` to `pending`, by hand or in
  a migration. Check Sent and the recipient, then create a new draft only when
  the user deliberately wants a new submission.
- The worker logs an `outbox fenced` warning (at most once a minute) if the
  database has been migrated by a newer build. That build is the writer; stop
  this one.
- Useful reads: `SELECT state, filing_state, error_code, count(*) FROM
  outbox_jobs GROUP BY 1,2,3;` and the Outbox page.
- Backups need the database and the matching `SECRET_KEY`.

## Duplicate-send windows that remain

These are the ways the same message can still be submitted twice. The first
three require a person's choice or an unfenceable external fact; none involves
this code resending after an uncertain outcome.

1. **A person deliberately creates a new submission after an `ambiguous` (or
   `failed`) entry.** The warning is shown; the new draft has a new key.
2. **A request with no `Idempotency-Key` whose acknowledgment is lost and is
   retried by the caller.** Each request gets a fresh random key. The dashboard
   and MCP always send keys; only third-party HTTP clients are exposed.
   Requiring the header on `/send` would close this at the cost of breaking such
   clients; that is a product decision, not made here.
3. **A pre-outbox build writing against this database** (rollback, or a second
   container): see Deployment. Not fenceable from here.
4. **A retry after the receipt expired**, 90 days after the original settled, or
   after more than 50,000 sends in 90 days from one owner forced a refusal
   (which is not a send). Dashboard drafts keep their key until acknowledged, so
   a draft left unacknowledged for more than 90 days and then retried is the
   exposure.
5. **A restored backup or a lagging promoted replica** (Deployment step 5).
6. **The provider accepted and reported failure** (a 4xx that did act, an
   SMTP server that rejects after keeping). Nothing a client can see
   distinguishes this from a real refusal.

Closed, and covered by tests: a missing or changed `SECRET_KEY` no longer leads
to any send (the entry fails closed and keeps its ciphertext); a receipt now
outlives its payload (90 days against 30) and is bounded per owner, so neither
retention nor another owner's volume can make a retry look new within the
window; an older outbox-aware build cannot admit or claim once a newer one has
migrated; and a crash or lost acknowledgment at any persistence or provider
boundary resolves to exactly one delivery or to a retained, visible, unsent
entry.

## Validation

All of these run against PostgreSQL 17 and real sockets, not scripted drivers:

- `outbox_pg_test.go`: the nine authored cases (same-key admission and restart;
  single claim, no ambiguous reclaim; undo, owner scope and account cascade;
  acceptance before filing; capacity and replay at capacity; interrupted claim;
  discard keeps the receipt; reserved-byte replacement; owner cascade).
- `outbox_retention_pg_test.go`, `outbox_seal_pg_test.go`,
  `outbox_fence_pg_test.go`, `outbox_export_pg_test.go`: retention and per-owner
  bounds, key failures and row binding, the fence, and the export.
- `outbox_faults_pg_test.go`: kill or fail at every boundary (before and after
  the insert commit, lost commit acknowledgment, pool exhaustion, after claim,
  every SMTP step, after 250 before the status write, after the status write,
  every IMAP APPEND step, graceful shutdown), then restart and recover.
- `outbox_concurrency_pg_test.go`: same-key submit, cancel against claim at the
  undo boundary, competing workers, wrong-attempt and late-success transitions,
  discard against filing, account deletion and logout against admission,
  submission and filing.
- `outbox_chaos_pg_test.go`: seeded random walks over the same fault space
  (`LULL_CHAOS_SEEDS`, `LULL_CHAOS_ROUNDS`).
- `outbox_migration_pg_test.go`: 7 to 8, idempotent rerun, checksum refusal,
  atomic failure, rollback and reapply, concurrent runners.
- `internal/faketransport` provides the loopback SMTP and IMAP servers (the
  product's real clients run against them); `tools/outbox-sink` serves the same
  fakes to the browser check `dashboard/e2e/outbox.mjs`.

Tests that need PostgreSQL skip unless `LULL_TEST_DATABASE_URL` (product) and
`NEUTRON_MAIL_TEST_DATABASE_URL` (engine) point at **disposable** databases; the
harness drops and recreates tables. Run the suites with `-p 1`.

The engine change behind `failed` versus `ambiguous` is
`mail-engine/send.go` (`NotSubmittedError`); it follows the vendoring policy in
`mail-engine/VENDOR.md` and must be backflowed to Neutron's `mail/` module.
