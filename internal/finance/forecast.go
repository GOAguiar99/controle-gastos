package finance

import (
	"strings"
	"time"
)

// ForecastCategory names the estimated day-to-day spending added to the cash flow.
const ForecastCategory = "Previsão de gastos"

// forecastDay is the day of the month the estimate leaves the cash.
const forecastDay = 15

// MinForecastMonths is how many past months with spending the forecast needs.
const MinForecastMonths = 3

// ReformCategory names one-off renovation spending (architect, builders,
// furniture). It stays out of the day-to-day average: in the past it would
// inflate the estimate, and future payments are launched on their own.
const ReformCategory = "Reforma"

// Variable reports whether e is day-to-day spending: an expense that is not
// fixed (recurrence) nor a debt payment, not renovation (ReformCategory) and
// not an estimate itself. Card purchases, installments and undetailed fatura
// totals all count.
func (e Entry) Variable() bool {
	return e.Kind == Expense && !e.Fixed && e.RecurrenceID == 0 && e.DebtID == 0 && !e.Forecast &&
		!strings.EqualFold(strings.TrimSpace(e.Category), ReformCategory)
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

// ForecastBase is the monthly day-to-day spending estimated from history:
// the average of the sampled months.
type ForecastBase struct {
	Average int64   // média mensal do gasto do dia a dia
	Months  []Month // months sampled (with any variable spending)
	Samples []int64 // their variable spending, same order as Months
	Min     int64   // lowest sampled month
	Max     int64   // highest sampled month
}

// Baselines samples the window closed months before today's month and
// returns their average. ok is false with fewer than MinForecastMonths
// months that had any variable spending.
func Baselines(entries []Entry, today time.Time, window int) (ForecastBase, bool) {
	cur := MonthOf(today)
	from := cur.Add(-window)
	by := VariableByMonth(entries, from, cur.Add(-1))
	var fb ForecastBase
	var total int64
	for m := from; m < cur; m++ {
		v := by[m]
		if v <= 0 {
			continue
		}
		fb.Months = append(fb.Months, m)
		fb.Samples = append(fb.Samples, v)
		total += v
		if fb.Min == 0 || v < fb.Min {
			fb.Min = v
		}
		fb.Max = max(fb.Max, v)
	}
	if len(fb.Samples) < MinForecastMonths {
		return fb, false
	}
	n := int64(len(fb.Samples))
	fb.Average = (total + n/2) / n
	return fb, true
}

// ForecastEntries tops up each month from today's through end's to amount
// of variable spending: what is already launched in the month (fatura
// purchases, future-dated expenses) counts within it, so nothing is
// counted twice and each month spends at least amount. Each top-up is one
// projected expense on the 15th, or the day after today in the current
// month; a month already over gets none.
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
