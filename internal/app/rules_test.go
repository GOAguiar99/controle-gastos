package app

import (
	"testing"

	"controle-gastos/internal/finance"
)

// Correcting a purchase's category teaches the app: the merchant's other
// purchases still in the old category move along, the ones the user filed
// elsewhere stay, and future purchases are suggested the new category.
func TestRecategorizeRemembers(t *testing.T) {
	s := newSvc(t)
	card := nubank(t, s)
	outros, food := CategoryRef{Name: "Outros", Create: true}, CategoryRef{Name: "Alimentação", Create: true}
	ids, err := s.ImportStatement(card, nov, []ImportItem{
		{Date: d("2026-10-10"), Description: "Vmt*Shelf", Amount: 1000, Category: outros},
		{Date: d("2026-10-11"), Description: "Shelf", Amount: 500, Category: outros},
		{Date: d("2026-10-12"), Description: "Vmt*Shelf", Amount: 700, Category: CategoryRef{Name: "Lazer", Create: true}},
		{Date: d("2026-10-13"), Description: "Padaria", Amount: 300, Category: outros},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Recategorize(ids[0], food, true)
	if err != nil || res.Others != 1 || !res.Remembered || res.Tx.Category != "Alimentação" {
		t.Fatalf("recategorize: %+v %v", res, err)
	}
	cat := func(id int64) string { e, _ := s.Tx(id); return e.Category }
	if cat(ids[1]) != "Alimentação" || cat(ids[2]) != "Lazer" || cat(ids[3]) != "Outros" {
		t.Fatalf("others: %s %s %s", cat(ids[1]), cat(ids[2]), cat(ids[3]))
	}
	// next fatura: the memory wins over the latest history (Lazer above)
	p, err := s.PreviewStatement(card, dec, []StatementLine{{Date: d("2026-11-20"), Description: "VMT*SHELF", Amount: 900}})
	if err != nil || p.Rows[0].Category != "Alimentação" || p.Rows[0].SuggestedFrom != "memória: Vmt*Shelf" {
		t.Fatalf("suggestion: %+v %v", p.Rows, err)
	}
	if rs, _ := s.CategoryRules(); len(rs) != 1 || rs[0].Category != "Alimentação" {
		t.Fatalf("rules: %+v", rs)
	}
	// without remember, only that purchase changes
	if res, _ = s.Recategorize(ids[3], food, false); res.Remembered || res.Others != 0 {
		t.Fatalf("no remember: %+v", res)
	}
	if err := s.ForgetCategory("Vmt*Shelf"); err != nil {
		t.Fatal(err)
	}
	if rs, _ := s.CategoryRules(); len(rs) != 0 {
		t.Fatalf("forget: %+v", rs)
	}
	mustInvalid(t, s.ForgetCategory("nada"), "nenhuma memória")
	if _, err := s.RememberCategory("Ifd*Pizzaria X", food); err != nil {
		t.Fatal(err)
	}
	p, _ = s.PreviewStatement(card, dec, []StatementLine{{Date: d("2026-11-20"), Description: "Pizzaria X", Amount: 900}})
	if p.Rows[0].Category != "Alimentação" {
		t.Fatalf("remember: %+v", p.Rows)
	}
	_ = finance.Expense
}
