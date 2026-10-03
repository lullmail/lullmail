# Recovering an old stranded staged scan

The atomic final-page repair prevents new scans from losing completion evidence.
It cannot infer whether a historical running scan finished its full enumeration.
Do not finalize a scan just because its continuation looks incremental.

The product CLI now provides an explicit non-destructive recovery path:

1. Back up the database and deploy the atomic final-page engine fix through the
   required Neutron upstream/re-vendor process first
2. Inspect running staged scans with `lullmail repair-scans`. This is read-only
   and does not start provider work. A long-running scan can be healthy; age
   alone does not diagnose stranding
3. After establishing which exact scan lost terminal evidence, run
   `lullmail repair-scans --scan <exact-id> --apply`
4. The next ordinary sync re-enumerates that mailbox from the beginning. It
   prunes only after authoritative completion, using the normal scan contract

The repair takes the same account advisory lock as engine maintenance, replaces
only that exact scan identity, empties its staged seen set, and resets its
continuation. It preserves policy generation, live envelopes, cached bodies,
mailbox membership, live cursor and user filing state. A concurrent completion
or newer replacement causes a no-op; late pages/finalizers for the retired ID
are rejected by the engine's post-lock scan-ID check.

No production database or provider was accessed during implementation. The CLI
argument guard is unit-tested; the live-data/cursor/body preservation,
late-writer rejection and idempotent targeted-retry tests require PostgreSQL and
remain unrun because this environment cannot create a PostgreSQL server socket.
