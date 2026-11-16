-- Users sign in with Google and belong to a ledger. These identity tables have
-- no RLS: they are reached only through db.DB methods (never a ledger Store),
-- always by user id or by the SHA-256 of a secret token.

-- A ledger created before sign-in existed (single-user install) waits for its
-- owner: the first verified Google login with this email joins it.
ALTER TABLE ledgers ADD COLUMN claim_email text;

CREATE TABLE users (
  id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  google_sub     text NOT NULL UNIQUE, -- stable Google account id; emails can change
  email          text NOT NULL,
  email_verified boolean NOT NULL,
  name           text NOT NULL DEFAULT '',
  picture_url    text NOT NULL DEFAULT '',
  created_at     timestamptz NOT NULL DEFAULT now(),
  last_login_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ledger_members (
  ledger_id bigint NOT NULL REFERENCES ledgers (id) ON DELETE CASCADE,
  user_id   bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  joined_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (ledger_id, user_id)
);
-- For now a user is in exactly one ledger.
CREATE UNIQUE INDEX ledger_members_one_per_user ON ledger_members (user_id);

CREATE TABLE sessions (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id      bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  token_hash   bytea NOT NULL UNIQUE, -- sha256 of the cookie value; the value itself is never stored
  created_at   timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  expires_at   timestamptz NOT NULL,
  ip           text NOT NULL DEFAULT '',
  user_agent   text NOT NULL DEFAULT '',
  revoked_at   timestamptz
);
CREATE INDEX sessions_user ON sessions (user_id);

-- Secret MCP links: each belongs to one member of one ledger and stops working
-- when revoked or when the user leaves the ledger.
CREATE TABLE mcp_tokens (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id      bigint NOT NULL,
  ledger_id    bigint NOT NULL,
  token_hash   bytea NOT NULL UNIQUE,
  label        text NOT NULL DEFAULT '',
  created_at   timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz,
  revoked_at   timestamptz,
  FOREIGN KEY (ledger_id, user_id) REFERENCES ledger_members (ledger_id, user_id) ON DELETE CASCADE
);
CREATE INDEX mcp_tokens_user ON mcp_tokens (user_id);

-- The single shared MCP secret is replaced by per-user links. RLS would hide
-- every row here (no ledger is set), so lift it for the owner just for this.
ALTER TABLE ledger_settings NO FORCE ROW LEVEL SECURITY;
DELETE FROM ledger_settings WHERE key = 'mcp_token';
ALTER TABLE ledger_settings FORCE ROW LEVEL SECURITY;
