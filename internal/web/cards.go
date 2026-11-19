package web

import (
	"fmt"
	"io"
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

func (s *Server) cardsPage(w http.ResponseWriter, r *http.Request) {
	svc := svcOf(r)
	cards, err := svc.Cards()
	if err != nil {
		s.fail(w, err)
		return
	}
	editID := formID(r, "editar")
	views := make([]cardView, 0, len(cards))
	var openTotal int64
	for _, c := range cards {
		v := cardView{Card: c, Editing: c.ID == editID}
		if v.Open, err = svc.Statement(c.ID, 0); err != nil {
			s.fail(w, err)
			return
		}
		if v.Statements, err = svc.CardStatements(c.ID, 6); err != nil {
			s.fail(w, err)
			return
		}
		if c.Active {
			openTotal += v.Open.Total()
		}
		views = append(views, v)
	}
	s.render(w, r, "cards", page{"Title": "Cartões", "Cards": views, "OpenTotal": openTotal})
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

func (s *Server) statementPage(w http.ResponseWriter, r *http.Request) {
	s.renderStatement(w, r, nil, "", "")
}

// renderStatement shows a fatura; with preview, also the import review, and
// errMsg above it when confirming failed.
func (s *Server) renderStatement(w http.ResponseWriter, r *http.Request, preview *app.ImportPreview, text, errMsg string) {
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
		"Title": "Fatura " + v.Card.Name + " · " + v.Month.Label(), "V": v, "Categories": cats,
		"Prev": v.Month.Add(-1), "Next": v.Month.Add(1), "Preview": preview, "Text": text,
		"Back": statementPath(id, v.Month), "Error": errMsg,
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

// maxStatementFile bounds an uploaded statement (the request body is already
// limited to maxBodyBytes).
const maxStatementFile = 512 << 10

func (s *Server) previewImport(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	text := r.FormValue("text")
	if f, _, err := r.FormFile("file"); err == nil {
		b, err := io.ReadAll(io.LimitReader(f, maxStatementFile+1))
		f.Close()
		if err != nil || len(b) > maxStatementFile {
			badRequest(w, "arquivo grande demais (máximo 512 KB)")
			return
		}
		text = strings.TrimSpace(text + "\n" + string(b))
	}
	p, err := svcOf(r).PreviewStatement(id, s.statementMonth(r), text)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.renderStatement(w, r, &p, text, "")
}

func (s *Server) confirmImport(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	m := s.statementMonth(r)
	n := formInt(r, "rows", 0)
	// the review as submitted, to show it again if something is wrong
	review := app.ImportPreview{Month: m}
	var items []app.ImportItem
	var lines []int // review line of each item
	problem := ""
	for i := 0; i < n && i < 2000; i++ {
		k := func(f string) string { return fmt.Sprintf("%s_%d", f, i) }
		use := r.FormValue(k("use")) == "on"
		row := app.ImportRow{Status: app.ImportNew, CategoryID: formID(r, k("cat"))}
		if !use {
			row.Status = app.ImportIgnore
		}
		row.Description, row.InstallmentN, row.Installments = r.FormValue(k("desc")), formInt(r, k("n"), 0), formInt(r, k("of"), 0)
		row.Date, err = finance.ParseDate(r.FormValue(k("date")))
		if err != nil && use && problem == "" {
			problem = fmt.Sprintf("linha %d: data inválida", i+1)
		}
		row.Amount, err = finance.ParseMoney(r.FormValue(k("amount")))
		if err != nil && use && problem == "" {
			problem = fmt.Sprintf("linha %d: valor inválido", i+1)
		}
		review.Rows = append(review.Rows, row)
		if !use {
			continue
		}
		review.NewCount++
		review.New += row.Amount
		it := app.ImportItem{Date: row.Date, Description: row.Description, Amount: row.Amount,
			InstallmentN: row.InstallmentN, Installments: row.Installments, Category: app.CategoryRef{ID: row.CategoryID}}
		if name := strings.TrimSpace(r.FormValue(k("newcat"))); name != "" {
			it.Category = app.CategoryRef{Name: name, Create: true}
		} else if row.CategoryID == 0 && problem == "" {
			problem = fmt.Sprintf("linha %d (%s): escolha uma categoria", i+1, row.Description)
		}
		items = append(items, it)
		lines = append(lines, i+1)
	}
	if len(items) == 0 && problem == "" {
		problem = "nenhuma linha marcada para importar"
	}
	var ids []int64
	if problem == "" {
		ids, err = svcOf(r).ImportStatement(id, m, items, app.SourceImport)
		if err != nil && !app.IsValidation(err) {
			s.fail(w, err)
			return
		}
		if err != nil {
			problem = itemToLine(err.Error(), lines)
		}
	}
	if problem != "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.renderStatement(w, r, &review, "", "Nada foi importado: "+problem)
		return
	}
	msg := fmt.Sprintf("%d compra(s) importada(s)", len(items))
	if extra := len(ids) - len(items); extra > 0 {
		msg += fmt.Sprintf(", mais %d parcela(s) futura(s) nas próximas faturas", extra)
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

// itemToLine turns the service's "item N: ..." (N counts only the checked
// rows) into the line number of the review.
func itemToLine(msg string, lines []int) string {
	var n int
	if _, err := fmt.Sscanf(msg, "item %d:", &n); err == nil && n >= 1 && n <= len(lines) {
		return fmt.Sprintf("linha %d:%s", lines[n-1], strings.SplitN(msg, ":", 2)[1])
	}
	return msg
}
