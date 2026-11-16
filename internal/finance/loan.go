package finance

import (
	"math"
	"time"
)

// Amortization systems.
const (
	LoanNone  = "none"  // sem juros: saldo = valor inicial - pagamentos
	LoanSAC   = "sac"   // amortização constante (parcela cai todo mês)
	LoanPrice = "price" // parcela constante (tabela Price)
)

// Loan describes an interest-bearing debt. The schedule is generated from the
// contract terms; Anchor* lets the user pin the real balance after a given
// installment (taken from the bank statement), so monetary correction (TR)
// and rounding already applied by the bank are respected from there on.
type Loan struct {
	System        string
	Principal     int64   // valor financiado (cents)
	MonthlyRate   float64 // % ao mês, e.g. 0.9631393
	Term          int     // total de parcelas
	FirstDue      time.Time
	Fees          int64 // seguros/taxas por parcela (cents), entram no valor pago mas não amortizam
	AnchorN       int   // parcela após a qual o saldo é conhecido (0 = nenhuma)
	AnchorBalance int64 // saldo devedor após a parcela AnchorN
}

type Installment struct {
	N        int
	Due      time.Time
	Amort    int64
	Interest int64
	Fees     int64
	Total    int64
	Balance  int64 // saldo devedor após esta parcela
}

func (l Loan) HasInterest() bool { return l.System == LoanSAC || l.System == LoanPrice }

// Schedule returns all installments. Before the anchor the rows are computed from
// the principal (approximate); from the anchor on they follow the real balance.
func (l Loan) Schedule() []Installment {
	if !l.HasInterest() || l.Term <= 0 || l.Principal <= 0 {
		return nil
	}
	r := l.MonthlyRate / 100
	out := make([]Installment, 0, l.Term)
	bal := float64(l.Principal)
	for n := 1; n <= l.Term; n++ {
		if l.AnchorN > 0 && n == l.AnchorN+1 {
			bal = float64(l.AnchorBalance)
		}
		remaining := l.Term - n + 1
		interest := round2(bal * r)
		var amort float64
		switch l.System {
		case LoanSAC:
			amort = round2(bal / float64(remaining))
		case LoanPrice:
			pmt := bal / float64(remaining)
			if r > 0 {
				pmt = bal * r / (1 - math.Pow(1+r, -float64(remaining)))
			}
			amort = round2(pmt) - interest
		}
		if n == l.Term || amort > bal {
			amort = bal
		}
		bal = round2(bal - amort)
		if bal < 0 {
			bal = 0
		}
		fees := l.Fees
		if n == l.Term {
			fees = 0
		}
		out = append(out, Installment{
			N: n, Due: l.DueDate(n), Amort: int64(amort), Interest: int64(interest), Fees: fees,
			Total: int64(amort) + int64(interest) + fees, Balance: int64(bal),
		})
	}
	return out
}

// round2 rounds a cents value to the nearest cent (values are already in cents).
func round2(v float64) float64 { return math.Round(v) }

// DueDate is the due date of installment n (1-based), keeping the day of FirstDue.
func (l Loan) DueDate(n int) time.Time {
	return MonthOf(l.FirstDue).Add(n - 1).Day(l.FirstDue.Day())
}

// InstallmentFor returns the installment due in month m, if any.
func (l Loan) InstallmentFor(sched []Installment, m Month) (Installment, bool) {
	n := int(m-MonthOf(l.FirstDue)) + 1
	if n < 1 || n > len(sched) {
		return Installment{}, false
	}
	return sched[n-1], true
}

// LoanStatus summarizes a loan given how many installments were paid.
type LoanStatus struct {
	Paid         int   // parcelas pagas
	Balance      int64 // saldo devedor atual
	Amortized    int64 // principal já abatido
	InterestPaid int64
	InterestLeft int64
	Next         *Installment
	Last         Installment
	ProgressPct  float64 // % do principal quitado
}

func (l Loan) Status(paid int) LoanStatus {
	sched := l.Schedule()
	st := LoanStatus{Paid: paid, Balance: l.Principal}
	if len(sched) == 0 {
		return st
	}
	if paid > len(sched) {
		paid = len(sched)
		st.Paid = paid
	}
	if paid > 0 {
		st.Balance = sched[paid-1].Balance
	}
	for i, in := range sched {
		if i < paid {
			st.InterestPaid += in.Interest
		} else {
			st.InterestLeft += in.Interest
		}
	}
	if paid < len(sched) {
		next := sched[paid]
		st.Next = &next
	}
	st.Last = sched[len(sched)-1]
	st.Amortized = l.Principal - st.Balance
	if l.Principal > 0 {
		st.ProgressPct = float64(st.Amortized) / float64(l.Principal)
	}
	return st
}
