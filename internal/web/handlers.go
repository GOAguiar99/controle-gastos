package web

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"controle-gastos/internal/app"
	"controle-gastos/internal/db"
	"controle-gastos/internal/finance"
)

// shortMonth is "nov/26": short enough for chart axes, unambiguous across years.
func shortMonth(m finance.Month) string {
	l := m.Label()
	return l[:len(l)-4] + l[len(l)-2:]
}

// ---------- dashboard ----------

type catTotal struct {
	Name   string
	Bucket finance.Bucket // group holding most of the category's money (bar color)
	Total  int64
	Count  int
	groups map[finance.Bucket]int64
}

func byCategory(es []finance.Entry) []catTotal {
	idx := map[string]int{}
	var out []catTotal
	for _, e := range es {
		i, ok := idx[e.Category]
		if !ok {
			i = len(out)
			idx[e.Category] = i
			out = append(out, catTotal{Name: e.Category, groups: map[finance.Bucket]int64{}})
		}
		out[i].Total += e.Amount
		out[i].Count++
		out[i].groups[e.Group()] += e.Amount
	}
	for i := range out {
		for _, b := range []finance.Bucket{finance.Card, finance.Fixed, finance.Other} {
			if out[i].Bucket == "" || out[i].groups[b] > out[i].groups[out[i].Bucket] {
				out[i].Bucket = b
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Total > out[j].Total })
	return out
}

// payTotal is the month's spending paid one way (Análise tab).
type payTotal struct {
	Method finance.PaymentMethod
	Total  int64
	Pct    float64
}

func byPayment(es []finance.Entry, total int64) []payTotal {
	sums := map[finance.PaymentMethod]int64{}
	for _, e := range es {
		sums[e.Payment] += e.Amount
	}
	var out []payTotal
	for _, p := range finance.PaymentMethods {
		if sums[p] > 0 {
			out = append(out, payTotal{Method: p, Total: sums[p], Pct: ratioOf(sums[p], total)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Total > out[j].Total })
	return out
}

func filter(es []finance.Entry, keep func(finance.Entry) bool) []finance.Entry {
	var out []finance.Entry
	for _, e := range es {
		if keep(e) {
			out = append(out, e)
		}
	}
	return out
}

func reais(c int64) float64 { return float64(c) / 100 }

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	sel := s.monthParam(r)
	o, err := svcOf(r).Overview(sel)
	if err != nil {
		s.fail(w, err)
		return
	}
	_, hasData, _ := svcOf(r).Store().FirstTxDate()
	today, cur := o.Today, finance.MonthOf(o.Today)
	selSum, lim, flow, flowRows := o.Summary, o.Limits, o.Flow, o.FlowRows
	var pastRows, nextRows []finance.FlowMonth
	for _, fm := range flowRows {
		if fm.Past {
			pastRows = append(pastRows, fm)
		} else {
			nextRows = append(nextRows, fm)
		}
	}

	monthEntries := filter(o.Entries, func(e finance.Entry) bool { return e.Month() == sel })
	expCats := byCategory(filter(monthEntries, func(e finance.Entry) bool { return e.Kind == finance.Expense }))
	var projectedCount int
	for _, e := range monthEntries {
		if e.Projected {
			projectedCount++
		}
	}

	type chartData struct {
		FlowX []string  `json:"flowX"` // daily projected balance
		FlowY []float64 `json:"flowY"`
		// per month, history + committed future (Cartão / Fixos / Outros vs entradas)
		ComX     []string  `json:"comX"`
		ComPast  []bool    `json:"comPast"`
		ComIn    []float64 `json:"comIn"`
		ComCard  []float64 `json:"comCard"`
		ComFixed []float64 `json:"comFixed"`
		ComOther []float64 `json:"comOther"`
		// previsão de gastos ligada: the balance under every scenario on FlowX
		ComForecast []float64            `json:"comForecast,omitempty"`
		FlowScen    map[string][]float64 `json:"flowScen,omitempty"`
		Scenario    string               `json:"scenario,omitempty"`
	}
	var cd chartData
	if o.Forecast.Active() {
		cd.FlowX, cd.FlowScen = scenarioSeries(o.Forecast.Flows)
		cd.FlowY, cd.Scenario = cd.FlowScen[string(o.Forecast.Mode)], string(o.Forecast.Mode)
	} else {
		for _, p := range flow.Points {
			cd.FlowX = append(cd.FlowX, p.Date.Format("2006-01-02"))
			cd.FlowY = append(cd.FlowY, reais(p.Balance))
		}
	}
	for _, fm := range flowRows {
		cd.ComX = append(cd.ComX, shortMonth(fm.Month))
		cd.ComPast = append(cd.ComPast, fm.Past)
		cd.ComIn = append(cd.ComIn, reais(fm.In))
		cd.ComCard = append(cd.ComCard, reais(fm.Card))
		cd.ComFixed = append(cd.ComFixed, reais(fm.Fixed))
		cd.ComOther = append(cd.ComOther, reais(fm.Other))
		if o.Forecast.Active() {
			cd.ComForecast = append(cd.ComForecast, reais(fm.Forecast))
		}
	}

	s.render(w, r, "dashboard", page{
		"Title": "Dashboard", "Month": sel, "Prev": sel.Add(-1), "Next": sel.Add(1), "Current": cur, "Welcome": !hasData,
		"Today": today, "Sum": selSum, "Limits": lim, "Snap": o.Snapshot, "Flow": flow, "Cash": o.Cash,
		"FlowRows": flowRows, "PastTot": finance.SumFlow(pastRows), "NextTot": finance.SumFlow(nextRows),
		"PastN": len(pastRows), "NextN": len(nextRows),
		"ExpCats": expCats, "ProjectedCount": projectedCount, "Chart": cd,
		"Fc": o.Forecast, "Scenarios": app.Scenarios,
		"CardPct": barPct(selSum.Card, lim.CardLimit), "CardRatio": ratioOf(selSum.Card, lim.CardLimit),
	})
}

// scenarioSeries puts the projected balance of every scenario on one axis:
// the union of their dates, each balance holding until its next change.
func scenarioSeries(flows map[app.Scenario]finance.CashFlow) ([]string, map[string][]float64) {
	seen := map[time.Time]bool{}
	var days []time.Time
	for _, cf := range flows {
		for _, p := range cf.Points {
			if !seen[p.Date] {
				seen[p.Date] = true
				days = append(days, p.Date)
			}
		}
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
	xs := make([]string, len(days))
	for i, d := range days {
		xs[i] = d.Format("2006-01-02")
	}
	ys := map[string][]float64{}
	for sc, cf := range flows {
		k, bal := 0, int64(0)
		for _, d := range days {
			for k < len(cf.Points) && !cf.Points[k].Date.After(d) {
				bal = cf.Points[k].Balance
				k++
			}
			ys[string(sc)] = append(ys[string(sc)], reais(bal))
		}
	}
	return xs, ys
}

// saveForecast turns the spending estimate on with a scenario, or off.
func (s *Server) saveForecast(w http.ResponseWriter, r *http.Request) {
	sc, err := app.ParseScenario(r.FormValue("mode"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := svcOf(r).SetForecast(sc); err != nil {
		s.fail(w, err)
		return
	}
	msg := "Previsão de gastos desativada"
	if sc != app.ScenarioOff {
		msg = "Previsão de gastos: cenário " + sc.Label()
	}
	to := "/?msg=" + url.QueryEscape(msg)
	if m, err := finance.ParseMonth(r.FormValue("mes")); err == nil {
		to = "/?mes=" + m.String() + "&msg=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, to+"#fluxo", http.StatusSeeOther)
}

func ratioOf(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func (s *Server) saveCash(w http.ResponseWriter, r *http.Request) {
	date, _ := finance.ParseDate(r.FormValue("date")) // zero = today
	if err := r.ParseForm(); err != nil {
		badRequest(w, "formulário inválido")
		return
	}
	// with cash accounts the form sends one balance per account (acct_<id>)
	balances := map[int64]int64{}
	for k, vs := range r.PostForm {
		id, ok := strings.CutPrefix(k, "acct_")
		if !ok || len(vs) == 0 || strings.TrimSpace(vs[0]) == "" {
			continue
		}
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			continue
		}
		v, err := finance.ParseMoney(vs[0])
		if err != nil {
			badRequest(w, "valor inválido")
			return
		}
		balances[n] = v
	}
	if len(balances) > 0 {
		if _, err := svcOf(r).SetCashBalances(date, balances, r.FormValue("note")); err != nil {
			s.fail(w, err)
			return
		}
	} else {
		amount, err := finance.ParseMoney(r.FormValue("amount"))
		if err != nil {
			badRequest(w, err.Error())
			return
		}
		if _, err := svcOf(r).SetCash(date, amount, r.FormValue("note")); err != nil {
			s.fail(w, err)
			return
		}
	}
	http.Redirect(w, r, "/?msg="+url.QueryEscape("Caixa atualizado"), http.StatusSeeOther)
}

func (s *Server) saveCashAccount(w http.ResponseWriter, r *http.Request) {
	pct, err := strconv.ParseFloat(strings.Replace(strings.TrimSpace(r.FormValue("cdi_pct")), ",", ".", 1), 64)
	if err != nil && strings.TrimSpace(r.FormValue("cdi_pct")) != "" {
		badRequest(w, "percentual do CDI inválido")
		return
	}
	in := app.CashAccountIn{Name: r.FormValue("name"), CDIPct: pct}
	if r.PathValue("id") != "" {
		if in.ID, err = pathID(r); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	if _, err := svcOf(r).SaveCashAccount(in); err != nil {
		s.fail(w, err)
		return
	}
	profileDone(w, r, tabPrefs, "Conta "+strings.TrimSpace(in.Name)+" salva")
}

func (s *Server) deleteCashAccount(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a, err := svcOf(r).DeleteCashAccount(id)
	if err != nil {
		s.fail(w, err)
		return
	}
	profileDone(w, r, tabPrefs, "Conta "+a.Name+" excluída")
}

// ---------- receitas / gastos ----------

func kindPath(k finance.Kind) string {
	if k == finance.Income {
		return "/receitas"
	}
	return "/gastos"
}

func (s *Server) list(kind finance.Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		today := s.today()
		mes := s.monthParam(r)
		entries, skipped, err := svcOf(r).Store().Entries(mes.Add(-1), mes)
		if err != nil {
			s.fail(w, err)
			return
		}
		ofKind := func(e finance.Entry) bool { return e.Kind == kind }
		cur := filter(entries, func(e finance.Entry) bool { return ofKind(e) && e.Month() == mes })
		prev := filter(entries, func(e finance.Entry) bool { return ofKind(e) && e.Month() == mes.Add(-1) })
		skipped = filter(skipped, func(e finance.Entry) bool { return ofKind(e) && e.Month() == mes })

		var total, prevTotal, projected int64
		buckets := map[finance.Bucket]int64{}
		for _, e := range cur {
			total += e.Amount
			buckets[e.Group()] += e.Amount
			if e.Projected {
				projected += e.Amount
			}
		}
		type bucketRow struct {
			Key   finance.Bucket
			Label string
			Total int64
			Pct   float64
		}
		var split []bucketRow
		for _, b := range []struct {
			k finance.Bucket
			l string
		}{{finance.Card, "Cartão de crédito"}, {finance.Fixed, "Fixos"}, {finance.Other, "Outros"}} {
			split = append(split, bucketRow{Key: b.k, Label: b.l, Total: buckets[b.k], Pct: ratioOf(buckets[b.k], total)})
		}
		for _, e := range prev {
			prevTotal += e.Amount
		}
		cats, err := svcOf(r).Store().Categories(kind)
		if err != nil {
			s.fail(w, err)
			return
		}
		var debts []db.Debt
		if kind == finance.Expense {
			if debts, err = svcOf(r).Store().Debts(today); err != nil {
				s.fail(w, err)
				return
			}
		}
		defDate := mes.First()
		if finance.MonthOf(today) == mes {
			defDate = today
		}
		title := "Gastos"
		if kind == finance.Income {
			title = "Receitas"
		}
		// the list opens on the entries; the charts live on the "Análise" tab
		analysis := r.URL.Query().Get("ver") == "analise"
		var chart app.CategoryChart
		if analysis {
			if chart, err = svcOf(r).CategoryReport(kind, mes); err != nil {
				s.fail(w, err)
				return
			}
		}

		byCat := byCategory(cur)
		var maxCat int64
		if len(byCat) > 0 {
			maxCat = byCat[0].Total
		}
		s.render(w, r, "list", page{
			"Title": title, "Kind": string(kind), "IsIncome": kind == finance.Income, "Path": kindPath(kind),
			"Month": mes, "Prev": mes.Add(-1), "Next": mes.Add(1), "Today": today,
			"Entries": cur, "Skipped": skipped, "Total": total, "PrevTotal": prevTotal, "Diff": total - prevTotal,
			"Projected": projected, "Real": total - projected, "ByCat": byCat, "MaxCat": maxCat, "Split": split,
			"ByPayment": byPayment(cur, total), "Rows": listRows(cur),
			"Categories": cats, "Debts": debts, "DefaultDate": defDate, "Cards": activeCards(r),
			"NewRec": finance.Recurrence{Interval: 1, EndMode: finance.EndNever},
			"Back":   kindPath(kind) + "?mes=" + mes.String(),
			"Chart":  chart, "Analysis": analysis,
		})
	}
}

// categoryFromForm reads the selected category, or "new_category" (created if missing).
func categoryFromForm(r *http.Request) app.CategoryRef {
	if name := strings.TrimSpace(r.FormValue("new_category")); name != "" {
		return app.CategoryRef{Name: name, Create: true}
	}
	return app.CategoryRef{ID: formID(r, "category_id")}
}

// txFromForm reads the fields shared by the create and edit forms.
func txFromForm(r *http.Request, kind finance.Kind) (app.TxIn, error) {
	in := app.TxIn{Kind: kind, Category: categoryFromForm(r), Description: r.FormValue("description"), DebtID: formID(r, "debt_id")}
	var err error
	if in.Payment, err = app.ParsePaymentMethod(r.FormValue("payment_method")); err != nil {
		return in, err
	}
	in.CardID = formID(r, "card_id")
	in.Installments = formInt(r, "installments", 0)
	if in.Date, err = finance.ParseDate(r.FormValue("date")); err != nil {
		return in, fmt.Errorf("data inválida")
	}
	if in.Amount, err = finance.ParseMoney(r.FormValue("amount")); err != nil {
		return in, fmt.Errorf("valor inválido")
	}
	return in, nil
}

func (s *Server) createTx(w http.ResponseWriter, r *http.Request) {
	kind, err := app.ParseKind(r.FormValue("kind"))
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	in, err := txFromForm(r, kind)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	dest := kindPath(kind) + "?mes=" + finance.MonthOf(in.Date).String()
	if r.FormValue("repeat") == "on" {
		sched, err := recurrenceFromForm(r)
		if err != nil {
			badRequest(w, err.Error())
			return
		}
		if _, err := svcOf(r).RecurrenceFromTx(in, sched); err != nil {
			s.fail(w, err)
			return
		}
		http.Redirect(w, r, dest+"&msg="+url.QueryEscape("Recorrência criada — já aparece nos meses seguintes"), http.StatusSeeOther)
		return
	}
	if _, err := svcOf(r).AddTx(in); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, dest+"&msg="+url.QueryEscape("Lançamento salvo"), http.StatusSeeOther)
}

func (s *Server) editTxPage(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	tx, err := svcOf(r).Store().Tx(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	cats, err := svcOf(r).Store().Categories(tx.Kind)
	if err != nil {
		s.fail(w, err)
		return
	}
	debts, _ := svcOf(r).Store().Debts(s.today())
	var rec *finance.Recurrence
	if tx.RecurrenceID != 0 {
		if rr, err := svcOf(r).Store().Recurrence(tx.RecurrenceID); err == nil {
			rec = &rr
		}
	}
	s.render(w, r, "edit_tx", page{
		"Title": "Editar lançamento", "Tx": tx, "Categories": cats, "Debts": debts, "Rec": rec, "Cards": activeCards(r),
		"IsIncome": tx.Kind == finance.Income,
		"Back":     localBack(r.URL.Query().Get("back"), kindPath(tx.Kind)+"?mes="+finance.MonthOf(tx.Date).String()),
	})
}

func (s *Server) updateTx(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	in, err := txFromForm(r, "")
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	tx, err := svcOf(r).UpdateTx(id, in)
	if err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, kindPath(tx.Kind)+"?mes="+finance.MonthOf(tx.Date).String()+"&msg="+url.QueryEscape("Lançamento atualizado"), http.StatusSeeOther)
}

func (s *Server) deleteTx(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := svcOf(r).DeleteTx(id); err != nil {
		s.fail(w, err)
		return
	}
	back(w, r, "/gastos")
}

func occurrenceFromForm(r *http.Request) (int64, finance.Month) {
	m, _ := finance.ParseMonth(r.FormValue("month")) // zero → validation error in the service
	return formID(r, "rec_id"), m
}

func (s *Server) confirmOccurrence(w http.ResponseWriter, r *http.Request) {
	id, m := occurrenceFromForm(r)
	var amount int64
	if v := r.FormValue("amount"); v != "" {
		var err error
		if amount, err = finance.ParseMoney(v); err != nil || amount <= 0 {
			badRequest(w, "valor inválido")
			return
		}
	}
	date, _ := finance.ParseDate(r.FormValue("date")) // zero = the recurrence's day
	if _, err := svcOf(r).ConfirmOccurrence(id, m, amount, date, app.SourceManual); err != nil {
		s.fail(w, err)
		return
	}
	rec, _ := svcOf(r).Recurrence(id)
	back(w, r, kindPath(rec.Kind))
}

func (s *Server) skipOccurrence(w http.ResponseWriter, r *http.Request) {
	rec, err := svcOf(r).SkipOccurrence(occurrenceFromForm(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	back(w, r, kindPath(rec.Kind))
}

func (s *Server) unskipOccurrence(w http.ResponseWriter, r *http.Request) {
	rec, err := svcOf(r).UnskipOccurrence(occurrenceFromForm(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	back(w, r, kindPath(rec.Kind))
}

// ---------- recorrentes ----------

// recurrenceFromForm reads the periodicity / duration fields; the service validates them.
func recurrenceFromForm(r *http.Request) (app.RecIn, error) {
	rec := app.RecIn{Interval: formInt(r, "interval", 1), EndMode: r.FormValue("end_mode")}
	switch rec.EndMode {
	case finance.EndCount:
		rec.Count = formInt(r, "count", 0)
	case finance.EndUntil:
		end, err := finance.ParseMonth(r.FormValue("end"))
		if err != nil {
			return rec, fmt.Errorf("mês final inválido")
		}
		rec.End = end
	default:
		rec.EndMode = finance.EndNever
	}
	return rec, nil
}

type recRow struct {
	Rec     finance.Recurrence
	Next    []finance.Month
	Ended   bool
	ThisMon bool
	Editing bool
}

func (s *Server) recurrencesPage(w http.ResponseWriter, r *http.Request) {
	kind := finance.Expense
	if r.URL.Query().Get("tipo") == string(finance.Income) {
		kind = finance.Income
	}
	today := s.today()
	cur := finance.MonthOf(today)
	recs, err := svcOf(r).Store().Recurrences()
	if err != nil {
		s.fail(w, err)
		return
	}
	editID := formID(r, "editar")
	form := finance.Recurrence{Kind: kind, Interval: 1, Start: cur, Day: today.Day(), EndMode: finance.EndNever, Active: true, Payment: finance.OtherPay}
	var rows []recRow
	var thisMonth, monthlyEq int64
	for _, rc := range recs {
		if rc.Kind != kind || rc.Pending {
			continue
		}
		last, bounded := rc.LastMonth()
		row := recRow{Rec: rc, Next: rc.Next(cur, 3), Ended: bounded && last < cur, Editing: rc.ID == editID}
		row.ThisMon = len(rc.Months(cur, cur)) > 0
		if row.ThisMon {
			thisMonth += rc.Amount
		}
		if rc.Active && !row.Ended {
			monthlyEq += rc.MonthlyEquivalent()
		}
		if rc.ID == editID {
			form = rc
		}
		rows = append(rows, row)
	}
	cats, err := svcOf(r).Store().Categories(kind)
	if err != nil {
		s.fail(w, err)
		return
	}
	debts, err := svcOf(r).Store().Debts(today)
	if err != nil {
		s.fail(w, err)
		return
	}
	debtNames := map[int64]string{}
	for _, d := range debts {
		debtNames[d.ID] = d.Name
	}
	suggestions, err := svcOf(r).Suggestions()
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, r, "recurrences", page{
		"Suggestions": suggestions,
		"Title":       "Recorrentes", "Kind": string(kind), "IsIncome": kind == finance.Income,
		"Rows": rows, "ThisMonth": thisMonth, "MonthlyEq": monthlyEq, "Current": cur,
		"Form": form, "Editing": editID != 0 && form.ID == editID, "Categories": cats,
		"Debts": debts, "DebtNames": debtNames, "Cards": activeCards(r),
	})
}

func (s *Server) saveRecurrence(w http.ResponseWriter, r *http.Request) {
	kind, err := app.ParseKind(r.FormValue("kind"))
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	in, err := recurrenceFromForm(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	if r.PathValue("id") != "" {
		if in.ID, err = pathID(r); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	in.Kind, in.Category = kind, categoryFromForm(r)
	in.Description = r.FormValue("description")
	if in.Amount, err = finance.ParseMoney(r.FormValue("amount")); err != nil {
		badRequest(w, "valor inválido")
		return
	}
	in.Day = formInt(r, "day", 1)
	if in.Start, err = finance.ParseMonth(r.FormValue("start")); err != nil {
		badRequest(w, "mês inicial inválido")
		return
	}
	in.Active = in.ID == 0 || r.FormValue("active") == "on"
	if kind == finance.Expense {
		in.DebtID = formID(r, "debt_id")
		if in.Payment, err = app.ParsePaymentMethod(r.FormValue("payment_method")); err != nil {
			s.fail(w, err)
			return
		}
		in.CardID = formID(r, "card_id")
	}
	if _, err := svcOf(r).SaveRecurrence(in); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/recorrentes?tipo="+string(kind)+"&msg="+url.QueryEscape("Recorrência salva — meses atualizados"), http.StatusSeeOther)
}

func (s *Server) acceptRecurrence(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rec, err := svcOf(r).AcceptRecurrence(id)
	if err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/recorrentes?tipo="+string(rec.Kind)+"&msg="+url.QueryEscape(rec.Description+" adicionado aos gastos recorrentes — as parcelas já aparecem como previstas"), http.StatusSeeOther)
}

func (s *Server) dismissRecurrence(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rec, err := svcOf(r).DismissRecurrence(id)
	if err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/recorrentes?tipo="+string(rec.Kind)+"&msg="+url.QueryEscape("Sugestão descartada"), http.StatusSeeOther)
}

func (s *Server) deleteRecurrence(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rec, err := svcOf(r).DeleteRecurrence(id)
	if err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/recorrentes?tipo="+string(rec.Kind)+"&msg="+url.QueryEscape("Recorrência excluída"), http.StatusSeeOther)
}

// ---------- dívidas ----------

// debtView adds the upcoming installments of a loan for the template.
type debtView struct {
	db.Debt
	Upcoming []finance.Installment
	Yearly   []yearPoint
}

type yearPoint struct {
	Year    int
	Balance int64
	BarPct  int
}

func (s *Server) debtsPage(w http.ResponseWriter, r *http.Request) {
	today := s.today()
	debts, err := svcOf(r).Store().Debts(today)
	if err != nil {
		s.fail(w, err)
		return
	}
	all, err := svcOf(r).Store().AllTransactions()
	if err != nil {
		s.fail(w, err)
		return
	}
	payments := map[int64][]finance.Entry{}
	for _, e := range all {
		if e.DebtID != 0 {
			payments[e.DebtID] = append(payments[e.DebtID], e)
		}
	}
	var totalInitial, totalCurrent, totalInterestLeft int64
	views := make([]debtView, 0, len(debts))
	for _, d := range debts {
		totalInitial += d.Initial
		totalCurrent += d.Current()
		v := debtView{Debt: d}
		if d.HasInterest() {
			totalInterestLeft += d.Status.InterestLeft
			sched := d.Loan.Schedule()
			for i := d.Status.Paid; i < len(sched) && len(v.Upcoming) < 12; i++ {
				v.Upcoming = append(v.Upcoming, sched[i])
			}
			// balance at the end of each year, for the long-term view
			for _, in := range sched {
				if in.Due.Month() == time.December || in.N == len(sched) {
					v.Yearly = append(v.Yearly, yearPoint{Year: in.Due.Year(), Balance: in.Balance, BarPct: barPct(in.Balance, d.Initial)})
				}
			}
		}
		views = append(views, v)
	}
	s.render(w, r, "debts", page{
		"Title": "Dívidas", "Debts": views, "Payments": payments, "Today": today,
		"TotalInitial": totalInitial, "TotalCurrent": totalCurrent, "TotalPaidOff": totalInitial - totalCurrent,
		"TotalInterestLeft": totalInterestLeft, "EditID": formID(r, "editar"),
	})
}

// parseRate accepts "0,9631393" or "0.9631393" (% ao mês).
func parseRate(v string) (float64, error) {
	v = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "%"))
	v = strings.Replace(v, ",", ".", 1)
	return strconv.ParseFloat(v, 64)
}

// debtFromForm parses the debt form; ranges are validated by the service.
func debtFromForm(r *http.Request) (db.DebtInput, error) {
	in := db.DebtInput{Name: r.FormValue("name"), System: r.FormValue("system")}
	var err error
	if in.Initial, err = finance.ParseMoney(r.FormValue("initial")); err != nil {
		return in, fmt.Errorf("nome e valor inicial são obrigatórios")
	}
	if in.System != finance.LoanSAC && in.System != finance.LoanPrice {
		return in, nil
	}
	if in.MonthlyRate, err = parseRate(r.FormValue("rate")); err != nil {
		return in, fmt.Errorf("taxa mensal inválida")
	}
	in.Term = formInt(r, "term", 0)
	if in.FirstDue, err = finance.ParseDate(r.FormValue("first_due")); err != nil {
		return in, fmt.Errorf("data da 1ª parcela inválida")
	}
	if v := r.FormValue("fees"); v != "" {
		if in.Fees, err = finance.ParseMoney(v); err != nil {
			return in, fmt.Errorf("seguros/taxas inválidos")
		}
	}
	in.AnchorN = formInt(r, "anchor_n", 0)
	if in.AnchorN > 0 {
		if in.AnchorBalance, err = finance.ParseMoney(r.FormValue("anchor_balance")); err != nil {
			return in, fmt.Errorf("saldo devedor informado inválido")
		}
	}
	return in, nil
}

func (s *Server) saveDebt(w http.ResponseWriter, r *http.Request) {
	in, err := debtFromForm(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	var id int64
	if r.PathValue("id") != "" {
		if id, err = pathID(r); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	res, err := svcOf(r).SaveDebt(id, in)
	if err != nil {
		s.fail(w, err)
		return
	}
	if res.SuggestedID != 0 {
		http.Redirect(w, r, "/recorrentes?msg="+url.QueryEscape("Dívida salva. Confira as parcelas sugeridas abaixo e adicione-as aos gastos recorrentes.")+"#sugestoes", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/dividas?msg="+url.QueryEscape("Dívida salva"), http.StatusSeeOther)
}

func (s *Server) deleteDebt(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := svcOf(r).DeleteDebt(id); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/dividas", http.StatusSeeOther)
}

// ---------- profile (Config) ----------

// Profile tabs: Conta, Preferências, Agentes de IA, Compartilhar.
const (
	tabAccount = "conta"
	tabPrefs   = "preferencias"
	tabAgents  = "agentes"
	tabShare   = "compartilhar"
)

// profileURL is where each tab lives.
func profileURL(tab string) string {
	if tab == tabAccount {
		return "/perfil"
	}
	return "/perfil/" + tab
}

// profileDone redirects back to a profile tab with a message.
func profileDone(w http.ResponseWriter, r *http.Request, tab, msg string) {
	http.Redirect(w, r, profileURL(tab)+"?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

func (s *Server) profilePage(tab string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if tab == tabAgents && s.mcp == nil {
			http.NotFound(w, r)
			return
		}
		data, err := s.profileData(r, tab)
		if err != nil {
			s.fail(w, err)
			return
		}
		s.render(w, r, "profile", data)
	}
}

// profileData loads only what the tab shows.
func (s *Server) profileData(r *http.Request, tab string) (page, error) {
	st := svcOf(r).Store()
	data := page{"Title": "Perfil", "Tab": tab, "MCPEnabled": s.mcp != nil}
	switch tab {
	case tabAccount:
		snap, _ := st.LatestSnapshot()
		data["Snap"] = snap
	case tabPrefs:
		cats, err := st.Categories("")
		if err != nil {
			return nil, err
		}
		var inc, exp []db.Category
		for _, c := range cats {
			if c.Kind == finance.Income {
				inc = append(inc, c)
			} else {
				exp = append(exp, c)
			}
		}
		data["Limits"], data["IncomeCats"], data["ExpenseCats"] = st.Limits(), inc, exp
		rules, err := st.CategoryRules()
		if err != nil {
			return nil, err
		}
		data["Rules"] = rules
		cash, err := svcOf(r).Cash()
		if err != nil {
			return nil, err
		}
		data["Cash"] = cash
	case tabAgents:
		tokens, err := s.app.MCPTokens(r.Context(), actorOf(r))
		if err != nil {
			return nil, err
		}
		data["MCPTokens"] = tokens
	case tabShare:
		sh, err := s.app.Sharing(r.Context(), actorOf(r))
		if err != nil {
			return nil, err
		}
		data["Sharing"] = sh
	}
	return data, nil
}

func (s *Server) saveLimits(w http.ResponseWriter, r *http.Request) {
	card, err1 := finance.ParseMoney(r.FormValue("card_limit"))
	minS, err2 := finance.ParseMoney(r.FormValue("min_savings"))
	if r.FormValue("min_savings") == "" {
		minS, err2 = 0, nil
	}
	if err1 != nil || err2 != nil {
		badRequest(w, "valores inválidos")
		return
	}
	if err := svcOf(r).SetLimits(finance.Limits{CardLimit: card, MinSavings: minS}); err != nil {
		s.fail(w, err)
		return
	}
	profileDone(w, r, tabPrefs, "Limites salvos")
}

func (s *Server) addCategory(w http.ResponseWriter, r *http.Request) {
	kind, _ := app.ParseKind(r.FormValue("kind"))
	if _, err := svcOf(r).AddCategory(r.FormValue("name"), kind); err != nil {
		s.fail(w, err)
		return
	}
	profileDone(w, r, tabPrefs, "Categoria adicionada")
}

func (s *Server) exportCSV(w http.ResponseWriter, r *http.Request) {
	all, err := svcOf(r).Store().AllTransactions()
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="lancamentos.csv"`)
	w.Write([]byte("\xEF\xBB\xBF")) // BOM so Excel reads UTF-8
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	cw.Write([]string{"data", "tipo", "categoria", "valor", "descricao", "recorrente", "lancado_por"})
	for _, e := range all {
		rec := ""
		if e.RecurrenceID != 0 {
			rec = "sim"
		}
		cw.Write([]string{e.Date.Format("2006-01-02"), string(e.Kind), csvText(e.Category), moneyInput(e.Amount), csvText(e.Description), rec, csvText(e.CreatedBy)})
	}
	cw.Flush()
	Audit("data_export", "user_id", actorOf(r).User.ID, "format", "csv")
}

// csvText neutralizes spreadsheet formulas (CSV injection): descriptions may
// come from pasted bank statements or from Claude, and Excel/Sheets execute
// cells starting with = + - @ (or a tab/CR). A leading ' makes them text.
func csvText(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}

// exportJSON downloads everything stored about the user (data portability).
func (s *Server) exportJSON(w http.ResponseWriter, r *http.Request) {
	data, err := svcOf(r).ExportData(actorOf(r).User)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="controle-gastos-dados.json"`)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(data); err != nil {
		logErr(err)
	}
	Audit("data_export", "user_id", actorOf(r).User.ID, "format", "json")
}

// deleteAccount erases the user's account and data, then signs them out.
// Web only on purpose: an MCP link is a bearer URL that an LLM uses, and
// irreversible account deletion must be a deliberate, confirmed human action.
func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	if err := s.app.DeleteAccount(r.Context(), actorOf(r), r.FormValue("confirm")); err != nil {
		s.fail(w, err)
		return
	}
	s.setCookie(w, s.sessionCookie(), "", -1)
	http.Redirect(w, r, "/login?msg="+url.QueryEscape("Sua conta e seus dados foram excluídos."), http.StatusSeeOther)
}

// legalPage serves the public pages (privacy, terms, guide). They need no
// session, but show the app's navigation to someone signed in.
func (s *Server) legalPage(name, title string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svcOf(r) == nil {
			if a, ok := s.currentActor(r); ok {
				r = s.withActor(r, a)
				w.Header().Set("Cache-Control", "no-store") // the nav shows who is signed in
			}
		}
		s.render(w, r, name, page{"Title": title})
	}
}

// ---------- sharing a ledger ----------

func (s *Server) sharingDone(w http.ResponseWriter, r *http.Request, err error, msg, to string) {
	if err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, to+"?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

func (s *Server) inviteMember(w http.ResponseWriter, r *http.Request) {
	err := s.app.InviteToLedger(r.Context(), actorOf(r), r.FormValue("email"))
	s.sharingDone(w, r, err, "Convite criado. Peça para a pessoa entrar no app com esse e-mail em até 7 dias.", profileURL(tabShare))
}

func (s *Server) cancelInvite(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.sharingDone(w, r, s.app.CancelInvite(r.Context(), actorOf(r), id), "Convite cancelado.", profileURL(tabShare))
}

func (s *Server) removeMember(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.sharingDone(w, r, s.app.RemoveMember(r.Context(), actorOf(r), id), "Pessoa removida da carteira.", profileURL(tabShare))
}

func (s *Server) leaveLedger(w http.ResponseWriter, r *http.Request) {
	if err := s.app.LeaveLedger(r.Context(), actorOf(r)); err != nil {
		s.fail(w, err)
		return
	}
	s.setCookie(w, s.sessionCookie(), "", -1)
	http.Redirect(w, r, "/login?msg="+url.QueryEscape("Você saiu da carteira compartilhada. Entre de novo para usar a sua."), http.StatusSeeOther)
}

func (s *Server) acceptInvite(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.sharingDone(w, r, s.app.AcceptInvite(r.Context(), actorOf(r), id), "Pronto: você agora usa a carteira compartilhada.", "/")
}

func (s *Server) declineInvite(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.sharingDone(w, r, s.app.DeclineInvite(r.Context(), actorOf(r), id), "Convite recusado.", profileURL(tabShare))
}

// activeCards lists the cards offered in expense forms.
func activeCards(r *http.Request) []finance.CreditCard {
	cards, _ := svcOf(r).Cards()
	out := cards[:0:0]
	for _, c := range cards {
		if c.Active {
			out = append(out, c)
		}
	}
	return out
}

// localBack is to when it is a path on this site, otherwise fallback.
func localBack(to, fallback string) string {
	if !strings.HasPrefix(to, "/") || strings.HasPrefix(to, "//") || strings.Contains(to, "\\") {
		return fallback
	}
	return to
}

func (s *Server) saveCardsModule(w http.ResponseWriter, r *http.Request) {
	on := r.FormValue("enabled") == "on"
	if err := svcOf(r).SetCardsEnabled(on); err != nil {
		s.fail(w, err)
		return
	}
	msg := "Módulo de cartões desativado"
	if on {
		msg = "Módulo de cartões ativado: cadastre seus cartões em Cartões"
	}
	profileDone(w, r, tabPrefs, msg)
}

// faturaRow stands for every card purchase of one fatura in the expense list.
type faturaRow struct {
	CardID    int64
	Card      string
	Statement finance.Month
	Due       time.Time
	Total     int64
	Count     int
	Projected bool // includes card recurrences not confirmed yet
}

// listRow is one line of the entries list: an entry, or a whole fatura.
type listRow struct {
	Entry  *finance.Entry
	Fatura *faturaRow
}

// listRows folds card purchases into one row per fatura (the detail lives on
// the fatura page) and keeps everything else as it is, ordered by date.
func listRows(es []finance.Entry) []listRow {
	type key struct {
		card int64
		m    finance.Month
	}
	faturas := map[key]*faturaRow{}
	var rows []listRow
	for i := range es {
		e := &es[i]
		if e.CardID == 0 {
			rows = append(rows, listRow{Entry: e})
			continue
		}
		k := key{e.CardID, e.Statement}
		f, ok := faturas[k]
		if !ok {
			f = &faturaRow{CardID: e.CardID, Card: e.Card, Statement: e.Statement, Due: e.CashDay()}
			faturas[k] = f
			rows = append(rows, listRow{Fatura: f})
		}
		f.Total += e.Amount
		f.Count++
		f.Projected = f.Projected || e.Projected
	}
	day := func(r listRow) time.Time {
		if r.Fatura != nil {
			return r.Fatura.Due
		}
		return r.Entry.Date
	}
	sort.SliceStable(rows, func(i, j int) bool { return day(rows[i]).Before(day(rows[j])) })
	return rows
}

func (s *Server) forgetCategory(w http.ResponseWriter, r *http.Request) {
	if err := svcOf(r).ForgetCategory(r.FormValue("merchant")); err != nil {
		s.fail(w, err)
		return
	}
	profileDone(w, r, tabPrefs, "Memória apagada: as próximas compras desse estabelecimento voltam a seguir o histórico")
}
