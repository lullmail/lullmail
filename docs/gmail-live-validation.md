# Gmail live validation — consumer Google account

The standing live-provider gate, Gmail half (GMAIL-READ-01/GMAIL-AUTH-01
remain live-gated; submission through a live Gmail mailbox is explicitly
outstanding per `AUDIT_OPEN.md`). Everything the synthetic fixes proved
against fake transports gets confirmed here against the real provider.
Run after or alongside `docs/graph-live-validation.md`; record results in
the template at the end and paste into `AUDIT_OPEN.md`.

lullmail requests `openid email gmail.modify gmail.send` — all usable by
a consumer account on an unverified app in Testing mode with the account
added as a test user (self-consent; expect Google's unverified-app
warning screen, which is expected for single-owner testing).

## A. Setup

1. **Google Cloud project** — [console.cloud.google.com](https://console.cloud.google.com)
   → new project (no billing needed for Gmail API consent flows).
2. **OAuth consent screen** — External, app name anything; add your own
   address as a Test user. Publishing status stays **Testing** for
   self-testing.
3. **Credentials** → OAuth client ID → Web application:
   - Authorized redirect URI:
     `https://<PUBLIC_URL>/api/oauth/google/callback`
     (`http://localhost:<port>/api/oauth/google/callback` for a local
     instance)
   - Copy the client ID and secret.
4. **Environment**: `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET`, and
   `PUBLIC_URL` matching the redirect origin.
5. **Boot and check**: `GET /api/oauth/status` reports `"google":true`;
   the Google connect option is active in Settings → Accounts.
6. **Connect** with the account whose mail you do not mind mirroring
   into the test database; disconnect removes the mirror.

## B. Connect-time checks

| # | Check | How | Expected |
|---|---|---|---|
| B1 | Identity captured | connect completes; account row has the address | email from the OAuth identity (no mailbox-key concept for Gmail) |
| B2 | Initial sync | Settings → Accounts → Sync (`POST /api/accounts/<id>?op=sync&wait=1`) | mail lists; labels appear as mailboxes; attachments flagged |
| B3 | Token refresh after expiry | after ≥1 h, trigger sync again | still syncs (mid-run 401 → refresh → resume is the GMAIL-AUTH-01 contract) |

## C. Probe 1 — label membership changes (identity stability)

The register's premise: Gmail message IDs are **stable across labels** —
identity does not churn on filing. Confirm live.

1. Note a message's id:
   `SELECT id, subject FROM mail_messages WHERE account_id=(SELECT mirror_account_id FROM email_accounts WHERE provider='gmail') ORDER BY received_at DESC LIMIT 5;`
2. In Gmail, apply a second label (or move INBOX → another label).
3. Sync. Re-run the query; also check memberships:
   `SELECT mailbox_id, count(*) FROM mail_message_mailboxes WHERE account_id='<mirror>' GROUP BY 1;`

**Outcomes**: same id + membership moved = the premise holds (Gmail needs
none of the identity machinery for filing); id changed = a live finding
that reshapes everything — record it prominently.

## D. Probe 2 — label rename (folder identity)

1. `SELECT id, name FROM mail_mailboxes WHERE account_id='<mirror>';`
2. Rename a label in Gmail. Sync. Re-run.

**Outcomes**: same id, new name = folder identity stable (no churn);
new id = Gmail folder churn is real and the Graph plan's §3 concerns
apply to Gmail too — record message-impact (`mail_messages` ids after).

## E. Probe 3 — live submission (explicitly outstanding)

The register names this separately: sends have never run against a live
Gmail mailbox.

1. Compose and send a plain message to an address you control.
2. Send a **reply** from that address, then reply back from lullmail
   (exercises thread seeding + native threadId persistence, LUL-D12).
3. Verify: delivery at the recipient; the Sent copy appears; the reply
   threads correctly in the conversation view.

**Outcomes**: clean send + threaded reply = the outstanding item closes;
any 4xx/5xx from Gmail's send endpoint, a wrong In-Reply-To/References
shape, or a broken thread = a real finding for the register.

## F. Probe 4 — attachments and bodies live

1. Send yourself a message with a PDF or image attachment.
2. Sync; open it: attachment detected, preview/streams work
   (`Attachment` path); body renders (text and/or HTML).
3. Check the MIME budget behavior is invisible in normal use (the
   36.7 MB preflight never trips on ordinary mail).

## G. Probe 5 — pacing and retry under real conditions

1. Trigger several syncs in quick succession (a few minutes apart).
2. Watch the account's sync outcome and any reauth-pause behavior.

**Outcomes**: normal pacing, no permanent pauses = the synthetic
reliability contract holds live; any pause that requires manual
re-authentication after a successful refresh = the GMAIL-AUTH-01 class
surviving — record the exact credential state.

## H. Results template

```
Gmail live validation (consumer), <date>, account <address partly redacted>:
- B1 identity: <ok/fail>
- B2 initial sync: <ok/fail; messages, labels>
- B3 refresh after expiry: <ok/fail>
- P1 label move: <same id / new id>; membership: <moved correctly/incorrect>
- P2 label rename: <same id / new id>
- P3 submission: <sent ok/fail>; sent copy: <yes/no>; reply threading: <ok/broken>
- P4 attachments: <detected+streamed/fail>; body render: <ok/fail>
- P5 pacing/pauses: <normal/permanent pause observed + detail>
Plan impact: <which gates close; which findings need fixing>
```

The SQL observation kit in `docs/graph-live-validation.md` works
unchanged for Gmail (substitute `provider='gmail'`).

The sync trigger is `POST /api/accounts/<product-account-id>?op=sync`
(async) or `...&wait=1` (synchronous); the product account id is the
`email_accounts.id` UUID.
