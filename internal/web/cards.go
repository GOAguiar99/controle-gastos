package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"controle-gastos/internal/app"
	"controle-gastos/internal/finance"
)

// ---------- cartões ----------

type cardView struct {
	Card       finance.CreditCard
	Open       app.StatementView
	Statements []finance.Statement
	Editing    bool
}

// cardHistory is a card's fatura totals month by month, for the chart.
type cardHistory struct {
	ID     int64     `json:"id"`
	Labels []string  `json:"labels"`
	Values []float64 `json:"values"` // reais
	Future []bool    `json:"future"` // fatura not closed yet (only parcels and recurrences so far)
}

// historyOf lays a card's faturas out on consecutive months: the last 12 up
// to the open one, plus future ones that already have parcels (up to 6).
func historyOf(c finance.CreditCard, open finance.Month, sts []finance.Statement) cardHistory {
	totals := map[finance.Month]int64{}
	last := open
	for _, st := range sts {
		totals[st.Month] = st.Total()
		if st.Month > last && st.Total() > 0 {
			last = st.Month
		}
	}
	last = min(last, open.Add(6))
	h := cardHistory{ID: c.ID}
	for m := open.Add(-11); m <= last; m++ {
		h.Labels = append(h.Labels, m.Label())
		h.Values = append(h.Values, float64(totals[m])/100)
		h.Future = append(h.Future, m >= open)
	}
	return h
}

// cardsOff sends card pages to the preference that turns the module on.
func (s *Server) cardsOff(w http.ResponseWriter, r *http.Request) bool {
	if svcOf(r).CardsEnabled() {
		return false
	}
	http.Redirect(w, r, "/perfil/preferencias?msg="+url.QueryEscape("O módulo de cartões está desativado; ative-o aqui")+"#cartoes", http.StatusSeeOther)
	return true
}

func (s *Server) cardsPage(w http.ResponseWriter, r *http.Request) {
	if s.cardsOff(w, r) {
		return
	}
	svc := svcOf(r)
	cards, err := svc.Cards()
	if err != nil {
		s.fail(w, err)
		return
	}
	editID := formID(r, "editar")
	views := make([]cardView, 0, len(cards))
	histories := make([]cardHistory, 0, len(cards))
	var openTotal int64
	for _, c := range cards {
		v := cardView{Card: c, Editing: c.ID == editID}
		if v.Open, err = svc.Statement(c.ID, 0); err != nil {
			s.fail(w, err)
			return
		}
		if v.Statements, err = svc.CardStatements(c.ID, 12); err != nil {
			s.fail(w, err)
			return
		}
		histories = append(histories, historyOf(c, v.Open.Month, v.Statements))
		if c.Active {
			openTotal += v.Open.Total()
		}
		views = append(views, v)
	}
	s.render(w, r, "cards", page{"Title": "Cartões", "Cards": views, "OpenTotal": openTotal, "History": histories})
}

func cardFromForm(r *http.Request) app.CardIn {
	return app.CardIn{Name: r.FormValue("name"), ClosingDay: formInt(r, "closing_day", 0), DueDay: formInt(r, "due_day", 0),
		Active: r.FormValue("active") == "on"}
}

func (s *Server) saveCard(w http.ResponseWriter, r *http.Request) {
	in := cardFromForm(r)
	if r.PathValue("id") != "" {
		id, err := pathID(r)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		in.ID = id
	}
	if _, err := svcOf(r).SaveCard(in); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/cartoes?msg="+url.QueryEscape("Cartão "+strings.TrimSpace(in.Name)+" salvo"), http.StatusSeeOther)
}

func (s *Server) deleteCard(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	c, err := svcOf(r).DeleteCard(id)
	if err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/cartoes?msg="+url.QueryEscape("Cartão "+c.Name+" excluído"), http.StatusSeeOther)
}

// ---------- fatura ----------

func statementPath(cardID int64, m finance.Month) string {
	return fmt.Sprintf("/cartoes/%d/fatura?mes=%s", cardID, m)
}

func (s *Server) statementMonth(r *http.Request) finance.Month {
	if m, err := finance.ParseMonth(r.FormValue("mes")); err == nil {
		return m
	}
	return 0
}

// statementPage shows a fatura. Importing it is done by the assistant over MCP
// (import_statement), which reads the bill and categorizes each purchase far
// better than a paste-and-pick form; the page shows the result.
func (s *Server) statementPage(w http.ResponseWriter, r *http.Request) {
	if s.cardsOff(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	svc := svcOf(r)
	v, err := svc.Statement(id, s.statementMonth(r))
	if err != nil {
		if app.IsValidation(err) {
			http.NotFound(w, r)
			return
		}
		s.fail(w, err)
		return
	}
	cats, err := svc.Categories(finance.Expense)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, r, "statement", page{
		"Categories": cats,
		"Title":      "Fatura " + v.Card.Name + " · " + v.Month.Label(), "V": v,
		"Prev": v.Month.Add(-1), "Next": v.Month.Add(1), "Back": statementPath(id, v.Month),
	})
}

func (s *Server) setStatementTotal(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	m := s.statementMonth(r)
	var total int64
	if v := strings.TrimSpace(r.FormValue("total")); v != "" {
		if total, err = finance.ParseMoney(v); err != nil {
			badRequest(w, "valor inválido")
			return
		}
	}
	if _, err := svcOf(r).SetStatementTotal(id, m, total); err != nil {
		s.fail(w, err)
		return
	}
	msg := "Valor da fatura salvo"
	if total == 0 {
		msg = "Valor da fatura removido: vale a soma dos itens"
	}
	http.Redirect(w, r, statementPath(id, m)+"&msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

func (s *Server) deleteInstallments(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	n, err := svcOf(r).DeleteInstallmentsFrom(id)
	if err != nil {
		s.fail(w, err)
		return
	}
	to := r.FormValue("back")
	if !strings.HasPrefix(to, "/") || strings.HasPrefix(to, "//") {
		to = "/cartoes"
	}
	sep := "?"
	if strings.Contains(to, "?") {
		sep = "&"
	}
	http.Redirect(w, r, to+sep+"msg="+url.QueryEscape(fmt.Sprintf("%d parcela(s) excluída(s)", n)), http.StatusSeeOther)
}

// recategorize files a purchase under another category from the fatura page
// and remembers it for the merchant's other and future purchases.
func (s *Server) recategorize(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ref := app.CategoryRef{ID: formID(r, "category_id")}
	if name := strings.TrimSpace(r.FormValue("new_category")); name != "" {
		ref = app.CategoryRef{Name: name, Create: true}
	}
	res, err := svcOf(r).Recategorize(id, ref, r.FormValue("remember") != "off")
	if err != nil {
		s.fail(w, err)
		return
	}
	msg := res.Merchant + " → " + res.Category
	if res.Others > 0 {
		msg += fmt.Sprintf("; %d outra(s) compra(s) iguais também mudaram", res.Others)
	}
	if res.Remembered {
		msg += ". As próximas compras desse estabelecimento já vêm nessa categoria"
	}
	to := localBack(r.FormValue("back"), "/cartoes")
	sep := "?"
	if strings.Contains(to, "?") {
		sep = "&"
	}
	http.Redirect(w, r, to+sep+"msg="+url.QueryEscape(msg)+"#compra-"+fmt.Sprint(id), http.StatusSeeOther)
}
