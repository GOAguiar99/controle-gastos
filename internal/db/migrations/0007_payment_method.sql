-- How an expense was paid now lives on the transaction (and recurrence), not on
-- its category: the same "Alimentação" can be paid by card one day and by Pix
-- the next. The expense group shown in summaries (Cartão / Fixos / Outros) is
-- derived from it: credito → Cartão, else fixed → Fixos, else Outros.
ALTER TABLE transactions
  ADD COLUMN payment_method text NOT NULL DEFAULT 'outro'
    CHECK (payment_method IN ('credito', 'debito', 'pix', 'dinheiro', 'boleto', 'outro')),
  -- Fixed expense: set when the transaction confirms a recurrence. Backfilled
  -- from the old category group so past months keep their totals.
  ADD COLUMN fixed boolean NOT NULL DEFAULT false;

ALTER TABLE recurrences
  ADD COLUMN payment_method text NOT NULL DEFAULT 'outro'
    CHECK (payment_method IN ('credito', 'debito', 'pix', 'dinheiro', 'boleto', 'outro'));

-- Backfill every ledger: RLS would hide all rows here (no ledger is set), so
-- lift it just for this, as 0006 does.
ALTER TABLE transactions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE recurrences NO FORCE ROW LEVEL SECURITY;
ALTER TABLE categories NO FORCE ROW LEVEL SECURITY;

UPDATE transactions t SET
    payment_method = CASE WHEN c.bucket = 'cartao' THEN 'credito' ELSE 'outro' END,
    fixed = (c.bucket = 'fixo')
  FROM categories c
  WHERE c.id = t.category_id AND t.kind = 'expense';

UPDATE recurrences r SET payment_method = 'credito'
  FROM categories c
  WHERE c.id = r.category_id AND r.kind = 'expense' AND c.bucket = 'cartao';

ALTER TABLE transactions FORCE ROW LEVEL SECURITY;
ALTER TABLE recurrences FORCE ROW LEVEL SECURITY;
ALTER TABLE categories FORCE ROW LEVEL SECURITY;
