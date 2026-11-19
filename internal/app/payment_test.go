package app

import (
	"testing"
	"time"

	"controle-gastos/internal/finance"
)

// The same category can be paid in different ways; the group follows the payment.
func TestPaymentMethodDecidesGroup(t *testing.T) {
	s := newSvc(t)
	food := CategoryRef{Name: "Alimentação", Create: true}
	card, err := s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-11-03"), Amount: 5000, Category: food, Payment: finance.Credit})
	if err != nil {
		t.Fatal(err)
	}
	pix, err := s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-11-04"), Amount: 3000, Category: food, Payment: finance.Pix})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.Tx(card)
	b, _ := s.Tx(pix)
	if a.Group() != finance.Card || b.Group() != finance.Other {
		t.Fatalf("groups: card=%s pix=%s", a.Group(), b.Group())
	}

	// no method: inherit the latest one used in the category (pix here)
	id, err := s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-11-05"), Amount: 100, Category: food})
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := s.Tx(id); e.Payment != finance.Pix {
		t.Fatalf("default payment: %q", e.Payment)
	}
	// new category without history: outro
	id, _ = s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-11-05"), Amount: 100, Category: CategoryRef{Name: "Nova", Create: true}})
	if e, _ := s.Tx(id); e.Payment != finance.OtherPay {
		t.Fatalf("default without history: %q", e.Payment)
	}

	// update without a method keeps it; with one, changes it
	e, err := s.UpdateTx(card, TxIn{Date: d("2026-11-03"), Amount: 5500, Category: food})
	if err != nil || e.Payment != finance.Credit {
		t.Fatalf("update kept payment: %+v %v", e, err)
	}
	e, err = s.UpdateTx(card, TxIn{Date: d("2026-11-03"), Amount: 5500, Category: food, Payment: finance.Debit})
	if err != nil || e.Payment != finance.Debit || e.Group() != finance.Other {
		t.Fatalf("update changed payment: %+v %v", e, err)
	}

	_, err = s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-11-05"), Amount: 1, Category: food, Payment: "cartao"})
	mustInvalid(t, err, "forma de pagamento inválida")

	// income has no payment method
	id, _ = s.AddTx(TxIn{Kind: finance.Income, Date: d("2026-11-05"), Amount: 1, Category: CategoryRef{Name: "Salário", Create: true}, Payment: finance.Credit})
	if e, _ := s.Tx(id); e.Payment != finance.OtherPay || e.Group() != finance.Other {
		t.Fatalf("income payment: %+v", e)
	}
}

// A recurrence's occurrences are fixed expenses, paid the recurrence's way.
func TestRecurrenceOccurrencesAreFixed(t *testing.T) {
	s := newSvc(t)
	nov := finance.MonthOf(d("2026-11-01"))
	net, err := s.SaveRecurrence(RecIn{Kind: finance.Expense, Category: CategoryRef{Name: "Internet", Create: true},
		Amount: 10000, Day: 10, Interval: 1, Start: nov, Active: true, Payment: finance.Boleto})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := s.SaveRecurrence(RecIn{Kind: finance.Expense, Category: CategoryRef{Name: "Streaming", Create: true},
		Amount: 4000, Day: 12, Interval: 1, Start: nov, Active: true, Payment: finance.Credit})
	if err != nil {
		t.Fatal(err)
	}
	es, _ := s.ListTx(TxFilter{From: nov, To: nov, IncludeProjected: true})
	groups := map[int64]finance.Bucket{}
	for _, e := range es {
		groups[e.RecurrenceID] = e.Group()
	}
	if groups[net] != finance.Fixed || groups[stream] != finance.Card {
		t.Fatalf("projected groups: %v", groups)
	}
	id, err := s.ConfirmOccurrence(net, nov, 0, time.Time{}, SourceManual)
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := s.Tx(id); !e.Fixed || e.Payment != finance.Boleto || e.Group() != finance.Fixed {
		t.Fatalf("confirmed occurrence: %+v", e)
	}
	// editing the recurrence without a method keeps it
	if _, err := s.SaveRecurrence(RecIn{ID: stream, Kind: finance.Expense, Category: CategoryRef{Name: "Streaming"},
		Amount: 4500, Day: 12, Interval: 1, Start: nov, Active: true}); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Recurrence(stream); r.Payment != finance.Credit {
		t.Fatalf("recurrence payment after edit: %q", r.Payment)
	}
}
