package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"controle-gastos/internal/db"
	"controle-gastos/internal/finance"
)

// FXSource fetches published exchange rates (reais per unit) for the
// business days in [from, to].
type FXSource interface {
	Quotes(ctx context.Context, currency string, from, to time.Time) ([]db.FXQuote, error)
}

// fxLookback is how far back the rate of a date may come from: the last
// business day before a weekend or a run of holidays.
const fxLookback = 10

// Rate is the rate used to convert currency on date: the PTAX of that day or,
// on a weekend, holiday or before the day's closing bulletin, of the last
// business day before it. Future dates use the latest rate. BRL is 1.
func (s *Service) Rate(currency string, date time.Time) (db.FXQuote, error) {
	c, ok := finance.CurrencyOf(currency)
	if !ok {
		return db.FXQuote{}, invalid("moeda %q não suportada (use %s)", currency, currencyCodes())
	}
	if c.Code == finance.BRLCode {
		return db.FXQuote{Currency: c.Code, Date: date, Rate: 1}, nil
	}
	d := date
	if today := s.Today(); d.IsZero() || d.After(today) {
		d = today
	}
	oldest := d.AddDate(0, 0, -fxLookback)
	q, ok, err := s.st.FXRateOn(c.Code, d, oldest)
	if err != nil || (ok && q.Date.Equal(d)) {
		return q, err
	}
	if s.fx != nil {
		if qs, ferr := s.fx.Quotes(s.st.Context(), c.Code, oldest, d); ferr == nil && len(qs) > 0 {
			if err := s.st.SaveFXRates(qs); err != nil {
				return q, err
			}
			if q, ok, err = s.st.FXRateOn(c.Code, d, oldest); err != nil {
				return q, err
			}
		}
	}
	if !ok {
		return q, invalid("cotação de %s em %s indisponível no Banco Central; informe a cotação usada", c.Code, d.Format("02/01/2006"))
	}
	return q, nil
}

func currencyCodes() string {
	codes := make([]string, len(finance.Currencies))
	for i, c := range finance.Currencies {
		codes[i] = c.Code
	}
	return strings.Join(codes, ", ")
}

// ParseCurrency is strict; "" is BRL.
func ParseCurrency(v string) (finance.Currency, error) {
	c, ok := finance.CurrencyOf(v)
	if !ok {
		return c, invalid("moeda %q não suportada (use %s)", v, currencyCodes())
	}
	return c, nil
}

// ---------- display currency ----------

const displayCurrencySetting = "display_currency"

// Display is the currency totals are shown in and the rate that converts
// reais to it (the latest PTAX). Rate is 1 for BRL.
type Display struct {
	finance.Currency
	Rate float64
	AsOf time.Time // date of the rate (zero for BRL)
	// Unavailable: the chosen currency has no rate yet, so values stay in reais.
	Unavailable bool
}

// IsBRL reports whether values are shown in reais.
func (d Display) IsBRL() bool { return d.Code == finance.BRLCode }

// Convert turns reais (cents) into the display currency (cents).
func (d Display) Convert(cents int64) int64 {
	if d.IsBRL() {
		return cents
	}
	return finance.FromBRL(cents, d.Rate)
}

// Format converts and formats reais (cents) in the display currency.
func (d Display) Format(cents int64) string { return finance.FormatMoney(d.Convert(cents), d.Code) }

// DisplayCurrency is the ledger's chosen display currency (default BRL).
func (s *Service) DisplayCurrency() finance.Currency {
	c, ok := finance.CurrencyOf(s.st.Setting(displayCurrencySetting))
	if !ok {
		c, _ = finance.CurrencyOf(finance.BRLCode)
	}
	return c
}

// Display returns the display currency with today's rate. Without a rate it
// falls back to reais and says so.
func (s *Service) Display() Display {
	c := s.DisplayCurrency()
	if c.Code == finance.BRLCode {
		return Display{Currency: c, Rate: 1}
	}
	q, err := s.Rate(c.Code, s.Today())
	if err != nil {
		brl, _ := finance.CurrencyOf(finance.BRLCode)
		return Display{Currency: brl, Rate: 1, Unavailable: true}
	}
	return Display{Currency: c, Rate: q.Rate, AsOf: q.Date}
}

// SetDisplayCurrency chooses the currency the ledger's values are shown in.
// Amounts stay stored in reais; only how they are shown changes.
func (s *Service) SetDisplayCurrency(code string) (Display, error) {
	c, err := ParseCurrency(code)
	if err != nil {
		return Display{}, err
	}
	if c.Code != finance.BRLCode {
		if _, err := s.Rate(c.Code, s.Today()); err != nil {
			return Display{}, err
		}
	}
	v := c.Code
	if v == finance.BRLCode {
		v = ""
	}
	if err := s.st.SetSetting(displayCurrencySetting, v); err != nil {
		return Display{}, err
	}
	return s.Display(), nil
}

// ---------- PTAX from the Banco Central ----------

// ptaxURL is the BCB Olinda PTAX service (cotações por moeda e período).
var ptaxURL = "https://olinda.bcb.gov.br/olinda/servico/PTAX/versao/v1/odata/CotacaoMoedaPeriodo(moeda=@moeda,dataInicial=@dataInicial,dataFinalCotacao=@dataFinalCotacao)"

// PTAX fetches the PTAX (venda, boletim de fechamento) from the Banco
// Central. A failed or empty answer for the same request is not retried for
// an hour, so pages keep working (on the stored rates) while the BCB is down.
type PTAX struct {
	Client *http.Client
	mu     sync.Mutex
	tried  map[string]time.Time
}

func NewPTAX() *PTAX { return &PTAX{Client: &http.Client{Timeout: 8 * time.Second}} }

func (p *PTAX) Quotes(ctx context.Context, currency string, from, to time.Time) ([]db.FXQuote, error) {
	key := currency + "|" + to.Format("2006-01-02")
	p.mu.Lock()
	if p.tried == nil {
		p.tried = map[string]time.Time{}
	}
	if t, ok := p.tried[key]; ok && time.Since(t) < time.Hour {
		p.mu.Unlock()
		return nil, fmt.Errorf("PTAX %s: tentado há pouco", currency)
	}
	p.tried[key] = time.Now()
	p.mu.Unlock()
	qs, err := p.fetch(ctx, currency, from, to)
	if err == nil && len(qs) > 0 && qs[len(qs)-1].Date.Equal(to) {
		p.mu.Lock()
		delete(p.tried, key) // complete: later lookups hit the database anyway
		p.mu.Unlock()
	}
	return qs, err
}

func (p *PTAX) fetch(ctx context.Context, currency string, from, to time.Time) ([]db.FXQuote, error) {
	q := url.Values{}
	q.Set("@moeda", "'"+currency+"'")
	q.Set("@dataInicial", "'"+from.Format("01-02-2006")+"'")
	q.Set("@dataFinalCotacao", "'"+to.Format("01-02-2006")+"'")
	q.Set("$format", "json")
	q.Set("$select", "cotacaoVenda,dataHoraCotacao,tipoBoletim")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ptaxURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	res, err := p.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("PTAX %s: HTTP %d", currency, res.StatusCode)
	}
	var body struct {
		Value []struct {
			CotacaoVenda    float64 `json:"cotacaoVenda"`
			DataHoraCotacao string  `json:"dataHoraCotacao"`
			TipoBoletim     string  `json:"tipoBoletim"`
		} `json:"value"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, res.Body, 1<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("PTAX %s: resposta inválida: %v", currency, err)
	}
	var out []db.FXQuote
	for _, v := range body.Value {
		if v.TipoBoletim != "Fechamento" || v.CotacaoVenda <= 0 || len(v.DataHoraCotacao) < 10 {
			continue
		}
		d, err := time.Parse("2006-01-02", v.DataHoraCotacao[:10])
		if err != nil {
			continue
		}
		out = append(out, db.FXQuote{Currency: currency, Date: d, Rate: v.CotacaoVenda})
	}
	return out, nil
}
