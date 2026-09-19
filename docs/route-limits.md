# API route limits

Every request body this server accepts has a route-specific bound, applied
before JSON decoding begins, and every provider (upstream) JSON response is
read through a bounded reader (audit OPS-01, closed 2026-09-18). This table
is the register the audit asked for: route, auth, body bound, notes.

## Conventions

- "session" = the HttpOnly session cookie; "agent" = agent Bearer token
  (`requireAgent`); "public" = no auth or setup-token gated.
- Body bounds are enforced with `http.MaxBytesReader` ahead of allocation;
  exceeding one answers **413 Request Too Large**, never a malformed-JSON 400.
- The send route additionally acquires one of 2 global decode slots BEFORE
  decoding (peak decode memory ≈ 2 × 34 MiB); requests that wait too long
  answer 503.

## Product API (`/api/...`)

| Route | Auth | Body bound | Notes |
|---|---|---|---|
| `POST /api/send` | session/agent | 34 MiB + decode slot | wire size of 25 MiB decoded attachments; per-file 15 MiB, ≤20 files, Graph 3 MB/file |
| `DELETE /api/outbox/{id}` | session/agent | — | undo window |
| `GET /api/accounts` `POST /api/accounts` | session/agent | 64 KiB | create: IMAP/JMAP credential form |
| `GET/POST/DELETE /api/accounts/{id}` | session/agent | 16 KiB, strict single-object, unknown fields rejected | settings (`decodeSettingsJSON`, audit 3 DATA-07) |
| `GET /api/accounts/{id}/export` | session/agent | — | build disk budget 2 GiB (`budgetedWriter`), per provider message read ≤128 MiB, temp file removed on every return path; stale temps swept at boot |
| `GET /api/events` | session/agent | — | SSE |
| `GET /api/security`, sessions, passkeys, TOTP, password | session | 4–16 KiB per ceremony | bootstrap begin 64 KiB (name/email form); TOTP confirm 4 KiB; TOTP/password/recovery mutations answer 428 without proof ≤10 min old (audit AUTH-02) |
| `POST /api/security/reauthenticate` | session | 16 KiB | password confirmation; KDF admission + account lockout as sign-in (audit AUTH-02) |
| `POST/DELETE /api/security/agent-tokens…` | session | 16 KiB | creation gated on ≤10 min proof (audit AUTH-02) |
| `DELETE /api/account` | session | 16 KiB | full-account delete confirmation |
| `GET /api/personal/export` | session/agent | — | trust export |
| `GET/POST/DELETE /api/push` | session | 64 KiB | |
| `POST /api/oauth/{provider}/start` | session | — | |
| `GET /api/screener`, `/api/counts`, `/api/prefs` | session/agent | — | |
| `POST /api/prefs` | session | 64 KiB | |
| `GET /api/search` | session/agent | — | `limit` 1–200 (default 60), keyset `cursor` |
| `GET /api/briefing`, `/api/board`, `/api/people`, `/api/recent`, `/api/folder`, `/api/mailboxes` | session/agent | — | |
| `POST /api/board/pin` `unpin` `cards` `cards/{id}/done` | session | 64 KiB | idempotent (`Idempotency-Key`) |
| `GET /api/notes` | session | — | |
| `POST /api/notes`, `POST /api/notes/{id}`, `DELETE /api/notes/{id}` | session | 64 KiB | idempotent |
| `POST /api/screener/decide`, `undecide` | session | 64 KiB | idempotent |
| `GET /api/buckets/{bucket}` | session/agent | — | `limit` 1–200 (default 200), keyset `cursor` |
| `GET /api/threads/{thread}` | session/agent | — | account param required; eager body fetch ≤8 messages / 8 s |
| `POST /api/messages/{message}/action` | session | 64 KiB | idempotent; snooze `until` absolute RFC3339 or null (audit DATA-07) |
| `GET /api/messages/{message}/attachment/{part}` | session | — | streams; aborts the connection on a mid-stream error |
| `GET /api/messages/{message}/eml` | session | — | provider read ≤128 MiB, mirror fallback beyond |
| `POST /api/classify` | session | — | |

## Auth surface (public, `/api/auth/...`)

| Route | Auth | Body bound |
|---|---|---|
| `GET /api/auth/status` | — | — |
| `POST /api/auth/bootstrap/begin` | one-time setup token | 64 KiB (name/email form) |
| `POST /api/auth/bootstrap/finish` `password` | one-time setup token | 16 KiB (WebAuthn/password ceremony) |
| `POST /api/auth/login/begin` `finish` | public | 16 KiB |
| `POST /api/auth/logout` | session | — |
| `POST /api/auth/password` | public (proof-carrying) | 16 KiB |
| `POST /api/auth/recovery` | public (code-carrying) | 16 KiB |
| `POST /api/auth/totp` | public (code-carrying) | 16 KiB | per-peer limiter plus durable per-user fixed window (10 failures / 5 min, audit AUTH-06) |

## OAuth callbacks (public, `/api/oauth/...`)

`GET /api/oauth/{google,microsoft}/callback` — no body; state validated
against `oauth_states`.

## Engine mirror surface (`/api/mail/...`)

Read-only behind `ownedMirror` + `readOnlyEngine`: GET/HEAD only, so no
request bodies. The engine's own handler still bounds its (unreachable
through this mount) send/mutation decodes at 34 MiB.

## Idempotency

Every mutable endpoint listed above with "idempotent" accepts an
`Idempotency-Key` header (≤128 chars): one recorded answer per (user, key)
in `api_mutations`, request-hash conflict detection (409 on reuse with a
different request), recorded answers pruned after 30 days. The buffered
idempotency copy of the request body is capped at 1 MiB.

## Provider (upstream) response bounds

| Path | Bound |
|---|---|
| OAuth identity (`oauthIdentity`) | 4 MiB JSON |
| Graph send API (`graphCall` with `out`) | 4 MiB JSON |
| Error bodies (all providers) | 2 KiB |
| Engine Graph adapter JSON | 64 MiB |
| Engine JMAP session | 4 MiB |
| Engine JMAP method responses | 64 MiB |
| Engine token callback | 1 MiB |
| Export raw message read | 128 MiB |

Every provider HTTP client carries a request timeout (15 s tokens, 60 s
Graph/JMAP, 30 s IMAP I/O); the send queue additionally enforces an
aggregate admission budget (8 jobs / 128 MiB retained, audit 3 SEND-03,
4 F14).
