package app

import (
	"strings"

	"controle-gastos/internal/db"
	"controle-gastos/internal/finance"
)

// CheckDebt validates a debt; System other than sac/price means a simple
// debt without interest, whose loan fields are cleared.
func CheckDebt(in *db.DebtInput) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || in.Initial <= 0 {
		return invalid("nome e valor inicial são obrigatórios")
	}
	if in.System != finance.LoanSAC && in.System != finance.LoanPrice {
		*in = db.DebtInput{Name: in.Name, Initial: in.Initial, System: finance.LoanNone}
		return nil
	}
	if in.MonthlyRate < 0 || in.MonthlyRate > 20 {
		return invalid("taxa mensal inválida")
	}
	if in.Term < 1 || in.Term > 600 {
		return invalid("prazo inválido")
	}
	if in.FirstDue.IsZero() {
		return invalid("data da 1ª parcela inválida")
	}
	if in.Fees < 0 {
		return invalid("seguros/taxas inválidos")
	}
	if in.AnchorN < 0 || in.AnchorN >= in.Term {
		return invalid("parcela do saldo informado inválida")
	}
	if in.AnchorN > 0 && in.AnchorBalance <= 0 {
		return invalid("saldo devedor informado inválido")
	}
	if in.AnchorN == 0 {
		in.AnchorBalance = 0
	}
	return nil
}

// DebtSaved reports what SaveDebt did.
type DebtSaved struct {
	ID           int64
	RecurrenceID int64 // installment recurrence created now (0 = none created)
}

// SaveDebt creates (id == 0) or updates a debt. A loan with interest (SAC or
// Price) that no recurrence pays yet gets one created for its installments,
// so they show up as projected expenses month by month. Simple debts don't:
// their payments are launched one by one.
func (s *Service) SaveDebt(id int64, in db.DebtInput) (DebtSaved, error) {
	if err := CheckDebt(&in); err != nil {
		return DebtSaved{}, err
	}
	var err error
	if id == 0 {
		if id, err = s.st.AddDebt(in); err != nil {
			return DebtSaved{}, err
		}
	} else {
		if _, err := s.st.Debt(id); err != nil {
			return DebtSaved{}, notFound(err, "dívida")
		}
		if err := s.st.UpdateDebt(id, in); err != nil {
			return DebtSaved{}, err
		}
	}
	recID, err := s.ensureLoanRecurrence(id)
	return DebtSaved{ID: id, RecurrenceID: recID}, err
}

// LoanCategory is where automatic installment recurrences go.
const LoanCategory = "Financiamento"

// ensureLoanRecurrence creates the monthly recurrence paying a loan's
// remaining installments, unless one already pays it. It starts at the
// first due month or, for loans already running, the current month (earlier
// installments are history, not pending), and ends with the last one.
func (s *Service) ensureLoanRecurrence(debtID int64) (int64, error) {
	d, err := s.st.Debt(debtID)
	if err != nil || !d.HasInterest() {
		return 0, err
	}
	recs, err := s.st.Recurrences()
	if err != nil {
		return 0, err
	}
	for _, r := range recs {
		if r.DebtID == debtID {
			return 0, nil
		}
	}
	sched := d.Loan.Schedule()
	first := finance.MonthOf(d.Loan.FirstDue)
	start := max(first, finance.MonthOf(s.Today()))
	done := int(start - first) // installments due before start
	if done >= len(sched) {
		return 0, nil // already paid off
	}
	cat, ok, err := s.findCategory(finance.Expense, LoanCategory)
	if err != nil {
		return 0, err
	}
	if !ok {
		if cat.ID, err = s.st.EnsureCategory(LoanCategory, finance.Expense, finance.Fixed); err != nil {
			return 0, err
		}
	}
	return s.SaveRecurrence(RecIn{
		Kind: finance.Expense, Description: d.Name, Category: CategoryRef{ID: cat.ID},
		Amount: sched[done].Total, Day: d.Loan.FirstDue.Day(), Interval: 1, Start: start,
		EndMode: finance.EndCount, Count: len(sched) - done, Active: true, DebtID: debtID,
	})
}

// DeleteDebt removes a debt and the recurrences that pay it (their future
// installments would make no sense anymore). Payments already launched stay
// as expenses.
func (s *Service) DeleteDebt(id int64) (db.Debt, error) {
	d, err := s.st.Debt(id)
	if err != nil {
		return d, notFound(err, "dívida")
	}
	recs, err := s.st.Recurrences()
	if err != nil {
		return d, err
	}
	for _, r := range recs {
		if r.DebtID == id {
			if err := s.st.DeleteRecurrence(r.ID); err != nil {
				return d, err
			}
		}
	}
	return d, s.st.DeleteDebt(id)
}

func (s *Service) Debts() ([]db.Debt, error) { return s.st.Debts(s.Today()) }
