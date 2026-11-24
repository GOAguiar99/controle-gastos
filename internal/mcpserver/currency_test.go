package mcpserver

import (
	"strings"
	"testing"
	"time"

	"controle-gastos/internal/db"
)

func TestCurrencyTools(t *testing.T) {
	cs, svc := connect(t) // today 2026-11-16 (Monday)
	if err := svc.Store().SaveFXRates([]db.FXQuote{
		{Currency: "USD", Date: d("2026-11-13"), Rate: 5.5},
		{Currency: "USD", Date: d("2026-11-16"), Rate: 5.6},
	}); err != nil {
		t.Fatal(err)
	}
	var cur struct {
		Display  string                  `json:"display_currency"`
		DispRate *struct{ Rate float64 } `json:"display_rate"`
		Rate     *struct {
			Date string
			Rate float64
		}
		Supported []string
	}
	call(t, cs, "get_currency", map[string]any{"currency": "usd", "date": "2026-11-15"}, &cur, false)
	if cur.Display != "BRL" || cur.DispRate != nil || cur.Rate == nil || cur.Rate.Rate != 5.5 || cur.Rate.Date != "2026-11-13" || len(cur.Supported) < 3 {
		t.Fatalf("get_currency: %+v", cur)
	}

	var added addTxOut
	call(t, cs, "add_transactions", map[string]any{"items": []map[string]any{
		{"kind": "expense", "date": "2026-11-15", "amount": 100, "currency": "USD", "category": "Viagem", "create_category": true, "payment_method": "debito"},
		{"kind": "expense", "date": "2026-11-15", "amount": 10, "category": "Viagem"},
	}}, &added, false)
	if !strings.Contains(added.Message, "R$ 560,00") {
		t.Fatalf("total in reais: %s", added.Message)
	}
	var list struct{ Transactions []txOut }
	call(t, cs, "list_transactions", map[string]any{"from": "2026-11", "text": "viagem"}, &list, false)
	var usd *txOut
	for i := range list.Transactions {
		if list.Transactions[i].Currency == "USD" {
			usd = &list.Transactions[i]
		}
	}
	if usd == nil || usd.AmountCents != 55000 || usd.FXRate != 5.5 || usd.Original == nil || usd.Original.Text != "US$ 100,00" {
		t.Fatalf("usd tx: %+v", list.Transactions)
	}

	// update in the original currency keeps the rate; a typed rate replaces it
	var upd txResult
	call(t, cs, "update_transaction", map[string]any{"id": usd.ID, "amount": 200}, &upd, false)
	if upd.Transaction.AmountCents != 110000 || upd.Transaction.Original.Cents != 20000 {
		t.Fatalf("update amount: %+v", upd.Transaction)
	}
	call(t, cs, "update_transaction", map[string]any{"id": usd.ID, "fx_rate": 6}, &upd, false)
	if upd.Transaction.AmountCents != 120000 || upd.Transaction.FXRate != 6 {
		t.Fatalf("update rate: %+v", upd.Transaction)
	}
	upd = txResult{} // omitted fields must not keep the previous values
	call(t, cs, "update_transaction", map[string]any{"id": usd.ID, "currency": "BRL", "amount": 1000}, &upd, false)
	if upd.Transaction.Currency != "" || upd.Transaction.AmountCents != 100000 {
		t.Fatalf("back to reais: %+v", upd.Transaction)
	}
	call(t, cs, "add_transactions", map[string]any{"items": []map[string]any{
		{"kind": "expense", "date": "2026-11-15", "amount": 1, "currency": "XYZ", "category": "Viagem"}}}, nil, true)

	call(t, cs, "set_display_currency", map[string]any{"currency": "usd"}, &cur, false)
	if cur.Display != "USD" || cur.DispRate == nil || cur.DispRate.Rate != 5.6 {
		t.Fatalf("display: %+v", cur)
	}
	if svc.DisplayCurrency().Code != "USD" {
		t.Fatal("not saved")
	}
	call(t, cs, "set_display_currency", map[string]any{"currency": "GBP"}, nil, true) // no rate stored
}

func d(s string) time.Time { t, _ := time.Parse("2006-01-02", s); return t }
