package importer

import (
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"controle-gastos/internal/db/dbtest"
	"controle-gastos/internal/finance"
)

// The legacy SQLite database (schema of the app before PostgreSQL) is copied
// with every relation remapped.
func TestFromSQLite(t *testing.T) {
	st := dbtest.Ledger(t)
	path := filepath.Join(t.TempDir(), "old.db")
	src, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("testdata/legacy_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{string(schema), `
		INSERT INTO categories (name, kind, bucket) VALUES ('Padaria', 'expense', 'cartao');
		UPDATE categories SET bucket = 'outros' WHERE name = 'Luz';
		INSERT INTO debts (name, initial_cents, system, monthly_rate, term_months, first_due) VALUES ('Casa', 100000, 'sac', 1.0, 10, '2026-01-05');
		INSERT INTO recurrences (kind, description, category_id, amount_cents, start_month, debt_id)
			VALUES ('expense', 'Parcela', (SELECT id FROM categories WHERE name = 'Financiamento'), 11000, '2026-01', 1);
		INSERT INTO recurrence_skips (recurrence_id, month) VALUES (1, '2026-03');
		INSERT INTO transactions (kind, date, amount_cents, category_id, description, recurrence_id, occurrence_month, debt_id, source)
			VALUES ('expense', '2026-01-05', 11000, (SELECT id FROM categories WHERE name = 'Financiamento'), 'Parcela', 1, '2026-01', 1, 'manual'),
			       ('expense', '2026-01-08', 2500, (SELECT id FROM categories WHERE name = 'Padaria'), 'pão', NULL, NULL, NULL, 'mcp');
		INSERT INTO cash_snapshots (date, amount_cents) VALUES ('2026-01-01', 500000);
		UPDATE settings SET value = '123400' WHERE key = 'card_limit_cents';`} {
		if _, err := src.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	src.Close()

	if err := FromSQLite(st, path, io.Discard); err != nil {
		t.Fatal(err)
	}
	txs, err := st.AllTransactions()
	if err != nil || len(txs) != 2 {
		t.Fatalf("transactions: %v %v", txs, err)
	}
	if txs[1].Category != "Padaria" || txs[1].Bucket != finance.Card || txs[1].Amount != 2500 {
		t.Fatalf("category not remapped: %+v", txs[1])
	}
	recs, _ := st.Recurrences()
	if len(recs) != 1 || txs[0].RecurrenceID != recs[0].ID || txs[0].DebtID != recs[0].DebtID || recs[0].DebtID == 0 {
		t.Fatalf("recurrence/debt links lost: %+v %+v", txs[0], recs)
	}
	debts, _ := st.Debts(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	if len(debts) != 1 || debts[0].Payments != 1 || !debts[0].HasInterest() {
		t.Fatalf("debts: %+v", debts)
	}
	_, skipped, _ := st.Entries(finance.NewMonth(2026, 3), finance.NewMonth(2026, 3))
	if len(skipped) != 1 {
		t.Fatalf("skip lost: %+v", skipped)
	}
	if st.SettingInt("card_limit_cents") != 123400 {
		t.Fatal("settings not copied")
	}
	if snap, _ := st.LatestSnapshot(); snap.Amount != 500000 {
		t.Fatalf("snapshot: %+v", snap)
	}
	cats, _ := st.Categories(finance.Expense)
	for _, c := range cats {
		if c.Name == "Luz" && c.Bucket != finance.Other {
			t.Fatal("bucket not copied over the seeded default")
		}
	}
}
