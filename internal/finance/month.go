// Package finance holds the pure business rules: months, recurrences,
// monthly summaries and the cash-flow projection. No I/O here.
package finance

import (
	"fmt"
	"time"
)

// Month is a calendar month encoded as year*12 + (month-1), so arithmetic is trivial.
type Month int

func NewMonth(y int, m time.Month) Month { return Month(y*12 + int(m) - 1) }
func MonthOf(t time.Time) Month          { return NewMonth(t.Year(), t.Month()) }

func (m Month) Year() int        { return int(m) / 12 }
func (m Month) Mon() time.Month  { return time.Month(int(m)%12 + 1) }
func (m Month) Add(n int) Month  { return m + Month(n) }
func (m Month) String() string   { return fmt.Sprintf("%04d-%02d", m.Year(), int(m.Mon())) }
func (m Month) First() time.Time { return time.Date(m.Year(), m.Mon(), 1, 0, 0, 0, 0, time.UTC) }
func (m Month) Last() time.Time  { return m.Add(1).First().AddDate(0, 0, -1) }

// Day returns the given day of the month, clamped to the month's last day (31 → 28/29/30).
func (m Month) Day(d int) time.Time {
	if d < 1 {
		d = 1
	}
	if last := m.Last().Day(); d > last {
		d = last
	}
	return time.Date(m.Year(), m.Mon(), d, 0, 0, 0, 0, time.UTC)
}

func ParseMonth(s string) (Month, error) {
	t, err := time.Parse("2006-01", s)
	if err != nil {
		return 0, fmt.Errorf("mês inválido %q", s)
	}
	return MonthOf(t), nil
}

var monthShort = [...]string{"jan", "fev", "mar", "abr", "mai", "jun", "jul", "ago", "set", "out", "nov", "dez"}
var monthLong = [...]string{"Janeiro", "Fevereiro", "Março", "Abril", "Maio", "Junho", "Julho", "Agosto", "Setembro", "Outubro", "Novembro", "Dezembro"}

// Label is the short pt-BR label, e.g. "nov/2026".
func (m Month) Label() string { return fmt.Sprintf("%s/%d", monthShort[m.Mon()-1], m.Year()) }

// LongLabel is e.g. "Novembro de 2026".
func (m Month) LongLabel() string { return fmt.Sprintf("%s de %d", monthLong[m.Mon()-1], m.Year()) }

// Date parses a YYYY-MM-DD date as UTC midnight.
func ParseDate(s string) (time.Time, error) { return time.Parse("2006-01-02", s) }

// DateOnly truncates a time to its calendar date in UTC.
func DateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
