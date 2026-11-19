package db

import (
	"errors"
	"sort"

	"github.com/jackc/pgx/v5"

	"controle-gastos/internal/finance"
)

// ---------- cards ----------

const cardSelect = `SELECT id, name, closing_day, due_day, active FROM cards`

func scanCard(sc interface{ Scan(...any) error }) (finance.CreditCard, error) {
	var c finance.CreditCard
	err := sc.Scan(&c.ID, &c.Name, &c.ClosingDay, &c.DueDay, &c.Active)
	return c, err
}

// Cards lists the ledger's credit cards, active first.
func (s *Store) Cards() ([]finance.CreditCard, error) {
	rows, err := s.query(cardSelect + ` ORDER BY active DESC, lower(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []finance.CreditCard
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CardMap is every card by id.
func (s *Store) CardMap() (map[int64]finance.CreditCard, error) {
	cs, err := s.Cards()
	if err != nil {
		return nil, err
	}
	m := make(map[int64]finance.CreditCard, len(cs))
	for _, c := range cs {
		m[c.ID] = c
	}
	return m, nil
}

func (s *Store) Card(id int64) (finance.CreditCard, error) {
	return scanCard(s.queryRow(cardSelect+` WHERE id = $1`, id))
}

// SaveCard inserts (ID == 0) or updates a card and returns its id.
func (s *Store) SaveCard(c finance.CreditCard) (int64, error) {
	if c.ID == 0 {
		return s.insert(`INSERT INTO cards (name, closing_day, due_day, active) VALUES ($1, $2, $3, $4)`,
			c.Name, c.ClosingDay, c.DueDay, c.Active)
	}
	err := s.exec(`UPDATE cards SET name = $1, closing_day = $2, due_day = $3, active = $4 WHERE id = $5`,
		c.Name, c.ClosingDay, c.DueDay, c.Active, c.ID)
	return c.ID, err
}

// RedateCard moves the cash date of every purchase on the card to its
// statement's due date (after the due day changed). Statements are kept.
func (s *Store) RedateCard(c finance.CreditCard) error {
	months, err := s.cardStatementMonths(c.ID)
	if err != nil {
		return err
	}
	return s.InTx(func(s *Store) error {
		for _, m := range months {
			if err := s.exec(`UPDATE transactions SET cash_date = $1 WHERE card_id = $2 AND statement_month = $3`,
				c.Due(m), c.ID, m.String()); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) cardStatementMonths(cardID int64) ([]finance.Month, error) {
	rows, err := s.query(`SELECT DISTINCT statement_month FROM transactions WHERE card_id = $1`, cardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []finance.Month
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		m, err := finance.ParseMonth(v)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CardTxCount is how many transactions are charged to the card.
func (s *Store) CardTxCount(id int64) (int, error) {
	var n int
	err := s.queryRow(`SELECT count(*) FROM transactions WHERE card_id = $1`, id).Scan(&n)
	return n, err
}

// DeleteCard removes a card with no transactions (and its declared totals).
func (s *Store) DeleteCard(id int64) error {
	return s.exec(`DELETE FROM cards WHERE id = $1`, id)
}

// ---------- statements ----------

// SetStatementTotal records the total the bank charges for a statement
// (valor cheio); 0 clears it.
func (s *Store) SetStatementTotal(cardID int64, m finance.Month, cents int64) error {
	if cents == 0 {
		return s.exec(`DELETE FROM card_statements WHERE card_id = $1 AND month = $2`, cardID, m.String())
	}
	return s.exec(`INSERT INTO card_statements (card_id, month, total_cents) VALUES ($1, $2, $3)
		ON CONFLICT (ledger_id, card_id, month) DO UPDATE SET total_cents = EXCLUDED.total_cents`, cardID, m.String(), cents)
}

type statementKey struct {
	card  int64
	month finance.Month
}

// Statements sums every statement of the cards in [from, to]: the items
// stored on it and the declared total. Months with neither are left out.
func (s *Store) Statements(cards map[int64]finance.CreditCard, from, to finance.Month) ([]finance.Statement, error) {
	sums := map[statementKey]*finance.Statement{}
	get := func(k statementKey) *finance.Statement {
		st, ok := sums[k]
		if !ok {
			st = &finance.Statement{Card: cards[k.card], Month: k.month}
			sums[k] = st
		}
		return st
	}
	rows, err := s.query(`SELECT card_id, statement_month, sum(amount_cents), count(*) FROM transactions
		WHERE card_id IS NOT NULL AND statement_month BETWEEN $1 AND $2 GROUP BY 1, 2`, from.String(), to.String())
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k statementKey
		var m string
		var sum int64
		var n int
		if err := rows.Scan(&k.card, &m, &sum, &n); err != nil {
			rows.Close()
			return nil, err
		}
		if k.month, err = finance.ParseMonth(m); err != nil {
			rows.Close()
			return nil, err
		}
		st := get(k)
		st.Items, st.Count = sum, n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.query(`SELECT card_id, month, total_cents FROM card_statements WHERE month BETWEEN $1 AND $2`, from.String(), to.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k statementKey
		var m string
		var total int64
		if err := rows.Scan(&k.card, &m, &total); err != nil {
			return nil, err
		}
		if k.month, err = finance.ParseMonth(m); err != nil {
			return nil, err
		}
		st := get(k)
		st.Declared, st.HasTotal = total, true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]finance.Statement, 0, len(sums))
	for _, st := range sums {
		if _, ok := cards[st.Card.ID]; ok {
			out = append(out, *st)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Month != out[j].Month {
			return out[i].Month > out[j].Month
		}
		return out[i].Card.Name < out[j].Card.Name
	})
	return out, nil
}

// StatementItems lists the transactions billed on one statement.
func (s *Store) StatementItems(cardID int64, m finance.Month) ([]finance.Entry, error) {
	rows, err := s.query(txSelect+` WHERE t.card_id = $1 AND t.statement_month = $2 ORDER BY t.date, t.id`, cardID, m.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []finance.Entry
	for rows.Next() {
		e, err := scanTx(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// InstallmentParcel finds parcel n of an installment purchase already on the
// card: same card, parcel number, count and amount (ok=false if none).
func (s *Store) InstallmentParcel(cardID int64, n, of int, amount int64, statement finance.Month) (finance.Entry, bool, error) {
	e, err := scanTx(s.queryRow(txSelect+` WHERE t.card_id = $1 AND t.installment_n = $2 AND t.installment_of = $3
		AND t.amount_cents = $4 AND t.statement_month = $5 ORDER BY t.id LIMIT 1`, cardID, n, of, amount, statement.String()))
	if errors.Is(err, pgx.ErrNoRows) {
		return e, false, nil
	}
	return e, err == nil, err
}

// billProjected puts projected card recurrences on their statement.
func billProjected(cards map[int64]finance.CreditCard, es []finance.Entry) {
	for i := range es {
		c, ok := cards[es[i].CardID]
		if !ok || es[i].Payment != finance.Credit {
			es[i].CardID = 0
			continue
		}
		es[i].Card = c.Name
		es[i].Statement = c.StatementFor(es[i].Date)
		es[i].CashDate = c.Due(es[i].Statement)
	}
}

// statementRests are the undetailed parts of declared statement totals whose
// closing date falls in [from, to]. Projected card recurrences on a statement
// count as its items, like the stored ones.
func (s *Store) statementRests(cards map[int64]finance.CreditCard, from, to finance.Month, proj []finance.Entry) ([]finance.Entry, error) {
	if len(cards) == 0 {
		return nil, nil
	}
	// a statement closing the month before can be due inside the window
	sts, err := s.Statements(cards, from.Add(-1), to)
	if err != nil {
		return nil, err
	}
	var out []finance.Entry
	for _, st := range sts {
		if !st.HasTotal {
			continue
		}
		if m := finance.MonthOf(st.Card.Due(st.Month)); m < from || m > to {
			continue
		}
		for _, e := range proj {
			if e.CardID == st.Card.ID && e.Statement == st.Month {
				st.Items += e.Amount
			}
		}
		if st.Rest() > 0 {
			out = append(out, st.RestEntry())
		}
	}
	return out, nil
}

// Charge bills the transaction on card c (when ok): its statement comes from
// the date and the closing day, its cash date is that statement's due date.
func (in TxInput) Charge(c finance.CreditCard, ok bool) TxInput {
	if !ok {
		return in
	}
	in.CardID = c.ID
	in.Statement = c.StatementFor(in.Date)
	in.CashDate = c.Due(in.Statement)
	return in
}

// DeleteInstallmentsFrom deletes parcels n.. of an installment group.
func (s *Store) DeleteInstallmentsFrom(group int64, n int) (int, error) {
	var count int
	err := s.queryRow(`WITH d AS (DELETE FROM transactions WHERE installment_group = $1 AND installment_n >= $2 RETURNING 1)
		SELECT count(*) FROM d`, group, n).Scan(&count)
	return count, err
}
