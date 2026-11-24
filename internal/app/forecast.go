package app

import (
	"strings"
	"time"

	"controle-gastos/internal/finance"
)

// ForecastWindow is how many closed months the average samples.
const ForecastWindow = HistoryMonths

// forecastSetting stores whether the spending forecast is on: "media" (or a
// scenario name saved by the first version) = on, "" = off.
const forecastSetting = "forecast"

// forecastOnValue is what SetForecast saves when turning it on.
const forecastOnValue = "media"

// ParseForecast reads an on/off answer for the spending forecast.
func ParseForecast(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "ligado", "ligada", "ligar", "on", "true", "sim", "media", "média",
		"otimista", "base", "pessimista": // nomes de cenário da primeira versão
		return true, nil
	case "", "desligado", "desligada", "desligar", "off", "false", "nao", "não", "nenhum":
		return false, nil
	}
	return false, invalid("valor inválido %q para a previsão de gastos: use ligado ou desligado", s)
}

// ForecastView is the average monthly day-to-day spending and how the cash
// looks if it stays that way.
type ForecastView struct {
	On   bool // saved preference
	Base finance.ForecastBase
	OK   bool // enough history to estimate (finance.MinForecastMonths)
	// Flow is the projected cash keeping the average (filled when OK, even
	// when off, so the UI can tell what turning it on would show).
	Flow finance.CashFlow
}

// Active reports whether the cash flow includes the estimate.
func (f ForecastView) Active() bool { return f.On && f.OK }

// Average is the monthly estimate (0 without enough history).
func (f ForecastView) Average() int64 {
	if !f.OK {
		return 0
	}
	return f.Base.Average
}

// ForecastOn reports whether the spending forecast is on.
func (s *Service) ForecastOn() bool {
	on, _ := ParseForecast(s.st.Setting(forecastSetting))
	return on
}

// SetForecast turns the spending forecast on or off.
func (s *Service) SetForecast(on bool) error {
	v := ""
	if on {
		v = forecastOnValue
	}
	return s.st.SetSetting(forecastSetting, v)
}

// Forecast returns the average and the cash projected keeping it for the
// next HorizonMonths.
func (s *Service) Forecast() (ForecastView, error) {
	o, err := s.Overview(0)
	if err != nil {
		return ForecastView{}, err
	}
	return o.Forecast, nil
}

// forecastFor estimates the spending from entries (which must cover the
// ForecastWindow closed months, plus cardLag for card purchases).
func (s *Service) forecastFor(entries []finance.Entry, on bool) ForecastView {
	fb, ok := finance.Baselines(entries, s.Today(), ForecastWindow)
	return ForecastView{On: on, Base: fb, OK: ok}
}

// flowEntries is what a cash flow sums: entries, the spending estimate
// (when withForecast) and then the cash's yield, which grows on the
// balance after the estimate.
func (s *Service) flowEntries(es []finance.Entry, snap finance.Snapshot, c CashSetup, f ForecastView, withForecast bool, end time.Time) []finance.Entry {
	if withForecast && f.OK {
		if fs := finance.ForecastEntries(es, f.Base.Average, s.Today(), end); len(fs) > 0 {
			es = append(append([]finance.Entry(nil), es...), fs...)
			finance.SortEntries(es)
		}
	}
	return s.addYield(es, snap, c, end)
}
