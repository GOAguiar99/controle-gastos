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
	// already launched for December: counts within the average
	if _, err := s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-12-05"), Amount: 300000,
		Category: CategoryRef{Name: "Mercado"}, Payment: finance.Pix}); err != nil {
		t.Fatal(err)
	}

	if s.ForecastOn() {
		t.Fatal("off by default")
	}
	off, err := s.Overview(0)
	if err != nil || off.Forecast.Active() || !off.Forecast.OK || off.Forecast.Average() != 350000 {
		t.Fatalf("off: %+v %v", off.Forecast, err)
	}
	// even off, it tells where keeping the average leads
	if off.Forecast.Flow.Low >= off.Flow.Low || len(off.Forecast.Flow.Months) != HorizonMonths {
		t.Fatalf("keeping the average: %d vs %d", off.Forecast.Flow.Low, off.Flow.Low)
	}

	// the first version saved a scenario name: it still means on
	if err := s.Store().SetSetting(forecastSetting, "otimista"); err != nil || !s.ForecastOn() {
		t.Fatalf("legacy value: %v", err)
	}
	if err := s.SetForecast(true); err != nil {
		t.Fatal(err)
	}
	on, _ := s.Overview(0)
	if !on.Forecast.Active() || on.Flow.Low != off.Forecast.Flow.Low || on.Forecast.Flow.Low != on.Flow.Low {
		t.Fatalf("on: %+v", on.Forecast)
	}
	for _, fm := range on.FlowRows {
		switch fm.Month {
		case finance.NewMonth(2026, time.December): // R$ 3.000 launched + R$ 500 estimated
			if fm.Forecast != 50000 || fm.DayToDay() != 350000 {
				t.Fatalf("december: %+v", fm)
			}
		case finance.NewMonth(2027, time.March):
			if fm.Forecast != 350000 || fm.DayToDay() != 350000 {
				t.Fatalf("march: %+v", fm)
			}
		}
	}
	// the month's own numbers never include the estimate
	if on.Summary != off.Summary {
		t.Fatalf("summary changed: %+v", on.Summary)
	}

	saved, _ := s.CashProjection(12, "")
	none, _ := s.CashProjection(12, "desligado")
	if saved.Low != on.Flow.Low || none.Low != off.Flow.Low {
		t.Fatalf("projection: saved %d none %d", saved.Low, none.Low)
	}
	_, err = s.CashProjection(12, "talvez")
	mustInvalid(t, err, "previsão de gastos")

	if err := s.SetForecast(false); err != nil || s.ForecastOn() {
		t.Fatalf("turn off: %v", err)
	}
}
