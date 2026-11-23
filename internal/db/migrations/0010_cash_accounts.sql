-- Cash accounts: how "caixa hoje" is split and what each part yields (gross,
-- as a % of the CDI). Updating the cash with accounts records each balance
-- and the snapshot is their sum.
CREATE TABLE cash_accounts (
  ledger_id     bigint NOT NULL DEFAULT current_ledger() REFERENCES ledgers (id) ON DELETE CASCADE,
  id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name          text NOT NULL,
  cdi_pct       double precision NOT NULL DEFAULT 0 CHECK (cdi_pct >= 0 AND cdi_pct <= 300), -- % do CDI (100 = 100% do CDI)
  balance_cents bigint NOT NULL DEFAULT 0,
  balance_date  date,
  UNIQUE (ledger_id, name),
  UNIQUE (ledger_id, id)
);
ALTER TABLE cash_accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE cash_accounts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant ON cash_accounts USING (ledger_id = current_ledger()) WITH CHECK (ledger_id = current_ledger());

-- Public market rates (not tenant data): the CDI fetched from the Banco
-- Central once a day, so the projection works offline with the last value.
CREATE TABLE market_rates (
  name       text PRIMARY KEY,
  value      double precision NOT NULL, -- % ao ano
  as_of      date NOT NULL,
  fetched_at timestamptz NOT NULL DEFAULT now()
);
