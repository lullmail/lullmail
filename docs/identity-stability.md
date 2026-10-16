# Identity stability and remap — verified diagnosis and implementation plan

OPS-03 / SYNC-03 remainder, re-diagnosed 2026-10-16. This document records
what two independent adversarial review passes verified against the code
(every claim carries file:line evidence), the corrected plan that follows
from it, and what is deliberately accepted as loss. The register entry
(`AUDIT_OPEN.md`, SYNC-03) summarizes; this is the full scaffold.

What is and is not proven is stated in [Residual
uncertainties](#residual-uncertainties-code-reading-cannot-settle-them).

## Verified diagnosis

### 1. The recorded loss mechanism — engine identity promotion — is production-dead

The engine's promotion set (`prepared.promoted`, retired by
`DeleteMessages` after `PutEnvelopes` in `apply`, `mail-engine/sync.go:576-591`)
is populated only by `UpgradeIdentity` (`mail-engine/sync.go:553`), and
`UpgradeIdentity` fires only for a **positional** identity that carries a
Message-ID header (`mail-engine/identity.go:123-135`).

That input is a logical contradiction for every shipped adapter:

- IMAP assigns identity from the **same field** `UpgradeIdentity` reads
  (`imap/adapter.go:370-374`): header present ⇒ `h:` identity at parse;
  header absent ⇒ positional. A positional envelope therefore necessarily
  has an empty header — there is nothing to upgrade to. There is no
  two-phase fetch: `fetchItems` always includes ENVELOPE (`imap/adapter.go:271`),
  and body prefetch writes only `mail_bodies`, never envelopes
  (`imap/adapter.go:558-616`).
- Gmail (`gmail/adapter.go:221,255,364`), Graph (`graph/adapter.go:332,382`)
  and JMAP (`jmap/adapter.go:407,415,425,678,692,756`) assign native IDs,
  which `UpgradeIdentity` never touches (`identity.go:124-126`).

`UpgradeIdentity` has exactly one production call site
(`sync.go:553`); the only thing that ever exercises the promotion path is a
synthetic test envelope (`mail-engine/sync_test.go:574-600`) constructing
positional-with-header, a combination no adapter emits. The pass-3 SYNC-03
ordering fix was real but sits on a path that never fires in production.

### 2. The live loss mechanisms are destroy-and-recreate, not promotion

**Graph folder move.** The delta reports `@removed` in the source folder ⇒
`ChangeDestroyed` (`graph/adapter.go:329-334`) ⇒ `RemoveFromMailbox` deletes
the message **and its body** once the last membership drops
(`store.go:588-633`). The same message arrives under its new ID as an
independent change — `ChangeCreated` during enumeration, `ChangeUpdated` in
steady state (`graph/adapter.go:337-340,382`) — with **no old↔new linkage
anywhere**; the engine's change model cannot express one
(`mail-engine/adapter.go:30-46`). Product `cleanupMirrorOrphans`
(`accounts.go:1025-1044`) then **deletes** the orphaned `hey_messages` and
`push_deliveries` rows: filing state is not remapped, it is destroyed. Graph
accounts are reachable today via OAuth despite the JSON API refusing
(`oauth.go:32-63` vs `accounts.go:173-175`), so this loss is live now on
every connected Graph account.

**IMAP UIDVALIDITY reset.** Fires `Changes{Reset: true}` (`imap/adapter.go:130-131`)
⇒ staged scan ⇒ absent IDs pruned by `finishScanTx`
(`store.go:1104-1184`, deleting bodies too, `:1139-1154`).

- Messages **with** a Message-ID header are safe: the `h:` identity digests
  only the header (`identity.go:65-67`), so the rescan re-stages them under
  the same ID and nothing is pruned. No work needed.
- Headerless messages churn positionally (`identity.go:77-83`) and are
  **unpairable by construction** — the old→new mapping is a one-way digest
  of (mailbox, UIDVALIDITY, UID). See [Accepted
  losses](#accepted-losses).

### 3. The Graph ImmutableId migration is a consumer of remap machinery, not a precondition

Flipping `Prefer: IdType="ImmutableId"` re-identifies **folder IDs**, not
just message IDs. Folder IDs are embedded in:

- the initial delta URL (`graph/adapter.go:295-296`) and every stored
  `nextLink`/`deltaLink` cursor (`:251-254,314,319`),
- `mail_sync_state`, keyed `(account_id, mailbox_id)` (`store.go:734-744`),
- membership rows, wholesale-replaced per envelope
  (`graph/adapter.go:394-401`, `store.go:425-440`).

The first post-flip `PutMailboxes` stale sweep then **razes the mirror**:
absent folder IDs lose their memberships, sync state, mailbox rows and
scans (`store.go:242-330`), and every message that lost its last membership
is deleted with its body (`:312-328`) — full re-enumeration and body
re-fetch under new IDs. Flipping before the remap machinery exists is the
single most dangerous ordering available; the migration is the first and
largest client of that machinery. A header-only patch is unsafe for
existing mirrors (already recorded at `AUDIT-CHATGPT-4.md:1049`).

### 4. The lock-order invariant is incomplete

"One lock per transaction" (`AUDIT_OPEN.md`, SYNC-04) describes
mirror-writing transactions only. Two exceptions exist:

- `bindGraphMailbox` takes row locks **then** the advisory lock
  (`identity.go:303-321`: `lockAuthUser` → `email_accounts FOR UPDATE` →
  `pg_advisory_xact_lock`) — inverted. It is dead today and fails closed
  (the vendored adapter does not implement `graphIdentityTranslator`,
  `identity.go:292-295`), but the order is wrong for the day it works.
- `deleteAccount` already supplies a live advisory→row edge
  (`accounts.go:777-778` then `:820`).

Promotion transactions would call `admitIdentityWrite` (`identity.go:41-48`,
row locks) inside an advisory-first engine transaction — the same cycle.
**Rule going forward: the account advisory lock is always the first
statement, in every transaction that takes it.**

### 5. Seam facts

- `identityTx` returns `*sql.Row`/`*sql.Rows` (`identity.go:26-30`);
  `*sql.Row` is a concrete struct with unexported fields — a `pgx.Tx`
  adapter cannot implement it. The seam must be reshaped to neutral row
  types when the upstream contract lands.
- The product-tx-driven harness is proven: `identity_pg_test.go:75-124`
  drives the entire promotion — engine-table writes **and** body copying —
  through one product `*sql.Tx`, the `putMirrorAccountTx` pattern
  (`accounts.go:126-143`). Both schemas share one PostgreSQL database; no
  2PC, no sagas.
- No callback contract exists upstream or vendored (verified across the
  Neutron monorepo including `mail/BACKLOG.local.md` and
  `FRAMEWORK_CONTRACT.md`); the vendored copy is byte-identical to upstream
  except CI-only test wiring (`mail-engine/VENDOR.md` gate).
- Bodies are not atomic in any identity change today: `DeleteMessages`
  drops `mail_bodies` with the old ID (`store.go:572`); the new body is
  fetched best-effort afterwards (`sync.go:621-652`) or lazily
  (`sync.go:672-677`).
- Sealed outbox payloads carry `ReplyParent` (`outbox.go:120,161`, consumed
  at `:630`) and ciphertext is never rewritten (`identity.go:129-131`), but
  **no alias table exists** — `schema.sql:256` defers it and the
  alias-resolving plumbing was deleted with the phantom-contract call
  sites. `graphReferenceInventory` assumes durable aliases will exist.

## Implementation plan (phased, gated)

Phases 1→4 are ordered by dependency; the flip is last. Each phase lands
green with its own fail-before/pass-after regressions. Estimates are
focused-days including review cycles across the two repositories.

**Phase 0 — this document and the register corrections** (2026-10-16).
Done in the commit that adds it. Phase 0.5 landed with it; Phases 1-4 are
gated on the decision above — the machinery's value is proportional to real
Graph usage, and Graph has never run against a live account (the standing
live-provider validation item), so executing them before that gate is
premature optimization.

**Phase 0.5 — cheap lock-order fix.** ✅ Landed 2026-10-16: `bindGraphMailbox`
reordered advisory-first; the cycle is proven by a direct-interleaving
regression (`TestIntegrationLockOrderRowLockBeforeAdvisoryDeadlocks`
reproduces the old order on two connections and observes the 40P01 abort;
`TestIntegrationLockOrderAdvisoryFirstCompletes` proves the new order
completes under the same pressure).

**Phase 1 — neutral seam + product-side migration harness + alias table.**
Reshape `identityTx` to engine-neutral row types; extract the
product-tx-driven migration driver from `identity_pg_test.go` into
production shape; land the alias table in `app_migrations` (next version),
consulted at outbox delivery and reply seeding, populated by every remap.
This unlocks Graph ID migration without upstream work. ~1-2 days.

**Phase 2 — upstream engine contract (two contracts, not one).**
In the Neutron repo first, fail-before PG tests included:

1. *Atomic replacement for known pairs* — a `ReplaceIdentities`-style
   **optional store capability** (an interface like `FinalScanPageStore`,
   `scan.go:109-111` — not a new `Store` method; consumers implement
   `Store` by design), carrying envelope + membership + body replacement
   and product callbacks on the engine's transaction. Callbacks execute on
   that transaction's connection; **nested store calls are prohibited**
   (a second connection self-deadlocks on the same advisory key). Body
   copy-vs-refetch is decided here (copy inside the transaction is the
   contract the product hooks already assume).
2. *Destroy-with-references* — for unpaired destroys (Graph moves today),
   either fingerprint pairing — only `m:` fingerprints; `c:` heuristic
   collisions (`identity.go:163-166`) must refuse — or an explicit
   references-dropped callback so the product can snapshot instead of
   silently orphaning.

~2-3 days.

**Phase 3 — re-vendor and wire.** Re-vendor under the `VENDOR.md` 0-line
diff gate; wire `mail.go`; reshape the product adapter; remove the
`graphIdentityTranslator` refuse-gate only once the real translator
(`translateExchangeIds` batches + `/me` verification) exists upstream;
lock-order regressions; full product PG suite + race. ~1 day.

**Phase 4 — per-account ImmutableId migration.** Only after 1-3: translate
message **and folder** IDs in bounded batches with `/me` verification
around every batch (`graphReferenceInventory` bounds the retained set);
migrate `mail_sync_state` keys and cursors (or clear them to force benign
re-enumeration — under immutable IDs, re-enumeration re-affirms the same
identities); enable the `Prefer` header in the upstream adapter; run the
product-state remap through the Phase 1 harness. ~2-3 days.

**Phase 5 — gauntlet and register.** Race suites, PG suites, dashboard
e2e, `AUDIT_OPEN.md` closure, and the live-provider gates below.

Total: ~5-9 focused days.

## Accepted losses

- **IMAP headerless UIDVALIDITY churn** is unpairable by construction.
  Documented as accepted; heuristic `c:`-fingerprint pinning would be a
  separate, explicitly-heuristic feature, not part of this work.
- `c:` fingerprints are never used for identity pairing (merge risk,
  `identity.go:163-166`).

## Residual uncertainties (code reading cannot settle them)

1. Whether a Graph move reliably surfaces as `@removed` in the source
   delta versus an updated `parentFolderId` — the code's disposition is
   fixed either way, but the symptom shape is not.
2. Whether Graph honors stored old-format `deltaLink`s after a preference
   flip.
3. Real IMAP servers' ENVELOPE completeness (grows the positional
   population exposed to UIDVALIDITY churn; cannot revive promotion).
4. Microsoft's immutable-ID lifetime across archive-mailbox moves
   (doc-level, `AUDIT-CHATGPT-5.md:908`).

All four are gated behind the standing live-provider validation item
(Gmail/Graph have never run against real accounts in this product). The
consumer-account checklist that settles them empirically is
`docs/graph-live-validation.md` — each probe maps to a plan decision
(Probe 4 alone can confirm or kill Phase 4's premise).
