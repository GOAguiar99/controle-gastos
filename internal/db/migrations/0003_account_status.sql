-- Operators can suspend an account (CLI: suspend-user). A suspended user
-- cannot sign in, and their sessions and MCP links stop working. Additive
-- only: existing users stay active (NULL).
ALTER TABLE users ADD COLUMN suspended_at timestamptz;
