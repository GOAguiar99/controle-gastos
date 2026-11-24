package mcpserver

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"controle-gastos/internal/app"
	"controle-gastos/internal/finance"
)

type currencyIn struct {
	Currency string `json:"currency,omitempty" jsonschema:"moeda (USD, EUR...) para consultar a cotação; vazio = só a moeda de exibição"`
	Date     string `json:"date,omitempty" jsonschema:"data da cotação AAAA-MM-DD; padrão hoje"`
}

type rateOut struct {
	Currency string  `json:"currency"`
	Date     string  `json:"date" jsonschema:"dia da PTAX usada (o último dia útil até a data pedida)"`
	Rate     float64 `json:"rate" jsonschema:"reais por unidade da moeda (PTAX de venda do Banco Central)"`
}

type currencyOut struct {
	Display     string   `json:"display_currency" jsonschema:"moeda em que o app mostra os valores (preferência da carteira; BRL = reais)"`
	DisplayRate *rateOut `json:"display_rate,omitempty" jsonschema:"cotação usada para mostrar os valores, se a moeda de exibição não é BRL"`
	Unavailable bool     `json:"display_unavailable,omitempty" jsonschema:"sem cotação agora: o app mostra em reais"`
	Rate        *rateOut `json:"rate,omitempty" jsonschema:"cotação da moeda pedida na data"`
	Supported   []string `json:"supported" jsonschema:"moedas aceitas em lançamentos e na exibição"`
}

func (t *tools) currencyOut(s *app.Service) currencyOut {
	d := s.Display()
	out := currencyOut{Display: s.DisplayCurrency().Code, Unavailable: d.Unavailable, Supported: []string{}}
	if !d.IsBRL() {
		out.DisplayRate = &rateOut{Currency: d.Code, Date: iso(d.AsOf), Rate: d.Rate}
	}
	for _, c := range finance.Currencies {
		out.Supported = append(out.Supported, c.Code)
	}
	return out
}

func (t *tools) getCurrency(ctx context.Context, _ *mcp.CallToolRequest, in currencyIn) (*mcp.CallToolResult, currencyOut, error) {
	s := t.svcFor(ctx)
	out := t.currencyOut(s)
	if strings.TrimSpace(in.Currency) == "" {
		return nil, out, nil
	}
	day, err := date(in.Date)
	if err != nil {
		return nil, out, err
	}
	if day.IsZero() {
		day = s.Today()
	}
	q, err := s.Rate(in.Currency, day)
	if err != nil {
		return nil, out, toolErr(err)
	}
	out.Rate = &rateOut{Currency: strings.ToUpper(strings.TrimSpace(in.Currency)), Date: iso(q.Date), Rate: q.Rate}
	return nil, out, nil
}

type displayIn struct {
	Currency string `json:"currency" jsonschema:"BRL, USD, EUR... (get_currency lista as aceitas)"`
}

func (t *tools) setDisplayCurrency(ctx context.Context, _ *mcp.CallToolRequest, in displayIn) (*mcp.CallToolResult, currencyOut, error) {
	s := t.svcFor(ctx)
	if _, err := s.SetDisplayCurrency(in.Currency); err != nil {
		return nil, currencyOut{}, toolErr(err)
	}
	return nil, t.currencyOut(s), nil
}
