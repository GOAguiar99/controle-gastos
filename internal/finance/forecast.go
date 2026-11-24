package finance

import (
	"sort"
	"time"
)

// ForecastCategory names the estimated day-to-day spending added to the cash flow.
const ForecastCategory = "Previsão de gastos"

// forecastDay is the day of the month the estimate leaves the cash.
const forecastDay = 15

// MinForecastMonths is how many past months with spending the forecast needs.
const MinForecastMonths = 3

// Variable reports whether e is day-to-day spending: an expense that is not
// fixed (recurrence) nor a debt payment, and not an estimate itself. Card
// purchases, installments and undetailed fatura totals all count.
func (e Entry) Variable() bool {
	return e.Kind == Expense && !e.Fixed && e.RecurrenceID == 0 && e.DebtID == 0 && !e.Forecast
}

// VariableByMonth sums the variable spending of each month in [from, to],
// counted in the month it is reported (fatura due month for card purchases).
func VariableByMonth(entries []Entry, from, to Month) map[Month]int64 {
	out := map[Month]int64{}
	for _, e := range entries {
		if m := e.Month(); e.Variable() && m >= from && m <= to {
			out[m] += e.Amount
		}
	}
	return out
}

// ForecastBase is the monthly variable spending estimated from history:
// the 25th, 50th and 75th percentiles of the sampled months.
type ForecastBase struct {
	Optimistic  int64
	Base        int64
	Pessimistic int64
	Months      []Month // months sampled (with any variable spending)
	Samples     []int64 // their variable spending, same order as Months
}

// Baselines samples the window closed months before today's month and
// returns the percentiles. ok is false with fewer than MinForecastMonths
// months that had any variable spending.
func Baselines(entries []Entry, today time.Time, window int) (ForecastBase, bool) {
	cur := MonthOf(today)
	from := cur.Add(-window)
	by := VariableByMonth(entries, from, cur.Add(-1))
	var fb ForecastBase
	for m := from; m < cur; m++ {
		if v := by[m]; v > 0 {
			fb.Months = append(fb.Months, m)
			fb.Samples = append(fb.Samples, v)
		}
	}
	if len(fb.Samples) < MinForecastMonths {
		return fb, false
	}
	sorted := append([]int64(nil), fb.Samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	fb.Optimistic = percentile(sorted, .25)
	fb.Base = percentile(sorted, .5)
	fb.Pessimistic = percentile(sorted, .75)
	return fb, true
}

// percentile interpolates linearly between the closest ranks of sorted.
func percentile(sorted []int64, p float64) int64 {
	pos := p * float64(len(sorted)-1)
	lo := int(pos)
	if lo+1 >= len(sorted) {
		return sorted[lo]
	}
	frac := pos - float64(lo)
	return sorted[lo] + int64(frac*float64(sorted[lo+1]-sorted[lo])+.5)
}

// ForecastEntries tops up each month from today's through end's to amount
// of variable spending: what is already launched in the month (fatura
// purchases, future-dated expenses) counts, so nothing is counted twice.
// Each top-up is one projected expense on the 15th, or the day after today
// in the current month; a month already over gets none.
func ForecastEntries(entries []Entry, amount int64, today, end time.Time) []Entry {
	if amount <= 0 {
		return nil
	}
	today, end = DateOnly(today), DateOnly(end)
	from, to := MonthOf(today), MonthOf(end)
	launched := VariableByMonth(entries, from, to)
	var out []Entry
	for m := from; m <= to; m++ {
		rest := amount - launched[m]
		if rest <= 0 {
			continue
		}
		day := m.Day(forecastDay)
		if !day.After(today) {
			day = today.AddDate(0, 0, 1)
		}
		if MonthOf(day) != m || day.After(end) {
			continue
		}
		out = append(out, Entry{Kind: Expense, Date: day, Amount: rest, Category: ForecastCategory,
			Description: ForecastCategory, Projected: true, Forecast: true})
	}
	return out
}
