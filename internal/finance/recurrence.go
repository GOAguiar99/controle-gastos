package finance

import (
	"fmt"
	"sort"
	"time"
)

type Kind string

const (
	Income  Kind = "income"
	Expense Kind = "expense"
)

// Bucket groups expenses the same way the old dashboard did (Cartão / Fixos / Outros).
type Bucket string

const (
	Card  Bucket = "cartao"
	Fixed Bucket = "fixo"
	Other Bucket = "outros"
)

const (
	EndNever = "never"
	EndCount = "count"
	EndUntil = "until"
)

// Recurrence is a fixed income/expense that repeats every Interval months.
// Its occurrences are computed on the fly, never stored, so editing it
// updates every future month at once.
type Recurrence struct {
	ID          int64
	Kind        Kind
	Description string
	CategoryID  int64
	Category    string
	Bucket      Bucket
	Amount      int64 // cents
	Day         int
	Interval    int // months between occurrences (1 = mensal, 12 = anual)
	Start       Month
	EndMode     string
	Count       int   // EndCount: number of occurrences
	End         Month // EndUntil: last month (inclusive)
	Active      bool
	DebtID      int64 // debt this recurring payment pays down (0 = none)
}

func (r Recurrence) interval() int {
	if r.Interval < 1 {
		return 1
	}
	return r.Interval
}

// LastMonth returns the last month with an occurrence, if the recurrence ends.
func (r Recurrence) LastMonth() (Month, bool) {
	switch r.EndMode {
	case EndCount:
		return r.Start.Add((r.Count - 1) * r.interval()), true
	case EndUntil:
		return r.End, true
	}
	return 0, false
}

// Months lists the occurrence months within [from, to].
func (r Recurrence) Months(from, to Month) []Month {
	if !r.Active {
		return nil
	}
	if last, ok := r.LastMonth(); ok && last < to {
		to = last
	}
	iv := r.interval()
	m := r.Start
	if from > m {
		k := (int(from-m) + iv - 1) / iv
		m = m.Add(k * iv)
	}
	var out []Month
	for ; m <= to; m = m.Add(iv) {
		out = append(out, m)
	}
	return out
}

// Next returns up to n occurrence months starting at from.
func (r Recurrence) Next(from Month, n int) []Month {
	ms := r.Months(from, from.Add(n*r.interval()))
	if len(ms) > n {
		ms = ms[:n]
	}
	return ms
}

// MonthlyEquivalent spreads the amount over the interval (anual de 1200 → 100/mês).
func (r Recurrence) MonthlyEquivalent() int64 { return r.Amount / int64(r.interval()) }

func (r Recurrence) FrequencyLabel() string {
	switch r.interval() {
	case 1:
		return "Mensal"
	case 2:
		return "Bimestral"
	case 3:
		return "Trimestral"
	case 6:
		return "Semestral"
	case 12:
		return "Anual"
	}
	return fmt.Sprintf("A cada %d meses", r.interval())
}

func (r Recurrence) EndLabel() string {
	switch r.EndMode {
	case EndCount:
		last, _ := r.LastMonth()
		return fmt.Sprintf("%d× (até %s)", r.Count, last.Label())
	case EndUntil:
		return "até " + r.End.Label()
	}
	return "sem fim"
}

// Entry is one income or expense line: a real transaction or a projected occurrence.
type Entry struct {
	ID           int64 // transaction id; 0 when Projected
	Kind         Kind
	Date         time.Time
	Amount       int64 // cents, always positive
	CategoryID   int64
	Category     string
	Bucket       Bucket
	Description  string
	RecurrenceID int64
	OccMonth     string
	DebtID       int64
	Projected    bool
}

// Signed returns +amount for income and -amount for expense.
func (e Entry) Signed() int64 {
	if e.Kind == Income {
		return e.Amount
	}
	return -e.Amount
}

// MonthSet is recurrenceID → months.
type MonthSet map[int64]map[Month]bool

func (s MonthSet) Add(id int64, m Month) {
	if s[id] == nil {
		s[id] = map[Month]bool{}
	}
	s[id][m] = true
}

func (s MonthSet) Has(id int64, m Month) bool { return s[id][m] }

// Project expands recurrences into entries for [from, to]. Months already
// covered by a real transaction (done) are left out; skipped months are
// returned separately so the UI can offer to restore them.
func Project(recs []Recurrence, done, skipped MonthSet, from, to Month) (active, skippedEntries []Entry) {
	for _, r := range recs {
		for _, m := range r.Months(from, to) {
			if done.Has(r.ID, m) {
				continue
			}
			e := Entry{
				Kind: r.Kind, Date: m.Day(r.Day), Amount: r.Amount,
				CategoryID: r.CategoryID, Category: r.Category, Bucket: r.Bucket,
				Description: r.Description, RecurrenceID: r.ID, OccMonth: m.String(), Projected: true,
			}
			if skipped.Has(r.ID, m) {
				skippedEntries = append(skippedEntries, e)
			} else {
				active = append(active, e)
			}
		}
	}
	return active, skippedEntries
}

// SortEntries orders by date, then real before projected, then id.
func SortEntries(es []Entry) {
	sort.SliceStable(es, func(i, j int) bool {
		if !es[i].Date.Equal(es[j].Date) {
			return es[i].Date.Before(es[j].Date)
		}
		if es[i].Projected != es[j].Projected {
			return !es[i].Projected
		}
		return es[i].ID < es[j].ID
	})
}
