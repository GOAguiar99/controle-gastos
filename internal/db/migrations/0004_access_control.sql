-- Access control for a public instance. Additive only.
--   approved_at: NULL = sign-up waiting for an admin's approval (no ledger yet).
--   role: 'admin' is granted only by the operator CLI (grant-admin), never by email.
--   password_hash / failed_logins / locked_until: self-hosted password mode
--   (APP_AUTH=password), argon2id PHC string; unused with Google sign-in.
ALTER TABLE users ADD COLUMN approved_at timestamptz;
ALTER TABLE users ADD COLUMN role text NOT NULL DEFAULT 'user' CHECK (role IN ('user', 'admin'));
ALTER TABLE users ADD COLUMN password_hash text;
ALTER TABLE users ADD COLUMN failed_logins integer NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN locked_until timestamptz;
-- everyone who already has an account stays active
UPDATE users SET approved_at = created_at;
CREATE INDEX users_pending ON users (created_at) WHERE approved_at IS NULL;
CREATE INDEX users_email ON users (lower(email));
