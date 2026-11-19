package app

import (
	"testing"

	"controle-gastos/internal/finance"
)

func TestImportStatement(t *testing.T) {
	s := newSvc(t)
	card := nubank(t, s)
	// history: Padaria was Alimentação, already one purchase entered on the card
	if _, err := s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-09-10"), Amount: 800, Category: CategoryRef{Name: "Alimentação", Create: true}, Description: "Padaria Pão Quente", Payment: finance.Pix}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-10-29"), Amount: 3000, Category: CategoryRef{Name: "Saúde", Create: true}, CardID: card}); err != nil {
		t.Fatal(err)
	}
	lines := []StatementLine{
		{Date: d("2026-10-28"), Description: "PADARIA PAO QUENTE", Amount: 1250},
		{Date: d("2026-10-30"), Description: "Farmacia", Amount: 3000},
		{Date: d("2026-10-30"), Description: "Magazine Luiza", Amount: 9990, InstallmentN: 3, Installments: 10},
	}
	p, err := s.PreviewStatement(card, nov, lines)
	if err != nil {
		t.Fatal(err)
	}
	if r := p.Rows[0]; r.Status != ImportNew || r.Category != "Alimentação" {
		t.Errorf("padaria: %+v", r)
	}
	if r := p.Rows[1]; r.Status != ImportDuplicate {
		t.Errorf("farmácia should be a duplicate: %+v", r)
	}
	if p.NewCount != 2 || p.New != 1250+9990 || p.Existing != 3000 {
		t.Errorf("totals: %+v", p)
	}
	_, err = s.PreviewStatement(card, nov, []StatementLine{{Date: d("2026-11-01"), Description: "Pagamento", Amount: -500}})
	mustInvalid(t, err, "valor positivo")

	ids, err := s.ImportStatement(card, nov, []ImportItem{
		{Date: d("2026-10-28"), Description: "Padaria Pão Quente", Amount: 1250, Category: CategoryRef{ID: p.Rows[0].CategoryID}},
		{Date: d("2026-10-30"), Description: "Magazine Luiza", Amount: 9990, Category: CategoryRef{Name: "Casa", Create: true}, InstallmentN: 3, Installments: 10},
	}, "")
	if err != nil || len(ids) != 1+8 {
		t.Fatalf("import: %d ids, %v", len(ids), err)
	}
	e, _ := s.Tx(ids[0])
	if e.Statement != nov || e.CardID != card || e.Payment != finance.Credit {
		t.Fatalf("imported line: %+v", e)
	}
	last, _ := s.Tx(ids[len(ids)-1])
	if last.InstallmentN != 10 || last.Amount != 9990 || last.Statement != nov.Add(7) {
		t.Fatalf("last parcel: %+v", last)
	}
	// next month's fatura repeats the parcel as 4/10: it is already there
	p, err = s.PreviewStatement(card, dec, []StatementLine{
		{Date: d("2026-10-30"), Description: "Magazine Luiza", Amount: 9990, InstallmentN: 4, Installments: 10},
		{Date: d("2026-11-05"), Description: "Cinema", Amount: 4000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Rows[0].Status != ImportDuplicate || p.Rows[1].Status != ImportNew {
		t.Fatalf("next statement: %+v", p.Rows)
	}
	v, _ := s.Statement(card, nov)
	if v.Statement.Items != 3000+1250+9990 {
		t.Fatalf("statement items: %d", v.Statement.Items)
	}
}

// Off, the cards module hides cards and credit purchases go back to leaving
// the cash on the purchase date; what was billed on a fatura keeps it.
func TestCardsModuleOff(t *testing.T) {
	s := newSvc(t)
	card := nubank(t, s)
	food := CategoryRef{Name: "Alimentação", Create: true}
	billed, err := s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-11-05"), Amount: 1000, Category: food, CardID: card})
	if err != nil {
		t.Fatal(err)
	}
	if !s.CardsEnabled() {
		t.Fatal("cards module should start on")
	}
	if err := s.SetCardsEnabled(false); err != nil {
		t.Fatal(err)
	}
	if cs, _ := s.Cards(); len(cs) != 0 {
		t.Fatalf("cards listed while off: %+v", cs)
	}
	_, err = s.SaveCard(CardIn{Name: "Outro", ClosingDay: 1, DueDay: 8})
	mustInvalid(t, err, "desativado")
	_, err = s.Statement(card, dec)
	mustInvalid(t, err, "desativado")
	_, err = s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-11-05"), Amount: 1, Category: food, CardID: card})
	mustInvalid(t, err, "desativado")

	// a new credit purchase has no fatura: cash moves on the purchase date
	id, err := s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-11-05"), Amount: 2000, Category: food, Payment: finance.Credit})
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := s.Tx(id); e.CardID != 0 || e.Group() != finance.Card || !e.CashDay().Equal(d("2026-11-05")) {
		t.Fatalf("credit with module off: %+v", e)
	}
	// editing a purchase already billed keeps its fatura
	e, err := s.UpdateTx(billed, TxIn{Date: d("2026-11-05"), Amount: 1100, Category: food, Description: "corrigido"})
	if err != nil || e.CardID != card || e.Statement != dec || !e.CashDay().Equal(d("2026-12-10")) {
		t.Fatalf("edit while off: %+v %v", e, err)
	}
	if err := s.SetCardsEnabled(true); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Statement(card, dec); err != nil || v.Statement.Items != 1100 {
		t.Fatalf("back on: %+v %v", v.Statement, err)
	}
}

// Importing an older fatura after a newer one: its "1/10" line must not
// create again the parcels the newer import already created, and the next
// fatura's parcel is recognized even when the bank's value differs by cents.
func TestImportOlderFaturaKeepsParcels(t *testing.T) {
	s := newSvc(t)
	card := nubank(t, s)
	sep, oct := finance.NewMonth(2026, 9), finance.NewMonth(2026, 10)
	shop := CategoryRef{Name: "Compras", Create: true}
	// November fatura first: "Annah Dress 3/10" creates parcels 4..10
	ids, err := s.ImportStatement(card, nov, []ImportItem{{Date: d("2026-10-02"), Description: "Annah Dress", Amount: 15189, Category: shop, InstallmentN: 3, Installments: 10}}, "")
	if err != nil || len(ids) != 8 {
		t.Fatalf("november: %d %v", len(ids), err)
	}
	// then the September fatura with "1/10": only parcel 2 is new
	ids, err = s.ImportStatement(card, sep, []ImportItem{{Date: d("2026-08-18"), Description: "Annah Dress", Amount: 15195, Category: shop, InstallmentN: 1, Installments: 10}}, "")
	if err != nil || len(ids) != 2 {
		t.Fatalf("september: %d ids %v", len(ids), err)
	}
	first, _ := s.Tx(ids[0])
	second, _ := s.Tx(ids[1])
	if second.InstallmentN != 2 || second.Statement != oct || second.InstallmentGroup == 0 {
		t.Fatalf("parcel 2: %+v", second)
	}
	all, _ := s.Store().ParcelsOf(card, 10)
	if len(all) != 10 {
		t.Fatalf("want parcels 1..10 once each, got %d", len(all))
	}
	for _, e := range all {
		if e.InstallmentGroup != first.InstallmentGroup {
			t.Fatalf("parcels in different groups: %+v", all)
		}
	}
	// October fatura: the bank charged 151,89 for 2/10, created with 151,95
	p, err := s.PreviewStatement(card, oct, []StatementLine{{Date: d("2026-09-02"), Description: "Annah Dress", Amount: 15189, InstallmentN: 2, Installments: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if r := p.Rows[0]; r.Status != ImportDuplicate || r.ExistingID != second.ID || r.ExistingAmount != 15195 {
		t.Fatalf("parcel with other cents: %+v", r)
	}
}

func TestSuggestionsIgnoreIntermediaries(t *testing.T) {
	s := newSvc(t)
	card := nubank(t, s)
	if _, err := s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-09-10"), Amount: 800, Category: CategoryRef{Name: "Alimentação", Create: true}, Description: "Shelf", CardID: card}); err != nil {
		t.Fatal(err)
	}
	p, err := s.PreviewStatement(card, nov, []StatementLine{
		{Date: d("2026-10-20"), Description: "Vmt*Shelf", Amount: 1200},
		{Date: d("2026-10-21"), Description: "50921356roberto", Amount: 6680},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := p.Rows[0]; r.Category != "Alimentação" || r.SuggestedFrom != "Shelf" || r.NeedsResearch {
		t.Errorf("Vmt*Shelf: %+v", r)
	}
	if r := p.Rows[1]; r.Category != "" || !r.NeedsResearch {
		t.Errorf("unknown name: %+v", r)
	}
}

// The same value a few days apart on the fatura before is another purchase.
func TestDuplicateOnlyWithinTheFatura(t *testing.T) {
	s := newSvc(t)
	card := nubank(t, s)
	park := CategoryRef{Name: "Transporte", Create: true}
	if _, err := s.ImportStatement(card, nov, []ImportItem{{Date: d("2026-10-03"), Description: "Bueno Park", Amount: 2800, Category: park}}, ""); err != nil {
		t.Fatal(err)
	}
	oct := finance.NewMonth(2026, 10)
	p, err := s.PreviewStatement(card, oct, []StatementLine{{Date: d("2026-10-01"), Description: "Bueno Park", Amount: 2800}})
	if err != nil || p.Rows[0].Status != ImportNew {
		t.Fatalf("other fatura: %+v %v", p.Rows, err)
	}
	p, _ = s.PreviewStatement(card, nov, []StatementLine{{Date: d("2026-10-04"), Description: "Bueno Park", Amount: 2800}})
	if p.Rows[0].Status != ImportDuplicate {
		t.Fatalf("same fatura: %+v", p.Rows)
	}
}
