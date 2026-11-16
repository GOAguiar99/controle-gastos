package app

import (
	"strings"
	"time"

	"controle-gastos/internal/db"
	"controle-gastos/internal/finance"
)

// RecIn creates (ID == 0) or updates a recurrence.
type RecIn struct {
	ID          int64
	Kind        finance.Kind
	Description string // defaults to the category name
	Category    CategoryRef
	Amount      int64 // cents, > 0
	Day         int   // day of month, 1–31 (clamped to the month's length)
	Interval    int   // 1 mensal, 2 bimestral, 3 trimestral, 6 semestral, 12 anual
	Start       finance.Month
	EndMode     string        // never | count | until
	Count       int           // EndMode count: number of occurrences
	End         finance.Month // EndMode until: last month (inclusive)
	Active      bool
	DebtID      int64 // expense paying down this debt
}

func checkSchedule(in *RecIn) error {
	switch in.Interval {
	case 1, 2, 3, 6, 12:
	default:
		return invalid("periodicidade inválida (1, 2, 3, 6 ou 12 meses)")
	}
	if in.Day < 1 || in.Day > 31 {
		return invalid("dia inválido")
	}
	if in.Start == 0 {
		return invalid("mês inicial inválido")
	}
	switch in.EndMode {
	case finance.EndCount:
		if in.Count < 1 || in.Count > 600 {
			return invalid("número de vezes inválido")
		}
	case finance.EndUntil:
		if in.End == 0 {
			return invalid("mês final inválido")
		}
		if in.End < in.Start {
			return invalid("mês final antes do inicial")
		}
	case finance.EndNever, "":
		in.EndMode = finance.EndNever
	default:
		return invalid("término inválido (never, count ou until)")
	}
	return nil
}

func (s *Service) SaveRecurrence(in RecIn) (int64, error) {
	if _, err := ParseKind(string(in.Kind)); err != nil {
		return 0, err
	}
	if in.ID != 0 {
		old, err := s.st.Recurrence(in.ID)
		if err != nil {
			return 0, notFound(err, "recorrência")
		}
		if old.Kind != in.Kind {
			return 0, invalid("recorrência não encontrada")
		}
	}
	if in.Amount <= 0 {
		return 0, invalid("valor inválido")
	}
	if err := checkSchedule(&in); err != nil {
		return 0, err
	}
	if in.DebtID != 0 {
		if in.Kind != finance.Expense {
			return 0, invalid("só gastos podem abater dívidas")
		}
		debt, err := s.st.Debt(in.DebtID)
		if err != nil {
			return 0, invalid("dívida inválida")
		}
		// a loan's installments come from one recurrence; a second would double them
		if debt.HasInterest() {
			recs, err := s.st.Recurrences()
			if err != nil {
				return 0, err
			}
			for _, r := range recs {
				if r.DebtID == in.DebtID && r.ID != in.ID {
					return 0, invalid("o financiamento %s já é pago pela recorrência %q (#%d); edite essa", debt.Name, r.Description, r.ID)
				}
			}
		}
	}
	c, err := s.resolveCategory(in.Kind, in.Category)
	if err != nil {
		return 0, err
	}
	desc := strings.TrimSpace(in.Description)
	if desc == "" {
		desc = c.Name
	}
	return s.st.SaveRecurrence(finance.Recurrence{
		ID: in.ID, Kind: in.Kind, Description: desc, CategoryID: c.ID, Amount: in.Amount, Day: in.Day,
		Interval: in.Interval, Start: in.Start, EndMode: in.EndMode, Count: in.Count, End: in.End,
		Active: in.Active, DebtID: in.DebtID,
	})
}

// RecurrenceFromTx turns a transaction into the recurrence that starts with
// it: same kind, category, amount and day, starting in the transaction's month.
func (s *Service) RecurrenceFromTx(tx TxIn, sched RecIn) (int64, error) {
	sched.ID = 0
	sched.Kind, sched.Description, sched.Category, sched.Amount = tx.Kind, tx.Description, tx.Category, tx.Amount
	sched.Day, sched.Active = tx.Date.Day(), true
	if !tx.Date.IsZero() {
		sched.Start = finance.MonthOf(tx.Date)
	}
	if tx.Kind == finance.Expense {
		sched.DebtID = tx.DebtID
	}
	return s.SaveRecurrence(sched)
}

func (s *Service) Recurrence(id int64) (finance.Recurrence, error) {
	r, err := s.st.Recurrence(id)
	return r, notFound(err, "recorrência")
}

// Recurrences lists recurrences of one kind ("" = all).
func (s *Service) Recurrences(kind finance.Kind) ([]finance.Recurrence, error) {
	all, err := s.st.Recurrences()
	if err != nil || kind == "" {
		return all, err
	}
	var out []finance.Recurrence
	for _, r := range all {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *Service) DeleteRecurrence(id int64) (finance.Recurrence, error) {
	r, err := s.Recurrence(id)
	if err != nil {
		return r, err
	}
	return r, s.st.DeleteRecurrence(id)
}

// occurrence checks that month m is a scheduled occurrence of recurrence id.
func (s *Service) occurrence(id int64, m finance.Month) (finance.Recurrence, error) {
	r, err := s.Recurrence(id)
	if err != nil {
		return r, err
	}
	if m == 0 {
		return r, invalid("mês inválido")
	}
	probe := r
	probe.Active = true
	if len(probe.Months(m, m)) == 0 {
		return r, invalid("%s não é um mês desta recorrência (%s)", m.Label(), r.FrequencyLabel())
	}
	return r, nil
}

// ConfirmOccurrence records the real transaction for month m of a recurrence.
// amount 0 keeps the recurrence's amount; a zero date uses its usual day.
func (s *Service) ConfirmOccurrence(id int64, m finance.Month, amount int64, date time.Time, source string) (int64, error) {
	r, err := s.occurrence(id, m)
	if err != nil {
		return 0, err
	}
	if done, err := s.st.OccurrenceConfirmed(id, m); err != nil {
		return 0, err
	} else if done {
		return 0, invalid("%s de %q já foi confirmado", m.Label(), r.Description)
	}
	if amount == 0 {
		amount = r.Amount
		// loan payments project that month's installment, not the fixed amount
		if es, _, err := s.st.Entries(m, m); err == nil {
			for _, e := range es {
				if e.Projected && e.RecurrenceID == id {
					amount = e.Amount
				}
			}
		}
	}
	if amount <= 0 {
		return 0, invalid("valor inválido")
	}
	if date.IsZero() {
		date = m.Day(r.Day)
	}
	return s.st.InsertTx(db.TxInput{
		Kind: r.Kind, Date: date, Amount: amount, CategoryID: r.CategoryID, Description: r.Description,
		RecurrenceID: r.ID, OccMonth: m.String(), DebtID: r.DebtID, Source: source,
	})
}

func (s *Service) SkipOccurrence(id int64, m finance.Month) (finance.Recurrence, error) {
	r, err := s.occurrence(id, m)
	if err != nil {
		return r, err
	}
	return r, s.st.Skip(id, m)
}

func (s *Service) UnskipOccurrence(id int64, m finance.Month) (finance.Recurrence, error) {
	r, err := s.Recurrence(id)
	if err != nil {
		return r, err
	}
	return r, s.st.Unskip(id, m)
}
