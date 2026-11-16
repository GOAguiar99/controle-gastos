package app

import (
	"strings"
	"time"

	"controle-gastos/internal/db"
	"controle-gastos/internal/finance"
)

// TxIn is a transaction (receita or gasto) to create or update.
type TxIn struct {
	Kind        finance.Kind // ignored on update: a transaction keeps its kind
	Date        time.Time
	Amount      int64 // cents, > 0
	Category    CategoryRef
	Description string
	DebtID      int64  // expense paying down this debt (0 = none)
	Source      string // manual (default) | mcp
}

func (s *Service) checkTx(in *TxIn) error {
	if _, err := ParseKind(string(in.Kind)); err != nil {
		return err
	}
	if in.Date.IsZero() {
		return invalid("data inválida")
	}
	if in.Amount <= 0 {
		return invalid("valor inválido")
	}
	if in.DebtID != 0 {
		if in.Kind != finance.Expense {
			return invalid("só gastos podem abater dívidas")
		}
		if _, err := s.st.Debt(in.DebtID); err != nil {
			return notFound(err, "dívida")
		}
	}
	in.Description = strings.TrimSpace(in.Description)
	return nil
}

func (s *Service) txInput(in TxIn) (db.TxInput, error) {
	if err := s.checkTx(&in); err != nil {
		return db.TxInput{}, err
	}
	c, err := s.resolveCategory(in.Kind, in.Category)
	if err != nil {
		return db.TxInput{}, err
	}
	return db.TxInput{Kind: in.Kind, Date: in.Date, Amount: in.Amount, CategoryID: c.ID,
		Description: in.Description, DebtID: in.DebtID, Source: in.Source}, nil
}

// AddTx saves one transaction and returns its id.
func (s *Service) AddTx(in TxIn) (int64, error) {
	ids, err := s.AddTxs([]TxIn{in})
	if err != nil {
		return 0, err
	}
	return ids[0], nil
}

// AddTxs validates every item first and then saves them all in one SQL
// transaction: either all are saved or none is.
func (s *Service) AddTxs(ins []TxIn) ([]int64, error) {
	if len(ins) == 0 {
		return nil, invalid("nenhum lançamento informado")
	}
	// validate everything before creating any category
	for i := range ins {
		if err := s.checkTx(&ins[i]); err != nil {
			return nil, itemErr(len(ins), i, err)
		}
		if ins[i].Category.ID == 0 && strings.TrimSpace(ins[i].Category.Name) == "" {
			return nil, itemErr(len(ins), i, invalid("escolha uma categoria"))
		}
	}
	rows := make([]db.TxInput, len(ins))
	for i, in := range ins {
		row, err := s.txInput(in)
		if err != nil {
			return nil, itemErr(len(ins), i, err)
		}
		rows[i] = row
	}
	return s.st.InsertTxs(rows)
}

func itemErr(n, i int, err error) error {
	if n == 1 || !IsValidation(err) {
		return err
	}
	return invalid("item %d: %s", i+1, err.Error())
}

// UpdateTx edits date, amount, category, description and debt link.
func (s *Service) UpdateTx(id int64, in TxIn) (finance.Entry, error) {
	old, err := s.st.Tx(id)
	if err != nil {
		return old, notFound(err, "transação")
	}
	in.Kind = old.Kind
	row, err := s.txInput(in)
	if err != nil {
		return old, err
	}
	if err := s.st.UpdateTx(id, row); err != nil {
		return old, err
	}
	return s.st.Tx(id)
}

func (s *Service) DeleteTx(id int64) (finance.Entry, error) {
	old, err := s.st.Tx(id)
	if err != nil {
		return old, notFound(err, "transação")
	}
	return old, s.st.DeleteTx(id)
}

func (s *Service) Tx(id int64) (finance.Entry, error) {
	e, err := s.st.Tx(id)
	return e, notFound(err, "transação")
}

// TxFilter selects entries for ListTx. Zero values mean "no filter";
// a zero From/To defaults to the current month.
type TxFilter struct {
	From, To         finance.Month
	Kind             finance.Kind
	Category         string // name, accent/case-insensitive
	Text             string // substring of description or category
	IncludeProjected bool   // also return pending recurrence occurrences
}

// ListTx returns real transactions (and optionally projected recurrence
// occurrences) in [From, To], sorted by date.
func (s *Service) ListTx(f TxFilter) ([]finance.Entry, error) {
	cur := finance.MonthOf(s.Today())
	if f.From == 0 {
		f.From = cur
	}
	if f.To == 0 {
		f.To = max(f.From, cur)
	}
	if f.To < f.From {
		return nil, invalid("período inválido: fim antes do início")
	}
	if f.To-f.From > 12*10 {
		return nil, invalid("período longo demais (máximo 10 anos)")
	}
	all, _, err := s.st.Entries(f.From, f.To)
	if err != nil {
		return nil, err
	}
	cat, text := fold(f.Category), fold(f.Text)
	out := []finance.Entry{}
	for _, e := range all {
		switch {
		case e.Projected && !f.IncludeProjected,
			f.Kind != "" && e.Kind != f.Kind,
			cat != "" && fold(e.Category) != cat,
			text != "" && !strings.Contains(fold(e.Description+" "+e.Category), text):
			continue
		}
		out = append(out, e)
	}
	return out, nil
}
