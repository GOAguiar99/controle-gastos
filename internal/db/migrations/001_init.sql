CREATE TABLE categories (
  id     INTEGER PRIMARY KEY,
  name   TEXT NOT NULL,
  kind   TEXT NOT NULL CHECK (kind IN ('income', 'expense')),
  bucket TEXT NOT NULL DEFAULT 'outros' CHECK (bucket IN ('cartao', 'fixo', 'outros')),
  UNIQUE (name, kind)
);

CREATE TABLE debts (
  id            INTEGER PRIMARY KEY,
  name          TEXT NOT NULL UNIQUE,
  initial_cents INTEGER NOT NULL
);

CREATE TABLE recurrences (
  id              INTEGER PRIMARY KEY,
  kind            TEXT NOT NULL CHECK (kind IN ('income', 'expense')),
  description     TEXT NOT NULL,
  category_id     INTEGER NOT NULL REFERENCES categories (id),
  amount_cents    INTEGER NOT NULL,
  day_of_month    INTEGER NOT NULL DEFAULT 1,
  interval_months INTEGER NOT NULL DEFAULT 1,
  start_month     TEXT NOT NULL,                 -- YYYY-MM
  end_mode        TEXT NOT NULL DEFAULT 'never' CHECK (end_mode IN ('never', 'count', 'until')),
  occurrences     INTEGER,
  end_month       TEXT,
  active          INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE recurrence_skips (
  recurrence_id INTEGER NOT NULL REFERENCES recurrences (id) ON DELETE CASCADE,
  month         TEXT NOT NULL,
  PRIMARY KEY (recurrence_id, month)
);

CREATE TABLE transactions (
  id               INTEGER PRIMARY KEY,
  kind             TEXT NOT NULL CHECK (kind IN ('income', 'expense')),
  date             TEXT NOT NULL,              -- YYYY-MM-DD
  amount_cents     INTEGER NOT NULL,
  category_id      INTEGER NOT NULL REFERENCES categories (id),
  description      TEXT NOT NULL DEFAULT '',
  recurrence_id    INTEGER REFERENCES recurrences (id) ON DELETE SET NULL,
  occurrence_month TEXT,                       -- which occurrence of the recurrence this confirms
  debt_id          INTEGER REFERENCES debts (id) ON DELETE SET NULL,
  source           TEXT NOT NULL DEFAULT 'manual', -- manual | import | (future) openfinance
  external_id      TEXT
);
CREATE INDEX transactions_date ON transactions (date);
CREATE UNIQUE INDEX transactions_occurrence ON transactions (recurrence_id, occurrence_month)
  WHERE recurrence_id IS NOT NULL;

CREATE TABLE cash_snapshots (
  id           INTEGER PRIMARY KEY,
  date         TEXT NOT NULL,
  amount_cents INTEGER NOT NULL,
  note         TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

INSERT INTO settings (key, value) VALUES ('card_limit_cents', '800000'), ('min_savings_cents', '0');

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
  ('Outros', 'income', 'outros');
