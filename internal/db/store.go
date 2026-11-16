package db

import (
	"database/sql"
	"errors"
	"strconv"
	"time"

	"controle-gastos/internal/finance"
)

type Store struct{ DB *sql.DB }

func (s *Store) Close() error { return s.DB.Close() }

const dateFmt = "2006-01-02"

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
	ID     int64
	Name   string
	Kind   finance.Kind
	Bucket finance.Bucket
}

func (s *Store) Categories(kind finance.Kind) ([]Category, error) {
	q := `SELECT id, name, kind, bucket FROM categories`
	args := []any{}
	if kind != "" {
		q += ` WHERE kind = ?`
		args = append(args, kind)
	}
	q += ` ORDER BY kind, name COLLATE NOCASE`
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.Bucket); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// EnsureCategory returns the id of (name, kind), creating it with bucket if missing.
func (s *Store) EnsureCategory(name string, kind finance.Kind, bucket finance.Bucket) (int64, error) {
	if bucket == "" {
		bucket = finance.Other
	}
	if _, err := s.DB.Exec(`INSERT OR IGNORE INTO categories (name, kind, bucket) VALUES (?, ?, ?)`, name, kind, bucket); err != nil {
		return 0, err
	}
	var id int64
	err := s.DB.QueryRow(`SELECT id FROM categories WHERE name = ? AND kind = ?`, name, kind).Scan(&id)
	return id, err
}

func (s *Store) SetCategoryBucket(id int64, bucket finance.Bucket) error {
	_, err := s.DB.Exec(`UPDATE categories SET bucket = ? WHERE id = ?`, bucket, id)
	return err
}

func (s *Store) Category(id int64) (Category, error) {
	var c Category
	err := s.DB.QueryRow(`SELECT id, name, kind, bucket FROM categories WHERE id = ?`, id).Scan(&c.ID, &c.Name, &c.Kind, &c.Bucket)
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
}

const txSelect = `SELECT t.id, t.kind, t.date, t.amount_cents, t.category_id, c.name, c.bucket, t.description,
	COALESCE(t.recurrence_id, 0), COALESCE(t.occurrence_month, ''), COALESCE(t.debt_id, 0)
	FROM transactions t JOIN categories c ON c.id = t.category_id`

func scanTx(sc interface{ Scan(...any) error }) (finance.Entry, error) {
	var e finance.Entry
	var date string
	err := sc.Scan(&e.ID, &e.Kind, &date, &e.Amount, &e.CategoryID, &e.Category, &e.Bucket, &e.Description,
		&e.RecurrenceID, &e.OccMonth, &e.DebtID)
	if err != nil {
		return e, err
	}
	e.Date, err = time.Parse(dateFmt, date)
	return e, err
}

// Transactions returns real transactions with from <= date <= to.
func (s *Store) Transactions(from, to time.Time) ([]finance.Entry, error) {
	rows, err := s.DB.Query(txSelect+` WHERE t.date BETWEEN ? AND ? ORDER BY t.date, t.id`,
		from.Format(dateFmt), to.Format(dateFmt))
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

func (s *Store) AllTransactions() ([]finance.Entry, error) {
	return s.Transactions(time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC))
}

func (s *Store) Tx(id int64) (finance.Entry, error) {
	return scanTx(s.DB.QueryRow(txSelect+` WHERE t.id = ?`, id))
}

func (s *Store) InsertTx(in TxInput) (int64, error) {
	if in.Source == "" {
		in.Source = "manual"
	}
	res, err := s.DB.Exec(`INSERT INTO transactions (kind, date, amount_cents, category_id, description, recurrence_id, occurrence_month, debt_id, source)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Kind, in.Date.Format(dateFmt), in.Amount, in.CategoryID, in.Description,
		nz(in.RecurrenceID), nzs(in.OccMonth), nz(in.DebtID), in.Source)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateTx edits the user-editable fields; the recurrence link is kept.
func (s *Store) UpdateTx(id int64, in TxInput) error {
	_, err := s.DB.Exec(`UPDATE transactions SET date = ?, amount_cents = ?, category_id = ?, description = ?, debt_id = ? WHERE id = ?`,
		in.Date.Format(dateFmt), in.Amount, in.CategoryID, in.Description, nz(in.DebtID), id)
	return err
}

func (s *Store) DeleteTx(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM transactions WHERE id = ?`, id)
	return err
}

// ---------- recurrences ----------

const recSelect = `SELECT r.id, r.kind, r.description, r.category_id, c.name, c.bucket, r.amount_cents, r.day_of_month,
	r.interval_months, r.start_month, r.end_mode, COALESCE(r.occurrences, 0), COALESCE(r.end_month, ''), r.active
	FROM recurrences r JOIN categories c ON c.id = r.category_id`

func scanRec(sc interface{ Scan(...any) error }) (finance.Recurrence, error) {
	var r finance.Recurrence
	var start, end string
	err := sc.Scan(&r.ID, &r.Kind, &r.Description, &r.CategoryID, &r.Category, &r.Bucket, &r.Amount, &r.Day,
		&r.Interval, &start, &r.EndMode, &r.Count, &end, &r.Active)
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
	rows, err := s.DB.Query(recSelect + ` ORDER BY r.kind, r.active DESC, r.amount_cents DESC`)
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
	return scanRec(s.DB.QueryRow(recSelect+` WHERE r.id = ?`, id))
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
	if r.ID == 0 {
		res, err := s.DB.Exec(`INSERT INTO recurrences (kind, description, category_id, amount_cents, day_of_month, interval_months, start_month, end_mode, occurrences, end_month, active)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.Kind, r.Description, r.CategoryID, r.Amount, r.Day, r.Interval, r.Start.String(), r.EndMode, count, end, r.Active)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.DB.Exec(`UPDATE recurrences SET description = ?, category_id = ?, amount_cents = ?, day_of_month = ?, interval_months = ?,
		start_month = ?, end_mode = ?, occurrences = ?, end_month = ?, active = ? WHERE id = ?`,
		r.Description, r.CategoryID, r.Amount, r.Day, r.Interval, r.Start.String(), r.EndMode, count, end, r.Active, r.ID)
	return r.ID, err
}

func (s *Store) DeleteRecurrence(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM recurrences WHERE id = ?`, id)
	return err
}

func (s *Store) Skip(recID int64, m finance.Month) error {
	_, err := s.DB.Exec(`INSERT OR IGNORE INTO recurrence_skips (recurrence_id, month) VALUES (?, ?)`, recID, m.String())
	return err
}

func (s *Store) Unskip(recID int64, m finance.Month) error {
	_, err := s.DB.Exec(`DELETE FROM recurrence_skips WHERE recurrence_id = ? AND month = ?`, recID, m.String())
	return err
}

func (s *Store) monthSet(q string) (finance.MonthSet, error) {
	rows, err := s.DB.Query(q)
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
	proj, skipped := finance.Project(recs, done, sk, from, to)
	active = append(txs, proj...)
	finance.SortEntries(active)
	finance.SortEntries(skipped)
	return active, skipped, nil
}

// FirstMonth is the month of the oldest transaction (or fallback).
func (s *Store) FirstMonth(fallback finance.Month) finance.Month {
	var d sql.NullString
	if err := s.DB.QueryRow(`SELECT min(date) FROM transactions`).Scan(&d); err != nil || !d.Valid {
		return fallback
	}
	t, err := time.Parse(dateFmt, d.String)
	if err != nil {
		return fallback
	}
	return finance.MonthOf(t)
}

// ---------- cash ----------

func (s *Store) LatestSnapshot() (finance.Snapshot, error) {
	var date string
	var snap finance.Snapshot
	err := s.DB.QueryRow(`SELECT date, amount_cents FROM cash_snapshots ORDER BY date DESC, id DESC LIMIT 1`).Scan(&date, &snap.Amount)
	if errors.Is(err, sql.ErrNoRows) {
		return snap, nil
	}
	if err != nil {
		return snap, err
	}
	snap.Date, err = time.Parse(dateFmt, date)
	snap.Set = err == nil
	return snap, err
}

func (s *Store) AddSnapshot(date time.Time, amount int64, note string) error {
	_, err := s.DB.Exec(`INSERT INTO cash_snapshots (date, amount_cents, note) VALUES (?, ?, ?)`, date.Format(dateFmt), amount, note)
	return err
}

// ---------- debts ----------

type Debt struct {
	ID      int64
	Name    string
	Initial int64
	Paid    int64
}

func (d Debt) Current() int64 { return d.Initial - d.Paid }
func (d Debt) PaidPct() int {
	if d.Initial <= 0 {
		return 0
	}
	p := int(d.Paid * 100 / d.Initial)
	if p > 100 {
		p = 100
	}
	return p
}

func (s *Store) Debts() ([]Debt, error) {
	rows, err := s.DB.Query(`SELECT d.id, d.name, d.initial_cents, COALESCE(SUM(t.amount_cents), 0)
		FROM debts d LEFT JOIN transactions t ON t.debt_id = d.id
		GROUP BY d.id ORDER BY d.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Debt
	for rows.Next() {
		var d Debt
		if err := rows.Scan(&d.ID, &d.Name, &d.Initial, &d.Paid); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) AddDebt(name string, initial int64) (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO debts (name, initial_cents) VALUES (?, ?)`, name, initial)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateDebt(id int64, name string, initial int64) error {
	_, err := s.DB.Exec(`UPDATE debts SET name = ?, initial_cents = ? WHERE id = ?`, name, initial, id)
	return err
}

func (s *Store) DeleteDebt(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM debts WHERE id = ?`, id)
	return err
}

// ---------- settings ----------

func (s *Store) SettingInt(key string) int64 {
	var v string
	if err := s.DB.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

func (s *Store) SetSettingInt(key string, v int64) error {
	_, err := s.DB.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		key, strconv.FormatInt(v, 10))
	return err
}

func (s *Store) Limits() finance.Limits {
	return finance.Limits{CardLimit: s.SettingInt("card_limit_cents"), MinSavings: s.SettingInt("min_savings_cents")}
}

// Backup writes a consistent copy of the database to path.
func (s *Store) Backup(path string) error {
	_, err := s.DB.Exec(`VACUUM INTO ?`, path)
	return err
}

// IsEmpty reports whether there is no user data yet (used by the importer).
func (s *Store) IsEmpty() (bool, error) {
	var n int
	err := s.DB.QueryRow(`SELECT (SELECT count(*) FROM transactions) + (SELECT count(*) FROM recurrences)`).Scan(&n)
	return n == 0, err
}

// Reset deletes all user data but keeps categories and settings.
func (s *Store) Reset() error {
	_, err := s.DB.Exec(`DELETE FROM transactions; DELETE FROM recurrence_skips; DELETE FROM recurrences; DELETE FROM debts; DELETE FROM cash_snapshots;`)
	return err
}
