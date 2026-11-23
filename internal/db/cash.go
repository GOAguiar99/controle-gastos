package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// CashAccount is one part of the cash: a bank account or an investment with
// liquidity, yielding CDIPct % of the CDI (gross).
type CashAccount struct {
	ID          int64
	Name        string
	CDIPct      float64
	Balance     int64
	BalanceDate time.Time // zero = never informed
}

func (s *Store) CashAccounts() ([]CashAccount, error) {
	rows, err := s.query(`SELECT id, name, cdi_pct, balance_cents, balance_date FROM cash_accounts ORDER BY balance_cents DESC, lower(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CashAccount
	for rows.Next() {
		var a CashAccount
		var d *time.Time
		if err := rows.Scan(&a.ID, &a.Name, &a.CDIPct, &a.Balance, &d); err != nil {
			return nil, err
		}
		if d != nil {
			a.BalanceDate = *d
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SaveCashAccount inserts (ID == 0) or updates name and yield of an account.
func (s *Store) SaveCashAccount(a CashAccount) (int64, error) {
	if a.ID == 0 {
		return s.insert(`INSERT INTO cash_accounts (name, cdi_pct) VALUES ($1, $2)`, a.Name, a.CDIPct)
	}
	return a.ID, s.exec(`UPDATE cash_accounts SET name = $1, cdi_pct = $2 WHERE id = $3`, a.Name, a.CDIPct, a.ID)
}

func (s *Store) DeleteCashAccount(id int64) error {
	return s.exec(`DELETE FROM cash_accounts WHERE id = $1`, id)
}

// SetCashBalances records each account's balance on date and the cash
// snapshot as their sum, in one transaction.
func (s *Store) SetCashBalances(date time.Time, balances map[int64]int64, note string) error {
	return s.InTx(func(st *Store) error {
		var total int64
		for id, v := range balances {
			if err := st.exec(`UPDATE cash_accounts SET balance_cents = $1, balance_date = $2 WHERE id = $3`, v, date, id); err != nil {
				return err
			}
			total += v
		}
		return st.AddSnapshot(date, total, note)
	})
}

// MarketRate is a public rate (% a.a.) and the date it refers to; ok=false
// when it was never fetched.
func (s *Store) MarketRate(name string) (value float64, asOf time.Time, ok bool, err error) {
	err = s.queryRow(`SELECT value, as_of FROM market_rates WHERE name = $1`, name).Scan(&value, &asOf)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, asOf, false, nil
	}
	return value, asOf, err == nil, err
}

// SetMarketRate stores the latest value of a public rate.
func (d *DB) SetMarketRate(ctx context.Context, name string, value float64, asOf time.Time) error {
	_, err := d.pool.Exec(ctx, `INSERT INTO market_rates (name, value, as_of) VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE SET value = EXCLUDED.value, as_of = EXCLUDED.as_of, fetched_at = now()`, name, value, asOf)
	return err
}

// SetMarketRate on a ledger store writes the same shared table (used by tests
// and tools that only hold a Store).
func (s *Store) SetMarketRate(name string, value float64, asOf time.Time) error {
	return s.exec(`INSERT INTO market_rates (name, value, as_of) VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE SET value = EXCLUDED.value, as_of = EXCLUDED.as_of, fetched_at = now()`, name, value, asOf)
}
