package app

import (
	"testing"

	"controle-gastos/internal/finance"
)

var nov, dec = finance.NewMonth(2026, 11), finance.NewMonth(2026, 12)

// today is 2026-11-16; this card closes on the 3rd and is due on the 10th
func nubank(t *testing.T, s *Service) int64 {
	t.Helper()
	id, err := s.SaveCard(CardIn{Name: "Nubank", ClosingDay: 3, DueDay: 10})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCardRules(t *testing.T) {
	s := newSvc(t)
	_, err := s.SaveCard(CardIn{Name: "", ClosingDay: 3, DueDay: 10})
	mustInvalid(t, err, "nome")
	_, err = s.SaveCard(CardIn{Name: "X", ClosingDay: 0, DueDay: 10})
	mustInvalid(t, err, "fechamento")
	_, err = s.SaveCard(CardIn{Name: "X", ClosingDay: 3, DueDay: 32})
	mustInvalid(t, err, "vencimento")
	id := nubank(t, s)
	_, err = s.SaveCard(CardIn{Name: "nubank", ClosingDay: 5, DueDay: 12})
	mustInvalid(t, err, "já existe")

	// one purchase: the card can't be deleted any more, only deactivated
	if _, err := s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-11-05"), Amount: 100, Category: CategoryRef{Name: "Lazer", Create: true}, CardID: id}); err != nil {
		t.Fatal(err)
	}
	_, err = s.DeleteCard(id)
	mustInvalid(t, err, "desative")
	if _, err := s.SaveCard(CardIn{ID: id, Name: "Nubank", ClosingDay: 3, DueDay: 10, Active: false}); err != nil {
		t.Fatal(err)
	}
	empty, _ := s.SaveCard(CardIn{Name: "Reserva", ClosingDay: 1, DueDay: 8})
	if _, err := s.DeleteCard(empty); err != nil {
		t.Fatal(err)
	}
}

// A credit purchase goes on a statement and leaves the cash on its due date.
func TestCardPurchaseStatementAndCash(t *testing.T) {
	s := newSvc(t)
	card := nubank(t, s)
	food := CategoryRef{Name: "Alimentação", Create: true}
	// only one card: credito without a card goes on it
	before, err := s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-11-02"), Amount: 10000, Category: food, Payment: finance.Credit})
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-11-04"), Amount: 20000, Category: food, CardID: card})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.Tx(before)
	b, _ := s.Tx(after)
	if a.CardID != card || a.Statement != nov || !a.CashDay().Equal(d("2026-11-10")) {
		t.Fatalf("antes do fechamento: %+v", a)
	}
	if b.Payment != finance.Credit || b.Statement != dec || !b.CashDay().Equal(d("2026-12-10")) {
		t.Fatalf("depois do fechamento: %+v", b)
	}
	// a pix purchase never goes on the card
	pix, _ := s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-11-04"), Amount: 1, Category: food, Payment: finance.Pix, CardID: card})
	if e, _ := s.Tx(pix); e.CardID != 0 || e.Statement != 0 {
		t.Fatalf("pix on card: %+v", e)
	}

	// the cash leaves on the due date
	if _, err := s.SetCash(d("2026-11-16"), 100000, ""); err != nil {
		t.Fatal(err)
	}
	cf, err := s.CashProjection(2, "")
	if err != nil {
		t.Fatal(err)
	}
	if cf.Current != 100000 || cf.Months[1].Card != 20000 || cf.Months[1].Closing != 80000 {
		t.Fatalf("fluxo: atual %d, dezembro %+v", cf.Current, cf.Months[1])
	}

	// editing keeps the statement; changing the due day moves the cash date
	e, err := s.UpdateTx(after, TxIn{Date: d("2026-11-04"), Amount: 21000, Category: food})
	if err != nil || e.Statement != dec || e.CardID != card {
		t.Fatalf("update: %+v %v", e, err)
	}
	if _, err := s.SaveCard(CardIn{ID: card, Name: "Nubank", ClosingDay: 3, DueDay: 15, Active: true}); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.Tx(after); !e.CashDay().Equal(d("2026-12-15")) || e.Statement != dec {
		t.Fatalf("after due-day change: %+v", e)
	}
}

// A fatura can be entered as its total (valor cheio), item by item, or both:
// the declared total only adds what the items don't cover.
func TestStatementTotalAndItems(t *testing.T) {
	s := newSvc(t)
	card := nubank(t, s)
	v, err := s.SetStatementTotal(card, nov, 100000)
	if err != nil || v.Total() != 100000 || v.Rest() != 100000 {
		t.Fatalf("total only: %+v %v", v, err)
	}
	sum := func() (card, rest int64) {
		es, err := s.ListTx(TxFilter{From: nov, To: nov, Kind: finance.Expense})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range es {
			if e.Group() == finance.Card {
				card += e.Amount
			}
			if e.StatementRest {
				rest += e.Amount
				if e.Category != finance.RestCategory || !e.CashDay().Equal(d("2026-11-10")) {
					t.Fatalf("rest entry: %+v", e)
				}
			}
		}
		return
	}
	if c, r := sum(); c != 100000 || r != 100000 {
		t.Fatalf("só o total: cartão %d, não detalhado %d", c, r)
	}
	// an item bought in October on the November statement counts against it
	if _, err := s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-11-01"), Amount: 30000, Category: CategoryRef{Name: "Mercado", Create: true}, CardID: card}); err != nil {
		t.Fatal(err)
	}
	if c, r := sum(); c != 100000 || r != 70000 {
		t.Fatalf("total + item: cartão %d, não detalhado %d", c, r)
	}
	if _, err := s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-11-02"), Amount: 90000, Category: CategoryRef{Name: "Mercado"}, CardID: card}); err != nil {
		t.Fatal(err)
	}
	if c, r := sum(); c != 120000 || r != 0 {
		t.Fatalf("itens passam do total: cartão %d, não detalhado %d", c, r)
	}
	v, _ = s.Statement(card, nov)
	if v.Count != 2 || v.Statement.Items != 120000 || v.Declared != 100000 {
		t.Fatalf("statement: %+v", v.Statement)
	}
	// clearing the total
	if v, _ = s.SetStatementTotal(card, nov, 0); v.HasTotal {
		t.Fatal("total not cleared")
	}
	_, err = s.SetStatementTotal(card, nov, -1)
	mustInvalid(t, err, "valor inválido")
}

func TestInstallments(t *testing.T) {
	s := newSvc(t)
	card := nubank(t, s)
	tv := CategoryRef{Name: "Casa", Create: true}
	// a new R$ 1.000,00 purchase in 3x: 333,34 + 333,33 + 333,33 on three faturas
	ids, err := s.AddTxs([]TxIn{{Kind: finance.Expense, Date: d("2026-11-05"), Amount: 100000, Category: tv, CardID: card, Installments: 3, Description: "TV"}})
	if err != nil || len(ids) != 3 {
		t.Fatalf("parcels: %v %v", ids, err)
	}
	var total int64
	for i, id := range ids {
		e, _ := s.Tx(id)
		total += e.Amount
		if e.InstallmentN != i+1 || e.InstallmentOf != 3 || e.Statement != dec.Add(i) || e.InstallmentGroup != ids[0] {
			t.Fatalf("parcel %d: %+v", i+1, e)
		}
	}
	if e, _ := s.Tx(ids[0]); e.Amount != 33334 || total != 100000 {
		t.Fatalf("split: first %d total %d", e.Amount, total)
	}
	// deleting from parcel 2 removes 2 and 3
	if n, err := s.DeleteInstallmentsFrom(ids[1]); err != nil || n != 2 {
		t.Fatalf("delete from: %d %v", n, err)
	}
	_, err = s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-11-05"), Amount: 1, Category: tv, Installments: 100})
	mustInvalid(t, err, "parcelas inválidas")
}

// Card recurrences are projected on the right fatura, paid on its due date.
func TestCardRecurrence(t *testing.T) {
	s := newSvc(t)
	card := nubank(t, s)
	if _, err := s.SaveRecurrence(RecIn{Kind: finance.Expense, Category: CategoryRef{Name: "Streaming", Create: true}, Amount: 4000,
		Day: 20, Interval: 1, Start: nov, Active: true, CardID: card}); err != nil {
		t.Fatal(err)
	}
	// the November occurrence goes on the December fatura and counts in December
	if es, _ := s.ListTx(TxFilter{From: nov, To: nov, IncludeProjected: true}); len(es) != 0 {
		t.Fatalf("november: %+v", es)
	}
	es, _ := s.ListTx(TxFilter{From: dec, To: dec, IncludeProjected: true})
	if len(es) != 1 || es[0].OccMonth != "2026-11" || es[0].Payment != finance.Credit || es[0].Statement != dec || !es[0].CashDay().Equal(d("2026-12-10")) {
		t.Fatalf("projected: %+v", es)
	}
	id, err := s.ConfirmOccurrence(es[0].RecurrenceID, nov, 0, d("2026-11-20"), SourceManual)
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := s.Tx(id); e.CardID != card || e.Statement != dec || !e.Fixed {
		t.Fatalf("confirmed: %+v", e)
	}
	if v, _ := s.Statement(card, dec); v.Count != 1 || v.Statement.Items != 4000 {
		t.Fatalf("statement items: %+v", v.Statement)
	}
}

// The old way: a whole fatura entered as one expense on the day it is paid
// goes on the statement due that day, not on the next one.
func TestBillTotalAsExpense(t *testing.T) {
	s := newSvc(t)
	card := nubank(t, s)
	id, err := s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-11-10"), Amount: 150000, Category: CategoryRef{Name: finance.RestCategory}, Payment: finance.Credit})
	if err != nil {
		t.Fatal(err)
	}
	e, _ := s.Tx(id)
	if e.CardID != card || e.Statement != nov || !e.CashDay().Equal(d("2026-11-10")) || e.Group() != finance.Card {
		t.Fatalf("fatura total: %+v", e)
	}
	// alone it's fine; with detailed items on the same fatura, it's flagged
	if v, _ := s.Statement(card, nov); len(v.Loose) != 0 || v.Total() != 150000 {
		t.Fatalf("alone: %+v", v.Loose)
	}
	if _, err := s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-11-01"), Amount: 1000, Category: CategoryRef{Name: "Lazer", Create: true}, CardID: card}); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Statement(card, nov); len(v.Loose) != 1 || v.Loose[0].ID != id {
		t.Fatalf("with items: %+v", v.Loose)
	}
}

// A card purchase counts in the month its fatura is due: October purchases
// on the fatura due 10/11 are November spending, like the whole fatura was.
func TestCardPurchasesCountInDueMonth(t *testing.T) {
	s := newSvc(t)
	card := nubank(t, s)
	oct := finance.NewMonth(2026, 10)
	if _, err := s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-10-20"), Amount: 5000, Category: CategoryRef{Name: "Lazer", Create: true}, CardID: card}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-10-20"), Amount: 700, Category: CategoryRef{Name: "Lazer"}, Payment: finance.Pix}); err != nil {
		t.Fatal(err)
	}
	inOct, _ := s.ListTx(TxFilter{From: oct, To: oct})
	inNov, _ := s.ListTx(TxFilter{From: nov, To: nov})
	if len(inOct) != 1 || inOct[0].Amount != 700 || len(inNov) != 1 || inNov[0].Amount != 5000 {
		t.Fatalf("october %+v, november %+v", inOct, inNov)
	}
	o, err := s.Overview(nov)
	if err != nil {
		t.Fatal(err)
	}
	if o.Summary.Card != 5000 || o.Summary.Expense != 5000 {
		t.Fatalf("november summary: %+v", o.Summary)
	}
	if o, _ = s.Overview(oct); o.Summary.Card != 0 || o.Summary.Expense != 700 {
		t.Fatalf("october summary: %+v", o.Summary)
	}
}

// The double-count warning only points at totals that look like this card's
// fatura (they name the card or match its value), not other cards' totals.
func TestStatementWarnsOnlyAboutThisCard(t *testing.T) {
	s := newSvc(t)
	card := nubank(t, s)
	bill := CategoryRef{Name: finance.RestCategory}
	// totals of other cards, entered before cards existed (no card on them)
	if err := s.SetCardsEnabled(false); err != nil {
		t.Fatal(err)
	}
	for _, desc := range []string{"Fatura Bradesco", "Fatura Cartão Carol"} {
		if _, err := s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-11-05"), Amount: 70000, Category: bill, Description: desc, Payment: finance.Credit}); err != nil {
			t.Fatal(err)
		}
	}
	mine, err := s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-11-10"), Amount: 150000, Category: bill, Description: "fatura Nubank Conjunta", Payment: finance.Credit})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetCardsEnabled(true); err != nil {
		t.Fatal(err)
	}
	// before importing (no items yet) the Nubank total is already pointed out
	v, _ := s.Statement(card, nov)
	if len(v.Loose) != 1 || v.Loose[0].ID != mine {
		t.Fatalf("before import: %+v", v.Loose)
	}
	// with items, a total with no card name but the same value is caught too
	if _, err := s.ImportStatement(card, nov, []ImportItem{{Date: d("2026-10-20"), Description: "Mercado", Amount: 70000, Category: CategoryRef{Name: "Alimentação", Create: true}}}, ""); err != nil {
		t.Fatal(err)
	}
	v, _ = s.Statement(card, nov)
	if len(v.Loose) != 3 {
		t.Fatalf("with items worth 700,00: %+v", v.Loose)
	}
	if _, err := s.DeleteTx(mine); err != nil {
		t.Fatal(err)
	}
	_, err = s.ImportStatement(card, nov, []ImportItem{{Date: d("2026-10-21"), Description: "Loja", Amount: 1, Category: CategoryRef{Name: "Alimentação"}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ = s.Statement(card, nov); len(v.Loose) != 0 {
		t.Fatalf("other cards' totals flagged: %+v", v.Loose)
	}
}

// A fatura total naming another bank isn't the only card's bill: it stays
// off the card and leaves cash on its date, even after an edit.
func TestOtherBankBillTotalStaysOffCard(t *testing.T) {
	s := newSvc(t)
	card := nubank(t, s)
	bill := CategoryRef{Name: finance.RestCategory}
	other, err := s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-11-24"), Amount: 120771, Category: bill, Description: "Fatura Bradesco - Adiantamento última parcela", Payment: finance.Credit})
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := s.Tx(other); e.CardID != 0 || e.Statement != 0 || !e.CashDay().Equal(d("2026-11-24")) {
		t.Fatalf("other bank: %+v", e)
	}
	if _, err := s.UpdateTx(other, TxIn{Date: d("2026-11-25"), Amount: 120771, Category: bill, Description: "Fatura Bradesco"}); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.Tx(other); e.CardID != 0 || !e.CashDay().Equal(d("2026-11-25")) {
		t.Fatalf("other bank edited: %+v", e)
	}
	// generic or naming the card: still the card's fatura; an explicit card always wins
	for _, desc := range []string{"", "Fatura", "Pagamento fatura novembro 2026", "Fatura Nubank"} {
		id, err := s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-11-10"), Amount: 1000, Category: bill, Description: desc, Payment: finance.Credit})
		if err != nil {
			t.Fatal(err)
		}
		if e, _ := s.Tx(id); e.CardID != card || e.Statement != nov {
			t.Fatalf("%q: %+v", desc, e)
		}
	}
	id, err := s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-11-10"), Amount: 1000, Category: bill, Description: "Fatura Bradesco", CardID: card})
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := s.Tx(id); e.CardID != card {
		t.Fatalf("explicit card: %+v", e)
	}
}
