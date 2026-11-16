-- Interest-bearing debts (financiamentos). system 'none' keeps the old behaviour:
-- saldo = valor inicial - soma dos pagamentos.
ALTER TABLE debts ADD COLUMN system TEXT NOT NULL DEFAULT 'none';
ALTER TABLE debts ADD COLUMN monthly_rate REAL NOT NULL DEFAULT 0;      -- % ao mês
ALTER TABLE debts ADD COLUMN term_months INTEGER NOT NULL DEFAULT 0;
ALTER TABLE debts ADD COLUMN first_due TEXT;                            -- YYYY-MM-DD
ALTER TABLE debts ADD COLUMN fees_cents INTEGER NOT NULL DEFAULT 0;     -- seguros/taxas por parcela
ALTER TABLE debts ADD COLUMN anchor_n INTEGER NOT NULL DEFAULT 0;       -- saldo conhecido após a parcela N
ALTER TABLE debts ADD COLUMN anchor_balance_cents INTEGER NOT NULL DEFAULT 0;

-- A recurring expense can pay a debt: its confirmed occurrences become payments.
ALTER TABLE recurrences ADD COLUMN debt_id INTEGER REFERENCES debts (id) ON DELETE SET NULL;
