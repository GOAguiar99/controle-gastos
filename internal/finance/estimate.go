package finance

import "time"

// EstimateCategory labels the synthetic entries that stand in for not-yet-known
// variable spending (cartão + outros) in the cash-flow projection.
const EstimateCategory = "Estimativa de variáveis"

// AverageVariable is the mean of Card+Other over the given months (typically
// the last 3 closed months).
func AverageVariable(sums []MonthSummary) int64 {
	if len(sums) == 0 {
		return 0
	}
	var total int64
	for _, s := range sums {
		total += s.Card + s.Other
	}
	return total / int64(len(sums))
}

// AddVariableEstimate appends, for each of the n months starting at today's
// month, an expense covering the part of est not yet registered as
// card/other spending in that month. Only future dates are used.
func AddVariableEstimate(entries []Entry, est int64, today time.Time, n int) []Entry {
	if est <= 0 {
		return entries
	}
	today = DateOnly(today)
	start := MonthOf(today)
	spent := map[Month]int64{}
	for _, e := range entries {
		if e.Kind == Expense && e.Bucket != Fixed {
			spent[MonthOf(e.Date)] += e.Amount
		}
	}
	out := append([]Entry(nil), entries...)
	for m := start; m < start.Add(n); m++ {
		remaining := est - spent[m]
		if remaining <= 0 {
			continue
		}
		date := m.Day(10)
		if !date.After(today) {
			date = m.Last()
			if !date.After(today) {
				continue
			}
		}
		out = append(out, Entry{
			Kind: Expense, Date: date, Amount: remaining, Category: EstimateCategory, Bucket: Other,
			Description: "Cartão + outros ainda não lançados", Projected: true, Estimate: true,
		})
	}
	SortEntries(out)
	return out
}
