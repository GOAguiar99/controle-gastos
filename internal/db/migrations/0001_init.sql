-- PostgreSQL schema. Every row of user data belongs to a ledger ("carteira").
-- Tenant isolation is enforced by Row-Level Security: the app sets
-- app.ledger_id on each pooled connection it acquires, and every policy below
-- compares against it. With no ledger set, the setting is empty, the
-- comparison is NULL and no row is visible or writable (fail closed).
-- FORCE ROW LEVEL SECURITY makes the policies apply to the table owner too, so
-- the app must not run as a superuser or a BYPASSRLS role (checked at startup).

CREATE FUNCTION current_ledger() RETURNS bigint
  LANGUAGE sql STABLE
  AS $$ SELECT nullif(current_setting('app.ledger_id', true), '')::bigint $$;

CREATE TABLE ledgers (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name       text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE categories (
  ledger_id bigint NOT NULL DEFAULT current_ledger() REFERENCES ledgers (id) ON DELETE CASCADE,
  id        bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name      text NOT NULL,
  kind      text NOT NULL CHECK (kind IN ('income', 'expense')),
  bucket    text NOT NULL DEFAULT 'outros' CHECK (bucket IN ('cartao', 'fixo', 'outros')),
  UNIQUE (ledger_id, name, kind),
  UNIQUE (ledger_id, id)
);

CREATE TABLE debts (
  ledger_id            bigint NOT NULL DEFAULT current_ledger() REFERENCES ledgers (id) ON DELETE CASCADE,
  id                   bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name                 text NOT NULL,
  initial_cents        bigint NOT NULL,
  system               text NOT NULL DEFAULT 'none',
  monthly_rate         double precision NOT NULL DEFAULT 0, -- % ao mês
  term_months          integer NOT NULL DEFAULT 0,
  first_due            date,
  fees_cents           bigint NOT NULL DEFAULT 0,           -- seguros/taxas por parcela
  anchor_n             integer NOT NULL DEFAULT 0,          -- saldo conhecido após a parcela N
  anchor_balance_cents bigint NOT NULL DEFAULT 0,
  UNIQUE (ledger_id, name),
  UNIQUE (ledger_id, id)
);

-- References between tenant tables use composite keys (ledger_id, id) so a row
-- can never point at another ledger's row: FK checks bypass RLS.
CREATE TABLE recurrences (
  ledger_id       bigint NOT NULL DEFAULT current_ledger() REFERENCES ledgers (id) ON DELETE CASCADE,
  id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  kind            text NOT NULL CHECK (kind IN ('income', 'expense')),
  description     text NOT NULL,
  category_id     bigint NOT NULL,
  amount_cents    bigint NOT NULL,
  day_of_month    integer NOT NULL DEFAULT 1,
  interval_months integer NOT NULL DEFAULT 1,
  start_month     text NOT NULL CHECK (start_month ~ '^\d{4}-\d{2}$'),
  end_mode        text NOT NULL DEFAULT 'never' CHECK (end_mode IN ('never', 'count', 'until')),
  occurrences     integer,
  end_month       text CHECK (end_month ~ '^\d{4}-\d{2}$'),
  active          boolean NOT NULL DEFAULT true,
  -- A recurring expense can pay a debt: its confirmed occurrences become payments.
  debt_id         bigint,
  -- Suggested by the app (installments of a new loan), waiting for the user.
  pending         boolean NOT NULL DEFAULT false,
  UNIQUE (ledger_id, id),
  FOREIGN KEY (ledger_id, category_id) REFERENCES categories (ledger_id, id),
  FOREIGN KEY (ledger_id, debt_id) REFERENCES debts (ledger_id, id) ON DELETE SET NULL (debt_id)
);

CREATE TABLE recurrence_skips (
  ledger_id     bigint NOT NULL DEFAULT current_ledger() REFERENCES ledgers (id) ON DELETE CASCADE,
  recurrence_id bigint NOT NULL,
  month         text NOT NULL CHECK (month ~ '^\d{4}-\d{2}$'),
  PRIMARY KEY (recurrence_id, month),
  FOREIGN KEY (ledger_id, recurrence_id) REFERENCES recurrences (ledger_id, id) ON DELETE CASCADE
);

CREATE TABLE transactions (
  ledger_id        bigint NOT NULL DEFAULT current_ledger() REFERENCES ledgers (id) ON DELETE CASCADE,
  id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  kind             text NOT NULL CHECK (kind IN ('income', 'expense')),
  date             date NOT NULL,
  amount_cents     bigint NOT NULL,
  category_id      bigint NOT NULL,
  description      text NOT NULL DEFAULT '',
  recurrence_id    bigint,
  occurrence_month text CHECK (occurrence_month ~ '^\d{4}-\d{2}$'), -- which occurrence this confirms
  debt_id          bigint,
  source           text NOT NULL DEFAULT 'manual', -- manual | import | mcp
  external_id      text,
  FOREIGN KEY (ledger_id, category_id) REFERENCES categories (ledger_id, id),
  FOREIGN KEY (ledger_id, recurrence_id) REFERENCES recurrences (ledger_id, id) ON DELETE SET NULL (recurrence_id),
  FOREIGN KEY (ledger_id, debt_id) REFERENCES debts (ledger_id, id) ON DELETE SET NULL (debt_id)
);
CREATE INDEX transactions_ledger_date ON transactions (ledger_id, date);
CREATE INDEX transactions_debt ON transactions (debt_id) WHERE debt_id IS NOT NULL;
CREATE UNIQUE INDEX transactions_occurrence ON transactions (recurrence_id, occurrence_month)
  WHERE recurrence_id IS NOT NULL;

CREATE TABLE cash_snapshots (
  ledger_id    bigint NOT NULL DEFAULT current_ledger() REFERENCES ledgers (id) ON DELETE CASCADE,
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  date         date NOT NULL,
  amount_cents bigint NOT NULL,
  note         text NOT NULL DEFAULT '',
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX cash_snapshots_ledger_date ON cash_snapshots (ledger_id, date);

CREATE TABLE ledger_settings (
  ledger_id bigint NOT NULL DEFAULT current_ledger() REFERENCES ledgers (id) ON DELETE CASCADE,
  key       text NOT NULL,
  value     text NOT NULL,
  PRIMARY KEY (ledger_id, key)
);

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['categories', 'debts', 'recurrences', 'recurrence_skips', 'transactions', 'cash_snapshots', 'ledger_settings']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant ON %I USING (ledger_id = current_ledger()) WITH CHECK (ledger_id = current_ledger())', t);
  END LOOP;
END $$;

-- seed_current_ledger fills a new ledger (the one in app.ledger_id) with the
-- default categories and settings.
CREATE FUNCTION seed_current_ledger() RETURNS void
  LANGUAGE sql
  AS $$
  -- variable_estimate_cents: estimated monthly variable spending (cartão +
  -- outros) for the cash-flow projection. -1 = automatic (average of the last
  -- 3 closed months), 0 = off.
  INSERT INTO ledger_settings (key, value) VALUES
    ('card_limit_cents', '800000'), ('min_savings_cents', '0'), ('variable_estimate_cents', '-1')
  ON CONFLICT DO NOTHING;
  INSERT INTO categories (name, kind, bucket) VALUES
    ('Fatura cartão', 'expense', 'cartao'),
    ('Condomínio', 'expense', 'fixo'),
    ('Financiamento', 'expense', 'fixo'),
    ('Luz', 'expense', 'fixo'),
    ('Gás', 'expense', 'fixo'),
    ('Internet', 'expense', 'fixo'),
    ('Alimentação', 'expense', 'outros'),
    ('Moradia', 'expense', 'outros'),
    ('Transporte', 'expense', 'outros'),
    ('Saúde', 'expense', 'outros'),
    ('Lazer', 'expense', 'outros'),
    ('Educação', 'expense', 'outros'),
    ('Assinaturas', 'expense', 'outros'),
    ('Compras', 'expense', 'outros'),
    ('Impostos/Taxas', 'expense', 'outros'),
    ('Dívida', 'expense', 'outros'),
    ('Outros', 'expense', 'outros'),
    ('Salário', 'income', 'outros'),
    ('Outros', 'income', 'outros')
  ON CONFLICT DO NOTHING;
  $$;
