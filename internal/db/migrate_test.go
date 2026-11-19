package db_test

import (
	"context"
	"testing"

	"controle-gastos/internal/db"
	"controle-gastos/internal/db/dbtest"
	"controle-gastos/internal/finance"
)

// 0007 moves the expense group from the category to each transaction. Months
// recorded before it must keep their Cartão / Fixos / Outros totals.
func TestMigrationPaymentMethodKeepsTotals(t *testing.T) {
	ctx := context.Background()
	d, err := db.OpenUntil(ctx, dbtest.URL(t), "0007")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	id, err := d.CreateLedger(ctx, "Antiga")
	if err != nil {
		t.Fatal(err)
	}
	st := d.Ledger(id)
	setup := []string{
		`INSERT INTO categories (name, kind, bucket) VALUES ('Luz', 'expense', 'fixo'), ('Lazer', 'expense', 'outros'), ('Academia', 'expense', 'outros') ON CONFLICT (ledger_id, name, kind) DO UPDATE SET bucket = EXCLUDED.bucket`,
		// a recurrence in a group-"outros" category: its confirmed occurrence stays in Outros
		`INSERT INTO recurrences (kind, description, category_id, amount_cents, start_month)
		   SELECT 'expense', 'Academia', id, 9000, '2026-01' FROM categories WHERE name = 'Academia'`,
		`INSERT INTO recurrences (kind, description, category_id, amount_cents, start_month)
		   SELECT 'expense', 'Fatura', id, 200000, '2026-03' FROM categories WHERE name = 'Fatura cartão'`,
		`INSERT INTO transactions (kind, date, amount_cents, category_id)
		   SELECT 'expense', '2026-01-10', 150000, id FROM categories WHERE name = 'Fatura cartão'`,
		`INSERT INTO transactions (kind, date, amount_cents, category_id)
		   SELECT 'expense', '2026-01-11', 20000, id FROM categories WHERE name = 'Luz'`,
		`INSERT INTO transactions (kind, date, amount_cents, category_id)
		   SELECT 'expense', '2026-01-12', 5000, id FROM categories WHERE name = 'Lazer'`,
		`INSERT INTO transactions (kind, date, amount_cents, category_id, recurrence_id, occurrence_month)
		   SELECT 'expense', '2026-01-05', 9000, c.id, r.id, '2026-01' FROM categories c, recurrences r
		   WHERE c.name = 'Academia' AND r.description = 'Academia'`,
		`INSERT INTO transactions (kind, date, amount_cents, category_id)
		   SELECT 'income', '2026-01-05', 500000, id FROM categories WHERE kind = 'income' LIMIT 1`,
	}
	for _, q := range setup {
		if err := st.ExecRaw(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := d.MigrateRest(ctx); err != nil {
		t.Fatal(err)
	}

	jan, mar := finance.NewMonth(2026, 1), finance.NewMonth(2026, 3)
	es, _, err := st.Entries(jan, mar)
	if err != nil {
		t.Fatal(err)
	}
	sums := finance.Summarize(es, jan, mar, finance.Limits{})
	if s := sums[0]; s.Card != 150000 || s.Fixed != 20000 || s.Other != 14000 || s.Income != 500000 {
		t.Fatalf("janeiro mudou: %+v", s)
	}
	// the card-bill recurrence keeps projecting into Cartão
	if s := sums[2]; s.Card != 200000 {
		t.Fatalf("março (recorrência da fatura): %+v", s)
	}
	recs, _ := st.Recurrences()
	for _, r := range recs {
		if r.Description == "Fatura" && r.Payment != finance.Credit {
			t.Fatalf("recorrência da fatura: %+v", r)
		}
	}
}
