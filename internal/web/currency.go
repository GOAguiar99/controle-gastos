package web

import (
	"html/template"
	"math"
	"net/http"
	"strconv"
	"strings"

	"controle-gastos/internal/app"
	"controle-gastos/internal/finance"
)

// parseFXRate reads a typed exchange rate ("5,4321" or "5.4321"); "" = 0 (PTAX).
func parseFXRate(v string) (float64, error) {
	v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "R$"))
	if v == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(strings.Replace(v, ",", ".", 1), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, &app.ValidationError{Msg: "cotação inválida (reais por unidade da moeda, ex. 5,43)"}
	}
	return f, nil
}

// withDisplay returns the page template t, or for another display currency a
// clone of its unexecuted original base where brl formats amounts in that
// currency (converted at the latest PTAX).
func withDisplay(t, base *template.Template, d app.Display) (*template.Template, error) {
	if d.IsBRL() {
		return t, nil
	}
	c, err := base.Clone()
	if err != nil {
		return nil, err
	}
	return c.Funcs(template.FuncMap{
		"brl":     func(cents int64) template.HTML { return moneySpan(d.Format(cents)) },
		"brlText": d.Format,
	}), nil
}

// origMoney prints the amount as entered: "US$ 100,00 a R$ 5,4321" (the
// amount masked by "Ocultar valores" like the others).
func origMoney(e finance.Entry) template.HTML {
	if !e.Foreign() {
		return ""
	}
	return moneySpan(finance.FormatMoney(e.OrigAmount, e.Currency)) + template.HTML(" a "+template.HTMLEscapeString(rateText(e.FXRate)))
}

func rateText(f float64) string {
	return "R$ " + strings.Replace(strconv.FormatFloat(f, 'f', -1, 64), ".", ",", 1)
}

func (s *Server) saveDisplayCurrency(w http.ResponseWriter, r *http.Request) {
	d, err := svcOf(r).SetDisplayCurrency(r.FormValue("currency"))
	if err != nil {
		s.fail(w, err)
		return
	}
	msg := "Valores em reais"
	if !d.IsBRL() {
		msg = "Valores em " + d.Name + " (PTAX de " + d.AsOf.Format("02/01") + ": " + rateText(d.Rate) + ")"
	}
	profileDone(w, r, tabPrefs, msg)
}
