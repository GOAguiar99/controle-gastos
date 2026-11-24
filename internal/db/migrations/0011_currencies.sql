-- Transactions in other currencies: amount_cents stays in reais (every
-- report, fatura and cash flow keeps working in BRL); the original currency,
-- the amount in it and the rate used (reais per unit) are kept alongside.
ALTER TABLE transactions
  ADD COLUMN currency          text CHECK (currency ~ '^[A-Z]{3}$' AND currency <> 'BRL'),
  ADD COLUMN orig_amount_cents bigint CHECK (orig_amount_cents > 0),
  ADD COLUMN fx_rate           double precision CHECK (fx_rate > 0),
  ADD CONSTRAINT transactions_currency_complete CHECK (
    (currency IS NULL AND orig_amount_cents IS NULL AND fx_rate IS NULL) OR
    (currency IS NOT NULL AND orig_amount_cents IS NOT NULL AND fx_rate IS NOT NULL));

-- Public exchange rates (not tenant data): the PTAX (venda, boletim de
-- fechamento) of the Banco Central, in reais per unit, by business day.
CREATE TABLE fx_rates (
  currency   text NOT NULL,
  date       date NOT NULL,
  rate       double precision NOT NULL CHECK (rate > 0),
  fetched_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (currency, date)
);
