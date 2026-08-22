# email-soft

A briefing-first mail client. Mail is a document you read, not a console you
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

Single-user v0 (see Status).

## Quickstart (self-host)

```
SECRET_KEY=$(openssl rand -hex 32) \
EMAILSOFT_TOKEN=$(openssl rand -hex 24) \
EMAILSOFT_USER_EMAIL=you@example.com \
docker compose up -d
```

Then open `http://localhost:8080`, sign in with `EMAILSOFT_TOKEN`, and
connect an IMAP/JMAP account (host, username, app password). Gmail and
Outlook OAuth are on the roadmap but not shipped yet.

Migrations run automatically at boot. To run them by hand: `email-soft migrate`.

## Environment

| Variable | Required | Notes |
|---|---|---|
| `DATABASE_URL` | yes | Postgres, e.g. `postgres://user:pass@host:5432/emailsoft` |
| `SECRET_KEY` | for sending/connect | Seals provider credentials (AES-256-GCM). Changing it invalidates stored credentials. |
| `EMAILSOFT_TOKEN` | for API access | The sign-in token; API returns 503 when unset. Long random string. |
| `EMAILSOFT_USER_EMAIL` | for sending/connect | Bootstraps the single user; used for whose-turn analysis. |
| `PORT` / `ADDR` | no | Defaults to `:8080`. |

## Develop

```
go build ./... && go test ./...
cd dashboard && npm i && npm run build   # dashboard is embedded via go:embed
go run . serve
```

A local mail world for testing (GreenMail — real IMAP/SMTP against fake
accounts):

```
docker run -d --name es-mail -p 127.0.0.1:10143:3143 -p 127.0.0.1:10025:3025 \
  greenmail/standalone:2.0.1 -Dgreenmail.setup.test.all \
  -Dgreenmail.users=you@local.test:password
```

Connect `you@local.test` / `password` / host `127.0.0.1` port `10143` from
the Accounts page. Loopback IMAP is allowed plaintext for exactly this.

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

Early. Shaped like a product, sized like a preview: single user with an
env-token sign-in (passkeys are the next auth milestone), Gmail/Outlook OAuth
pending verification paperwork, export/deletion controls in flight, no
mobile apps. SPEC.md is the product truth; TASKS.md is the build order.

## License

MIT — see [LICENSE](./LICENSE).
