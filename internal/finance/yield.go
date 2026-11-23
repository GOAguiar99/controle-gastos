package finance

import (
	"math"
	"time"
)

// YieldCategory names the estimated income from the cash's own yield.
const YieldCategory = "Rendimentos"

// AnnualToPeriod converts an annual rate (% a.a.) into the gross rate of a
// period of days (calendar days over 365, compounded).
func AnnualToPeriod(annualPct float64, days int) float64 {
	return math.Pow(1+annualPct/100, float64(days)/365) - 1
}

// Yield estimates what the cash earns from the snapshot until end: in each
// period (up to today, then month by month) the positive balance at its
// start grows at annualPct % a.a. Each period becomes a projected income
// entry dated on its last day, so cash balances and projections include it.
func Yield(entries []Entry, snap Snapshot, today, end time.Time, annualPct float64, desc string) []Entry {
	if !snap.Set || annualPct <= 0 {
		return nil
	}
	today, end = DateOnly(today), DateOnly(end)
	var out []Entry
	start := DateOnly(snap.Date)
	for start.Before(end) {
		stop := MonthOf(start).Last()
		if !stop.After(start) { // started on a month's last day
			stop = MonthOf(start).Add(1).Last()
		}
		if start.Before(today) && today.Before(stop) {
			stop = today // what accrued up to today counts in "caixa hoje"
		}
		if stop.After(end) {
			stop = end
		}
		days := int(stop.Sub(start).Hours() / 24)
		bal := BalanceAt(append(entries, out...), snap, start)
		if bal > 0 && days > 0 {
			if v := int64(math.Round(float64(bal) * AnnualToPeriod(annualPct, days))); v > 0 {
				out = append(out, Entry{Kind: Income, Date: stop, Amount: v, Category: YieldCategory,
					Description: desc, Projected: true})
			}
		}
		start = stop
	}
	return out
}
