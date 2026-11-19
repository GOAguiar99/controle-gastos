-- Category memory: when the user files a purchase from a merchant under a
-- category, later purchases from the same merchant (same normalized
-- description) get that category too.
CREATE TABLE category_rules (
  ledger_id    bigint NOT NULL DEFAULT current_ledger() REFERENCES ledgers (id) ON DELETE CASCADE,
  merchant_key text NOT NULL,             -- normalized description (app.descKey)
  example      text NOT NULL DEFAULT '',  -- the description as it was seen, for display
  category_id  bigint NOT NULL,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (ledger_id, merchant_key),
  FOREIGN KEY (ledger_id, category_id) REFERENCES categories (ledger_id, id) ON DELETE CASCADE
);
ALTER TABLE category_rules ENABLE ROW LEVEL SECURITY;
ALTER TABLE category_rules FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant ON category_rules USING (ledger_id = current_ledger()) WITH CHECK (ledger_id = current_ledger());
