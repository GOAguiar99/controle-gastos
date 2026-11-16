package app

import (
	"time"

	"controle-gastos/internal/finance"
)

const (
	HorizonMonths = 12 // months of cash projection
	HistoryMonths = 6  // months of history in the dashboard flow table
)

// Overview is everything the dashboard shows for a selected month.
type Overview struct {
	Today    time.Time
	Month    finance.Month
	Summary  finance.MonthSummary // receitas, gastos, sobra e status do mês
	Limits   finance.Limits
	Snapshot finance.Snapshot // último "caixa hoje" informado
	Flow     finance.CashFlow // caixa projetado dia a dia / mês a mês
	FlowRows []finance.FlowMonth
	Entries  []finance.Entry // all entries loaded (history through the horizon)
}

func (s *Service) Overview(sel finance.Month) (Overview, error) {
	today := s.Today()
	cur := finance.MonthOf(today)
	if sel == 0 {
		sel = cur
	}
	o := Overview{Today: today, Month: sel, Limits: s.st.Limits()}
	var err error
	if o.Snapshot, err = s.st.LatestSnapshot(); err != nil {
		return o, err
	}
	lo, hi := min(sel, cur.Add(-HistoryMonths)), max(sel, cur.Add(HorizonMonths-1))
	if o.Snapshot.Set {
		lo = min(lo, finance.MonthOf(o.Snapshot.Date))
	}
	if o.Entries, _, err = s.st.Entries(lo, hi); err != nil {
		return o, err
	}
	o.Summary = finance.Summarize(o.Entries, sel, sel, o.Limits)[0]
	// The future only counts what is launched (recurrences, loan parcels and
	// future-dated expenses): what's left is what can still be spent.
	o.Flow = finance.ProjectCash(o.Entries, o.Snapshot, today, HorizonMonths)
	o.FlowRows = finance.MonthlyFlow(o.Entries, o.Snapshot, today, cur.Add(-HistoryMonths), cur.Add(HorizonMonths-1))
	return o, nil
}

// CashProjection projects the cash balance for the next n months.
func (s *Service) CashProjection(n int) (finance.CashFlow, error) {
	if n < 1 || n > 60 {
		return finance.CashFlow{}, invalid("meses deve estar entre 1 e 60")
	}
	today := s.Today()
	cur := finance.MonthOf(today)
	snap, err := s.st.LatestSnapshot()
	if err != nil {
		return finance.CashFlow{}, err
	}
	lo := cur
	if snap.Set {
		lo = min(lo, finance.MonthOf(snap.Date))
	}
	es, _, err := s.st.Entries(lo, cur.Add(n-1))
	if err != nil {
		return finance.CashFlow{}, err
	}
	return finance.ProjectCash(es, snap, today, n), nil
}

// SetCash records the real cash balance ("caixa hoje") on date (zero = today).
// Entries dated on or before it are considered already included in it.
func (s *Service) SetCash(date time.Time, amount int64, note string) (time.Time, error) {
	if date.IsZero() {
		date = s.Today()
	}
	return date, s.st.AddSnapshot(date, amount, note)
}

// CategoryReport is the yearly per-category history (from the first
// transaction through next year) plus the composition of the selected month,
// its year and all history up to today.
func (s *Service) CategoryReport(kind finance.Kind, sel finance.Month) (CategoryChart, error) {
	if _, err := ParseKind(string(kind)); err != nil {
		return CategoryChart{}, err
	}
	today := s.Today()
	if sel == 0 {
		sel = finance.MonthOf(today)
	}
	firstM := finance.MonthOf(today).Add(-11)
	if t, ok, err := s.st.FirstTxDate(); err != nil {
		return CategoryChart{}, err
	} else if ok {
		firstM = finance.MonthOf(t)
	}
	firstM = min(firstM, sel)
	lastYear := max(today.Year()+1, sel.Year())
	es, _, err := s.st.Entries(firstM, finance.NewMonth(lastYear, time.December))
	if err != nil {
		return CategoryChart{}, err
	}
	var ofKind []finance.Entry
	for _, e := range es {
		if e.Kind == kind {
			ofKind = append(ofKind, e)
		}
	}
	return buildCatChart(ofKind, firstM, sel, today, lastYear), nil
}

func reais(c int64) float64 { return float64(c) / 100 }
