-- email-soft product layer.
-- The mail mirror (mail_accounts, mail_mailboxes, mail_messages,
-- mail_message_mailboxes, mail_bodies, mail_sync_state) is created and owned
-- by neutron-mail's own schema migration. Read from it; never write to it or
-- alter it here.

CREATE TABLE IF NOT EXISTS users (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email text NOT NULL UNIQUE,
  display_name text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE users ADD COLUMN IF NOT EXISTS webauthn_handle bytea;

-- Authentication material is server-side. Browser cookies contain only a
-- random session or ceremony token; their hashes are what reach Postgres.
CREATE TABLE IF NOT EXISTS auth_credentials (
  id text PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name text NOT NULL DEFAULT 'Passkey',
  credential_ciphertext text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz
);
CREATE INDEX IF NOT EXISTS auth_credentials_user ON auth_credentials (user_id);

CREATE TABLE IF NOT EXISTS auth_sessions (
  id_hash text PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  user_agent text NOT NULL DEFAULT '',
  login_method text CHECK (login_method IN ('passkey','recovery','totp','bootstrap'))
);
ALTER TABLE auth_sessions ADD COLUMN IF NOT EXISTS login_method text
  CHECK (login_method IN ('passkey','recovery','totp','bootstrap'));
CREATE INDEX IF NOT EXISTS auth_sessions_user ON auth_sessions (user_id, expires_at);

CREATE TABLE IF NOT EXISTS auth_challenges (
  id_hash text PRIMARY KEY,
  user_id uuid REFERENCES users(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('bootstrap','register','login')),
  session_json text NOT NULL,
  expires_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS auth_recovery_codes (
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_hash text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  used_at timestamptz,
  PRIMARY KEY (user_id, code_hash)
);

CREATE TABLE IF NOT EXISTS auth_totp (
  user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  secret_ciphertext text NOT NULL,
  enabled_at timestamptz
);

CREATE TABLE IF NOT EXISTS push_subscriptions (
  endpoint_hash text PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  subscription_ciphertext text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS push_subscriptions_user ON push_subscriptions (user_id);

CREATE TABLE IF NOT EXISTS push_deliveries (
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  message_id text NOT NULL,
  delivered_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, message_id)
);

CREATE TABLE IF NOT EXISTS oauth_states (
  state_hash text PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider text NOT NULL CHECK (provider IN ('gmail','graph')),
  verifier text NOT NULL,
  expires_at timestamptz NOT NULL
);

-- Per-sender Screener decision. One decision covers all future mail from the
-- sender: allowed senders route straight to their bucket, unknown senders
-- land in the Screener.
CREATE TABLE IF NOT EXISTS hey_senders (
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  sender_key text NOT NULL,
  allowed boolean NOT NULL DEFAULT false,
  route text NOT NULL DEFAULT 'screener'
    CHECK (route IN ('screener','imbox','paper_trail','feed','blocked')),
  first_seen timestamptz NOT NULL DEFAULT now(),
  decided_at timestamptz,
  PRIMARY KEY (user_id, sender_key)
);

-- Per-message classification layered on top of the mirror. message identity
-- references neutron-mail's stable message ids/fingerprints.
CREATE TABLE IF NOT EXISTS hey_messages (
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  message_id text NOT NULL,
  bucket text NOT NULL DEFAULT 'screener'
    CHECK (bucket IN ('screener','imbox','paper_trail','feed','set_aside','later','dropped')),
  read_at timestamptz,
  set_aside_until timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, message_id)
);

-- Connected mailboxes. Credentials (app passwords until OAuth in Phase 1b)
-- are AES-256-GCM sealed with SECRET_KEY. mirror_account_id is the row this
-- product created in mail_accounts; the mirror is derived state owned by
-- neutron-mail, dropped wholesale when an account is disconnected.
CREATE TABLE IF NOT EXISTS email_accounts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  mirror_account_id text NOT NULL UNIQUE,
  provider text NOT NULL CHECK (provider IN ('imap','jmap','gmail','graph')),
  address text NOT NULL,
  label text NOT NULL DEFAULT '',
  username text NOT NULL DEFAULT '',
  host text NOT NULL DEFAULT '',
  port integer NOT NULL DEFAULT 0,
  smtp_host text NOT NULL DEFAULT '',
  smtp_port integer NOT NULL DEFAULT 587,
  cred_ciphertext text NOT NULL,
  backfill_days integer NOT NULL DEFAULT 90,
  retention_days integer NOT NULL DEFAULT 0 CHECK (retention_days >= 0),
  sync_enabled boolean NOT NULL DEFAULT true,
  last_sync_at timestamptz,
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE email_accounts ADD COLUMN IF NOT EXISTS retention_days integer NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS email_accounts_user ON email_accounts (user_id);

-- Board cards (branch experiment): pinned threads and manual notes laid over
-- the briefing's derived columns. A pin never moves mail — it is a marker on
-- top of whatever bucket the thread lives in, so it cannot fight the
-- classifier or the sweep. thread_key NULL = a manual note card.
CREATE TABLE IF NOT EXISTS board_cards (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  thread_key text,
  title text NOT NULL DEFAULT '',
  note text NOT NULL DEFAULT '',
  done_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS board_cards_user ON board_cards (user_id);
CREATE UNIQUE INDEX IF NOT EXISTS board_cards_one_pin
  ON board_cards (user_id, thread_key) WHERE thread_key IS NOT NULL;

-- Sticky notes: the spatial canvas. x/y are positions on the wall (px);
-- color is an index into the client's curated palette. Notes are thoughts,
-- not mail — they never touch buckets.
CREATE TABLE IF NOT EXISTS sticky_notes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  x integer NOT NULL DEFAULT 0,
  y integer NOT NULL DEFAULT 0,
  text text NOT NULL DEFAULT '',
  color integer NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS sticky_notes_user ON sticky_notes (user_id);

-- Installation-level settings written by first-run setup (currently: the
-- pinned browser origin when PUBLIC_URL was not set). Small on purpose:
-- anything richer belongs to a real feature table.
CREATE TABLE IF NOT EXISTS app_settings (
  key text PRIMARY KEY,
  value text NOT NULL
);

-- Phase 2+ tables (aliases, calendar, contacts, campaigns) land in later
-- migrations, only when their phase ships. See SPEC.md sections 6.2-6.5.
