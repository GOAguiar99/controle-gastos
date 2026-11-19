package app

import (
	"testing"

	"controle-gastos/internal/finance"
)

func TestParseStatementFormats(t *testing.T) {
	text := `date,title,amount
2026-10-28,Padaria Pão Quente,12.50
2026-10-30,Magazine Luiza - Parcela 3/10,99.90
2026-11-01,Pagamento recebido,-500.00

12/10 IFOOD *RESTAURANTE  R$ 45,90
15 OUT Uber Trip 2/3 1.234,56
02/11/2026;Farmácia;R$ 30,00
lixo sem data nenhuma 10,00`
	lines, unread := ParseStatement(text, nov)
	if len(lines) != 6 || len(unread) != 1 {
		t.Fatalf("lines %d unread %v: %+v", len(lines), unread, lines)
	}
	want := []struct {
		date, desc string
		amount     int64
		credit     bool
		n, of      int
	}{
		{"2026-10-28", "Padaria Pão Quente", 1250, false, 0, 0},
		{"2026-10-30", "Magazine Luiza", 9990, false, 3, 10},
		{"2026-11-01", "Pagamento recebido", 50000, true, 0, 0},
		{"2026-10-12", "IFOOD *RESTAURANTE", 4590, false, 0, 0},
		{"2026-10-15", "Uber Trip", 123456, false, 2, 3},
		{"2026-11-02", "Farmácia", 3000, false, 0, 0},
	}
	for i, w := range want {
		l := lines[i]
		if l.Date.Format("2006-01-02") != w.date || l.Description != w.desc || l.Amount != w.amount || l.Credit != w.credit ||
			l.InstallmentN != w.n || l.Installments != w.of {
			t.Errorf("line %d: %+v, want %+v", i, l, w)
		}
	}
	// exports that show purchases as negative numbers
	lines, _ = ParseStatement("01/11;Mercado;-50,00\n02/11;Posto;-100,00\n03/11;Estorno Mercado;50,00", nov)
	if lines[0].Credit || lines[1].Credit || !lines[2].Credit {
		t.Fatalf("negative convention: %+v", lines)
	}
	// a December line in a January statement is from the year before
	lines, _ = ParseStatement("28/12 Loja 10,00", finance.NewMonth(2027, 1))
	if lines[0].Date.Format("2006-01-02") != "2026-12-28" {
		t.Fatalf("year: %v", lines[0].Date)
	}
}

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
	text := "28/10 Padaria Pão Quente 12,50\n30/10 Farmacia 30,00\n30/10 Magazine Luiza 3/10 99,90\n01/11 Pagamento recebido -500,00"
	p, err := s.PreviewStatement(card, nov, text)
	if err != nil {
		t.Fatal(err)
	}
	st := map[string]ImportRow{}
	for _, r := range p.Rows {
		st[r.Description] = r
	}
	if r := st["Padaria Pão Quente"]; r.Status != ImportNew || r.Category != "Alimentação" {
		t.Errorf("padaria: %+v", r)
	}
	if r := st["Farmacia"]; r.Status != ImportDuplicate {
		t.Errorf("farmácia should be a duplicate: %+v", r)
	}
	if r := st["Pagamento recebido"]; r.Status != ImportIgnore {
		t.Errorf("pagamento should be ignored: %+v", r)
	}
	if p.NewCount != 2 || p.New != 1250+9990 || p.Existing != 3000 {
		t.Errorf("totals: %+v", p)
	}

	ids, err := s.ImportStatement(card, nov, []ImportItem{
		{Date: d("2026-10-28"), Description: "Padaria Pão Quente", Amount: 1250, Category: CategoryRef{ID: st["Padaria Pão Quente"].CategoryID}},
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
	p, err = s.PreviewStatement(card, dec, "30/11 Magazine Luiza 4/10 99,90\n05/11 Cinema 40,00")
	if err != nil {
		t.Fatal(err)
	}
	if p.Rows[0].Status != ImportDuplicate || p.Rows[1].Status != ImportNew {
		t.Fatalf("next statement: %+v", p.Rows)
	}
	// the November fatura now: 30,00 + 12,50 + 99,90
	v, _ := s.Statement(card, nov)
	if v.Statement.Items != 3000+1250+9990 {
		t.Fatalf("statement items: %d", v.Statement.Items)
	}
	_, err = s.PreviewStatement(card, nov, "nada aqui")
	mustInvalid(t, err, "nenhuma linha")
}
