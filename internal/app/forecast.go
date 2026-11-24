package app

import (
	"strings"
	"time"

	"controle-gastos/internal/finance"
)

// Scenario picks which estimate of the day-to-day spending the cash flow
// uses (previsão de gastos). ScenarioOff leaves the flow with only what is
// launched.
type Scenario string

const (
	ScenarioOff         Scenario = ""
	ScenarioOptimistic  Scenario = "otimista"
	ScenarioBase        Scenario = "base"
	ScenarioPessimistic Scenario = "pessimista"
)

// Scenarios lists the estimates in display order.
var Scenarios = []Scenario{ScenarioOptimistic, ScenarioBase, ScenarioPessimistic}

// ForecastWindow is how many closed months the estimate samples.
const ForecastWindow = HistoryMonths

// forecastSetting stores the chosen scenario ("" = off).
const forecastSetting = "forecast"

// ParseScenario reads a scenario name; "desligado" (or empty) turns it off.
func ParseScenario(s string) (Scenario, error) {
	switch v := Scenario(strings.ToLower(strings.TrimSpace(s))); v {
	case ScenarioOptimistic, ScenarioBase, ScenarioPessimistic:
		return v, nil
	case "", "desligado", "nenhum", "off":
		return ScenarioOff, nil
	}
	return ScenarioOff, invalid("cenário inválido %q: use otimista, base, pessimista ou desligado", s)
}

// Label is the pt-BR name shown in the UI.
func (sc Scenario) Label() string {
	switch sc {
	case ScenarioOptimistic:
		return "Otimista"
	case ScenarioBase:
		return "Base"
	case ScenarioPessimistic:
		return "Pessimista"
	}
	return "Desligada"
}

// ForecastView is the estimate of the monthly day-to-day spending and how
// the projected cash looks under each scenario.
type ForecastView struct {
	Mode Scenario // chosen scenario ("" = off)
	Base finance.ForecastBase
	OK   bool // enough history to estimate (finance.MinForecastMonths)
	// Flows is the projected cash per scenario (filled when Active).
	Flows map[Scenario]finance.CashFlow
}

// Active reports whether the cash flow includes the estimate.
func (f ForecastView) Active() bool { return f.Mode != ScenarioOff && f.OK }

// Amount is the monthly estimate of a scenario (0 when off).
func (f ForecastView) Amount(sc Scenario) int64 {
	switch sc {
	case ScenarioOptimistic:
		return f.Base.Optimistic
	case ScenarioBase:
		return f.Base.Base
	case ScenarioPessimistic:
		return f.Base.Pessimistic
	}
	return 0
}

// ForecastMode is the saved scenario ("" = off).
func (s *Service) ForecastMode() Scenario {
	sc, _ := ParseScenario(s.st.Setting(forecastSetting))
	return sc
}

// SetForecast turns the spending estimate on with a scenario, or off.
func (s *Service) SetForecast(sc Scenario) error {
	if _, err := ParseScenario(string(sc)); err != nil {
		return err
	}
	return s.st.SetSetting(forecastSetting, string(sc))
}

// Forecast returns the estimate and the projected cash under each scenario
// for the next HorizonMonths.
func (s *Service) Forecast() (ForecastView, error) {
	o, err := s.Overview(0)
	if err != nil {
		return ForecastView{}, err
	}
	if !o.Forecast.OK {
		return o.Forecast, nil
	}
	if o.Forecast.Flows == nil { // off: still show what each scenario would do
		end := finance.MonthOf(o.Today).Add(HorizonMonths - 1).Last()
		o.Forecast.Flows = s.scenarioFlows(o.Entries, o.Snapshot, o.Cash, o.Forecast, end, HorizonMonths)
	}
	return o.Forecast, nil
}

// forecastFor estimates the spending from entries (which must cover the
// ForecastWindow closed months, plus cardLag for card purchases).
func (s *Service) forecastFor(entries []finance.Entry, mode Scenario) ForecastView {
	fb, ok := finance.Baselines(entries, s.Today(), ForecastWindow)
	return ForecastView{Mode: mode, Base: fb, OK: ok}
}

// flowEntries is what a cash flow sums: entries, the spending estimate of
// sc and then the cash's yield (which grows on the balance after the estimate).
func (s *Service) flowEntries(es []finance.Entry, snap finance.Snapshot, c CashSetup, f ForecastView, sc Scenario, end time.Time) []finance.Entry {
	if f.OK && sc != ScenarioOff {
		if fs := finance.ForecastEntries(es, f.Amount(sc), s.Today(), end); len(fs) > 0 {
			es = append(append([]finance.Entry(nil), es...), fs...)
			finance.SortEntries(es)
		}
	}
	return s.addYield(es, snap, c, end)
}

// scenarioFlows projects the cash for n months under every scenario.
func (s *Service) scenarioFlows(es []finance.Entry, snap finance.Snapshot, c CashSetup, f ForecastView, end time.Time, n int) map[Scenario]finance.CashFlow {
	out := map[Scenario]finance.CashFlow{}
	for _, sc := range Scenarios {
		out[sc] = finance.ProjectCash(s.flowEntries(es, snap, c, f, sc, end), snap, s.Today(), n)
	}
	return out
}
