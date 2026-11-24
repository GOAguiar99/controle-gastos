package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"controle-gastos/internal/db"
)

type fixedFX float64

func (r fixedFX) Quotes(_ context.Context, cur string, from, to time.Time) ([]db.FXQuote, error) {
	return []db.FXQuote{{Currency: cur, Date: to, Rate: float64(r)}}, nil
}

// A purchase in dollars is saved in reais and shows what was entered; the
// display currency converts every amount.
func TestForeignCurrencyWeb(t *testing.T) {
	ts, client, env := newTestServer(t)
	env.app.FX = fixedFX(5)
	env.mustLogin(t, client, anaUser)
	page := string(mustGet(t, client, ts.URL+"/gastos?mes=2026-11"))
	if !strings.Contains(page, `name="currency"`) || !strings.Contains(page, `name="fx_rate"`) {
		t.Fatal("new-expense form has no currency")
	}
	post := func(v url.Values) *http.Response {
		res, err := client.PostForm(ts.URL+"/lancamentos", v)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	base := url.Values{"kind": {"expense"}, "date": {"2026-11-10"}, "amount": {"100,00"}, "new_category": {"Viagem"}, "payment_method": {"debito"}, "currency": {"USD"}}
	if res := post(base); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST USD: %d", res.StatusCode)
	}
	typed := url.Values{}
	for k, v := range base {
		typed[k] = v
	}
	typed.Set("fx_rate", "5,50")
	if res := post(typed); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST typed rate: %d", res.StatusCode)
	}
	typed.Set("fx_rate", "abc")
	if res := post(typed); res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad rate: %d", res.StatusCode)
	}
	list := string(mustGet(t, client, ts.URL+"/gastos?mes=2026-11"))
	for _, want := range []string{"R$ 500,00", "US$ 100,00</span></span> a R$ 5<", "R$ 550,00", "US$ 100,00</span></span> a R$ 5,5<"} {
		if !strings.Contains(list, want) {
			t.Errorf("list misses %q", want)
		}
	}

	// display in dollars
	if res, _ := client.PostForm(ts.URL+"/config/moeda", url.Values{"currency": {"USD"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("display: %d", res.StatusCode)
	}
	list = string(mustGet(t, client, ts.URL+"/gastos?mes=2026-11"))
	for _, want := range []string{"US$ 100,00", `<span class="money"><span>US$ 110,00`, `data-currency="USD"`, "Valores em USD"} {
		if !strings.Contains(list, want) {
			t.Errorf("display list misses %q", want)
		}
	}
	if dash := string(mustGet(t, client, ts.URL+"/")); !strings.Contains(dash, "US$") {
		t.Error("dashboard not in dollars")
	}
	if p := string(mustGet(t, client, ts.URL+"/perfil/preferencias")); !strings.Contains(p, `<option value="USD" selected>`) {
		t.Error("preference not shown")
	}
	if res, _ := client.PostForm(ts.URL+"/config/moeda", url.Values{"currency": {"XXX"}}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad currency: %d", res.StatusCode)
	}
}
