package db

import (
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// FXQuote is a published exchange rate: reais per unit of a currency on a day.
type FXQuote struct {
	Currency string
	Date     time.Time
	Rate     float64
}

// FXRateOn is the latest stored rate of currency on or before date, no older
// than oldest (ok=false when there is none in that window).
func (s *Store) FXRateOn(currency string, date, oldest time.Time) (q FXQuote, ok bool, err error) {
	q.Currency = currency
	err = s.queryRow(`SELECT date, rate FROM fx_rates WHERE currency = $1 AND date <= $2 AND date >= $3
		ORDER BY date DESC LIMIT 1`, currency, date, oldest).Scan(&q.Date, &q.Rate)
	if errors.Is(err, pgx.ErrNoRows) {
		return q, false, nil
	}
	return q, err == nil, err
}

// SaveFXRates stores published rates (shared, not tenant data).
func (s *Store) SaveFXRates(qs []FXQuote) error {
	return s.InTx(func(st *Store) error {
		for _, q := range qs {
			if err := st.exec(`INSERT INTO fx_rates (currency, date, rate) VALUES ($1, $2, $3)
				ON CONFLICT (currency, date) DO UPDATE SET rate = EXCLUDED.rate, fetched_at = now()`, q.Currency, q.Date, q.Rate); err != nil {
				return err
			}
		}
		return nil
	})
}
