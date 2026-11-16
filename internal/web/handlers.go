package web

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"controle-gastos/internal/db"
	"controle-gastos/internal/finance"
)

const (
	horizonMonths = 12
	dashWindow    = 6 // months of history in the cash-flow table/chart
)

// shortMonth is "nov/26": short enough for chart axes, unambiguous across years.
func shortMonth(m finance.Month) string {
	l := m.Label()
	return l[:len(l)-4] + l[len(l)-2:]
}

// ---------- login ----------

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if !s.auth.Enabled() || s.auth.valid(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, r, "login", page{"Title": "Entrar"})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ok, locked := s.auth.check(r, r.FormValue("password"))
	if !ok {
		msg := "Senha incorreta."
		if locked {
			msg = "Muitas tentativas. Aguarde 15 minutos."
		}
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, r, "login", page{"Title": "Entrar", "Error": msg})
		return
	}
	s.auth.newSession(w, r)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.auth.clearSession(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// ---------- dashboard ----------

type catTotal struct {
	Name   string
	Bucket finance.Bucket
	Total  int64
	Count  int
}

func byCategory(es []finance.Entry) []catTotal {
	idx := map[string]int{}
	var out []catTotal
	for _, e := range es {
		i, ok := idx[e.Category]
		if !ok {
			i = len(out)
			idx[e.Category] = i
			out = append(out, catTotal{Name: e.Category, Bucket: e.Bucket})
		}
		out[i].Total += e.Amount
		out[i].Count++
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Total > out[j].Total })
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
	today := s.today()
	cur := finance.MonthOf(today)
	sel := s.monthParam(r)

	snap, err := s.st.LatestSnapshot()
	if err != nil {
		s.fail(w, err)
		return
	}
	lo, hi := min(sel, cur.Add(-dashWindow)), max(sel, cur.Add(horizonMonths-1))
	if snap.Set {
		lo = min(lo, finance.MonthOf(snap.Date))
	}
	entries, _, err := s.st.Entries(lo, hi)
	if err != nil {
		s.fail(w, err)
		return
	}
	lim := s.st.Limits()
	selSum := finance.Summarize(entries, sel, sel, lim)[0]
	// The future only counts what is launched (recurrences, loan parcels and
	// future-dated expenses): what's left is what can still be spent.
	flow := finance.ProjectCash(entries, snap, today, horizonMonths)
	// Month-by-month table: 6 months of history + current + next 11 months.
	flowRows := finance.MonthlyFlow(entries, snap, today, cur.Add(-dashWindow), cur.Add(horizonMonths-1))
	var pastRows, nextRows []finance.FlowMonth
	for _, fm := range flowRows {
		if fm.Past {
			pastRows = append(pastRows, fm)
		} else {
			nextRows = append(nextRows, fm)
		}
	}

	monthEntries := filter(entries, func(e finance.Entry) bool { return finance.MonthOf(e.Date) == sel })
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
	}
	var cd chartData
	for _, p := range flow.Points {
		cd.FlowX = append(cd.FlowX, p.Date.Format("2006-01-02"))
		cd.FlowY = append(cd.FlowY, reais(p.Balance))
	}
	for _, fm := range flowRows {
		cd.ComX = append(cd.ComX, shortMonth(fm.Month))
		cd.ComPast = append(cd.ComPast, fm.Past)
		cd.ComIn = append(cd.ComIn, reais(fm.In))
		cd.ComCard = append(cd.ComCard, reais(fm.Card))
		cd.ComFixed = append(cd.ComFixed, reais(fm.Fixed))
		cd.ComOther = append(cd.ComOther, reais(fm.Other))
	}

	s.render(w, r, "dashboard", page{
		"Title": "Dashboard", "Month": sel, "Prev": sel.Add(-1), "Next": sel.Add(1), "Current": cur,
		"Today": today, "Sum": selSum, "Limits": lim, "Snap": snap, "Flow": flow,
		"FlowRows": flowRows, "PastTot": finance.SumFlow(pastRows), "NextTot": finance.SumFlow(nextRows),
		"PastN": len(pastRows), "NextN": len(nextRows),
		"ExpCats": expCats, "ProjectedCount": projectedCount, "Chart": cd,
		"CardPct": barPct(selSum.Card, lim.CardLimit), "CardRatio": ratioOf(selSum.Card, lim.CardLimit),
	})
}

func ratioOf(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func (s *Server) saveCash(w http.ResponseWriter, r *http.Request) {
	amount, err := ParseMoney(r.FormValue("amount"))
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	date := s.today()
	if d, err := finance.ParseDate(r.FormValue("date")); err == nil {
		date = d
	}
	if err := s.st.AddSnapshot(date, amount, r.FormValue("note")); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/?msg="+url.QueryEscape("Caixa atualizado"), http.StatusSeeOther)
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
		entries, skipped, err := s.st.Entries(mes.Add(-1), mes)
		if err != nil {
			s.fail(w, err)
			return
		}
		ofKind := func(e finance.Entry) bool { return e.Kind == kind }
		cur := filter(entries, func(e finance.Entry) bool { return ofKind(e) && finance.MonthOf(e.Date) == mes })
		prev := filter(entries, func(e finance.Entry) bool { return ofKind(e) && finance.MonthOf(e.Date) == mes.Add(-1) })
		skipped = filter(skipped, func(e finance.Entry) bool { return ofKind(e) && finance.MonthOf(e.Date) == mes })

		var total, prevTotal, projected int64
		buckets := map[finance.Bucket]int64{}
		for _, e := range cur {
			total += e.Amount
			buckets[e.Bucket] += e.Amount
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
		cats, err := s.st.Categories(kind)
		if err != nil {
			s.fail(w, err)
			return
		}
		var debts []db.Debt
		if kind == finance.Expense {
			if debts, err = s.st.Debts(today); err != nil {
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
			"Categories": cats, "Debts": debts, "DefaultDate": defDate,
			"NewRec": finance.Recurrence{Interval: 1, EndMode: finance.EndNever},
			"Back":   kindPath(kind) + "?mes=" + mes.String(),
		})
	}
}

// categoryFromForm returns the selected category, creating "new_category" when filled in.
func (s *Server) categoryFromForm(r *http.Request, kind finance.Kind) (int64, error) {
	if name := strings.TrimSpace(r.FormValue("new_category")); name != "" {
		return s.st.EnsureCategory(name, kind, finance.Other)
	}
	id := formID(r, "category_id")
	if id == 0 {
		return 0, fmt.Errorf("escolha uma categoria")
	}
	c, err := s.st.Category(id)
	if err != nil || c.Kind != kind {
		return 0, fmt.Errorf("categoria inválida")
	}
	return id, nil
}

func parseKind(v string) (finance.Kind, error) {
	switch finance.Kind(v) {
	case finance.Income, finance.Expense:
		return finance.Kind(v), nil
	}
	return "", fmt.Errorf("tipo inválido")
}

func (s *Server) createTx(w http.ResponseWriter, r *http.Request) {
	kind, err := parseKind(r.FormValue("kind"))
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	date, err := finance.ParseDate(r.FormValue("date"))
	if err != nil {
		badRequest(w, "data inválida")
		return
	}
	amount, err := ParseMoney(r.FormValue("amount"))
	if err != nil || amount <= 0 {
		badRequest(w, "valor inválido")
		return
	}
	catID, err := s.categoryFromForm(r, kind)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	desc := strings.TrimSpace(r.FormValue("description"))
	dest := kindPath(kind) + "?mes=" + finance.MonthOf(date).String()

	if r.FormValue("repeat") == "on" {
		rec, err := recurrenceFromForm(r)
		if err != nil {
			badRequest(w, err.Error())
			return
		}
		if desc == "" {
			c, _ := s.st.Category(catID)
			desc = c.Name
		}
		rec.Kind, rec.Description, rec.CategoryID, rec.Amount = kind, desc, catID, amount
		rec.Day, rec.Start, rec.Active = date.Day(), finance.MonthOf(date), true
		if _, err := s.st.SaveRecurrence(rec); err != nil {
			s.fail(w, err)
			return
		}
		http.Redirect(w, r, dest+"&msg="+url.QueryEscape("Recorrência criada — já aparece nos meses seguintes"), http.StatusSeeOther)
		return
	}

	_, err = s.st.InsertTx(db.TxInput{Kind: kind, Date: date, Amount: amount, CategoryID: catID, Description: desc, DebtID: formID(r, "debt_id")})
	if err != nil {
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
	tx, err := s.st.Tx(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	cats, err := s.st.Categories(tx.Kind)
	if err != nil {
		s.fail(w, err)
		return
	}
	debts, _ := s.st.Debts(s.today())
	var rec *finance.Recurrence
	if tx.RecurrenceID != 0 {
		if rr, err := s.st.Recurrence(tx.RecurrenceID); err == nil {
			rec = &rr
		}
	}
	s.render(w, r, "edit_tx", page{
		"Title": "Editar lançamento", "Tx": tx, "Categories": cats, "Debts": debts, "Rec": rec,
		"IsIncome": tx.Kind == finance.Income,
		"Back":     kindPath(tx.Kind) + "?mes=" + finance.MonthOf(tx.Date).String(),
	})
}

func (s *Server) updateTx(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	tx, err := s.st.Tx(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	date, err := finance.ParseDate(r.FormValue("date"))
	if err != nil {
		badRequest(w, "data inválida")
		return
	}
	amount, err := ParseMoney(r.FormValue("amount"))
	if err != nil || amount <= 0 {
		badRequest(w, "valor inválido")
		return
	}
	catID, err := s.categoryFromForm(r, tx.Kind)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	in := db.TxInput{Date: date, Amount: amount, CategoryID: catID, Description: strings.TrimSpace(r.FormValue("description")), DebtID: formID(r, "debt_id")}
	if err := s.st.UpdateTx(id, in); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, kindPath(tx.Kind)+"?mes="+finance.MonthOf(date).String()+"&msg="+url.QueryEscape("Lançamento atualizado"), http.StatusSeeOther)
}

func (s *Server) deleteTx(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.st.DeleteTx(id); err != nil {
		s.fail(w, err)
		return
	}
	back(w, r, "/gastos")
}

func (s *Server) occurrenceFromForm(r *http.Request) (finance.Recurrence, finance.Month, error) {
	rec, err := s.st.Recurrence(formID(r, "rec_id"))
	if err != nil {
		return rec, 0, fmt.Errorf("recorrência não encontrada")
	}
	m, err := finance.ParseMonth(r.FormValue("month"))
	return rec, m, err
}

func (s *Server) confirmOccurrence(w http.ResponseWriter, r *http.Request) {
	rec, m, err := s.occurrenceFromForm(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	amount := rec.Amount
	if v := r.FormValue("amount"); v != "" {
		if amount, err = ParseMoney(v); err != nil || amount <= 0 {
			badRequest(w, "valor inválido")
			return
		}
	}
	date := m.Day(rec.Day)
	if d, err := finance.ParseDate(r.FormValue("date")); err == nil {
		date = d
	}
	_, err = s.st.InsertTx(db.TxInput{
		Kind: rec.Kind, Date: date, Amount: amount, CategoryID: rec.CategoryID, Description: rec.Description,
		RecurrenceID: rec.ID, OccMonth: m.String(), DebtID: rec.DebtID,
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	back(w, r, kindPath(rec.Kind))
}

func (s *Server) skipOccurrence(w http.ResponseWriter, r *http.Request) {
	rec, m, err := s.occurrenceFromForm(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	if err := s.st.Skip(rec.ID, m); err != nil {
		s.fail(w, err)
		return
	}
	back(w, r, kindPath(rec.Kind))
}

func (s *Server) unskipOccurrence(w http.ResponseWriter, r *http.Request) {
	rec, m, err := s.occurrenceFromForm(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	if err := s.st.Unskip(rec.ID, m); err != nil {
		s.fail(w, err)
		return
	}
	back(w, r, kindPath(rec.Kind))
}

// ---------- recorrentes ----------

// recurrenceFromForm reads the periodicity / duration fields.
func recurrenceFromForm(r *http.Request) (finance.Recurrence, error) {
	rec := finance.Recurrence{Interval: formInt(r, "interval", 1), EndMode: r.FormValue("end_mode")}
	switch rec.Interval {
	case 1, 2, 3, 6, 12:
	default:
		return rec, fmt.Errorf("periodicidade inválida")
	}
	switch rec.EndMode {
	case finance.EndCount:
		rec.Count = formInt(r, "count", 0)
		if rec.Count < 1 || rec.Count > 600 {
			return rec, fmt.Errorf("número de vezes inválido")
		}
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
	recs, err := s.st.Recurrences()
	if err != nil {
		s.fail(w, err)
		return
	}
	editID := formID(r, "editar")
	form := finance.Recurrence{Kind: kind, Interval: 1, Start: cur, Day: today.Day(), EndMode: finance.EndNever, Active: true}
	var rows []recRow
	var thisMonth, monthlyEq int64
	for _, rc := range recs {
		if rc.Kind != kind {
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
	cats, err := s.st.Categories(kind)
	if err != nil {
		s.fail(w, err)
		return
	}
	debts, err := s.st.Debts(today)
	if err != nil {
		s.fail(w, err)
		return
	}
	debtNames := map[int64]string{}
	for _, d := range debts {
		debtNames[d.ID] = d.Name
	}
	s.render(w, r, "recurrences", page{
		"Title": "Recorrentes", "Kind": string(kind), "IsIncome": kind == finance.Income,
		"Rows": rows, "ThisMonth": thisMonth, "MonthlyEq": monthlyEq, "Current": cur,
		"Form": form, "Editing": editID != 0 && form.ID == editID, "Categories": cats,
		"Debts": debts, "DebtNames": debtNames,
	})
}

func (s *Server) saveRecurrence(w http.ResponseWriter, r *http.Request) {
	kind, err := parseKind(r.FormValue("kind"))
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	rec, err := recurrenceFromForm(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	if idStr := r.PathValue("id"); idStr != "" {
		if rec.ID, err = pathID(r); err != nil {
			http.NotFound(w, r)
			return
		}
		old, err := s.st.Recurrence(rec.ID)
		if err != nil || old.Kind != kind {
			http.NotFound(w, r)
			return
		}
	}
	rec.Kind = kind
	rec.Description = strings.TrimSpace(r.FormValue("description"))
	if rec.CategoryID, err = s.categoryFromForm(r, kind); err != nil {
		badRequest(w, err.Error())
		return
	}
	if rec.Description == "" {
		c, _ := s.st.Category(rec.CategoryID)
		rec.Description = c.Name
	}
	if rec.Amount, err = ParseMoney(r.FormValue("amount")); err != nil || rec.Amount <= 0 {
		badRequest(w, "valor inválido")
		return
	}
	rec.Day = formInt(r, "day", 1)
	if rec.Day < 1 || rec.Day > 31 {
		badRequest(w, "dia inválido")
		return
	}
	if rec.Start, err = finance.ParseMonth(r.FormValue("start")); err != nil {
		badRequest(w, "mês inicial inválido")
		return
	}
	if rec.EndMode == finance.EndUntil && rec.End < rec.Start {
		badRequest(w, "mês final antes do inicial")
		return
	}
	rec.Active = rec.ID == 0 || r.FormValue("active") == "on"
	if kind == finance.Expense {
		if rec.DebtID = formID(r, "debt_id"); rec.DebtID != 0 {
			if _, err := s.st.Debt(rec.DebtID); err != nil {
				badRequest(w, "dívida inválida")
				return
			}
		}
	}
	if _, err := s.st.SaveRecurrence(rec); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/recorrentes?tipo="+string(kind)+"&msg="+url.QueryEscape("Recorrência salva — meses atualizados"), http.StatusSeeOther)
}

func (s *Server) deleteRecurrence(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rec, err := s.st.Recurrence(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.st.DeleteRecurrence(id); err != nil {
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
	debts, err := s.st.Debts(today)
	if err != nil {
		s.fail(w, err)
		return
	}
	all, err := s.st.AllTransactions()
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

func debtFromForm(r *http.Request) (db.DebtInput, error) {
	in := db.DebtInput{Name: strings.TrimSpace(r.FormValue("name")), System: r.FormValue("system")}
	var err error
	if in.Initial, err = ParseMoney(r.FormValue("initial")); in.Name == "" || err != nil || in.Initial <= 0 {
		return in, fmt.Errorf("nome e valor inicial são obrigatórios")
	}
	if in.System != finance.LoanSAC && in.System != finance.LoanPrice {
		in.System = finance.LoanNone
		return in, nil
	}
	if in.MonthlyRate, err = parseRate(r.FormValue("rate")); err != nil || in.MonthlyRate < 0 || in.MonthlyRate > 20 {
		return in, fmt.Errorf("taxa mensal inválida")
	}
	if in.Term = formInt(r, "term", 0); in.Term < 1 || in.Term > 600 {
		return in, fmt.Errorf("prazo inválido")
	}
	if in.FirstDue, err = finance.ParseDate(r.FormValue("first_due")); err != nil {
		return in, fmt.Errorf("data da 1ª parcela inválida")
	}
	if v := r.FormValue("fees"); v != "" {
		if in.Fees, err = ParseMoney(v); err != nil || in.Fees < 0 {
			return in, fmt.Errorf("seguros/taxas inválidos")
		}
	}
	in.AnchorN = formInt(r, "anchor_n", 0)
	if in.AnchorN < 0 || in.AnchorN >= in.Term {
		return in, fmt.Errorf("parcela do saldo informado inválida")
	}
	if in.AnchorN > 0 {
		if in.AnchorBalance, err = ParseMoney(r.FormValue("anchor_balance")); err != nil || in.AnchorBalance <= 0 {
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
	if r.PathValue("id") != "" {
		id, err := pathID(r)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		err = s.st.UpdateDebt(id, in)
	} else {
		_, err = s.st.AddDebt(in)
	}
	if err != nil {
		s.fail(w, err)
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
	if err := s.st.DeleteDebt(id); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/dividas", http.StatusSeeOther)
}

// ---------- config ----------

func (s *Server) configPage(w http.ResponseWriter, r *http.Request) {
	cats, err := s.st.Categories("")
	if err != nil {
		s.fail(w, err)
		return
	}
	var inc, exp []db.Category
	for _, c := range cats {
		if c.Kind == finance.Income {
			inc = append(inc, c)
		} else {
			exp = append(exp, c)
		}
	}
	snap, _ := s.st.LatestSnapshot()
	s.render(w, r, "config", page{
		"Title": "Configurações", "Limits": s.st.Limits(), "IncomeCats": inc, "ExpenseCats": exp, "Snap": snap,
	})
}

func (s *Server) saveLimits(w http.ResponseWriter, r *http.Request) {
	card, err1 := ParseMoney(r.FormValue("card_limit"))
	minS, err2 := ParseMoney(r.FormValue("min_savings"))
	if r.FormValue("min_savings") == "" {
		minS, err2 = 0, nil
	}
	if err1 != nil || err2 != nil {
		badRequest(w, "valores inválidos")
		return
	}
	if err := s.st.SetSettingInt("card_limit_cents", card); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.st.SetSettingInt("min_savings_cents", minS); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/config?msg="+url.QueryEscape("Limites salvos"), http.StatusSeeOther)
}

func parseBucket(v string) finance.Bucket {
	switch finance.Bucket(v) {
	case finance.Card, finance.Fixed:
		return finance.Bucket(v)
	}
	return finance.Other
}

func (s *Server) addCategory(w http.ResponseWriter, r *http.Request) {
	kind, err := parseKind(r.FormValue("kind"))
	name := strings.TrimSpace(r.FormValue("name"))
	if err != nil || name == "" {
		badRequest(w, "nome e tipo são obrigatórios")
		return
	}
	if _, err := s.st.EnsureCategory(name, kind, parseBucket(r.FormValue("bucket"))); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/config?msg="+url.QueryEscape("Categoria adicionada"), http.StatusSeeOther)
}

func (s *Server) updateCategory(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.st.SetCategoryBucket(id, parseBucket(r.FormValue("bucket"))); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/config?msg="+url.QueryEscape("Categoria atualizada"), http.StatusSeeOther)
}

func (s *Server) exportCSV(w http.ResponseWriter, r *http.Request) {
	all, err := s.st.AllTransactions()
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="lancamentos.csv"`)
	w.Write([]byte("\xEF\xBB\xBF")) // BOM so Excel reads UTF-8
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	cw.Write([]string{"data", "tipo", "categoria", "valor", "descricao", "recorrente"})
	for _, e := range all {
		rec := ""
		if e.RecurrenceID != 0 {
			rec = "sim"
		}
		cw.Write([]string{e.Date.Format("2006-01-02"), string(e.Kind), e.Category, moneyInput(e.Amount), e.Description, rec})
	}
	cw.Flush()
}

func (s *Server) exportDB(w http.ResponseWriter, r *http.Request) {
	dir, err := os.MkdirTemp("", "cg-backup")
	if err != nil {
		s.fail(w, err)
		return
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "backup.db")
	if err := s.st.Backup(path); err != nil {
		s.fail(w, err)
		return
	}
	name := "controle-gastos-" + s.today().Format("2006-01-02") + ".db"
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeFile(w, r, path)
}
