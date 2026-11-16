package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"controle-gastos/internal/app"
	"controle-gastos/internal/db"
)

func connect(t *testing.T) (*mcp.ClientSession, *app.Service) {
	t.Helper()
	st, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := app.New(st, time.UTC)
	svc.SetClock(func() time.Time { return time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC) })

	ctx := context.Background()
	ct, stT := mcp.NewInMemoryTransports()
	if _, err := New(svc).Connect(ctx, stT, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, svc
}

// call runs a tool and decodes its structured output into out; it fails the
// test when the tool errors unless wantErr is set, and returns the text.
func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any, wantErr bool) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	text := ""
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			text = tc.Text
		}
	}
	if res.IsError != wantErr {
		t.Fatalf("%s: isError=%v want %v: %s", name, res.IsError, wantErr, text)
	}
	if out != nil && !wantErr {
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}
	}
	return text
}

func TestToolsRoundTrip(t *testing.T) {
	cs, _ := connect(t)
	ctx := context.Background()

	tl, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*mcp.Tool{}
	for _, tool := range tl.Tools {
		byName[tool.Name] = tool
	}
	for _, n := range []string{"get_overview", "list_transactions", "add_transactions", "confirm_occurrence", "delete_transaction", "category_report"} {
		if byName[n] == nil {
			t.Fatalf("missing tool %s", n)
		}
	}
	if a := byName["delete_transaction"].Annotations; a == nil || a.DestructiveHint == nil || !*a.DestructiveHint {
		t.Fatal("delete_transaction must be destructive")
	}
	if a := byName["list_transactions"].Annotations; a == nil || !a.ReadOnlyHint {
		t.Fatal("list_transactions must be read-only")
	}

	// unknown category is a tool error Claude can read, nothing is saved
	msg := call(t, cs, "add_transactions", map[string]any{"items": []any{
		map[string]any{"kind": "expense", "date": "2026-11-10", "amount": 50, "category": "Mercadinho"},
	}}, nil, true)
	if !strings.Contains(msg, "não existe") {
		t.Fatalf("error message: %s", msg)
	}

	var added addTxOut
	call(t, cs, "add_transactions", map[string]any{"items": []any{
		map[string]any{"kind": "expense", "date": "2026-11-10", "amount": 129.9, "category": "Mercado", "create_category": true, "description": "Feira"},
		map[string]any{"kind": "income", "date": "2026-11-05", "amount": 5000, "category": "Empresa", "create_category": true},
	}}, &added, false)
	if len(added.IDs) != 2 {
		t.Fatalf("added: %+v", added)
	}

	var rec idOut
	call(t, cs, "save_recurrence", map[string]any{"kind": "expense", "category": "Aluguel", "create_category": true,
		"amount": 2000, "day": 5, "start": "2026-11"}, &rec, false)

	var list listTxOut
	call(t, cs, "list_transactions", map[string]any{"include_projected": true}, &list, false)
	if list.Count != 3 || list.Expense != 12990+200000 || list.Income != 500000 {
		t.Fatalf("list: %+v", list)
	}
	var projected *txOut
	for i, tx := range list.Transactions {
		if tx.Projected {
			projected = &list.Transactions[i]
		}
	}
	if projected == nil || projected.RecurrenceID != rec.ID || projected.Month != "2026-11" {
		t.Fatalf("projected: %+v", list.Transactions)
	}

	var confirmed txResult
	call(t, cs, "confirm_occurrence", map[string]any{"recurrence_id": rec.ID, "month": "2026-11", "amount": 1950.5}, &confirmed, false)
	if confirmed.Transaction.AmountCents != 195050 || confirmed.Transaction.ID == 0 {
		t.Fatalf("confirmed: %+v", confirmed)
	}

	var upd txResult
	call(t, cs, "update_transaction", map[string]any{"id": added.IDs[0], "description": "Feira do mês"}, &upd, false)
	if upd.Transaction.Description != "Feira do mês" || upd.Transaction.AmountCents != 12990 {
		t.Fatalf("update: %+v", upd)
	}

	call(t, cs, "set_cash", map[string]any{"amount": 3745}, nil, false)
	var ov overviewOut
	call(t, cs, "get_overview", map[string]any{}, &ov, false)
	if ov.Today != "2026-11-16" || ov.CashInformed == nil || ov.CashInformed.Cents != 374500 || ov.Expense.Cents != 12990+195050 {
		t.Fatalf("overview: %+v", ov)
	}

	var rep reportOut
	call(t, cs, "category_report", map[string]any{"kind": "expense"}, &rep, false)
	if len(rep.Categories) != 2 || len(rep.Years) == 0 {
		t.Fatalf("report: %+v", rep)
	}

	var debt debtSavedOut
	call(t, cs, "save_debt", map[string]any{"name": "Carro", "initial": 12000, "system": "sac", "monthly_rate_pct": 1.5,
		"term": 12, "first_due": "2026-12-10"}, &debt, false)
	if debt.SuggestedRecurrenceID == 0 || !strings.Contains(debt.Message, "accept_recurrence") {
		t.Fatalf("loan suggestion: %+v", debt)
	}
	var recs recurrencesOut
	call(t, cs, "list_recurrences", map[string]any{"kind": "expense"}, &recs, false)
	pending := 0
	for _, r := range recs.Recurrences {
		if r.Pending {
			pending++
		}
	}
	if pending != 1 {
		t.Fatalf("pending: %+v", recs)
	}
	call(t, cs, "accept_recurrence", map[string]any{"id": debt.SuggestedRecurrenceID}, nil, false)
	call(t, cs, "accept_recurrence", map[string]any{"id": debt.SuggestedRecurrenceID}, nil, true)
	var dec listTxOut
	call(t, cs, "list_transactions", map[string]any{"from": "2026-12", "text": "Carro", "include_projected": true}, &dec, false)
	if dec.Count != 1 || dec.Expense != 118000 {
		t.Fatalf("accepted installment: %+v", dec)
	}
	var simple debtSavedOut
	call(t, cs, "save_debt", map[string]any{"name": "Pais", "initial": 5000}, &simple, false)
	if simple.SuggestedRecurrenceID != 0 {
		t.Fatalf("simple debt got a recurrence: %+v", simple)
	}

	var del txResult
	call(t, cs, "delete_transaction", map[string]any{"id": added.IDs[0]}, &del, false)
	call(t, cs, "delete_transaction", map[string]any{"id": added.IDs[0]}, nil, true)
	call(t, cs, "list_transactions", map[string]any{"kind": "expense"}, &list, false)
	if list.Count != 1 {
		t.Fatalf("after delete: %+v", list)
	}
}
