# Demo instance

A public showroom: Lull Mail on made-up mail, no sign-in.

**Never point this at real mail.** With `LULL_DEMO=1` every visitor is the
owner. The mode exists for an instance whose database holds only the seed in
`dashboard/e2e/seed.sql` and is reset on a timer.

## What demo mode changes

- No sign-in and no first-run setup: the app creates one owner and treats
  every request as that owner.
- Visitors can read everything and do what the product is for — open, file,
  snooze and screen mail, pin to the board.
- Everything else is refused with a 403 that says why: sending, connecting a
  mailbox, credentials, tokens, exports, notes and board cards (a visitor's
  own words would be shown to the next visitor), and preferences. The list
  is an allowlist in `internal/app/demo.go`; a new route is refused until
  someone adds it there.
- The dashboard shows a banner saying it is a demo.

## Running one

1. Deploy the normal image with `LULL_DEMO=1` and its own Postgres. Give it
   its own database — not a schema inside a real install's.
2. Start it once, so the owner row exists.
3. Load the mail, and repeat on a timer (every fifteen minutes is what the
   banner promises):

       tools/demo/reset.sh | docker exec -i <demo-postgres> psql -U <user> -d <db> -v ON_ERROR_STOP=1

`reset.sh` only prints SQL; it holds no connection details. The reset runs
in one transaction, so a visitor never sees a half-empty mailbox.
