-- Who entered each transaction, now that a ledger can be shared. NULL =
-- unknown (bulk imports, or a user who has since deleted their account).
ALTER TABLE transactions ADD COLUMN created_by bigint REFERENCES users (id) ON DELETE SET NULL;

-- Everything recorded so far predates sharing: it was entered by the ledger's
-- owner. RLS would hide every row here (no ledger is set), so lift it for the
-- owner just for this backfill, as 0002 does.
ALTER TABLE transactions NO FORCE ROW LEVEL SECURITY;
UPDATE transactions t SET created_by = m.user_id
  FROM ledger_members m
  WHERE m.ledger_id = t.ledger_id AND m.role = 'owner' AND t.created_by IS NULL;
ALTER TABLE transactions FORCE ROW LEVEL SECURITY;
