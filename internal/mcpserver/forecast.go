package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"controle-gastos/internal/app"
)

// forecastBrief says whether the spending forecast is on and its average.
type forecastBrief struct {
	Enabled   bool      `json:"enabled" jsonschema:"previsão de gastos ligada (a projeção de caixa inclui a média)"`
	Available bool      `json:"available" jsonschema:"há histórico suficiente (3+ meses com gastos) para calcular a média"`
	Average   *moneyOut `json:"monthly_average,omitempty" jsonschema:"média mensal do gasto do dia a dia nos últimos meses fechados"`
}

func briefOf(f app.ForecastView) forecastBrief {
	b := forecastBrief{Enabled: f.On, Available: f.OK}
	if f.OK {
		b.Average = ptr(money(f.Base.Average))
	}
	return b
}

type sampleOut struct {
	Month  string   `json:"month"`
	Amount moneyOut `json:"amount" jsonschema:"gasto do dia a dia nesse mês"`
}

type forecastOut struct {
	forecastBrief
	Samples       []sampleOut `json:"months" jsonschema:"meses fechados usados na média e o gasto do dia a dia de cada um"`
	Min           *moneyOut   `json:"lowest_month,omitempty"`
	Max           *moneyOut   `json:"highest_month,omitempty"`
	End           *moneyOut   `json:"cash_at_end,omitempty" jsonschema:"caixa projetado no fim do horizonte mantendo a média"`
	EndMonth      string      `json:"end_month,omitempty"`
	Lowest        *moneyOut   `json:"lowest_cash,omitempty" jsonschema:"menor saldo projetado mantendo a média"`
	LowestDate    string      `json:"lowest_cash_date,omitempty"`
	FirstNegative string      `json:"first_negative_date,omitempty"`
}

func (t *tools) getForecast(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, forecastOut, error) {
	f, err := t.svcFor(ctx).Forecast()
	if err != nil {
		return nil, forecastOut{}, toolErr(err)
	}
	out := forecastOut{forecastBrief: briefOf(f), Samples: []sampleOut{}}
	for i, m := range f.Base.Months {
		out.Samples = append(out.Samples, sampleOut{Month: m.String(), Amount: money(f.Base.Samples[i])})
	}
	if !f.OK {
		return nil, out, nil
	}
	cf := f.Flow
	out.Min, out.Max = ptr(money(f.Base.Min)), ptr(money(f.Base.Max))
	out.Lowest, out.LowestDate, out.FirstNegative = ptr(money(cf.Low)), iso(cf.LowDate), iso(cf.FirstNegative)
	if n := len(cf.Months); n > 0 {
		out.End, out.EndMonth = ptr(money(cf.Months[n-1].Closing)), cf.Months[n-1].Month.String()
	}
	return nil, out, nil
}

type forecastIn struct {
	Mode string `json:"mode" jsonschema:"ligado ou desligado"`
}

func (t *tools) setForecast(ctx context.Context, _ *mcp.CallToolRequest, in forecastIn) (*mcp.CallToolResult, msgOut, error) {
	on, err := app.ParseForecast(in.Mode)
	if err != nil {
		return nil, msgOut{}, toolErr(err)
	}
	if err := t.svcFor(ctx).SetForecast(on); err != nil {
		return nil, msgOut{}, toolErr(err)
	}
	if !on {
		return nil, msgOut{Message: "previsão de gastos desligada"}, nil
	}
	return nil, msgOut{Message: "previsão de gastos ligada: a projeção de caixa inclui a média mensal do gasto do dia a dia"}, nil
}
