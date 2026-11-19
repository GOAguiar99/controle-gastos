package finance

import (
	"sort"
	"time"
)

// Snapshot is a cash balance the user typed in ("caixa") on a given date.
// Entries dated on or before Date are considered already reflected in Amount.
type Snapshot struct {
	Date   time.Time
	Amount int64
	Set    bool
}

type FlowPoint struct {
	Date    time.Time
	Balance int64
}

type FlowMonth struct {
	Month   Month
	Partial bool // ProjectCash only: current month counts just what comes after today
	Past    bool // MonthlyFlow: month already closed (valores realizados)
	Current bool // MonthlyFlow: the current month
	Opening int64
	In      int64
	Out     int64
	Card    int64 // saídas por grupo, como na planilha
	Fixed   int64
	Other   int64
	Closing int64
	Low     int64
}

// Net is the month's surplus: income minus everything already committed.
func (f FlowMonth) Net() int64 { return f.In - f.Out }

func (f *FlowMonth) add(e Entry) {
	if e.Kind == Income {
		f.In += e.Amount
		return
	}
	f.Out += e.Amount
	switch e.Group() {
	case Card:
		f.Card += e.Amount
	case Fixed:
		f.Fixed += e.Amount
	default:
		f.Other += e.Amount
	}
}

// SumFlow adds up the given months (opening/closing are left empty).
func SumFlow(months []FlowMonth) FlowMonth {
	var t FlowMonth
	for _, m := range months {
		t.In += m.In
		t.Out += m.Out
		t.Card += m.Card
		t.Fixed += m.Fixed
		t.Other += m.Other
	}
	return t
}

// MonthlyFlow lists whole months from..to: realized values for the past,
// realized + committed for the current month and committed for the future.
// Opening/closing balances are end-of-month cash anchored on snap, so past
// months show the history and future months the projection.
func MonthlyFlow(entries []Entry, snap Snapshot, today time.Time, from, to Month) []FlowMonth {
	today = DateOnly(today)
	if !snap.Set {
		snap = Snapshot{Date: today}
	}
	cur := MonthOf(today)
	out := make([]FlowMonth, 0, int(to-from)+1)
	for m := from; m <= to; m++ {
		fm := FlowMonth{Month: m, Past: m < cur, Current: m == cur,
			Opening: BalanceAt(entries, snap, m.First().AddDate(0, 0, -1)),
			Closing: BalanceAt(entries, snap, m.Last())}
		for _, e := range entries {
			if MonthOf(e.CashDay()) == m {
				fm.add(e)
			}
		}
		out = append(out, fm)
	}
	return out
}

type CashFlow struct {
	Today   time.Time
	Current int64 // caixa estimado hoje
	Points  []FlowPoint
	Months  []FlowMonth
	Low     int64
	LowDate time.Time
	// FirstNegative is the first date the projected balance goes below zero (zero value if never).
	FirstNegative time.Time
}

// BalanceAt returns the cash balance at the end of day t, anchored on snap.
func BalanceAt(entries []Entry, snap Snapshot, t time.Time) int64 {
	bal := snap.Amount
	for _, e := range entries {
		switch {
		case t.After(snap.Date) && e.CashDay().After(snap.Date) && !e.CashDay().After(t):
			bal += e.Signed()
		case t.Before(snap.Date) && e.CashDay().After(t) && !e.CashDay().After(snap.Date):
			bal -= e.Signed()
		}
	}
	return bal
}

// FillClosing sets the end-of-month cash balance on each summary.
func FillClosing(sums []MonthSummary, entries []Entry, snap Snapshot) {
	if !snap.Set {
		return
	}
	for i := range sums {
		sums[i].Closing = BalanceAt(entries, snap, sums[i].Month.Last())
		sums[i].HasClosing = true
	}
}

// ProjectCash builds the future cash flow from today through the next n months.
// entries must cover every entry whose cash day is in snap.Date .. end of
// horizon (card purchases move the cash on the statement due date).
func ProjectCash(entries []Entry, snap Snapshot, today time.Time, n int) CashFlow {
	entries = byCashDay(entries)
	today = DateOnly(today)
	if !snap.Set || snap.Date.After(today) {
		snap.Date = today
	}
	cf := CashFlow{Today: today, Current: BalanceAt(entries, snap, today)}
	bal := cf.Current
	cf.Low, cf.LowDate = bal, today
	cf.Points = append(cf.Points, FlowPoint{today, bal})
	if bal < 0 {
		cf.FirstNegative = today
	}

	start := MonthOf(today)
	end := start.Add(n - 1).Last()
	i := 0
	for i < len(entries) && !entries[i].CashDay().After(today) {
		i++
	}
	for m := start; m <= MonthOf(end); m++ {
		fm := FlowMonth{Month: m, Partial: m == start, Opening: bal, Low: bal}
		for i < len(entries) && MonthOf(entries[i].CashDay()) == m {
			e := entries[i]
			i++
			fm.add(e)
			bal += e.Signed()
			if last := len(cf.Points) - 1; cf.Points[last].Date.Equal(e.CashDay()) {
				cf.Points[last].Balance = bal
			} else {
				cf.Points = append(cf.Points, FlowPoint{e.CashDay(), bal})
			}
			if bal < fm.Low {
				fm.Low = bal
			}
			if bal < cf.Low {
				cf.Low, cf.LowDate = bal, e.CashDay()
			}
			if bal < 0 && cf.FirstNegative.IsZero() {
				cf.FirstNegative = e.CashDay()
			}
		}
		fm.Closing = bal
		cf.Months = append(cf.Months, fm)
	}
	if last := cf.Points[len(cf.Points)-1]; last.Date.Before(end) {
		cf.Points = append(cf.Points, FlowPoint{end, bal})
	}
	return cf
}

// byCashDay returns a copy of entries ordered by the day they move the cash.
func byCashDay(entries []Entry) []Entry {
	out := append([]Entry(nil), entries...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].CashDay().Before(out[j].CashDay()) })
	return out
}
