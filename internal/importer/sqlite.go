package importer

import (
	"database/sql"
	"fmt"
	"io"
	"time"

	_ "modernc.org/sqlite" // reads the legacy single-user database

	"controle-gastos/internal/db"
	"controle-gastos/internal/finance"
)

// FromSQLite copies every row of the legacy SQLite database (the app before
// PostgreSQL) into the ledger of st, in one transaction: either all of it is
// copied or nothing is. Ids are remapped; the ledger should be empty.
func FromSQLite(st *db.Store, path string, out io.Writer) error {
	src, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=foreign_keys(1)")
	if err != nil {
		return err
	}
	defer src.Close()
	if err := src.Ping(); err != nil {
		return fmt.Errorf("abrir %s: %w", path, err)
	}
	return st.InTx(func(st *db.Store) error {
		c := &sqliteCopy{src: src, st: st, cats: map[int64]int64{}, debts: map[int64]int64{}, recs: map[int64]int64{}}
		for _, step := range []struct {
			name string
			fn   func() (int, error)
		}{
			{"configurações", c.settings},
			{"categorias", c.categories},
			{"dívidas", c.debtRows},
			{"recorrências", c.recurrences},
			{"ocorrências puladas", c.skips},
			{"lançamentos", c.transactions},
			{"caixa informado", c.snapshots},
		} {
			n, err := step.fn()
			if err != nil {
				return fmt.Errorf("%s: %w", step.name, err)
			}
			fmt.Fprintf(out, "%-20s %d\n", step.name, n)
		}
		return nil
	})
}

type sqliteCopy struct {
	src               *sql.DB
	st                *db.Store
	cats, debts, recs map[int64]int64 // old id -> new id
}

// each runs q on the source and calls fn for every row.
func (c *sqliteCopy) each(q string, fn func(*sql.Rows) error) (int, error) {
	rows, err := c.src.Query(q)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		if err := fn(rows); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

func parseDate(s string) (time.Time, error) { return time.Parse("2006-01-02", s) }

func (c *sqliteCopy) settings() (int, error) {
	return c.each(`SELECT key, value FROM settings`, func(r *sql.Rows) error {
		var k, v string
		if err := r.Scan(&k, &v); err != nil {
			return err
		}
		return c.st.SetSetting(k, v)
	})
}

func (c *sqliteCopy) categories() (int, error) {
	return c.each(`SELECT id, name, kind, bucket FROM categories`, func(r *sql.Rows) error {
		var id int64
		var name, kind, bucket string
		if err := r.Scan(&id, &name, &kind, &bucket); err != nil {
			return err
		}
		nid, err := c.st.EnsureCategory(name, finance.Kind(kind), finance.Bucket(bucket))
		if err != nil {
			return err
		}
		c.cats[id] = nid
		// the new ledger is seeded with defaults whose bucket may differ
		return c.st.SetCategoryBucket(nid, finance.Bucket(bucket))
	})
}

func (c *sqliteCopy) debtRows() (int, error) {
	return c.each(`SELECT id, name, initial_cents, system, monthly_rate, term_months, COALESCE(first_due, ''),
		fees_cents, anchor_n, anchor_balance_cents FROM debts`, func(r *sql.Rows) error {
		var id int64
		var in db.DebtInput
		var first string
		if err := r.Scan(&id, &in.Name, &in.Initial, &in.System, &in.MonthlyRate, &in.Term, &first,
			&in.Fees, &in.AnchorN, &in.AnchorBalance); err != nil {
			return err
		}
		if first != "" {
			t, err := parseDate(first)
			if err != nil {
				return err
			}
			in.FirstDue = t
		}
		nid, err := c.st.AddDebt(in)
		c.debts[id] = nid
		return err
	})
}

func (c *sqliteCopy) recurrences() (int, error) {
	return c.each(`SELECT id, kind, description, category_id, amount_cents, day_of_month, interval_months, start_month,
		end_mode, COALESCE(occurrences, 0), COALESCE(end_month, ''), active, COALESCE(debt_id, 0), pending FROM recurrences`,
		func(r *sql.Rows) error {
			var rec finance.Recurrence
			var start, end string
			var catID, debtID int64
			if err := r.Scan(&rec.ID, &rec.Kind, &rec.Description, &catID, &rec.Amount, &rec.Day, &rec.Interval, &start,
				&rec.EndMode, &rec.Count, &end, &rec.Active, &debtID, &rec.Pending); err != nil {
				return err
			}
			old := rec.ID
			var err error
			if rec.Start, err = finance.ParseMonth(start); err != nil {
				return err
			}
			if end != "" {
				if rec.End, err = finance.ParseMonth(end); err != nil {
					return err
				}
			}
			rec.ID, rec.CategoryID, rec.DebtID = 0, c.cats[catID], c.debts[debtID]
			nid, err := c.st.SaveRecurrence(rec)
			c.recs[old] = nid
			return err
		})
}

func (c *sqliteCopy) skips() (int, error) {
	return c.each(`SELECT recurrence_id, month FROM recurrence_skips`, func(r *sql.Rows) error {
		var id int64
		var ms string
		if err := r.Scan(&id, &ms); err != nil {
			return err
		}
		m, err := finance.ParseMonth(ms)
		if err != nil {
			return err
		}
		return c.st.Skip(c.recs[id], m)
	})
}

func (c *sqliteCopy) transactions() (int, error) {
	return c.each(`SELECT kind, date, amount_cents, category_id, description, COALESCE(recurrence_id, 0),
		COALESCE(occurrence_month, ''), COALESCE(debt_id, 0), source FROM transactions ORDER BY id`, func(r *sql.Rows) error {
		var in db.TxInput
		var date string
		var catID, recID, debtID int64
		if err := r.Scan(&in.Kind, &date, &in.Amount, &catID, &in.Description, &recID, &in.OccMonth, &debtID, &in.Source); err != nil {
			return err
		}
		t, err := parseDate(date)
		if err != nil {
			return err
		}
		in.Date, in.CategoryID, in.RecurrenceID, in.DebtID = t, c.cats[catID], c.recs[recID], c.debts[debtID]
		if in.RecurrenceID == 0 {
			in.OccMonth = ""
		}
		_, err = c.st.InsertTx(in)
		return err
	})
}

func (c *sqliteCopy) snapshots() (int, error) {
	return c.each(`SELECT date, amount_cents, note FROM cash_snapshots ORDER BY id`, func(r *sql.Rows) error {
		var date, note string
		var amount int64
		if err := r.Scan(&date, &amount, &note); err != nil {
			return err
		}
		t, err := parseDate(date)
		if err != nil {
			return err
		}
		return c.st.AddSnapshot(t, amount, note)
	})
}
