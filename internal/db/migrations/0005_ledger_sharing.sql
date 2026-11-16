-- Sharing a ledger ("carteira") with other people. Additive only.
-- Everyone already in a ledger is its owner.
ALTER TABLE ledger_members ADD COLUMN role text NOT NULL DEFAULT 'owner' CHECK (role IN ('owner', 'member'));

-- An invite is bound to an email; it is accepted only by a sign-in whose
-- (Google-verified, or CLI-created) email matches. Identity table: no RLS,
-- reached only through db.DB methods.
CREATE TABLE ledger_invites (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  ledger_id   bigint NOT NULL REFERENCES ledgers (id) ON DELETE CASCADE,
  email       text NOT NULL CHECK (email = lower(email)),
  invited_by  bigint REFERENCES users (id) ON DELETE SET NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  expires_at  timestamptz NOT NULL,
  accepted_at timestamptz,
  revoked_at  timestamptz
);
CREATE UNIQUE INDEX ledger_invites_open ON ledger_invites (ledger_id, email)
  WHERE accepted_at IS NULL AND revoked_at IS NULL;
CREATE INDEX ledger_invites_email ON ledger_invites (email) WHERE accepted_at IS NULL AND revoked_at IS NULL;
