# Repo conventions

How this repository is organized and why. Written down after the 2026-10
reorganization so the shape is a policy, not an accident. New first-party
apps in the family should start from this page, not from this repo's history.

## Root is wiring and nothing else

Root holds: `main.go` (the subcommand dispatcher and the dashboard asset
embed — the only Go file at root), the docs (`README.md`, `AGENTS.md`,
`AUDIT_OPEN.md`, `CONVENTIONS.md`), and the deploy surfaces
(`compose.yaml`, `teploy.yml`, `Dockerfile`). Any frozen process document
goes under `docs/` — never the root.

## One product package, by policy

The product layer is a single package at `internal/app/`. That is
deliberate, not sprawl: the subsystems (outbox, identity, auth, accounts,
briefing) share one `App`, one database handle, and one integration
harness (`productPG`, `seedSession`, `jsonBody`). Splitting them into
further packages would mean exporting dozens of identifiers and
duplicating the harness — churn with no boundary gained. `internal/app`
exports exactly four entry points to `main.go`: `Serve`, `Migrate`,
`RepairScans`, `BackfillBodies`.

A subsystem earns its own package or module only when it has a **hard
boundary**: its own deployable, its own consumers outside this repo, or no
shared `App`/harness state. Current examples:

| Piece | Form | Boundary |
| --- | --- | --- |
| `mail-engine/` | vendored Go module | owned by the Neutron mail project, re-vendored on updates |
| `mcp/` | separate Go module | optional adapter; the server never depends on it |
| `internal/faketransport/` | package | standalone fake SMTP/IMAP with no product imports |

When in doubt: keep it in the package and give it a `docs/<name>.md`
contract instead (see `docs/durable-outbox.md`, `docs/identity-contract.md`).

## Docs

- `docs/*.md` — design contracts and validation records.
- `docs/audits/` — frozen audit reports, verbatim; `AUDIT_OPEN.md` at root
  tracks open/deferred items and is the authority.
- Local-only scratch (`STATUS.md`, `_archive/`) stays gitignored. Never
  track session notes, handoffs, or continuation prompts.

## License

FSL-1.1-MIT (each release becomes MIT after two years). Every public
surface — `LICENSE`, README, the marketing site, and the GitHub About
description/topics — must name the same license. Metadata drift here is a
bug: it tells people the code carries permissions it does not.

## Testing gates

- Unit: `go test ./... && go vet ./...` for the root, `mail-engine/`, and
  `mcp/` modules.
- Integration: the `*_pg_test.go` suites skip unless
  `LULL_TEST_DATABASE_URL` (product) or `NEUTRON_MAIL_TEST_DATABASE_URL`
  (engine) points at a disposable database. A green run that silently
  skipped every database test is not green (audit 3 OPS-07).
- Dashboard: `npm ci && npm test && npm run typecheck && npm run build`.

## Commits

Conventional style (`feat:`, `fix:`, `chore:`, `docs:`, `deploy:`).
History is public. A commit that closes an audit finding cites the report
and finding ID.
