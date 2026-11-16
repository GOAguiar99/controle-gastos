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

// SaveDebt creates (id == 0) or updates a debt and returns its id.
func (s *Service) SaveDebt(id int64, in db.DebtInput) (int64, error) {
	if err := CheckDebt(&in); err != nil {
		return 0, err
	}
	if id == 0 {
		return s.st.AddDebt(in)
	}
	if _, err := s.st.Debt(id); err != nil {
		return 0, notFound(err, "dívida")
	}
	return id, s.st.UpdateDebt(id, in)
}

func (s *Service) DeleteDebt(id int64) (db.Debt, error) {
	d, err := s.st.Debt(id)
	if err != nil {
		return d, notFound(err, "dívida")
	}
	return d, s.st.DeleteDebt(id)
}

func (s *Service) Debts() ([]db.Debt, error) { return s.st.Debts(s.Today()) }
