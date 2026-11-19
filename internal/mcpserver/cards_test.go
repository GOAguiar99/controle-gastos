package mcpserver

import (
	"testing"
)

type stmt struct {
	Statement struct {
		Month      string                `json:"month"`
		Due        string                `json:"due_date"`
		Count      int                   `json:"items_count"`
		Total      struct{ Cents int64 } `json:"total"`
		Undetailed struct{ Cents int64 } `json:"undetailed"`
	} `json:"statement"`
	Items []struct {
		Description string `json:"description"`
		Installment string `json:"installment"`
		Card        string `json:"card"`
	} `json:"items"`
	Warning string `json:"warning"`
}

func TestCardTools(t *testing.T) {
	cs, _ := connect(t)
	call(t, cs, "save_card", map[string]any{"name": "Nubank", "closing_day": 3, "due_day": 10}, nil, false)
	call(t, cs, "save_card", map[string]any{"name": "nubank", "closing_day": 3, "due_day": 10}, nil, true)
	var cards struct {
		Cards []struct {
			Name string `json:"name"`
			Open struct {
				Month string `json:"month"`
			} `json:"open_statement"`
		} `json:"cards"`
	}
	call(t, cs, "list_cards", map[string]any{}, &cards, false)
	if len(cards.Cards) != 1 || cards.Cards[0].Open.Month != "2026-12" { // today 16/11, closes on the 3rd
		t.Fatalf("cards: %+v", cards)
	}

	// valor cheio
	var s stmt
	call(t, cs, "set_statement_total", map[string]any{"card": "Nubank", "month": "2026-11", "total": 1000}, &s, false)
	if s.Statement.Total.Cents != 100000 || s.Statement.Undetailed.Cents != 100000 || s.Statement.Due != "2026-11-10" {
		t.Fatalf("total: %+v", s)
	}
	// item a item na mesma fatura: o não detalhado diminui
	call(t, cs, "import_statement", map[string]any{"card": "Nubank", "month": "2026-11", "items": []map[string]any{
		{"date": "2026-10-28", "description": "Padaria", "amount": 12.5, "category": "Alimentação", "create_category": true},
		{"date": "2026-10-30", "description": "Magazine", "amount": 99.9, "category": "Casa", "create_category": true, "installment_n": 3, "installments": 10},
	}}, nil, false)
	call(t, cs, "get_statement", map[string]any{"card": "Nubank", "month": "2026-11"}, &s, false)
	if s.Statement.Count != 2 || s.Statement.Undetailed.Cents != 100000-1250-9990 || s.Statement.Total.Cents != 100000 {
		t.Fatalf("after import: %+v", s)
	}
	// a parcela 4/10 já está na fatura seguinte
	call(t, cs, "get_statement", map[string]any{"card": "Nubank", "month": "2026-12"}, &s, false)
	if len(s.Items) != 1 || s.Items[0].Installment != "4/10" {
		t.Fatalf("next statement: %+v", s.Items)
	}

	// compra parcelada nova pelo add_transactions: total dividido
	var added struct {
		IDs []int64 `json:"ids"`
	}
	call(t, cs, "add_transactions", map[string]any{"items": []map[string]any{
		{"kind": "expense", "date": "2026-11-16", "amount": 300, "category": "Casa", "card": "Nubank", "installments": 3},
	}}, &added, false)
	if len(added.IDs) != 3 {
		t.Fatalf("parcels: %v", added.IDs)
	}
	var list struct {
		Transactions []struct {
			Card, Statement, Installment, Group string
			CashDate                            string `json:"cash_date"`
		} `json:"transactions"`
	}
	call(t, cs, "list_transactions", map[string]any{"from": "2026-11", "kind": "expense", "text": "casa"}, &list, false)
	found := false
	for _, tx := range list.Transactions {
		if tx.Installment == "1/3" {
			found = tx.Statement == "2026-12" && tx.CashDate == "2026-12-10" && tx.Group == "cartao" && tx.Card == "Nubank"
		}
	}
	if !found {
		t.Fatalf("parcel 1/3: %+v", list.Transactions)
	}
	call(t, cs, "add_transactions", map[string]any{"items": []map[string]any{
		{"kind": "expense", "date": "2026-11-16", "amount": 1, "category": "Casa", "card": "Inexistente"},
	}}, nil, true)
	call(t, cs, "delete_card", map[string]any{"id": 1}, nil, true) // has purchases
}
