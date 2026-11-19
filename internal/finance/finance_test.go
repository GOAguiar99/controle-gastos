package finance

import (
	"reflect"
	"testing"
	"time"
)

func d(s string) time.Time {
	t, err := ParseDate(s)
	if err != nil {
		panic(err)
	}
	return t
}

func m(s string) Month {
	mm, err := ParseMonth(s)
	if err != nil {
		panic(err)
	}
	return mm
}

func labels(ms []Month) []string {
	var out []string
	for _, x := range ms {
		out = append(out, x.String())
	}
	return out
}

func TestMonthBasics(t *testing.T) {
	if got := m("2026-12").Add(1).String(); got != "2027-01" {
		t.Fatalf("Add: %s", got)
	}
	if got := m("2027-02").Day(31); !got.Equal(d("2027-02-28")) {
		t.Fatalf("clamp: %s", got)
	}
	if got := m("2028-02").Day(31); !got.Equal(d("2028-02-29")) {
		t.Fatalf("leap clamp: %s", got)
	}
	if got := m("2026-11").Label(); got != "nov/2026" {
		t.Fatalf("label: %s", got)
	}
}

func TestRecurrenceMonths(t *testing.T) {
	cases := []struct {
		name string
		r    Recurrence
		from string
		to   string
		want []string
	}{
		{"mensal sem fim", Recurrence{Interval: 1, Start: m("2026-10"), EndMode: EndNever, Active: true},
			"2026-09", "2027-01", []string{"2026-10", "2026-11", "2026-12", "2027-01"}},
		{"bimestral a partir do meio", Recurrence{Interval: 2, Start: m("2026-01"), EndMode: EndNever, Active: true},
			"2026-04", "2026-09", []string{"2026-05", "2026-07", "2026-09"}},
		{"trimestral 4 vezes", Recurrence{Interval: 3, Start: m("2026-11"), EndMode: EndCount, Count: 4, Active: true},
			"2026-01", "2028-12", []string{"2026-11", "2027-02", "2027-05", "2027-08"}},
		{"até data", Recurrence{Interval: 1, Start: m("2026-11"), EndMode: EndUntil, End: m("2027-01"), Active: true},
			"2026-01", "2027-12", []string{"2026-11", "2026-12", "2027-01"}},
		{"inativa", Recurrence{Interval: 1, Start: m("2026-01"), Active: false},
			"2026-01", "2026-12", nil},
		{"anual", Recurrence{Interval: 12, Start: m("2026-03"), EndMode: EndNever, Active: true},
			"2026-01", "2028-12", []string{"2026-03", "2027-03", "2028-03"}},
	}
	for _, c := range cases {
		got := labels(c.r.Months(m(c.from), m(c.to)))
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestProjectSkipsDoneAndSkipped(t *testing.T) {
	r := Recurrence{ID: 7, Kind: Expense, Amount: 1000, Day: 31, Interval: 1, Start: m("2027-01"), EndMode: EndNever, Active: true}
	done, skipped := MonthSet{}, MonthSet{}
	done.Add(7, m("2027-01"))
	skipped.Add(7, m("2027-03"))
	active, sk := Project([]Recurrence{r}, done, skipped, m("2027-01"), m("2027-04"))
	if len(active) != 2 || !active[0].Date.Equal(d("2027-02-28")) || !active[1].Date.Equal(d("2027-04-30")) {
		t.Fatalf("active: %+v", active)
	}
	if len(sk) != 1 || sk[0].OccMonth != "2027-03" {
		t.Fatalf("skipped: %+v", sk)
	}
}

func TestSummarizeStatus(t *testing.T) {
	es := []Entry{
		{Kind: Income, Date: d("2026-01-05"), Amount: 10000},
		{Kind: Expense, Date: d("2026-01-10"), Amount: 9000, Payment: Credit},
		{Kind: Expense, Date: d("2026-01-11"), Amount: 500, Fixed: true},
		{Kind: Expense, Date: d("2026-01-12"), Amount: 1000},
		{Kind: Income, Date: d("2026-02-05"), Amount: 10000},
		{Kind: Expense, Date: d("2026-02-10"), Amount: 1000, Payment: Credit},
	}
	s := Summarize(es, m("2026-01"), m("2026-03"), Limits{CardLimit: 8000})
	if s[0].Expense != 10500 || s[0].Balance != -500 || s[0].Status != "Sobra e cartão" {
		t.Fatalf("jan: %+v", s[0])
	}
	if s[1].Status != "OK" || !s[1].StatusOK || s[1].SavingsPct != 0.9 {
		t.Fatalf("fev: %+v", s[1])
	}
	if s[2].Status != "" {
		t.Fatalf("mar should be empty: %+v", s[2])
	}
}

func TestCashFlow(t *testing.T) {
	es := []Entry{
		{Kind: Income, Date: d("2026-11-05"), Amount: 1000},   // before snapshot: ignored for current
		{Kind: Expense, Date: d("2026-11-12"), Amount: 300},   // after snapshot, before today
		{Kind: Expense, Date: d("2026-11-20"), Amount: 2000},  // future
		{Kind: Income, Date: d("2026-12-05"), Amount: 5000},   // future
		{Kind: Expense, Date: d("2026-12-05"), Amount: 100},   // same day
		{Kind: Expense, Date: d("2027-03-01"), Amount: 99999}, // beyond horizon
	}
	snap := Snapshot{Date: d("2026-11-10"), Amount: 1500, Set: true}
	cf := ProjectCash(es, snap, d("2026-11-16"), 2)
	if cf.Current != 1200 {
		t.Fatalf("current: %d", cf.Current)
	}
	if len(cf.Months) != 2 || cf.Months[0].Closing != -800 || cf.Months[1].Closing != 4100 {
		t.Fatalf("months: %+v", cf.Months)
	}
	// buckets (entries above have no bucket → Outros) and month surplus
	if dez := cf.Months[1]; dez.Other != 100 || dez.Card != 0 || dez.Net() != 4900 || cf.Months[1].Partial || !cf.Months[0].Partial {
		t.Fatalf("buckets: %+v", cf.Months)
	}
	if cf.Low != -800 || !cf.LowDate.Equal(d("2026-11-20")) || !cf.FirstNegative.Equal(d("2026-11-20")) {
		t.Fatalf("low: %+v", cf)
	}
	// today, 11-20, 12-05 (merged), end of horizon
	if len(cf.Points) != 4 || cf.Points[2].Balance != 4100 || !cf.Points[3].Date.Equal(d("2026-12-31")) {
		t.Fatalf("points: %+v", cf.Points)
	}
	// whole months with history: out (past), nov (current, full), dez (future)
	rows := MonthlyFlow(es, snap, d("2026-11-16"), m("2026-10"), m("2026-12"))
	if len(rows) != 3 || !rows[0].Past || !rows[1].Current || rows[2].Past || rows[2].Current {
		t.Fatalf("flags: %+v", rows)
	}
	// nov: +1000 -300 -2000; opening = balance on 31/10 = 1500 - 1000 (income before the snapshot) = 500
	if nov := rows[1]; nov.In != 1000 || nov.Out != 2300 || nov.Opening != 500 || nov.Closing != -800 {
		t.Fatalf("nov: %+v", nov)
	}
	if dez := rows[2]; dez.Opening != -800 || dez.Closing != 4100 || dez.Net() != 4900 {
		t.Fatalf("dez: %+v", dez)
	}
	if tot := SumFlow(rows[1:]); tot.In != 6000 || tot.Out != 2400 {
		t.Fatalf("totals: %+v", tot)
	}

	// balance before the snapshot walks backwards
	if got := BalanceAt(es, snap, d("2026-11-01")); got != 500 {
		t.Fatalf("backwards: %d", got)
	}
}

func TestEntryGroup(t *testing.T) {
	cases := []struct {
		name string
		e    Entry
		want Bucket
	}{
		{"pix", Entry{Payment: Pix}, Other},
		{"credito", Entry{Payment: Credit}, Card},
		{"credito ganha de fixo", Entry{Payment: Credit, Fixed: true}, Card},
		{"fixo", Entry{Payment: Boleto, Fixed: true}, Fixed},
		{"recorrência prevista", Entry{Projected: true, RecurrenceID: 3}, Fixed},
		{"recorrência prevista no cartão", Entry{Projected: true, RecurrenceID: 3, Payment: Credit}, Card},
	}
	for _, c := range cases {
		if got := c.e.Group(); got != c.want {
			t.Errorf("%s: Group() = %s, want %s", c.name, got, c.want)
		}
	}
}
