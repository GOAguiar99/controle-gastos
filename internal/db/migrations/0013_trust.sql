-- "Seus dados e segurança" (/seguranca): which version of that page each
-- user confirmed having read. A user whose version is behind
-- app.TrustVersion is shown the page again before the dashboard.
ALTER TABLE users ADD COLUMN trust_version integer NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN trust_ack_at timestamptz;
