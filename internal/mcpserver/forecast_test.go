package mcpserver

import (
	"testing"
	"time"

	"controle-gastos/internal/app"
	"controle-gastos/internal/finance"
)

func TestForecastTools(t *testing.T) {
	cs, svc := connect(t) // today 2026-11-16
	if _, err := svc.SetCash(time.Time{}, 5000000, ""); err != nil {
		t.Fatal(err)
	}
	var f struct {
		Scenario  string
		Available bool
		Base      *struct{ Cents int64 }
		Months    []string
		Scenarios []struct {
			Scenario string
			Lowest   struct{ Cents int64 }
		}
	}
	call(t, cs, "get_forecast", map[string]any{}, &f, false)
	if f.Available || f.Base != nil || len(f.Scenarios) != 0 {
		t.Fatalf("no history: %+v", f)
	}
	for i, v := range []int64{300000, 100000, 600000, 200000, 500000, 400000} {
		if _, err := svc.AddTx(app.TxIn{Kind: finance.Expense, Date: finance.NewMonth(2026, time.May).Add(i).Day(10), Amount: v,
			Category: app.CategoryRef{Name: "Mercado", Create: true}, Payment: finance.Pix}); err != nil {
			t.Fatal(err)
		}
	}
	call(t, cs, "get_forecast", map[string]any{}, &f, false)
	if !f.Available || f.Scenario != "" || f.Base == nil || f.Base.Cents != 350000 || len(f.Months) != 6 || len(f.Scenarios) != 3 {
		t.Fatalf("forecast: %+v", f)
	}
	if f.Scenarios[0].Lowest.Cents <= f.Scenarios[2].Lowest.Cents {
		t.Fatalf("optimistic keeps more cash: %+v", f.Scenarios)
	}

	var p struct {
		Lowest struct{ Cents int64 }
		Months []struct {
			Forecast *struct{ Cents int64 }
		}
	}
	call(t, cs, "get_cash_projection", map[string]any{}, &p, false)
	off := p.Lowest.Cents
	if p.Months[1].Forecast != nil {
		t.Fatalf("off: %+v", p.Months[1])
	}
	call(t, cs, "set_forecast", map[string]any{"mode": "talvez"}, nil, true)
	call(t, cs, "set_forecast", map[string]any{"mode": "Base"}, nil, false)
	call(t, cs, "get_cash_projection", map[string]any{}, &p, false)
	if p.Lowest.Cents >= off || p.Months[1].Forecast == nil || p.Months[1].Forecast.Cents != 350000 {
		t.Fatalf("on: %d vs %d %+v", p.Lowest.Cents, off, p.Months[1])
	}
	call(t, cs, "get_cash_projection", map[string]any{"scenario": "desligado"}, &p, false)
	if p.Lowest.Cents != off {
		t.Fatalf("scenario off: %d", p.Lowest.Cents)
	}
	var o struct {
		Forecast struct{ Scenario string }
	}
	call(t, cs, "get_overview", map[string]any{}, &o, false)
	if o.Forecast.Scenario != "base" {
		t.Fatalf("overview: %+v", o)
	}
	call(t, cs, "set_forecast", map[string]any{"mode": "desligado"}, nil, false)
	if svc.ForecastMode() != app.ScenarioOff {
		t.Fatal("still on")
	}
}
