# Lull Mail MCP adapter

An MCP server that exposes [Lull Mail](..) to AI agents and scripts.
Lull Mail itself is agent-agnostic: it has an HTTP API and revocable agent
tokens, nothing more. This adapter is one optional client of that API — any
MCP-capable agent can use it, and Lull Mail keeps working without it.

A nested Go module on purpose: it depends only on Lull Mail's HTTP surface,
never its internals, and the main build never compiles it.

## Setup

1. In Lull Mail: **Settings → Security → Agent tokens → Create token**.
   Copy the `lull_...` value (shown once).
2. Build: `go build -o lullmail-mcp .`
3. Point any MCP client at it:

```json
{
  "mcpServers": {
    "lullmail": {
      "command": "/usr/local/bin/lullmail-mcp",
      "env": {
        "LULL_URL": "https://lullmail.com",
        "LULL_AGENT_TOKEN": "lull_..."
      }
    }
  }
}
```

## Tools

**Accounts:** `list_accounts` · `add_account` (IMAP/JMAP; backfill 0 = all
history) · `update_account` (pause sync / retention / history window) ·
`delete_account` · `sync_account`

**Read:** `list_bucket` (screener / imbox / paper_trail / feed / snoozed /
set_aside / later) · `search_mail` · `read_thread` · `recent_mail` ·
`list_mailboxes` + `list_folder` (raw provider folders) · `get_attachment` /
`get_eml` (base64) · `get_counts` · `people_list` · `today_briefing`

**Act:** `message_action` (read/unread/move/snooze — operates on the whole
thread) · `send_mail` (plain or HTML; HTML sends multipart/alternative with
an automatic plain fallback) · `undo_send` · `screener_list` ·
`screener_decide` · `screener_undecide`

**Work surfaces:** `board_list` / `board_pin` / `board_unpin` /
`board_add_card` / `board_card_done` · `notes_list` / `note_create` /
`note_update` / `note_delete` · `classify_now`

Not exposed, on purpose: sign-in, passkeys, sessions, TOTP, agent-token
management, full-account deletion, OAuth browser flows. Agent tokens are
fenced to mail + work surfaces at the router (`agent.go` in the server).

## The bulk-import pattern

The reason this exists: "I have 40 mailboxes on Purelymail, set them all up."

With your mail provider's own API (Purelymail exposes one), an agent can do
the whole loop through these tools:

1. List mailboxes via the provider's API.
2. `add_account` for each — IMAP host `imap.purelymail.com`, SMTP
   `smtp.purelymail.com`, username = full address.
3. `sync_account`, then `screener_decide` for known senders.

Tokens are scoped: they can manage mailboxes and mail, never sign-in,
passkeys, sessions, or other tokens. Revoke in Security settings to kill one
instantly.
