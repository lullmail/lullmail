# Graph live validation — consumer Outlook account

The standing live-provider gate (Gmail and Graph have never run against real
accounts in this product) and the four residual uncertainties in
`docs/identity-stability.md` are settled by this checklist. Every probe
maps to a plan decision: run them in order, record results in the template
at the end, and paste the filled table into `AUDIT_OPEN.md`.

Consumer accounts are valid Graph citizens: the delegated scopes lullmail
requests (`openid email offline_access User.Read Mail.ReadWrite Mail.Send`)
are all consentable by personal Microsoft accounts, and lullmail's OAuth
endpoint uses tenant `common` by default, which accepts them.

## A. Setup

1. **App registration** — sign in at
   [entra.microsoft.com](https://entra.microsoft.com) with the Outlook
   account → App registrations → New registration:
   - Name: e.g. `lullmail-dev`
   - Supported account types: **Accounts in any organizational directory
     and personal Microsoft accounts**
   - Redirect URI (Web): `https://<PUBLIC_URL>/api/oauth/microsoft/callback`
     — for a local instance `http://localhost:<port>/api/oauth/microsoft/callback`
     is accepted by Microsoft
2. **Secret** — Certificates & secrets → New client secret → copy the
   **Value** immediately (shown once).
3. **Environment**:
   - `MICROSOFT_CLIENT_ID` — Application (client) ID
   - `MICROSOFT_CLIENT_SECRET` — the secret value
   - `PUBLIC_URL` — the origin matching the redirect URI
   - `MICROSOFT_TENANT` — leave unset (`common` default covers consumer)
4. **Boot and check**: `curl -s http://localhost:<port>/api/oauth/status`
   must report `"microsoft":true`. In Settings → Accounts the Microsoft
   connect option is active.
5. **Connect**: run the OAuth flow with the Outlook account. Expect an
   "unverified publisher" warning — this is your own app self-consented;
   proceed.

Use an account whose mail you do not mind mirroring into the test database;
everything it syncs lands in Postgres, and a clean disconnect afterwards
removes the mirror.

## B. Connect-time checks

| # | Check | How | Expected |
|---|---|---|---|
| B1 | Verified mailbox key captured | `SELECT address, graph_mailbox_key<>'' AS key_ok FROM email_accounts WHERE provider='graph';` | `key_ok = t` (the `/me` id — OAuth connect refuses without it) |
| B2 | Initial sync completes | Settings → Accounts → Sync (or `POST /api/accounts/<id>?op=sync&wait=1`) | mail appears; account shows a healthy sync outcome |
| B3 | Token refresh survives expiry | after ≥1 h (access-token lifetime), trigger another sync | still syncs — a permanent 401-pause here would be the Graph analog of GMAIL-AUTH-01 and must be recorded |

## C. Probe 1 — message ID stability across a folder move (Graph Explorer, direct)

Isolates Microsoft's behavior from lullmail entirely.

1. [Graph Explorer](https://developer.microsoft.com/graph/graph-explorer)
   signed in with the same account.
2. `GET /v1.0/me/messages?$top=1&$select=id,subject` — record the `id`
   and the subject.
3. Move it: `POST /v1.0/me/messages/{id}/move` body
   `{"destinationId":"<some-other-folder-id>"}`.
4. Find it in the destination
   (`GET /v1.0/me/mailFolders/{destId}/messages?$select=id,subject`) and
   compare ids.

**Outcomes**

| Result | Meaning for the plan |
|---|---|
| Same id after move | Consumer accounts may not churn IDs on moves — the destroy-and-recreate loss may not exist here; Phases 1–2 shrink dramatically. Re-verify on folder *delete* and *archive* moves too. |
| Different id | The loss mechanism is confirmed live on consumer accounts; Phase 2's destroy-with-references contract is justified. Record both id shapes (length/prefix) — mutable vs immutable formats are visually distinct. |

## D. Probe 2 — in-product folder move + sync

1. Pick 2–3 distinctive messages in the dashboard; note their ids:
   `SELECT id, subject FROM mail_messages WHERE account_id=(SELECT mirror_account_id FROM email_accounts WHERE provider='graph') ORDER BY received_at DESC LIMIT 10;`
2. Classify at least one (e.g. Paper Trail) so product filing state exists;
   confirm its row: `SELECT message_id, bucket FROM hey_messages WHERE account_id='<mirror>';`
3. Move those messages to another folder in Outlook web (or Explorer).
4. Trigger sync: `POST /api/accounts/<product-account-id>?op=sync&wait=1`.
5. Re-run both queries plus:
   `SELECT mailbox_id, count(*) FROM mail_message_mailboxes WHERE account_id='<mirror>' GROUP BY 1;`

**Outcomes**

| Result | Meaning |
|---|---|
| Old ids gone, new ids present, `hey_messages` rows for them deleted | Confirmed end-to-end loss (destroy-and-recreate + orphan cleanup) — the exact scenario `docs/identity-stability.md` §2 describes. Strongest possible justification for Phases 1–2. |
| Old ids gone, new ids present, `hey_messages` rows linger | Cleanup missed them — a bug in `cleanupMirrorOrphans` worth its own register entry. |
| Ids unchanged | Consistent with Probe 1 same-id outcome. |

## E. Probe 3 — folder rename / move (folder identity churn)

1. Snapshot:
   `SELECT id, name FROM mail_mailboxes WHERE account_id='<mirror>';`
   `SELECT mailbox_id, cursor FROM mail_sync_state WHERE account_id='<mirror>';`
2. Rename a folder in Outlook web. Trigger sync.
3. Re-run both snapshots.

**Outcomes**

| Result | Meaning |
|---|---|
| Folder row same id, new name | Folder ids stable under rename — one less churn source. |
| Old folder row gone, new row new id; memberships re-pointed or dropped | Folder-ID churn confirmed — the mirror-razing dimension (§3) is real on consumer accounts; Phase 4 must migrate folder ids. Note whether `mail_sync_state` gained a new key and whether bodies survived (`SELECT count(*) FROM mail_bodies WHERE account_id='<mirror>';`). |

## F. Probe 4 — ImmutableId support on consumer accounts

1. In Graph Explorer: add request header `Prefer: IdType="ImmutableId"`.
2. `GET /v1.0/me/messages?$top=5&$select=id` — record id shapes.
3. Repeat without the header; also `GET /v1.0/me/mailFolders?$select=id`
   with and without the header (folder ids matter more — §3).

**Outcomes**

| Result | Meaning |
|---|---|
| Different id format with the header (and stable across a move with the header) | ImmutableId honored on consumer accounts — Phase 4's premise holds. |
| Header ignored / error | Phase 4 is dead for consumer accounts; the plan must re-scope to fingerprint pairing or accepted loss. This is the single most plan-changing probe. |

## G. Results template

Paste filled into `AUDIT_OPEN.md` under the identity-stability entry:

```
Graph live validation (consumer), <date>, account <address partly redacted>:
- B1 mailbox key: <ok/fail>
- B2 initial sync: <ok/fail, messages>
- B3 refresh after expiry: <ok/fail>
- P1 move via Explorer: <same id / new id> (id shape: <len/prefix>)
- P2 in-product move+sync: <remapped cleanly / orphaned+deleted / lingered>
- P3 folder rename: <stable id / new id; memberships/bodies outcome>
- P4 ImmutableId: <honored / ignored / error>, folder ids: <same>
Plan impact: <which phases confirmed / shrunk / killed>
```

## SQL observation kit

```sql
-- mirror account id (substitute into <mirror> below)
SELECT id, mirror_account_id, address, graph_mailbox_key<>'' AS key_ok
  FROM email_accounts WHERE provider='graph';

-- message identities
SELECT id, subject, received_at FROM mail_messages
  WHERE account_id='<mirror>' ORDER BY received_at DESC LIMIT 15;

-- product filing state
SELECT message_id, bucket FROM hey_messages WHERE account_id='<mirror>';

-- memberships
SELECT mailbox_id, count(*) FROM mail_message_mailboxes
  WHERE account_id='<mirror>' GROUP BY 1 ORDER BY 1;

-- folders and sync cursors
SELECT id, name FROM mail_mailboxes WHERE account_id='<mirror>';
SELECT mailbox_id, cursor FROM mail_sync_state WHERE account_id='<mirror>';

-- bodies (churn casualty check)
SELECT count(*) FROM mail_bodies WHERE account_id='<mirror>';
```

The sync trigger is `POST /api/accounts/<product-account-id>?op=sync`
(async) or `...&wait=1` (synchronous); the product account id is the
`email_accounts.id` UUID, not the mirror id.
