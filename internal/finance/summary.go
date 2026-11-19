package finance

// Limits are the user's thresholds (cents).
type Limits struct {
	CardLimit  int64
	MinSavings int64
}

// MonthSummary mirrors one row of the old dashboard table.
type MonthSummary struct {
	Month      Month
	Income     int64
	Card       int64
	Fixed      int64
	Other      int64
	Expense    int64
	Balance    int64 // sobra = receitas - gastos
	SavingsPct float64
	Status     string
	StatusOK   bool
	Closing    int64 // caixa no fim do mês, ancorado no caixa informado
	HasClosing bool
}

// Summarize aggregates entries per month for [from, to].
func Summarize(entries []Entry, from, to Month, lim Limits) []MonthSummary {
	if to < from {
		return nil
	}
	out := make([]MonthSummary, int(to-from)+1)
	for i := range out {
		out[i].Month = from.Add(i)
	}
	for _, e := range entries {
		m := MonthOf(e.Date)
		if m < from || m > to {
			continue
		}
		s := &out[m-from]
		if e.Kind == Income {
			s.Income += e.Amount
			continue
		}
		s.Expense += e.Amount
		switch e.Group() {
		case Card:
			s.Card += e.Amount
		case Fixed:
			s.Fixed += e.Amount
		default:
			s.Other += e.Amount
		}
	}
	for i := range out {
		s := &out[i]
		s.Balance = s.Income - s.Expense
		if s.Income > 0 {
			s.SavingsPct = float64(s.Balance) / float64(s.Income)
		}
		s.Status, s.StatusOK = status(*s, lim)
	}
	return out
}

// status reproduces the spreadsheet's "Status" column.
func status(s MonthSummary, lim Limits) (string, bool) {
	if s.Income == 0 && s.Expense == 0 {
		return "", true
	}
	lowSavings := s.Balance < lim.MinSavings
	overCard := lim.CardLimit > 0 && s.Card > lim.CardLimit
	switch {
	case lowSavings && overCard:
		return "Sobra e cartão", false
	case lowSavings:
		return "Sobra < meta", false
	case overCard:
		return "Cartão > limite", false
	}
	return "OK", true
}
