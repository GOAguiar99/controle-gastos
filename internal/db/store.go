package db

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"controle-gastos/internal/finance"
)

// querier is implemented by both the pool and a transaction.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store reads and writes one ledger's data. Row-Level Security limits every
// query to that ledger (see package doc); get one from DB.Ledger.
type Store struct {
	pool     *pgxpool.Pool
	q        querier
	ctx      context.Context
	ledgerID int64
	userID   int64 // who is acting; recorded as created_by on new transactions (0 = unknown)
}

// LedgerID is the ledger this store is bound to.
func (s *Store) LedgerID() int64 { return s.ledgerID }

// WithContext returns a copy of the store whose queries use ctx (cancellation,
// deadlines), still bound to the same ledger.
func (s *Store) WithContext(ctx context.Context) *Store {
	c := *s
	c.ctx = withLedger(ctx, s.ledgerID)
	return &c
}

// WithUser returns a copy of the store that records userID as the author of
// the transactions it creates.
func (s *Store) WithUser(userID int64) *Store {
	c := *s
	c.userID = userID
	return &c
}

// InTx runs fn with a store whose queries share one database transaction:
// either everything fn does is saved or nothing is. Nested calls join the
// outer transaction.
func (s *Store) InTx(fn func(*Store) error) error {
	if _, ok := s.q.(pgx.Tx); ok {
		return fn(s)
	}
	return pgx.BeginFunc(s.ctx, s.pool, func(tx pgx.Tx) error {
		c := *s
		c.q = tx
		return fn(&c)
	})
}

func (s *Store) exec(sql string, args ...any) error {
	_, err := s.q.Exec(s.ctx, sql, args...)
	return err
}

func (s *Store) query(sql string, args ...any) (pgx.Rows, error) {
	return s.q.Query(s.ctx, sql, args...)
}

func (s *Store) queryRow(sql string, args ...any) pgx.Row { return s.q.QueryRow(s.ctx, sql, args...) }

// insert runs an INSERT ... RETURNING id.
func (s *Store) insert(sql string, args ...any) (int64, error) {
	var id int64
	err := s.q.QueryRow(s.ctx, sql+` RETURNING id`, args...).Scan(&id)
	return id, err
}

// nz turns 0 into SQL NULL for optional foreign keys.
func nz(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nzs(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// ---------- categories ----------

type Category struct {
	ID   int64
	Name string
	Kind finance.Kind
}

func (s *Store) Categories(kind finance.Kind) ([]Category, error) {
	q := `SELECT id, name, kind FROM categories`
	args := []any{}
	if kind != "" {
		q += ` WHERE kind = $1`
		args = append(args, kind)
	}
	q += ` ORDER BY kind, lower(name)`
	rows, err := s.query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// EnsureCategory returns the id of (name, kind), creating it if missing.
func (s *Store) EnsureCategory(name string, kind finance.Kind) (int64, error) {
	if err := s.exec(`INSERT INTO categories (name, kind) VALUES ($1, $2) ON CONFLICT DO NOTHING`, name, kind); err != nil {
		return 0, err
	}
	var id int64
	err := s.queryRow(`SELECT id FROM categories WHERE name = $1 AND kind = $2`, name, kind).Scan(&id)
	return id, err
}

func (s *Store) Category(id int64) (Category, error) {
	var c Category
	err := s.queryRow(`SELECT id, name, kind FROM categories WHERE id = $1`, id).Scan(&c.ID, &c.Name, &c.Kind)
	return c, err
}

// ---------- transactions ----------

type TxInput struct {
	Kind         finance.Kind
	Date         time.Time
	Amount       int64
	CategoryID   int64
	Description  string
	RecurrenceID int64
	OccMonth     string
	DebtID       int64
	Source       string
	Payment      finance.PaymentMethod // "" saves as outro; the service fills the default
	Fixed        bool
	// Card purchases: card, statement and the statement's due date (set together).
	CardID    int64
	Statement finance.Month
	CashDate  time.Time
	// Parcelado: parcel N of Of. Rows of one InsertTxs call with the same
	// nonzero GroupKey share a new installment group (the first row's id),
	// unless InstallmentGroup names an existing one to join.
	InstallmentN, InstallmentOf int
	GroupKey                    int
	InstallmentGroup            int64
	// Another currency: Amount is in reais, converted from OrigAmount cents
	// of Currency at FXRate. "" = entered in reais (the others are ignored).
	Currency   string
	OrigAmount int64
	FXRate     float64
}

const txSelect = `SELECT t.id, t.kind, t.date, t.amount_cents, t.category_id, c.name, t.payment_method, t.fixed, t.description,
	COALESCE(t.recurrence_id, 0), COALESCE(t.occurrence_month, ''), COALESCE(t.debt_id, 0),
	COALESCE(t.created_by, 0), COALESCE(NULLIF(u.name, ''), u.email, ''),
	COALESCE(t.card_id, 0), COALESCE(k.name, ''), COALESCE(t.statement_month, ''), t.cash_date,
	COALESCE(t.installment_group, 0), COALESCE(t.installment_n, 0), COALESCE(t.installment_of, 0),
	COALESCE(t.currency, ''), COALESCE(t.orig_amount_cents, 0), COALESCE(t.fx_rate, 0)
	FROM transactions t JOIN categories c ON c.id = t.category_id LEFT JOIN users u ON u.id = t.created_by
	LEFT JOIN cards k ON k.id = t.card_id`

func scanTx(sc interface{ Scan(...any) error }) (finance.Entry, error) {
	var e finance.Entry
	var statement string
	var cash *time.Time
	err := sc.Scan(&e.ID, &e.Kind, &e.Date, &e.Amount, &e.CategoryID, &e.Category, &e.Payment, &e.Fixed, &e.Description,
		&e.RecurrenceID, &e.OccMonth, &e.DebtID, &e.CreatedByID, &e.CreatedBy,
		&e.CardID, &e.Card, &statement, &cash, &e.InstallmentGroup, &e.InstallmentN, &e.InstallmentOf,
		&e.Currency, &e.OrigAmount, &e.FXRate)
	if err != nil {
		return e, err
	}
	if cash != nil {
		e.CashDate = *cash
	}
	if statement != "" {
		e.Statement, err = finance.ParseMonth(statement)
	}
	return e, err
}

// Transactions returns real transactions with from <= date <= to, plus card
// purchases whose fatura is due in that range (they count when it is due).
func (s *Store) Transactions(from, to time.Time) ([]finance.Entry, error) {
	rows, err := s.query(txSelect+` WHERE t.date BETWEEN $1 AND $2 OR t.cash_date BETWEEN $1 AND $2 ORDER BY t.date, t.id`,
		from, to)
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

// FirstTxDate returns the date of the oldest transaction (ok=false when there are none).
func (s *Store) FirstTxDate() (t time.Time, ok bool, err error) {
	var date *time.Time
	if err = s.queryRow(`SELECT MIN(date) FROM transactions`).Scan(&date); err != nil || date == nil {
		return t, false, err
	}
	return *date, true, nil
}

func (s *Store) AllTransactions() ([]finance.Entry, error) {
	return s.Transactions(time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC))
}

func (s *Store) Tx(id int64) (finance.Entry, error) {
	return scanTx(s.queryRow(txSelect+` WHERE t.id = $1`, id))
}

const txInsert = `INSERT INTO transactions (kind, date, amount_cents, category_id, description, recurrence_id, occurrence_month, debt_id, source, created_by, payment_method, fixed,
	card_id, statement_month, cash_date, installment_n, installment_of, installment_group, currency, orig_amount_cents, fx_rate)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)`

// txArgs builds the insert arguments; the author is always the store's
// acting user, never something from the input.
func (s *Store) txArgs(in TxInput) []any {
	if in.Source == "" {
		in.Source = "manual"
	}
	if in.Payment == "" {
		in.Payment = finance.OtherPay
	}
	args := []any{in.Kind, in.Date, in.Amount, in.CategoryID, in.Description,
		nz(in.RecurrenceID), nzs(in.OccMonth), nz(in.DebtID), in.Source, nz(s.userID), in.Payment, in.Fixed,
		nz(in.CardID), statementArg(in), cashArg(in), nzi(in.InstallmentN), nzi(in.InstallmentOf), nil}
	return append(args, fxArgs(in)...)
}

// fxArgs are currency, orig_amount_cents and fx_rate (all NULL in reais).
func fxArgs(in TxInput) []any {
	if in.Currency == "" || in.Currency == finance.BRLCode {
		return []any{nil, nil, nil}
	}
	return []any{in.Currency, in.OrigAmount, in.FXRate}
}

func statementArg(in TxInput) any {
	if in.CardID == 0 || in.Statement == 0 {
		return nil
	}
	return in.Statement.String()
}

func cashArg(in TxInput) any {
	if in.CardID == 0 || in.CashDate.IsZero() {
		return nil
	}
	return in.CashDate
}

func nzi(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

func (s *Store) InsertTx(in TxInput) (int64, error) {
	return s.insert(txInsert, s.txArgs(in)...)
}

// InsertTxs inserts all transactions in one SQL transaction: either every row
// is saved or none is.
func (s *Store) InsertTxs(ins []TxInput) ([]int64, error) {
	ids := make([]int64, 0, len(ins))
	err := s.InTx(func(s *Store) error {
		groups := map[int]int64{} // GroupKey -> installment group (first parcel's id)
		for _, in := range ins {
			id, err := s.insert(txInsert, s.txArgs(in)...)
			if err != nil {
				return err
			}
			ids = append(ids, id)
			if in.GroupKey == 0 && in.InstallmentGroup == 0 {
				continue
			}
			g, ok := groups[in.GroupKey]
			switch {
			case in.InstallmentGroup != 0:
				g = in.InstallmentGroup
			case !ok:
				g = id
				groups[in.GroupKey] = id
			}
			if err := s.exec(`UPDATE transactions SET installment_group = $1 WHERE id = $2`, g, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// TxSource reports where a transaction came from (manual, import, mcp).
func (s *Store) TxSource(id int64) (string, error) {
	var src string
	err := s.queryRow(`SELECT source FROM transactions WHERE id = $1`, id).Scan(&src)
	return src, err
}

// OccurrenceConfirmed reports whether month m of recurrence recID already has a transaction.
func (s *Store) OccurrenceConfirmed(recID int64, m finance.Month) (bool, error) {
	var n int
	err := s.queryRow(`SELECT count(*) FROM transactions WHERE recurrence_id = $1 AND occurrence_month = $2`, recID, m.String()).Scan(&n)
	return n > 0, err
}

// UpdateTx edits the user-editable fields; the recurrence link and the fixed
// flag are kept.
func (s *Store) UpdateTx(id int64, in TxInput) error {
	if in.Payment == "" {
		in.Payment = finance.OtherPay
	}
	fx := fxArgs(in)
	err := s.exec(`UPDATE transactions SET date = $1, amount_cents = $2, category_id = $3, description = $4, debt_id = $5, payment_method = $6,
		card_id = $7, statement_month = $8, cash_date = $9, currency = $11, orig_amount_cents = $12, fx_rate = $13 WHERE id = $10`,
		in.Date, in.Amount, in.CategoryID, in.Description, nz(in.DebtID), in.Payment, nz(in.CardID), statementArg(in), cashArg(in), id,
		fx[0], fx[1], fx[2])
	return err
}

// LastPaymentMethod is how the most recent transaction in the category was
// paid (ok=false when it has none), used as the default for a new one.
func (s *Store) LastPaymentMethod(categoryID int64) (p finance.PaymentMethod, ok bool, err error) {
	err = s.queryRow(`SELECT payment_method FROM transactions WHERE category_id = $1 ORDER BY date DESC, id DESC LIMIT 1`, categoryID).Scan(&p)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return p, err == nil, err
}

func (s *Store) DeleteTx(id int64) error {
	err := s.exec(`DELETE FROM transactions WHERE id = $1`, id)
	return err
}

// ---------- recurrences ----------

const recSelect = `SELECT r.id, r.kind, r.description, r.category_id, c.name, r.payment_method, COALESCE(r.card_id, 0), r.amount_cents, r.day_of_month,
	r.interval_months, r.start_month, r.end_mode, COALESCE(r.occurrences, 0), COALESCE(r.end_month, ''), r.active,
	COALESCE(r.debt_id, 0), r.pending
	FROM recurrences r JOIN categories c ON c.id = r.category_id`

func scanRec(sc interface{ Scan(...any) error }) (finance.Recurrence, error) {
	var r finance.Recurrence
	var start, end string
	err := sc.Scan(&r.ID, &r.Kind, &r.Description, &r.CategoryID, &r.Category, &r.Payment, &r.CardID, &r.Amount, &r.Day,
		&r.Interval, &start, &r.EndMode, &r.Count, &end, &r.Active, &r.DebtID, &r.Pending)
	if err != nil {
		return r, err
	}
	if r.Start, err = finance.ParseMonth(start); err != nil {
		return r, err
	}
	if end != "" {
		r.End, err = finance.ParseMonth(end)
	}
	return r, err
}

func (s *Store) Recurrences() ([]finance.Recurrence, error) {
	rows, err := s.query(recSelect + ` ORDER BY r.kind, r.active DESC, r.amount_cents DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []finance.Recurrence
	for rows.Next() {
		r, err := scanRec(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Recurrence(id int64) (finance.Recurrence, error) {
	return scanRec(s.queryRow(recSelect+` WHERE r.id = $1`, id))
}

// SaveRecurrence inserts (ID == 0) or updates a recurrence and returns its id.
func (s *Store) SaveRecurrence(r finance.Recurrence) (int64, error) {
	var count, end any
	switch r.EndMode {
	case finance.EndCount:
		count = r.Count
	case finance.EndUntil:
		end = r.End.String()
	default:
		r.EndMode = finance.EndNever
	}
	if r.Interval < 1 {
		r.Interval = 1
	}
	if r.Payment == "" {
		r.Payment = finance.OtherPay
	}
	if r.ID == 0 {
		return s.insert(`INSERT INTO recurrences (kind, description, category_id, amount_cents, day_of_month, interval_months, start_month, end_mode, occurrences, end_month, active, debt_id, pending, payment_method, card_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
			r.Kind, r.Description, r.CategoryID, r.Amount, r.Day, r.Interval, r.Start.String(), r.EndMode, count, end, r.Active, nz(r.DebtID), r.Pending, r.Payment, nz(r.CardID))
	}
	err := s.exec(`UPDATE recurrences SET description = $1, category_id = $2, amount_cents = $3, day_of_month = $4, interval_months = $5,
		start_month = $6, end_mode = $7, occurrences = $8, end_month = $9, active = $10, debt_id = $11, pending = $12, payment_method = $13, card_id = $14 WHERE id = $15`,
		r.Description, r.CategoryID, r.Amount, r.Day, r.Interval, r.Start.String(), r.EndMode, count, end, r.Active, nz(r.DebtID), r.Pending, r.Payment, nz(r.CardID), r.ID)
	if err != nil {
		return r.ID, err
	}
	return r.ID, s.linkRecurrencePayments(r.ID, r.DebtID)
}

// linkRecurrencePayments marks the recurrence's confirmed occurrences as payments
// of debtID (only those not already pointing at another debt).
func (s *Store) linkRecurrencePayments(recID, debtID int64) error {
	if debtID == 0 {
		return nil
	}
	err := s.exec(`UPDATE transactions SET debt_id = $1 WHERE recurrence_id = $2 AND debt_id IS NULL`, debtID, recID)
	return err
}

// AcceptRecurrence turns a suggested recurrence into an active one.
func (s *Store) AcceptRecurrence(id int64) error {
	err := s.exec(`UPDATE recurrences SET pending = false, active = true WHERE id = $1`, id)
	return err
}

// PendingRecurrences counts suggestions waiting for the user (nav badge).
func (s *Store) PendingRecurrences() int {
	var n int
	s.queryRow(`SELECT count(*) FROM recurrences WHERE pending`).Scan(&n)
	return n
}

func (s *Store) DeleteRecurrence(id int64) error {
	err := s.exec(`DELETE FROM recurrences WHERE id = $1`, id)
	return err
}

func (s *Store) Skip(recID int64, m finance.Month) error {
	err := s.exec(`INSERT INTO recurrence_skips (recurrence_id, month) VALUES ($1, $2) ON CONFLICT DO NOTHING`, recID, m.String())
	return err
}

func (s *Store) Unskip(recID int64, m finance.Month) error {
	err := s.exec(`DELETE FROM recurrence_skips WHERE recurrence_id = $1 AND month = $2`, recID, m.String())
	return err
}

func (s *Store) monthSet(q string) (finance.MonthSet, error) {
	rows, err := s.query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := finance.MonthSet{}
	for rows.Next() {
		var id int64
		var ms string
		if err := rows.Scan(&id, &ms); err != nil {
			return nil, err
		}
		m, err := finance.ParseMonth(ms)
		if err != nil {
			return nil, err
		}
		set.Add(id, m)
	}
	return set, rows.Err()
}

// Entries returns real transactions plus projected recurrence occurrences
// for the months [from, to], sorted by date. Skipped occurrences come separately.
func (s *Store) Entries(from, to finance.Month) (active, skipped []finance.Entry, err error) {
	txs, err := s.Transactions(from.First(), to.Last())
	if err != nil {
		return nil, nil, err
	}
	recs, err := s.Recurrences()
	if err != nil {
		return nil, nil, err
	}
	done, err := s.monthSet(`SELECT recurrence_id, occurrence_month FROM transactions WHERE recurrence_id IS NOT NULL AND occurrence_month IS NOT NULL`)
	if err != nil {
		return nil, nil, err
	}
	sk, err := s.monthSet(`SELECT recurrence_id, month FROM recurrence_skips`)
	if err != nil {
		return nil, nil, err
	}
	// card occurrences count when their fatura is due, up to two months later
	proj, skipped := finance.Project(recs, done, sk, from.Add(-2), to)
	if err := s.applyLoanInstallments(recs, proj); err != nil {
		return nil, nil, err
	}
	cards, err := s.CardMap()
	if err != nil {
		return nil, nil, err
	}
	billProjected(cards, proj)
	billProjected(cards, skipped)
	proj, skipped = inWindow(proj, from, to), inWindow(skipped, from, to)
	active = append(txs, proj...)
	rests, err := s.statementRests(cards, from, to, proj)
	if err != nil {
		return nil, nil, err
	}
	active = append(active, rests...)
	finance.SortEntries(active)
	finance.SortEntries(skipped)
	return active, skipped, nil
}

// applyLoanInstallments replaces the fixed amount of projected occurrences of
// recurrences that pay an interest-bearing debt with that month's installment
// (amortização + juros + seguros), so SAC parcels shrink month by month.
func (s *Store) applyLoanInstallments(recs []finance.Recurrence, proj []finance.Entry) error {
	recDebt := map[int64]int64{}
	for _, r := range recs {
		if r.DebtID != 0 {
			recDebt[r.ID] = r.DebtID
		}
	}
	if len(recDebt) == 0 {
		return nil
	}
	loans, err := s.Loans()
	if err != nil {
		return err
	}
	scheds := map[int64][]finance.Installment{}
	for i := range proj {
		debtID := recDebt[proj[i].RecurrenceID]
		loan, ok := loans[debtID]
		if !ok || !loan.HasInterest() {
			continue
		}
		if _, ok := scheds[debtID]; !ok {
			scheds[debtID] = loan.Schedule()
		}
		if in, ok := loan.InstallmentFor(scheds[debtID], finance.MonthOf(proj[i].Date)); ok {
			proj[i].Amount = in.Total
			proj[i].DebtID = debtID
		}
	}
	return nil
}

// FirstMonth is the month of the oldest transaction (or fallback).
func (s *Store) FirstMonth(fallback finance.Month) finance.Month {
	t, ok, err := s.FirstTxDate()
	if err != nil || !ok {
		return fallback
	}
	return finance.MonthOf(t)
}

// ---------- cash ----------

func (s *Store) LatestSnapshot() (finance.Snapshot, error) {
	var snap finance.Snapshot
	err := s.queryRow(`SELECT date, amount_cents FROM cash_snapshots ORDER BY date DESC, id DESC LIMIT 1`).Scan(&snap.Date, &snap.Amount)
	if errors.Is(err, pgx.ErrNoRows) {
		return snap, nil
	}
	snap.Set = err == nil
	return snap, err
}

// SnapshotRow is one "caixa hoje" entry as stored.
type SnapshotRow struct {
	Date      time.Time
	Amount    int64
	Note      string
	CreatedAt time.Time
}

// Snapshots lists every cash snapshot, oldest first.
func (s *Store) Snapshots() ([]SnapshotRow, error) {
	rows, err := s.query(`SELECT date, amount_cents, note, created_at FROM cash_snapshots ORDER BY date, id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SnapshotRow, error) {
		var x SnapshotRow
		err := r.Scan(&x.Date, &x.Amount, &x.Note, &x.CreatedAt)
		return x, err
	})
}

func (s *Store) AddSnapshot(date time.Time, amount int64, note string) error {
	err := s.exec(`INSERT INTO cash_snapshots (date, amount_cents, note) VALUES ($1, $2, $3)`, date, amount, note)
	return err
}

// ---------- debts ----------

// Debt is either a simple debt (saldo = inicial - pagamentos) or a loan with
// interest, whose balance follows the amortization schedule: each payment
// made up to today counts as one installment.
type Debt struct {
	ID       int64
	Name     string
	Initial  int64 // valor inicial / financiado
	Paid     int64 // soma dos pagamentos vinculados
	Payments int   // pagamentos vinculados até hoje
	Loan     finance.Loan
	Status   finance.LoanStatus // só para dívidas com juros
}

func (d Debt) HasInterest() bool { return d.Loan.HasInterest() }

func (d Debt) Current() int64 {
	if d.HasInterest() {
		return d.Status.Balance
	}
	return d.Initial - d.Paid
}

// Progress is the share of the original amount already paid off (0..1).
func (d Debt) Progress() float64 {
	if d.HasInterest() {
		return d.Status.ProgressPct
	}
	if d.Initial <= 0 {
		return 0
	}
	return min(1, float64(d.Paid)/float64(d.Initial))
}

func (d Debt) PaidPct() int { return int(d.Progress() * 100) }

type DebtInput struct {
	Name          string
	Initial       int64
	System        string
	MonthlyRate   float64
	Term          int
	FirstDue      time.Time
	Fees          int64
	AnchorN       int
	AnchorBalance int64
}

const debtSelect = `SELECT d.id, d.name, d.initial_cents, d.system, d.monthly_rate, d.term_months, d.first_due,
	d.fees_cents, d.anchor_n, d.anchor_balance_cents FROM debts d`

func scanDebt(sc interface{ Scan(...any) error }) (Debt, error) {
	var d Debt
	var first *time.Time
	err := sc.Scan(&d.ID, &d.Name, &d.Initial, &d.Loan.System, &d.Loan.MonthlyRate, &d.Loan.Term, &first,
		&d.Loan.Fees, &d.Loan.AnchorN, &d.Loan.AnchorBalance)
	if err != nil {
		return d, err
	}
	d.Loan.Principal = d.Initial
	if first != nil {
		d.Loan.FirstDue = *first
	}
	return d, nil
}

// Loans returns every debt's loan terms by id.
func (s *Store) Loans() (map[int64]finance.Loan, error) {
	rows, err := s.query(debtSelect)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]finance.Loan{}
	for rows.Next() {
		d, err := scanDebt(rows)
		if err != nil {
			return nil, err
		}
		out[d.ID] = d.Loan
	}
	return out, rows.Err()
}

// Debts returns all debts with their payments made up to today.
func (s *Store) Debts(today time.Time) ([]Debt, error) {
	rows, err := s.query(debtSelect + ` ORDER BY d.name`)
	if err != nil {
		return nil, err
	}
	var out []Debt
	for rows.Next() {
		d, err := scanDebt(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		d := &out[i]
		err := s.queryRow(`SELECT COALESCE(SUM(amount_cents), 0), COUNT(*) FROM transactions WHERE debt_id = $1 AND date <= $2`,
			d.ID, today).Scan(&d.Paid, &d.Payments)
		if err != nil {
			return nil, err
		}
		if d.HasInterest() {
			d.Status = d.Loan.Status(d.Payments)
		}
	}
	return out, nil
}

func (s *Store) Debt(id int64) (Debt, error) {
	return scanDebt(s.queryRow(debtSelect+` WHERE d.id = $1`, id))
}

func debtArgs(in DebtInput) []any {
	if in.System != finance.LoanSAC && in.System != finance.LoanPrice {
		in = DebtInput{Name: in.Name, Initial: in.Initial, System: finance.LoanNone}
	}
	var first any
	if !in.FirstDue.IsZero() {
		first = in.FirstDue
	}
	return []any{in.Name, in.Initial, in.System, in.MonthlyRate, in.Term, first, in.Fees, in.AnchorN, in.AnchorBalance}
}

func (s *Store) AddDebt(in DebtInput) (int64, error) {
	return s.insert(`INSERT INTO debts (name, initial_cents, system, monthly_rate, term_months, first_due, fees_cents, anchor_n, anchor_balance_cents)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, debtArgs(in)...)
}

func (s *Store) UpdateDebt(id int64, in DebtInput) error {
	args := append(debtArgs(in), id)
	err := s.exec(`UPDATE debts SET name = $1, initial_cents = $2, system = $3, monthly_rate = $4, term_months = $5, first_due = $6,
		fees_cents = $7, anchor_n = $8, anchor_balance_cents = $9 WHERE id = $10`, args...)
	return err
}

func (s *Store) DeleteDebt(id int64) error {
	err := s.exec(`DELETE FROM debts WHERE id = $1`, id)
	return err
}

// ---------- settings ----------

// Setting returns a text setting ("" when missing).
func (s *Store) Setting(key string) string {
	var v string
	if err := s.queryRow(`SELECT value FROM ledger_settings WHERE key = $1`, key).Scan(&v); err != nil {
		return ""
	}
	return v
}

func (s *Store) SetSetting(key, v string) error {
	err := s.exec(`INSERT INTO ledger_settings (key, value) VALUES ($1, $2) ON CONFLICT (ledger_id, key) DO UPDATE SET value = excluded.value`, key, v)
	return err
}

func (s *Store) SettingInt(key string) int64 {
	var v string
	if err := s.queryRow(`SELECT value FROM ledger_settings WHERE key = $1`, key).Scan(&v); err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

func (s *Store) SetSettingInt(key string, v int64) error {
	err := s.exec(`INSERT INTO ledger_settings (key, value) VALUES ($1, $2) ON CONFLICT (ledger_id, key) DO UPDATE SET value = excluded.value`,
		key, strconv.FormatInt(v, 10))
	return err
}

func (s *Store) Limits() finance.Limits {
	return finance.Limits{CardLimit: s.SettingInt("card_limit_cents"), MinSavings: s.SettingInt("min_savings_cents")}
}

// IsEmpty reports whether there is no user data yet (used by the importer).
func (s *Store) IsEmpty() (bool, error) {
	var n int
	err := s.queryRow(`SELECT (SELECT count(*) FROM transactions) + (SELECT count(*) FROM recurrences)`).Scan(&n)
	return n == 0, err
}

// Reset deletes the ledger's data but keeps its categories and settings.
func (s *Store) Reset() error {
	return s.InTx(func(s *Store) error {
		for _, t := range []string{"transactions", "recurrence_skips", "recurrences", "debts", "cash_snapshots"} {
			if err := s.exec(`DELETE FROM ` + t); err != nil {
				return err
			}
		}
		return nil
	})
}

// inWindow keeps the entries that count in [from, to] (see Entry.Month).
func inWindow(es []finance.Entry, from, to finance.Month) []finance.Entry {
	out := es[:0]
	for _, e := range es {
		if m := e.Month(); m >= from && m <= to {
			out = append(out, e)
		}
	}
	return out
}

// Context is the context the store's queries use.
func (s *Store) Context() context.Context { return s.ctx }
