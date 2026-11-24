package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"controle-gastos/internal/db"
	"controle-gastos/internal/finance"
)

// fakeFX serves fixed rates and counts the calls.
type fakeFX struct {
	rates map[string]float64 // "USD 2026-11-13" -> rate
	calls int
}

func (f *fakeFX) Quotes(_ context.Context, cur string, from, to time.Time) ([]db.FXQuote, error) {
	f.calls++
	var out []db.FXQuote
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		if r, ok := f.rates[cur+" "+d.Format("2006-01-02")]; ok {
			out = append(out, db.FXQuote{Currency: cur, Date: d, Rate: r})
		}
	}
	return out, nil
}

func TestForeignCurrencyTransactions(t *testing.T) {
	s := newSvc(t) // today 2026-11-16 (Monday)
	cat := CategoryRef{Name: "Viagem", Create: true}
	in := TxIn{Kind: finance.Expense, Date: d("2026-11-15"), Amount: 10000, Currency: "usd", Category: cat, Payment: finance.Debit}

	// no stored rate and nowhere to fetch it: ask for the rate
	_, err := s.AddTx(in)
	mustInvalid(t, err, "informe a cotação")
	_, err = s.AddTx(TxIn{Kind: finance.Expense, Date: d("2026-11-15"), Amount: 100, Currency: "XYZ", Category: cat})
	mustInvalid(t, err, "não suportada")
	bad := in
	bad.FXRate = -1
	_, err = s.AddTx(bad)
	mustInvalid(t, err, "cotação inválida")

	// Sunday: the PTAX of Friday (last business day) is used
	fx := &fakeFX{rates: map[string]float64{"USD 2026-11-13": 5.5, "USD 2026-11-16": 5.6, "EUR 2026-11-16": 6.2}}
	s.SetFX(fx)
	id, err := s.AddTx(in)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := s.Tx(id)
	if e.Currency != "USD" || e.OrigAmount != 10000 || e.FXRate != 5.5 || e.Amount != 55000 {
		t.Fatalf("converted: %+v", e)
	}
	// a stored rate for the exact day is reused without fetching again
	calls := fx.calls
	if q, err := s.Rate("USD", d("2026-11-13")); err != nil || q.Rate != 5.5 || fx.calls != calls {
		t.Fatalf("cached: %+v %v calls %d→%d", q, err, calls, fx.calls)
	}

	// a typed rate wins; reais ignore it
	typed := in
	typed.FXRate = 5.8
	id2, _ := s.AddTx(typed)
	if e, _ := s.Tx(id2); e.Amount != 58000 || e.FXRate != 5.8 {
		t.Fatalf("typed: %+v", e)
	}
	brl := TxIn{Kind: finance.Expense, Date: d("2026-11-15"), Amount: 1234, Currency: "BRL", FXRate: 9, Category: cat}
	id3, _ := s.AddTx(brl)
	if e, _ := s.Tx(id3); e.Foreign() || e.Amount != 1234 {
		t.Fatalf("brl: %+v", e)
	}

	// update: same currency and date keep the typed rate; a new date takes that day's PTAX
	upd := TxIn{Date: d("2026-11-15"), Amount: 20000, Currency: "USD", Category: CategoryRef{ID: e.CategoryID}}
	e2, err := s.UpdateTx(id2, upd)
	if err != nil || e2.FXRate != 5.8 || e2.Amount != 116000 {
		t.Fatalf("update keeps rate: %+v %v", e2, err)
	}
	upd.Date = d("2026-11-16")
	if e2, _ = s.UpdateTx(id2, upd); e2.FXRate != 5.6 || e2.Amount != 112000 {
		t.Fatalf("update new date: %+v", e2)
	}
	// back to reais clears the currency
	upd.Currency = ""
	if e2, _ = s.UpdateTx(id2, upd); e2.Foreign() || e2.Amount != 20000 {
		t.Fatalf("back to BRL: %+v", e2)
	}

	// installments split the amount as entered, each parcel converted
	parc := TxIn{Kind: finance.Expense, Date: d("2026-11-16"), Amount: 10001, Currency: "EUR", Installments: 2, Category: cat, Payment: finance.Debit}
	ids, err := s.AddTxs([]TxIn{parc})
	if err != nil || len(ids) != 2 {
		t.Fatalf("parcels: %v %v", ids, err)
	}
	p1, _ := s.Tx(ids[0])
	p2, _ := s.Tx(ids[1])
	if p1.OrigAmount != 5001 || p2.OrigAmount != 5000 || p1.Amount != 31006 || p2.Amount != 31000 || p2.Currency != "EUR" {
		t.Fatalf("parcels: %+v / %+v", p1, p2)
	}

	// recurrences stay in reais
	_, err = s.RecurrenceFromTx(in, RecIn{Interval: 1, EndMode: finance.EndNever})
	mustInvalid(t, err, "só em reais")
}

func TestDisplayCurrency(t *testing.T) {
	s := newSvc(t)
	if d := s.Display(); !d.IsBRL() || d.Format(123456) != "R$ 1.234,56" {
		t.Fatalf("default: %+v", d)
	}
	_, err := s.SetDisplayCurrency("USD")
	mustInvalid(t, err, "indisponível")
	_, err = s.SetDisplayCurrency("ABC")
	mustInvalid(t, err, "não suportada")
	s.SetFX(&fakeFX{rates: map[string]float64{"USD 2026-11-13": 5}})
	disp, err := s.SetDisplayCurrency("usd")
	if err != nil || disp.Code != "USD" || disp.Rate != 5 || !disp.AsOf.Equal(d("2026-11-13")) {
		t.Fatalf("usd: %+v %v", disp, err)
	}
	if got := disp.Format(123456); got != "US$ 246,91" {
		t.Fatalf("format: %s", got)
	}
	if s.DisplayCurrency().Code != "USD" || s.Display().Code != "USD" {
		t.Fatal("not saved")
	}
	if disp, _ = s.SetDisplayCurrency("BRL"); !disp.IsBRL() || s.Store().Setting(displayCurrencySetting) != "" {
		t.Fatalf("back to BRL: %+v", disp)
	}
}

func TestPTAXFetch(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Write([]byte(`{"value":[
			{"cotacaoVenda":5.69,"dataHoraCotacao":"2026-11-13 10:02:21.8","tipoBoletim":"Abertura"},
			{"cotacaoVenda":5.7324,"dataHoraCotacao":"2026-11-13 13:06:32.5","tipoBoletim":"Fechamento"},
			{"cotacaoVenda":5.78,"dataHoraCotacao":"2026-11-16 10:11:13.3","tipoBoletim":"Abertura"}]}`))
	}))
	defer srv.Close()
	old := ptaxURL
	ptaxURL = srv.URL
	defer func() { ptaxURL = old }()
	p := NewPTAX()
	qs, err := p.Quotes(context.Background(), "USD", d("2026-11-06"), d("2026-11-16"))
	if err != nil || len(qs) != 1 || qs[0].Rate != 5.7324 || !qs[0].Date.Equal(d("2026-11-13")) {
		t.Fatalf("quotes: %+v %v", qs, err)
	}
	if !strings.Contains(query, "USD") || !strings.Contains(query, "11-16-2026") {
		t.Fatalf("query: %s", query)
	}
	// incomplete (no closing bulletin for the last day yet): not retried for a while
	if _, err := p.Quotes(context.Background(), "USD", d("2026-11-06"), d("2026-11-16")); err == nil {
		t.Fatal("retried at once")
	}
}
