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
		Enabled   bool
		Available bool
		Average   *struct{ Cents int64 } `json:"monthly_average"`
		Months    []struct{ Month string }
		End       *struct{ Cents int64 } `json:"cash_at_end"`
		Lowest    *struct{ Cents int64 } `json:"lowest_cash"`
	}
	call(t, cs, "get_forecast", map[string]any{}, &f, false)
	if f.Available || f.Average != nil || f.End != nil {
		t.Fatalf("no history: %+v", f)
	}
	for i, v := range []int64{300000, 100000, 600000, 200000, 500000, 400000} {
		if _, err := svc.AddTx(app.TxIn{Kind: finance.Expense, Date: finance.NewMonth(2026, time.May).Add(i).Day(10), Amount: v,
			Category: app.CategoryRef{Name: "Mercado", Create: true}, Payment: finance.Pix}); err != nil {
			t.Fatal(err)
		}
	}
	call(t, cs, "get_forecast", map[string]any{}, &f, false)
	if !f.Available || f.Enabled || f.Average == nil || f.Average.Cents != 350000 || len(f.Months) != 6 || f.Months[0].Month != "2026-05" || f.End == nil {
		t.Fatalf("forecast: %+v", f)
	}

	var p struct {
		Lowest struct{ Cents int64 }
		Months []struct {
			Forecast *struct{ Cents int64 }
			DayToDay *struct{ Cents int64 } `json:"day_to_day"`
		}
	}
	call(t, cs, "get_cash_projection", map[string]any{}, &p, false)
	off := p.Lowest.Cents
	if p.Months[1].Forecast != nil {
		t.Fatalf("off: %+v", p.Months[1])
	}
	call(t, cs, "set_forecast", map[string]any{"mode": "talvez"}, nil, true)
	call(t, cs, "set_forecast", map[string]any{"mode": "ligado"}, nil, false)
	call(t, cs, "get_cash_projection", map[string]any{}, &p, false)
	if p.Lowest.Cents >= off || p.Lowest.Cents != f.Lowest.Cents || p.Months[1].DayToDay == nil || p.Months[1].DayToDay.Cents != 350000 {
		t.Fatalf("on: %d vs %d %+v", p.Lowest.Cents, off, p.Months[1])
	}
	call(t, cs, "get_cash_projection", map[string]any{"forecast": "desligado"}, &p, false)
	if p.Lowest.Cents != off {
		t.Fatalf("forecast off: %d", p.Lowest.Cents)
	}
	var o struct {
		Forecast struct{ Enabled bool }
	}
	call(t, cs, "get_overview", map[string]any{}, &o, false)
	if !o.Forecast.Enabled {
		t.Fatalf("overview: %+v", o)
	}
	call(t, cs, "set_forecast", map[string]any{"mode": "desligado"}, nil, false)
	if svc.ForecastOn() {
		t.Fatal("still on")
	}
}
