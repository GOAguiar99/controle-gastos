package app

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"controle-gastos/internal/db"
	"controle-gastos/internal/finance"
)

func newSvc(t *testing.T) *Service {
	t.Helper()
	st, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, time.UTC)
	s.SetClock(func() time.Time { return d("2026-11-16") })
	return s
}

func mustInvalid(t *testing.T, err error, want string) {
	t.Helper()
	if !IsValidation(err) || !strings.Contains(err.Error(), want) {
		t.Fatalf("want validation error containing %q, got %v", want, err)
	}
}

func TestTxRules(t *testing.T) {
	s := newSvc(t)
	base := TxIn{Kind: finance.Expense, Date: d("2026-11-10"), Amount: 5000, Category: CategoryRef{Name: "Mercado", Create: true}}

	id, err := s.AddTx(base)
	if err != nil || id == 0 {
		t.Fatalf("add: %v", err)
	}
	// name matching ignores case and accents; no duplicate category is created
	in := base
	in.Category = CategoryRef{Name: "  mercado "}
	if _, err := s.AddTx(in); err != nil {
		t.Fatalf("match by name: %v", err)
	}
	cats, _ := s.Categories(finance.Expense)
	n := 0
	for _, c := range cats {
		if fold(c.Name) == "mercado" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("duplicate categories: %d", n)
	}
	// unknown category without Create is refused and lists the options
	in.Category = CategoryRef{Name: "Mercadinho"}
	_, err = s.AddTx(in)
	mustInvalid(t, err, "não existe")

	bad := base
	bad.Amount = 0
	_, err = s.AddTx(bad)
	mustInvalid(t, err, "valor inválido")
	bad = base
	bad.Kind = "x"
	_, err = s.AddTx(bad)
	mustInvalid(t, err, "tipo inválido")
	bad = base
	bad.Kind, bad.DebtID = finance.Income, 1
	bad.Category = CategoryRef{Name: "Salário", Create: true}
	_, err = s.AddTx(bad)
	mustInvalid(t, err, "só gastos")

	// income category id used for an expense is refused
	incID, _ := s.AddCategory("Empresa", finance.Income, "")
	bad = base
	bad.Category = CategoryRef{ID: incID}
	_, err = s.AddTx(bad)
	mustInvalid(t, err, "categoria inválida")
}

func TestAddTxsAllOrNothing(t *testing.T) {
	s := newSvc(t)
	ok := TxIn{Kind: finance.Expense, Date: d("2026-11-10"), Amount: 100, Category: CategoryRef{Name: "Lazer", Create: true}, Source: SourceMCP}
	bad := ok
	bad.Amount = -1
	_, err := s.AddTxs([]TxIn{ok, bad})
	mustInvalid(t, err, "item 2")
	if es, _ := s.ListTx(TxFilter{}); len(es) != 0 {
		t.Fatalf("partial insert: %d", len(es))
	}
	ids, err := s.AddTxs([]TxIn{ok, ok})
	if err != nil || len(ids) != 2 {
		t.Fatalf("batch: %v %v", ids, err)
	}
	if src, _ := s.Store().TxSource(ids[0]); src != SourceMCP {
		t.Fatalf("source: %q", src)
	}
	// update keeps the kind and validates; delete reports not found afterwards
	e, err := s.UpdateTx(ids[0], TxIn{Kind: finance.Income, Date: d("2026-11-12"), Amount: 250, Category: CategoryRef{Name: "lazer"}})
	if err != nil || e.Kind != finance.Expense || e.Amount != 250 {
		t.Fatalf("update: %+v %v", e, err)
	}
	if _, err := s.DeleteTx(ids[0]); err != nil {
		t.Fatal(err)
	}
	_, err = s.DeleteTx(ids[0])
	mustInvalid(t, err, "não encontrada")
}

func TestRecurrenceRules(t *testing.T) {
	s := newSvc(t)
	in := RecIn{Kind: finance.Expense, Category: CategoryRef{Name: "Aluguel", Create: true}, Amount: 200000,
		Day: 5, Interval: 1, Start: finance.MonthOf(d("2026-11-01")), Active: true}
	for _, c := range []struct {
		mut  func(*RecIn)
		want string
	}{
		{func(r *RecIn) { r.Interval = 4 }, "periodicidade"},
		{func(r *RecIn) { r.Day = 32 }, "dia inválido"},
		{func(r *RecIn) { r.Amount = 0 }, "valor inválido"},
		{func(r *RecIn) { r.EndMode, r.Count = finance.EndCount, 0 }, "número de vezes"},
		{func(r *RecIn) { r.EndMode, r.End = finance.EndUntil, r.Start.Add(-1) }, "antes do inicial"},
		{func(r *RecIn) { r.DebtID = 99 }, "dívida inválida"},
		{func(r *RecIn) { r.ID = 99 }, "não encontrada"},
	} {
		r := in
		c.mut(&r)
		_, err := s.SaveRecurrence(r)
		mustInvalid(t, err, c.want)
	}
	id, err := s.SaveRecurrence(in)
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Recurrence(id); r.Description != "Aluguel" {
		t.Fatalf("description should default to category: %q", r.Description)
	}
	nov, dec := finance.MonthOf(d("2026-11-01")), finance.MonthOf(d("2026-12-01"))

	_, err = s.ConfirmOccurrence(id, nov.Add(-1), 0, time.Time{}, SourceMCP)
	mustInvalid(t, err, "não é um mês")
	txID, err := s.ConfirmOccurrence(id, nov, 0, time.Time{}, SourceMCP)
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := s.Tx(txID); e.Amount != 200000 || !e.Date.Equal(d("2026-11-05")) || e.OccMonth != "2026-11" {
		t.Fatalf("confirmed: %+v", e)
	}
	_, err = s.ConfirmOccurrence(id, nov, 0, time.Time{}, SourceMCP)
	mustInvalid(t, err, "já foi confirmado")

	if _, err := s.SkipOccurrence(id, dec); err != nil {
		t.Fatal(err)
	}
	es, _ := s.ListTx(TxFilter{From: dec, To: dec, IncludeProjected: true})
	if len(es) != 0 {
		t.Fatalf("skipped month still projected: %+v", es)
	}
	if _, err := s.UnskipOccurrence(id, dec); err != nil {
		t.Fatal(err)
	}
	es, _ = s.ListTx(TxFilter{From: dec, To: dec, IncludeProjected: true})
	if len(es) != 1 || !es[0].Projected || es[0].RecurrenceID != id {
		t.Fatalf("unskipped: %+v", es)
	}
	if es, _ := s.ListTx(TxFilter{From: dec, To: dec}); len(es) != 0 {
		t.Fatal("projected entries must be opt-in")
	}
}

func TestDebtAndConfigRules(t *testing.T) {
	s := newSvc(t)
	_, err := s.SaveDebt(0, db.DebtInput{Name: "Banco", Initial: 100000, System: finance.LoanSAC, MonthlyRate: 1, Term: 0})
	mustInvalid(t, err, "prazo")
	_, err = s.SaveDebt(0, db.DebtInput{Name: " ", Initial: 1})
	mustInvalid(t, err, "obrigatórios")
	id, err := s.SaveDebt(0, db.DebtInput{Name: "Fulano", Initial: 100000})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SaveDebt(id+10, db.DebtInput{Name: "X", Initial: 1})
	mustInvalid(t, err, "não encontrada")

	mustInvalid(t, s.SetLimits(finance.Limits{CardLimit: -1}), "inválidos")
	mustInvalid(t, s.SetCategoryBucket(999, finance.Card), "não encontrada")
	_, err = ParseBucket("cartão")
	mustInvalid(t, err, "grupo inválido")

	tok, err := s.MCPToken()
	if err != nil || len(tok) < 40 {
		t.Fatalf("token: %q %v", tok, err)
	}
	if again, _ := s.MCPToken(); again != tok {
		t.Fatal("token must be stable")
	}
	if rot, _ := s.RotateMCPToken(); rot == tok {
		t.Fatal("rotate must change the token")
	}
}

func d(s string) time.Time {
	t, err := finance.ParseDate(s)
	if err != nil {
		panic(err)
	}
	return t
}
