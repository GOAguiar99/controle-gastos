-- When each user last used the app (web session or MCP link). Account
-- metadata, written at most every few minutes per user (app.lastSeenEvery);
-- NULL = never seen since this column existed. Shown to the user, to the
-- members of their ledger and to admins; never in metrics labels or logs.
ALTER TABLE users ADD COLUMN last_seen_at timestamptz;
UPDATE users SET last_seen_at = last_login_at;
