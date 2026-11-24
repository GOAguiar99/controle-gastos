package app

import (
	"testing"
	"time"

	"controle-gastos/internal/finance"
)

func TestForecast(t *testing.T) {
	s := newSvc(t)                                                      // today 2026-11-16
	if _, err := s.SetCash(d("2026-11-16"), 10000000, ""); err != nil { // R$ 100 mil
		t.Fatal(err)
	}
	// six closed months of day-to-day spending: R$ 1.000 .. R$ 6.000
	for i, v := range []int64{300000, 100000, 600000, 200000, 500000, 400000} {
		in := TxIn{Kind: finance.Expense, Date: finance.NewMonth(2026, time.May).Add(i).Day(10), Amount: v,
			Category: CategoryRef{Name: "Mercado", Create: true}, Payment: finance.Pix}
		if _, err := s.AddTx(in); err != nil {
			t.Fatal(err)
		}
	}
	// already launched for December: counts within the estimate
	if _, err := s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-12-05"), Amount: 300000,
		Category: CategoryRef{Name: "Mercado"}, Payment: finance.Pix}); err != nil {
		t.Fatal(err)
	}

	if s.ForecastMode() != ScenarioOff {
		t.Fatal("off by default")
	}
	off, err := s.Overview(0)
	if err != nil || off.Forecast.Active() || off.Forecast.Flows != nil || !off.Forecast.OK {
		t.Fatalf("off: %+v %v", off.Forecast, err)
	}
	if b := off.Forecast.Base; b.Optimistic != 225000 || b.Base != 350000 || b.Pessimistic != 475000 {
		t.Fatalf("percentiles: %+v", b)
	}
	// even off, Forecast shows what each scenario would do
	fv, err := s.Forecast()
	if err != nil || len(fv.Flows) != 3 || fv.Flows[ScenarioOptimistic].Low <= fv.Flows[ScenarioPessimistic].Low {
		t.Fatalf("forecast view: %+v %v", fv, err)
	}

	mustInvalid(t, s.SetForecast("talvez"), "cenário inválido")
	if err := s.SetForecast(ScenarioBase); err != nil {
		t.Fatal(err)
	}
	on, _ := s.Overview(0)
	if !on.Forecast.Active() || len(on.Forecast.Flows) != 3 {
		t.Fatalf("on: %+v", on.Forecast)
	}
	var dec finance.FlowMonth
	for _, fm := range on.FlowRows {
		if fm.Month == finance.NewMonth(2026, time.December) {
			dec = fm
		}
	}
	if dec.Forecast != 50000 { // R$ 3.500 estimated − R$ 3.000 launched
		t.Fatalf("december top-up: %+v", dec)
	}
	if on.Flow.Low != on.Forecast.Flows[ScenarioBase].Low || on.Flow.Low >= off.Flow.Low {
		t.Fatalf("flow uses the base scenario: on %d off %d", on.Flow.Low, off.Flow.Low)
	}
	// the month's own numbers never include the estimate
	if on.Summary != off.Summary {
		t.Fatalf("summary changed: %+v", on.Summary)
	}

	// the projection follows the saved scenario, or the one asked for
	saved, _ := s.CashProjection(12, "")
	none, _ := s.CashProjection(12, "desligado")
	pess, _ := s.CashProjection(12, "pessimista")
	if saved.Low != on.Flow.Low || none.Low != off.Flow.Low || pess.Low >= saved.Low {
		t.Fatalf("projection: saved %d none %d pess %d", saved.Low, none.Low, pess.Low)
	}
	_, err = s.CashProjection(12, "x")
	mustInvalid(t, err, "cenário inválido")

	if err := s.SetForecast(ScenarioOff); err != nil || s.ForecastMode() != ScenarioOff {
		t.Fatalf("turn off: %v", err)
	}
}
