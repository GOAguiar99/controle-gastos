package finance

import "time"

// CreditCard is a credit card. Purchases are grouped into monthly statements
// (faturas): a statement is named by the month it closes in, and its total
// leaves the cash on the due date.
type CreditCard struct {
	ID         int64
	Name       string
	ClosingDay int // dia do fechamento, 1–31 (clamped to the month's length)
	DueDay     int // dia do vencimento, 1–31
	Active     bool
}

// StatementFor is the statement a purchase on d falls into: the one closing
// this month, or the next one when bought on the closing day or after it.
func (c CreditCard) StatementFor(d time.Time) Month {
	m := MonthOf(d)
	if !d.Before(c.Closing(m)) {
		return m.Add(1)
	}
	return m
}

// Closing is the closing date of statement m.
func (c CreditCard) Closing(m Month) time.Time { return m.Day(c.ClosingDay) }

// Due is the due date of statement m: in the closing month when the due day
// comes after the closing day, otherwise in the following month.
func (c CreditCard) Due(m Month) time.Time {
	if c.DueDay > c.ClosingDay {
		return m.Day(c.DueDay)
	}
	return m.Add(1).Day(c.DueDay)
}

// StatementDueNear is the statement whose due date is closest to d (the bill
// being paid on d).
func (c CreditCard) StatementDueNear(d time.Time) Month {
	best, bestDiff := Month(0), time.Duration(-1)
	for m := MonthOf(d).Add(-2); m <= MonthOf(d).Add(1); m++ {
		diff := c.Due(m).Sub(d)
		if diff < 0 {
			diff = -diff
		}
		if bestDiff < 0 || diff < bestDiff {
			best, bestDiff = m, diff
		}
	}
	return best
}

// OpenStatement is the statement still taking purchases on day today.
func (c CreditCard) OpenStatement(today time.Time) Month { return c.StatementFor(today) }

// Statement is one fatura: the items charged to it and, optionally, the total
// the bank says it has (valor cheio). When the declared total is larger than
// the items, the difference counts as an undetailed card expense, so a fatura
// can be entered as one total, item by item, or partly both, never twice.
type Statement struct {
	Card     CreditCard
	Month    Month
	Items    int64 // soma dos lançamentos desta fatura
	Count    int
	Declared int64 // valor informado da fatura (0 = não informado)
	HasTotal bool
}

// Total is what the statement charges: the declared total or, if larger or
// not declared, the sum of its items.
func (s Statement) Total() int64 { return max(s.Items, s.Declared) }

// Rest is the undetailed part of a declared total.
func (s Statement) Rest() int64 { return max(0, s.Declared-s.Items) }

// RestCategory is the category name of the undetailed part of a fatura.
const RestCategory = "Fatura cartão"

// RestEntry is the entry standing for the undetailed part of the statement:
// dated on the closing day (competência) and paid on the due date.
func (s Statement) RestEntry() Entry {
	return Entry{
		Kind: Expense, Date: s.Card.Closing(s.Month), Amount: s.Rest(), Category: RestCategory,
		Description: "Fatura " + s.Card.Name + " (não detalhado)", Payment: Credit,
		CardID: s.Card.ID, Card: s.Card.Name, Statement: s.Month, CashDate: s.Card.Due(s.Month), StatementRest: true,
	}
}
