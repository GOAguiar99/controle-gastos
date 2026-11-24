package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"controle-gastos/internal/app"
)

// forecastBrief is the spending estimate in use and its monthly amounts.
type forecastBrief struct {
	Scenario    string    `json:"scenario" jsonschema:"cenário em uso: otimista, base, pessimista ou vazio (desligada)"`
	Available   bool      `json:"available" jsonschema:"há histórico suficiente (3+ meses com gastos) para estimar"`
	Optimistic  *moneyOut `json:"optimistic,omitempty" jsonschema:"gasto variável mensal estimado, P25"`
	Base        *moneyOut `json:"base,omitempty" jsonschema:"mediana"`
	Pessimistic *moneyOut `json:"pessimistic,omitempty" jsonschema:"P75"`
}

func briefOf(f app.ForecastView) forecastBrief {
	b := forecastBrief{Scenario: string(f.Mode), Available: f.OK}
	if f.OK {
		b.Optimistic, b.Base, b.Pessimistic = ptr(money(f.Base.Optimistic)), ptr(money(f.Base.Base)), ptr(money(f.Base.Pessimistic))
	}
	return b
}

type scenarioOut struct {
	Scenario      string   `json:"scenario"`
	Monthly       moneyOut `json:"monthly" jsonschema:"gasto variável mensal estimado neste cenário"`
	Lowest        moneyOut `json:"lowest" jsonschema:"menor saldo projetado nos próximos 12 meses"`
	LowestDate    string   `json:"lowest_date"`
	FirstNegative string   `json:"first_negative_date,omitempty"`
	End           moneyOut `json:"end" jsonschema:"saldo projetado no fim do horizonte"`
}

type forecastOut struct {
	forecastBrief
	Months    []string      `json:"months" jsonschema:"meses (AAAA-MM) usados na estimativa"`
	Samples   []moneyOut    `json:"samples" jsonschema:"gasto variável de cada mês usado, na mesma ordem"`
	Scenarios []scenarioOut `json:"scenarios"`
}

func (t *tools) getForecast(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, forecastOut, error) {
	f, err := t.svcFor(ctx).Forecast()
	if err != nil {
		return nil, forecastOut{}, toolErr(err)
	}
	out := forecastOut{forecastBrief: briefOf(f), Months: []string{}, Samples: []moneyOut{}, Scenarios: []scenarioOut{}}
	for i, m := range f.Base.Months {
		out.Months = append(out.Months, m.String())
		out.Samples = append(out.Samples, money(f.Base.Samples[i]))
	}
	for _, sc := range app.Scenarios {
		cf, ok := f.Flows[sc]
		if !ok {
			continue
		}
		so := scenarioOut{Scenario: string(sc), Monthly: money(f.Amount(sc)), Lowest: money(cf.Low),
			LowestDate: iso(cf.LowDate), FirstNegative: iso(cf.FirstNegative)}
		if n := len(cf.Months); n > 0 {
			so.End = money(cf.Months[n-1].Closing)
		}
		out.Scenarios = append(out.Scenarios, so)
	}
	return nil, out, nil
}

type forecastIn struct {
	Mode string `json:"mode" jsonschema:"otimista, base, pessimista ou desligado"`
}

func (t *tools) setForecast(ctx context.Context, _ *mcp.CallToolRequest, in forecastIn) (*mcp.CallToolResult, msgOut, error) {
	sc, err := app.ParseScenario(in.Mode)
	if err != nil {
		return nil, msgOut{}, toolErr(err)
	}
	if err := t.svcFor(ctx).SetForecast(sc); err != nil {
		return nil, msgOut{}, toolErr(err)
	}
	if sc == app.ScenarioOff {
		return nil, msgOut{Message: "previsão de gastos desligada"}, nil
	}
	return nil, msgOut{Message: "previsão de gastos ligada: cenário " + sc.Label()}, nil
}
