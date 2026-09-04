# Lull Mail

[lullmail.com](https://lullmail.com) — a briefing-first mail client. Mail is a document you read, not a console you
operate: senders are screened once and routed forever, a daily digest tells
you what actually needs you, and the work surfaces (board, calendar, notes)
derive themselves from your mail instead of asking you to maintain them.

The design stance: your mail is yours. Credentials are sealed at rest,
tracker images are blocked by default, and one-click export (mbox/EML,
RFC 4155) is a table-stakes feature, not a cancellation flow.

## What's in

- **The Screener** — new senders wait for one decision; everything they ever
  send follows it. Reversible, per sender.
- **Today** — the briefing: what needs you, who you're waiting on (with
  aging), what quietly arrived. "You're done." is a real state.
- **Inbox / Reading / Receipts / Snoozed** — mail files itself by your
  sender rules; dated snoozes return to the Inbox on their day.
- **Board** — a personal kanban where the cards write themselves: unread
  Inbox mail is the to-do column, threads you answered are the waiting-on
  column, dated snoozes are the deferred pile. Pin anything; pins are
  markers and never move your mail.
- **Calendar** — year/month/week over the mail that comes back to you.
- **Notes** — a spatial canvas of stickies. Thoughts, not tasks.
- Keyboard-first (j/k, verbs, `g` jumps, one command palette), undo on every
  action, light/sepia/dark, no telemetry.
- **Rich sending** — compose plain text or paste HTML: HTML goes out as
  multipart/alternative with an automatic plain-text fallback, a sandboxed
  preview shows the rendering, and IMAP/Gmail/Graph providers all carry the
  rich part. Attachments ride along as multipart/mixed (15 MiB per file,
  25 MiB per message) and survive parked drafts; To/Cc/Bcc accept
  comma-separated address lists and the From account is a dropdown.
- **Leave cleanly** — download one original message as EML or a whole account
  as mailbox-by-mailbox mboxrd, with a manifest that discloses any local-mirror
  fallback instead of silently dropping data.
- **Explicit history** — every account states how much history Lull Mail
  organizes: 30 days, 90 days (default), 1 year, 3 years, or "All history."
  Changing the window grows or shrinks the organized views only; the local
  mirror obeys the separate retention setting, and provider mail is never
  touched. Message bodies and attachments stay lazy — history organizes
  metadata and fetches content when a thread is opened.
- Installable PWA with an explicit iOS path, account-bound offline reading,
  resilient local drafts, a conservative replay queue for reversible filing
  actions, and optional private web-push alerts.
- Recovery-first security: discoverable passkeys with required user
  verification, multiple-key management, printable one-use recovery codes,
  optional TOTP, HttpOnly server sessions, revocation, rate limiting, and
  provable self-serve deletion.

Standalone and single-owner by design: one owner per install, no multi-user
tenancy, no campaign-sending infrastructure.

## Quickstart (self-host)

No configuration required:

```
docker compose up -d
docker compose logs app      # one-time setup token, valid 24h
```

Open `http://localhost:8080` and finish setup in the browser: paste the
token, enter your address, create a passkey, save the recovery codes. The
token stops authenticating the moment the first passkey exists.

Deploying with [teploy](https://teploy.com) instead of raw Docker? A
template `teploy.yml` is committed — point `server:` at a host from your
`~/.teploy/servers.yml` and `teploy deploy <server>`.

`SECRET_KEY` (seals credentials) and the setup token are generated on first
boot and kept in the `appdata` volume; the browser origin is detected from
your first visit and pinned — so behind a reverse proxy, reach the app
through the final public URL when you run setup. Set the env vars below only
to override any of this; env always wins. Migrations run automatically at
boot (`lullmail migrate` runs them by hand).

## Environment

| Variable | Required | Notes |
|---|---|---|
| `DATABASE_URL` | yes | Postgres, e.g. `postgres://user:pass@host:5432/lullmail` |
| `SECRET_KEY` | no | Seals mail credentials, OAuth/passkey records, TOTP, and push subscriptions with AES-256-GCM. Generated on first boot into `DATA_DIR/secret.key` when unset; changing it invalidates sealed data. |
| `LULL_TOKEN` | no | One-time installation token for registering the first passkey. Generated (24h expiry, printed to logs) when unset. Rejected after first setup; restart regenerates while no passkey exists. |
| `LULL_USER_EMAIL` | no | Owner address; normally entered on the setup page instead. |
| `PUBLIC_URL` | no | Browser origin, e.g. `https://mail.example.com`. Auto-detected from the first setup visit and pinned in the database; the env var forces an origin. WebAuthn and mutation-origin checks reject a different origin. |
| `DATA_DIR` | no | Where the generated key and setup token live (default `./data`). Mount it as a volume or restarts regenerate them. |
| `WEBAUTHN_RP_ID` | no | Relying-party domain; derived from the effective origin. |
| `PORT` / `ADDR` | no | Defaults to `:8080`. |

Optional Google: `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET`. Redirect URI:
`$PUBLIC_URL/api/oauth/google/callback`. Required scopes are `gmail.modify`
and `gmail.send` plus identity; public multi-user distribution still requires
Google's restricted-scope verification/CASA process.

Optional Microsoft: `MICROSOFT_CLIENT_ID`, `MICROSOFT_CLIENT_SECRET`, and
`MICROSOFT_TENANT` (default `common`). Redirect URI:
`$PUBLIC_URL/api/oauth/microsoft/callback`; delegated scopes are
`User.Read`, `Mail.ReadWrite`, `Mail.Send`, and `offline_access`.

Optional web push: `VAPID_PUBLIC_KEY`, `VAPID_PRIVATE_KEY`, and
`VAPID_SUBJECT` (normally `mailto:you@example.com`). Generate a pair with any
standards-compatible VAPID tool. Notifications are deliberately generic and
never put subject/sender content on the lock screen.

## Develop

```
go build ./... && go test ./... && go vet ./...
cd dashboard && npm ci && npm test && npm run typecheck && npm run build
go run . serve
```

`dist/` is embedded into the binary at Go build time — after changing
dashboard code, rebuild the dashboard, then rebuild/restart the binary; a
running process keeps serving the copy it embedded.

The marketing site is a separate Neutron static build (`site/`), deployed
at [lullmail.com](https://lullmail.com):

```
cd site && npm install && npm run dev
```

A local mail world for testing (GreenMail — real IMAP/SMTP against fake
accounts):

```
docker run -d --name lullmail-mail -p 127.0.0.1:10143:3143 -p 127.0.0.1:10025:3025 \
  greenmail/standalone:2.0.1 -Dgreenmail.setup.test.all \
  -Dgreenmail.users=you@local.test:password
```

Connect `you@local.test` / `password` / host `127.0.0.1` port `10143` from
the Accounts page. Loopback IMAP is allowed plaintext for exactly this.

## Agents and MCP

Mail is a great context source for scripts and AI agents, so the agent
surface is first-class: long-lived revocable Bearer tokens (fenced to mail
and work surfaces — they can never touch sign-in, passkeys, or sessions),
and a standalone MCP adapter in [`mcp/`](./mcp) that exposes the whole
read/screener/send/board/notes surface to any MCP-capable client. The
server itself stays agent-agnostic; the adapter is optional and the product
never depends on it.

## Architecture

One Go binary. The mail engine ([`mail-engine/`](./mail-engine), vendored
from Neutron's mail module) owns IMAP/JMAP sync and the `mail_*` mirror
tables; the product layer (this repo's Go files) owns users, sender rules,
buckets, briefing/board derivation, and the embedded Preact dashboard.
Postgres is the only dependency.

```
provider ──sync──> mail_* mirror ──classify──> buckets ──derive──> Today / Board / Calendar
```

## Status

Release-candidate. The complete local product path is implemented:
auth/recovery, IMAP/JMAP, provider-OAuth plumbing and refresh,
sync/triage/read/send, responsive PWA/offline behavior, privacy controls,
push, standards exports, retention, mailbox disconnect, and full-account
deletion. What remains is operator work, not missing code: Google/Microsoft
public consent approval for a distributed OAuth client (self-hosters can
bring their own client IDs today — see Environment), and a real-provider
credential smoke test. Campaign-sending infrastructure is deliberately out
of scope for this mailbox.

## License

AGPL-3.0 — see [LICENSE](./LICENSE). The copyright is held by a single
author, so a commercial/hosted license remains possible alongside the AGPL;
the vendored `mail-engine/` module is covered by the same license.
