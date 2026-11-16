package db_test

import (
	"context"
	"os"
	"testing"
	"time"

	"controle-gastos/internal/db"
	"controle-gastos/internal/db/dbtest"
	"controle-gastos/internal/finance"
)

var day = time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)

func twoLedgers(t *testing.T) (a, b *db.Store) {
	t.Helper()
	d := dbtest.Open(t)
	ctx := context.Background()
	ia, err := d.CreateLedger(ctx, "A")
	if err != nil {
		t.Fatal(err)
	}
	ib, err := d.CreateLedger(ctx, "B")
	if err != nil {
		t.Fatal(err)
	}
	return d.Ledger(ia), d.Ledger(ib)
}

func addTx(t *testing.T, st *db.Store, desc string) int64 {
	t.Helper()
	cat, err := st.EnsureCategory("Mercado", finance.Expense, finance.Other)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.InsertTx(db.TxInput{Kind: finance.Expense, Date: day, Amount: 1000, CategoryID: cat, Description: desc})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// A ledger never sees, changes or deletes another ledger's rows.
func TestLedgerIsolation(t *testing.T) {
	a, b := twoLedgers(t)
	idA := addTx(t, a, "de A")
	addTx(t, b, "de B")
	if err := a.SetSettingInt("card_limit_cents", 1); err != nil {
		t.Fatal(err)
	}

	txs, err := b.AllTransactions()
	if err != nil || len(txs) != 1 || txs[0].Description != "de B" {
		t.Fatalf("B sees %+v, %v", txs, err)
	}
	if _, err := b.Tx(idA); err == nil {
		t.Fatal("B read A's transaction by id")
	}
	if err := b.DeleteTx(idA); err != nil {
		t.Fatal(err)
	}
	if err := b.UpdateTx(idA, db.TxInput{Date: day, Amount: 1, CategoryID: 1}); err != nil {
		t.Fatal(err)
	}
	e, err := a.Tx(idA)
	if err != nil || e.Amount != 1000 {
		t.Fatalf("B changed A's transaction: %+v %v", e, err)
	}
	if got := b.SettingInt("card_limit_cents"); got != 800000 {
		t.Fatalf("settings leaked between ledgers: %d", got)
	}
	ids := map[int64]bool{}
	for _, c := range mustCats(t, a) {
		ids[c.ID] = true
	}
	for _, c := range mustCats(t, b) {
		if ids[c.ID] {
			t.Fatalf("B sees A's category %q", c.Name)
		}
	}
}

func mustCats(t *testing.T, st *db.Store) []db.Category {
	t.Helper()
	cats, err := st.Categories("")
	if err != nil {
		t.Fatal(err)
	}
	return cats
}

// Foreign keys are checked without RLS, so they are composite (ledger_id, id):
// a row can't point at another ledger's category, debt or recurrence.
func TestNoCrossLedgerReferences(t *testing.T) {
	a, b := twoLedgers(t)
	catA, err := a.EnsureCategory("Mercado", finance.Expense, finance.Other)
	if err != nil {
		t.Fatal(err)
	}
	debtA, err := a.AddDebt(db.DebtInput{Name: "Banco", Initial: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.InsertTx(db.TxInput{Kind: finance.Expense, Date: day, Amount: 1, CategoryID: catA}); err == nil {
		t.Fatal("B used A's category")
	}
	catB, _ := b.EnsureCategory("Mercado", finance.Expense, finance.Other)
	if _, err := b.InsertTx(db.TxInput{Kind: finance.Expense, Date: day, Amount: 1, CategoryID: catB, DebtID: debtA}); err == nil {
		t.Fatal("B linked a payment to A's debt")
	}
}

// Without a ledger in the context nothing is visible (fail closed).
func TestNoLedgerSeesNothing(t *testing.T) {
	d := dbtest.Open(t)
	a, _ := d.CreateLedger(context.Background(), "A")
	addTx(t, d.Ledger(a), "x")
	txs, err := d.Ledger(0).AllTransactions()
	if err != nil || len(txs) != 0 {
		t.Fatalf("unscoped store sees %d rows (%v)", len(txs), err)
	}
	if _, err := d.Ledger(0).EnsureCategory("X", finance.Expense, finance.Other); err == nil {
		t.Fatal("unscoped store could write")
	}
}

func TestInTxRollsBack(t *testing.T) {
	a, _ := twoLedgers(t)
	cat, _ := a.EnsureCategory("Mercado", finance.Expense, finance.Other)
	_, err := a.InsertTxs([]db.TxInput{
		{Kind: finance.Expense, Date: day, Amount: 1, CategoryID: cat},
		{Kind: finance.Expense, Date: day, Amount: 1, CategoryID: -1},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if txs, _ := a.AllTransactions(); len(txs) != 0 {
		t.Fatalf("partial insert kept %d rows", len(txs))
	}
}

// RLS does not apply to superusers, so Open refuses them.
func TestRefusesSuperuser(t *testing.T) {
	admin := os.Getenv("TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("TEST_DATABASE_URL não definida")
	}
	if d, err := db.Open(context.Background(), admin); err == nil {
		d.Close()
		t.Fatal("opened as superuser")
	}
}
