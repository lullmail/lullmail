# email-soft MCP adapter

An MCP server that exposes [email-soft](..) to AI agents and scripts.
email-soft itself is agent-agnostic: it has an HTTP API and revocable agent
tokens, nothing more. This adapter is one optional client of that API — any
MCP-capable agent can use it, and email-soft keeps working without it.

A nested Go module on purpose: it depends only on email-soft's HTTP surface,
never its internals, and the main build never compiles it.

## Setup

1. In email-soft: **Settings → Security → Agent tokens → Create token**.
   Copy the `es_...` value (shown once).
2. Build: `go build -o email-soft-mcp .`
3. Point any MCP client at it:

```json
{
  "mcpServers": {
    "email-soft": {
      "command": "/usr/local/bin/email-soft-mcp",
      "env": {
        "EMAILSOFT_URL": "https://mail.example.com",
        "EMAILSOFT_AGENT_TOKEN": "es_..."
      }
    }
  }
}
```

## Tools

| Tool | What it does |
|---|---|
| `list_accounts` | Connected mailboxes, sync status, counts |
| `add_account` | Connect an IMAP/JMAP mailbox (encrypted at rest by the server) |
| `delete_account` | Disconnect a mailbox; provider mail is never touched |
| `sync_account` | On-demand sync |
| `today_briefing` | What needs a reply, who owes you, what arrived quietly |
| `list_bucket` | Threads in screener / imbox / paper_trail / feed / snoozed / set_aside / later |
| `search_mail` | Search subjects, participants, previews across all mailboxes |
| `read_thread` | Full messages of one thread, oldest first |
| `send_mail` | Send plain text; server-side reply threading |
| `screener_list` | Senders awaiting a one-time decision |
| `screener_decide` | Allow (inbox/reading/feed) or block a sender forever |

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
