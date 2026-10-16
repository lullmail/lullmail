# The agent-token contract — OPS-10 (owner-approved 2026-10-16)

The single source of truth for the OPS-10 build lane. Decisions are
recorded and approved; the lane does not reopen them. Report genuine
defects rather than resolving them unilaterally.

## Decision record

1. **Four named scopes**, layered over (not replacing) the existing path
   allowlist, which remains the outer boundary: `mail:read`, `mail:send`,
   `mail:act`, `agent:manage`. Default for newly minted tokens: all four
   (byte-for-byte today's trust behavior — no compat break).
2. **Opaque tokens, server-side claims** — 32 random bytes, `lull_` prefix,
   SHA-256-at-rest like today. No JWT; consistent with the session model.
3. **Optional expiry**: per-token `expires_at` chosen at mint; default
   none. Expired tokens fail with a clear RFC 7807 problem (401, distinct
   detail from invalid).
4. **Rotation**: `POST /security/agent-tokens/{id}/rotate` mints a
   successor token and marks the old row superseded with a 10-minute
   dual-validity grace (`rotated_to`, `rotated_grace_until`), then dead.
   One rotation at a time; rotating a superseded token refuses.
5. **Legacy compatibility (binding)**: every `es_`/`lull_` token row that
   exists at migration time becomes full-scope, no expiry, forever
   (AGENTS.md: pre-rename `es_` tokens must stay valid).
6. **Deny-by-default** outside granted scopes: 403 RFC 7807 problem in
   repo style. The route→scope table is test-verified exhaustively.

## Scope → route mapping (the lane verifies every entry against the mux)

The existing `agentAllowed` map (agent.go) is the universe; each entry
moves to exactly one scope, plus `agentMayUndo` stays within `mail:act`:

| Scope | Routes (API-relative) |
|---|---|
| `mail:read` | `/recent`, `/folder`, `/mailboxes`, `/buckets/`, `/threads/`, `/messages/` (GET semantics), `/people`, `/board` + `/board/` + `/notes` + `/notes/` reads, `/outbox` (outcomes list) |
| `mail:send` | `/send`, outbox cancel-within-undo (`DELETE /outbox/{id}` via `agentMayUndo`) |
| `mail:act` | `/classify`, `/messages/` mutations (actions/undo), `/board/` mutations (pin/unpin/cards), `/notes/` mutations |
| `agent:manage` | `/security/agent-tokens` (GET/POST), `/{id}` (DELETE/rotate) |
| (unchanged exclusions) | `/accounts/*/export` stays agent-unreachable; `/personal/export` stays `mail:read` |

Method-sensitive entries (read vs mutate on one prefix) resolve by HTTP
method: GET/HEAD are reads, everything else is the act/send scope. Where
the lane finds an entry that does not fit exactly one bucket, it reports
back rather than inventing a fifth scope.

## Schema (product migration v12, name "agent token scopes")

```sql
ALTER TABLE agent_tokens ADD COLUMN scopes text[] NOT NULL
  DEFAULT ARRAY['mail:read','mail:send','mail:act','agent:manage'];
ALTER TABLE agent_tokens ADD COLUMN expires_at timestamptz;
ALTER TABLE agent_tokens ADD COLUMN rotated_to uuid
  REFERENCES agent_tokens(id) ON DELETE SET NULL;
ALTER TABLE agent_tokens ADD COLUMN rotated_grace_until timestamptz;
```

The default IS the legacy backfill: existing rows keep full trust with no
expiry. No UPDATE needed; do not add one.

## API surface

- `POST /security/agent-tokens` gains optional `scopes` (array, validated
  against the four; default all) and `expires_at` (RFC 3339, future;
  optional). Response unchanged plus the new fields.
- `GET /security/agent-tokens` shows scopes, expires_at, and rotation
  state (never the secret).
- `POST /security/agent-tokens/{id}/rotate` — body optional; accepts the
  same optional `scopes`/`expires_at` for the successor (defaults:
  inherit the rotated token's). Returns the successor's secret once,
  like mint. Old token: usable until `rotated_grace_until`, then 401.
- `DELETE` unchanged (revoke).

## Enforcement

`requireAgent` resolves the token row (already does the hash lookup) and
after the existing allowlist check applies the scope check from the
route→scope table. Superseded-but-in-grace authenticates; past grace does
not (indistinguishable from revoked). Expired is a 401 with
`title: "Token Expired"`; scope denial is 403 `title: "Insufficient
Token Scope"`.

## Test obligations (fail-before/pass-after)

1. Exhaustive route×scope table test: every allowlisted route, every
   scope combination, allow/deny exactly per the table.
2. Mint with reduced scopes → reduced access (fail-before: today
   everything allowed).
3. Expiry: future expiry works, past expiry 401 with the distinct title.
4. Rotation: successor works immediately; predecessor works within
   grace, 401 after (fake clock or short grace in test); double-rotation
   refuses; revoking the successor kills only the successor.
5. Legacy row (seeded pre-migration shape) authenticates full-scope with
   no expiry after migration v12.
6. Scope denial is 403 RFC 7807; expired is 401 — titles distinct.

## Gates

Product suite + `go vet` against real PostgreSQL (scratch db pattern);
dashboard untouched unless the tokens UI surfaces scopes (if the settings
page renders tokens, add the read-only fields — fixtures updated, vitest
green; typecheck + build).

Lane does NOT commit; orchestrator reviews and commits. Same-repo serial
discipline: this lane launches only after the tri-state lane lands.
