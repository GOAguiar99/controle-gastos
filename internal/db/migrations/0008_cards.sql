-- Credit cards and their monthly statements (faturas). A purchase on a card
-- belongs to one statement and leaves the cash on that statement's due date.
CREATE TABLE cards (
  ledger_id   bigint NOT NULL DEFAULT current_ledger() REFERENCES ledgers (id) ON DELETE CASCADE,
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name        text NOT NULL,
  closing_day integer NOT NULL CHECK (closing_day BETWEEN 1 AND 31),
  due_day     integer NOT NULL CHECK (due_day BETWEEN 1 AND 31),
  active      boolean NOT NULL DEFAULT true,
  UNIQUE (ledger_id, name),
  UNIQUE (ledger_id, id)
);

-- The total the bank charges for a statement (valor cheio), optional. When it
-- is larger than the items entered, the difference counts as undetailed.
CREATE TABLE card_statements (
  ledger_id   bigint NOT NULL DEFAULT current_ledger() REFERENCES ledgers (id) ON DELETE CASCADE,
  card_id     bigint NOT NULL,
  month       text NOT NULL CHECK (month ~ '^\d{4}-\d{2}$'),
  total_cents bigint NOT NULL CHECK (total_cents >= 0),
  PRIMARY KEY (ledger_id, card_id, month),
  FOREIGN KEY (ledger_id, card_id) REFERENCES cards (ledger_id, id) ON DELETE CASCADE
);

ALTER TABLE transactions
  ADD COLUMN card_id bigint,
  ADD COLUMN statement_month text CHECK (statement_month ~ '^\d{4}-\d{2}$'),
  ADD COLUMN cash_date date,
  -- parcelado: parcel installment_n of installment_of; parcels share installment_group
  ADD COLUMN installment_group bigint,
  ADD COLUMN installment_n integer,
  ADD COLUMN installment_of integer,
  ADD FOREIGN KEY (ledger_id, card_id) REFERENCES cards (ledger_id, id),
  ADD CHECK ((card_id IS NULL) = (statement_month IS NULL)),
  ADD CHECK (installment_of IS NULL OR (installment_n BETWEEN 1 AND installment_of AND installment_of <= 99));
CREATE INDEX transactions_statement ON transactions (ledger_id, card_id, statement_month) WHERE card_id IS NOT NULL;

ALTER TABLE recurrences
  ADD COLUMN card_id bigint,
  ADD FOREIGN KEY (ledger_id, card_id) REFERENCES cards (ledger_id, id) ON DELETE SET NULL (card_id);

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['cards', 'card_statements']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant ON %I USING (ledger_id = current_ledger()) WITH CHECK (ledger_id = current_ledger())', t);
  END LOOP;
END $$;
