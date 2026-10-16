# The identity contract — Go-level specification

The single source of truth for Phases 1–3 of `docs/identity-stability.md`.
Both lanes build against THIS document; if code and this document disagree,
this document wins until amended. Design decisions recorded here are made
(2026-10-16, owner-approved build launch); lanes do not reopen them.

## Decision record

1. **Destroy-with-references ships as an explicit callback** (`ReferencesDropped`).
   Fingerprint pairing is REFUSED: `c:` fingerprints can merge distinct
   messages (documented heuristic, `mail-engine/identity.go:163-166`) and
   `m:` fingerprints only exist when a Message-ID header exists — rare in
   the destroy path and unnecessary once Phase 4's `translateExchangeIds`
   provides real pairings. The callback lets the product snapshot instead
   of silently orphaning.
2. **Callbacks execute on the engine's transaction, through an
   engine-owned handle** (`HookTx`). The tx types live in the engine
   because the engine owns the connection. The product imports the engine
   (existing direction); never the reverse.
3. **Body replacement is copy-inside-transaction** when a `Body` is
   supplied with a replacement — matching the shape the product's test
   harness already proves (`identity_pg_test.go:75-124`).
4. **Both new capabilities are optional store interfaces** (the
   `FinalScanPageStore` pattern, `scan.go:108-111`), never additions to
   `Store` — consumers implement `Store` by design.
5. **The alias table is product-side** (`app_migrations` v11) and is the
   eventually-consistent fallback for any future where the two sides do
   not share one transactional store.

## Engine-side contract (upstream `github.com/neutron-build/neutron/mail`)

```go
// HookRow is the single-row result of a hook query.
type HookRow interface{ Scan(dest ...any) error }

// HookRows is the multi-row result of a hook query.
type HookRows interface {
    Next() bool
    Scan(dest ...any) error
    Err() error
    Close()
}

// HookTx executes caller SQL on THIS transaction's connection. Given to
// product callbacks so their writes commit with the engine's unit of work.
type HookTx interface {
    Exec(ctx context.Context, query string, args ...any) error
    QueryRow(ctx context.Context, query string, args ...any) HookRow
    Query(ctx context.Context, query string, args ...any) (HookRows, error)
}

// IdentityPair is one old→new identity replacement.
type IdentityPair struct{ OldID, NewID MessageID }

// IdentityHooks are product callbacks run inside engine transactions,
// after the account advisory lock, before any old identity is retired.
type IdentityHooks interface {
    // Admit fences the unit on the owner rows (row locks are legal here;
    // the advisory lock is already held — never take it again).
    AdmitIdentityWrite(ctx context.Context, tx HookTx, acct AccountID) error
    // Remap moves retained product references for every pair.
    RemapIdentity(ctx context.Context, tx HookTx, acct AccountID, pairs []IdentityPair) error
    // ReferencesDropped reports identities about to be deleted with no
    // known replacement. The product snapshots; it must not resurrect.
    ReferencesDropped(ctx context.Context, tx HookTx, acct AccountID, ids []MessageID) error
}

// IdentityReplacement is one atomic identity replacement.
type IdentityReplacement struct {
    OldID    MessageID
    Envelope Envelope // new identity; written FIRST
    Body     *Body    // optional; copied inside the transaction when non-nil
}

// IdentityReplaceStore is the optional atomic-replacement capability.
type IdentityReplaceStore interface {
    // ReplaceIdentities, in ONE transaction (advisory lock first):
    // AdmitIdentityWrite → write every replacement envelope + membership
    // (+ body copy) → RemapIdentity(all pairs) → retire old identities.
    // Any failure rolls back everything; an interrupted replacement must
    // leave the old record readable and the unit retryable (idempotent:
    // envelope upserts, old-id deletes are no-ops when absent).
    ReplaceIdentities(ctx context.Context, acct AccountID, reps []IdentityReplacement, hooks IdentityHooks) error
}

// ReferenceAwareStore is the optional destroy-with-references capability.
type ReferenceAwareStore interface {
    // RemoveFromMailboxReferenced behaves as RemoveFromMailbox but calls
    // ReferencesDropped inside the transaction (advisory first), before
    // deletion, with every id that will lose its LAST membership and be
    // deleted with its body. Callers fall back to RemoveFromMailbox when
    // the capability is absent.
    RemoveFromMailboxReferenced(ctx context.Context, acct AccountID, box MailboxID, ids []MessageID, hooks IdentityHooks) error
}
```

### Prohibitions (enforced by tests and review)

Inside any hook running on a `HookTx`:
- **No new connections, no engine-store calls** — a second connection
  self-deadlocks on the account advisory lock.
- No additional advisory locks; no DDL.
- Only the caller's own (product) tables.

### Engine wiring obligations

- `apply` and the scan completion path probe `IdentityReplaceStore`; when
  present and `prepared.promoted` is non-empty, the paired
  replacement path runs INSTEAD of `PutEnvelopes`+`DeleteMessages`.
  (Today that set is production-dead; the seam is for Phase 4's
  translate-confirmed pairs and future engines. Preserve current behavior
  when the capability is absent or hooks are nil.)
- The destroyed path probes `ReferenceAwareStore` and prefers
  `RemoveFromMailboxReferenced` when hooks are supplied.
- Hook panics/errors abort the transaction (error return; no partial
  commits).

### Engine test obligations (fail-before/pass-after, real PostgreSQL)

1. Replacement: old row readable after an injected mid-transaction hook
   failure; retry after rollback succeeds; body copied atomically; hook
   saw a single transaction (same `txid` across hook calls is the
   simplest witness).
2. Destroy-with-references: `ReferencesDropped` fires exactly for
   last-membership ids, inside the tx, before deletion; a hook error
   aborts the deletion.
3. Capability absence: current behavior byte-for-byte (suite must stay
   green with the capability unimplemented — use a wrapper store).

## Product-side contract (lullmail)

### Alias table (`app_migrations` v11, name "message identity aliases")

```sql
CREATE TABLE message_identity_aliases (
    account_id TEXT NOT NULL,
    old_id     TEXT NOT NULL,
    new_id     TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, old_id)
);
```

- `remapProductIdentity` records one alias per pair it moves (inside the
  same transaction).
- Consulted at **outbox delivery** (`sendqueue.go:612` `deliveryFor`):
  when the direct `ReplyParent` lookup misses, one alias hop resolves the
  current identity (loop-guarded: alias chains collapse on write, and
  resolution follows at most one hop).
- Consulted at **reply seeding** where a stored parent identity is used.
- Never rewritten into sealed ciphertext; the alias IS the durable
  resolution (`docs/identity-stability.md` §5).

### Migration harness extraction

The product-tx driver proven in `identity_pg_test.go:75-124` moves to
production shape (a `runIdentityMigration`-style driver taking `*sql.Tx`),
unchanged in behavior, with the test switching to the production driver.
The driver: advisory lock first → `admitIdentityWrite` → engine-table
replacement writes (envelope, membership, body copy) →
`remapProductIdentity` (now also writing aliases) → commit.

### Phase 3 (after both lanes land; separate lane)

Re-vendor the engine under `mail-engine/VENDOR.md` (0-line diff gate),
reshape `identityTx` to the engine's `HookTx` (a thin `*sql.Tx` adapter
implements `HookTx` so the migration harness keeps working), wire the
hooks into `mail.go` (probe `IdentityReplaceStore`/`ReferenceAwareStore`),
and keep the `graphIdentityTranslator` refuse-gate until Phase 4's real
translator exists upstream.

## Test commands (gates)

- lullmail product:
  `createdb` a scratch db; `LULL_TEST_DATABASE_URL=postgres://tyler@localhost:5432/<db>?sslmode=disable go test -count=1 ./...`
- upstream engine (Neutron `mail/`): same pattern with
  `NEUTRON_MAIL_TEST_DATABASE_URL`.
- Lanes do NOT commit. Implementation + green suites + fail-before
  evidence in the final report; the orchestrator reviews and commits.
