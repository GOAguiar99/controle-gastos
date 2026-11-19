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
	// Payment is how an expense was paid. "" = the method of the latest
	// transaction in the same category (outro if none); on update, keep it.
	// A card implies credito.
	Payment finance.PaymentMethod
	// CardID is the credit card charged (only with credito). 0 = the only
	// active card, if there is exactly one; on update, keep the current one.
	CardID int64
	// Statement forces the fatura (AAAA-MM) the purchase is billed on, as
	// when importing a statement; 0 = from the purchase date and closing day.
	Statement finance.Month
	// Parcelado: Installments is the number of parcels (2–99). InstallmentN
	// is the first parcel being entered (default 1: a new purchase, Amount is
	// its total and is split); with InstallmentN > 1 (a statement line such as
	// "3/10") Amount is one parcel and parcels N..Installments are created,
	// each on the following statement.
	Installments, InstallmentN int
	// ParcelAmount: Amount is one parcel even for parcel 1 (a statement line
	// "1/10"), not the purchase total to split.
	ParcelAmount bool
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
	if _, err := ParsePaymentMethod(string(in.Payment)); err != nil {
		return err
	}
	if in.Kind == finance.Income {
		in.Payment, in.CardID, in.Installments = finance.OtherPay, 0, 0 // receitas não têm forma de pagamento
	}
	if in.Installments == 1 {
		in.Installments = 0
	}
	if in.Installments != 0 {
		if in.Installments < 2 || in.Installments > 99 {
			return invalid("parcelas inválidas (2 a 99)")
		}
		if in.InstallmentN == 0 {
			in.InstallmentN = 1
		}
		if in.InstallmentN < 1 || in.InstallmentN > in.Installments {
			return invalid("parcela %d de %d inválida", in.InstallmentN, in.Installments)
		}
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
	pay := in.Payment
	if pay == "" && in.CardID != 0 {
		pay = finance.Credit
	}
	if pay == "" {
		if pay, err = s.defaultPayment(c.ID); err != nil {
			return db.TxInput{}, err
		}
	}
	row := db.TxInput{Kind: in.Kind, Date: in.Date, Amount: in.Amount, CategoryID: c.ID,
		Description: in.Description, DebtID: in.DebtID, Source: in.Source, Payment: pay}
	if in.Kind == finance.Expense && pay == finance.Credit {
		card, ok, err := s.chargeCard(in.CardID)
		if err != nil {
			return row, err
		}
		if ok {
			row.CardID, row.Statement = card.ID, in.Statement
			switch {
			case row.Statement != 0:
			case isBillTotal(c.Name):
				// a whole fatura entered as one expense, as before cards
				// existed: it is the bill due closest to that date
				row.Statement = card.StatementDueNear(in.Date)
			default:
				row.Statement = card.StatementFor(in.Date)
			}
			row.CashDate = card.Due(row.Statement)
		}
	}
	return row, nil
}

// isBillTotal reports whether a category holds whole-fatura totals.
func isBillTotal(category string) bool { return fold(category) == fold(finance.RestCategory) }

// chargeCard is the card a credit purchase goes on: the given one, or the
// only active card when none is given (ok=false: no card, cash moves on the
// purchase date as before cards existed).
func (s *Service) chargeCard(id int64) (finance.CreditCard, bool, error) {
	if !s.CardsEnabled() {
		if id != 0 {
			return finance.CreditCard{}, false, errCardsOff()
		}
		return finance.CreditCard{}, false, nil
	}
	if id != 0 {
		c, err := s.st.Card(id)
		if err != nil {
			return c, false, notFound(err, "cartão")
		}
		return c, true, nil
	}
	cards, err := s.st.Cards()
	if err != nil {
		return finance.CreditCard{}, false, err
	}
	var active []finance.CreditCard
	for _, c := range cards {
		if c.Active {
			active = append(active, c)
		}
	}
	if len(active) == 1 {
		return active[0], true, nil
	}
	return finance.CreditCard{}, false, nil
}

// parcels expands an installment purchase into one row per parcel, each a
// month later and on the next statement. The first parcel absorbs the cents
// left over when splitting a total.
func parcels(in TxIn, row db.TxInput, key int) []db.TxInput {
	if in.Installments == 0 {
		return []db.TxInput{row}
	}
	amount, extra := row.Amount, int64(0)
	if in.InstallmentN == 1 && !in.ParcelAmount {
		amount = row.Amount / int64(in.Installments)
		extra = row.Amount - amount*int64(in.Installments)
	}
	var out []db.TxInput
	for k := in.InstallmentN; k <= in.Installments; k++ {
		p := row
		off := k - in.InstallmentN
		p.Date = addMonths(row.Date, off)
		p.Amount = amount
		if k == in.InstallmentN {
			p.Amount += extra
		}
		p.InstallmentN, p.InstallmentOf, p.GroupKey = k, in.Installments, key
		if row.CardID != 0 {
			p.Statement = row.Statement.Add(off)
			p.CashDate = time.Time{} // recomputed by the caller from the card
		}
		out = append(out, p)
	}
	return out
}

// addMonths moves d n months ahead, clamping the day to the month's length.
func addMonths(d time.Time, n int) time.Time { return finance.MonthOf(d).Add(n).Day(d.Day()) }

// defaultPayment is how the latest transaction in the category was paid, so
// whoever keeps entering "Fatura cartão" totals keeps landing in Cartão.
func (s *Service) defaultPayment(categoryID int64) (finance.PaymentMethod, error) {
	p, ok, err := s.st.LastPaymentMethod(categoryID)
	if err != nil || !ok {
		return finance.OtherPay, err
	}
	return p, nil
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
	var rows []db.TxInput
	for i, in := range ins {
		row, err := s.txInput(in)
		if err != nil {
			return nil, itemErr(len(ins), i, err)
		}
		for _, p := range parcels(in, row, i+1) {
			if p.CardID != 0 && p.CashDate.IsZero() {
				c, err := s.st.Card(p.CardID)
				if err != nil {
					return nil, err
				}
				p.CashDate = c.Due(p.Statement)
			}
			rows = append(rows, p)
		}
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
	if in.Payment == "" {
		in.Payment = old.Payment
	}
	in.Installments = 0 // parcels are edited one by one
	// with the cards module off, a purchase already on a fatura keeps it
	keepBilling := !s.CardsEnabled() && old.CardID != 0 && in.Payment == finance.Credit && (in.CardID == 0 || in.CardID == old.CardID)
	if keepBilling {
		in.CardID = 0
	} else if in.Payment == finance.Credit && in.CardID == 0 {
		in.CardID = old.CardID
	}
	// same card and date: keep the statement it was billed (or imported) on
	if in.Statement == 0 && in.CardID == old.CardID && in.Date.Equal(old.Date) {
		in.Statement = old.Statement
	}
	row, err := s.txInput(in)
	if err != nil {
		return old, err
	}
	if keepBilling {
		row.CardID, row.Statement, row.CashDate = old.CardID, old.Statement, old.CashDate
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
