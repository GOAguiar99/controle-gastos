package finance

import "time"

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
	Opening int64
	In      int64
	Out     int64
	Closing int64
	Low     int64
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
		case t.After(snap.Date) && e.Date.After(snap.Date) && !e.Date.After(t):
			bal += e.Signed()
		case t.Before(snap.Date) && e.Date.After(t) && !e.Date.After(snap.Date):
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
// entries must be sorted by date and cover snap.Date .. end of horizon.
func ProjectCash(entries []Entry, snap Snapshot, today time.Time, n int) CashFlow {
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
	for i < len(entries) && !entries[i].Date.After(today) {
		i++
	}
	for m := start; m <= MonthOf(end); m++ {
		fm := FlowMonth{Month: m, Opening: bal, Low: bal}
		for i < len(entries) && MonthOf(entries[i].Date) == m {
			e := entries[i]
			i++
			if e.Kind == Income {
				fm.In += e.Amount
			} else {
				fm.Out += e.Amount
			}
			bal += e.Signed()
			if last := len(cf.Points) - 1; cf.Points[last].Date.Equal(e.Date) {
				cf.Points[last].Balance = bal
			} else {
				cf.Points = append(cf.Points, FlowPoint{e.Date, bal})
			}
			if bal < fm.Low {
				fm.Low = bal
			}
			if bal < cf.Low {
				cf.Low, cf.LowDate = bal, e.Date
			}
			if bal < 0 && cf.FirstNegative.IsZero() {
				cf.FirstNegative = e.Date
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
