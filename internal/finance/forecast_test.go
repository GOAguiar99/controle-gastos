package finance

import "testing"

func TestBaselines(t *testing.T) {
	today := d("2026-11-23")
	var es []Entry
	// six closed months of variable spending: 1000..6000 (R$ 10 .. R$ 60)
	for i, v := range []int64{3000, 1000, 6000, 2000, 5000, 4000} {
		es = append(es, Entry{Kind: Expense, Date: m("2026-05").Add(i).Day(10), Amount: v})
	}
	// none of these is day-to-day spending
	es = append(es,
		Entry{Kind: Expense, Date: d("2026-06-16"), Amount: 99999, Fixed: true},
		Entry{Kind: Expense, Date: d("2026-07-16"), Amount: 99999, RecurrenceID: 1},
		Entry{Kind: Expense, Date: d("2026-08-16"), Amount: 99999, DebtID: 2},
		Entry{Kind: Income, Date: d("2026-09-05"), Amount: 99999},
		Entry{Kind: Expense, Date: d("2026-11-05"), Amount: 99999}, // current month: not sampled
		Entry{Kind: Expense, Date: d("2026-04-05"), Amount: 99999}, // before the window
	)
	fb, ok := Baselines(es, today, 6)
	if !ok || len(fb.Months) != 6 {
		t.Fatalf("ok=%v months=%v", ok, labels(fb.Months))
	}
	// sorted 1000..6000: P25 = 2250, P50 = 3500, P75 = 4750
	if fb.Optimistic != 2250 || fb.Base != 3500 || fb.Pessimistic != 4750 {
		t.Fatalf("percentiles: %+v", fb)
	}

	// a card purchase counts in its fatura's due month
	card := Entry{Kind: Expense, Date: d("2026-04-20"), CashDate: d("2026-05-09"), CardID: 1, Amount: 500}
	if by := VariableByMonth([]Entry{card}, m("2026-05"), m("2026-05")); by[m("2026-05")] != 500 {
		t.Fatalf("card by due month: %v", by)
	}

	if _, ok := Baselines(es[:2], today, 6); ok {
		t.Fatal("two months are not enough")
	}
}

func TestForecastEntries(t *testing.T) {
	today := d("2026-11-23")
	es := []Entry{
		{Kind: Expense, Date: d("2026-11-09"), Amount: 800},              // current month: rest 200
		{Kind: Expense, Date: d("2026-12-01"), Amount: 5000},             // more than the estimate: no top-up
		{Kind: Expense, Date: d("2027-01-16"), Amount: 700, Fixed: true}, // fixed does not count
		{Kind: Expense, Date: d("2027-01-05"), Amount: 300},
	}
	fs := ForecastEntries(es, 1000, today, d("2027-02-28"))
	if len(fs) != 3 {
		t.Fatalf("entries: %+v", fs)
	}
	nov, jan, feb := fs[0], fs[1], fs[2]
	if !nov.Date.Equal(d("2026-11-24")) || nov.Amount != 200 {
		t.Fatalf("current month: %+v", nov)
	}
	if !jan.Date.Equal(d("2027-01-15")) || jan.Amount != 700 {
		t.Fatalf("january: %+v", jan)
	}
	if feb.Amount != 1000 || !feb.Forecast || !feb.Projected || feb.Group() != Forecast || feb.Variable() {
		t.Fatalf("february: %+v", feb)
	}
	// the last day of the month leaves nothing for it
	if fs := ForecastEntries(nil, 1000, d("2026-11-30"), d("2026-12-31")); len(fs) != 1 || MonthOf(fs[0].Date) != m("2026-12") {
		t.Fatalf("month over: %+v", fs)
	}
	if ForecastEntries(es, 0, today, d("2027-02-28")) != nil {
		t.Fatal("no estimate, no entries")
	}
	// the estimate moves the projected cash
	snap := Snapshot{Set: true, Date: today, Amount: 10000}
	cf := ProjectCash(append(append([]Entry(nil), es...), fs...), snap, today, 4)
	if cf.Months[3].Forecast != 1000 || cf.Months[3].Closing != 10000-200-5000-700-300-700-1000 {
		t.Fatalf("flow: %+v", cf.Months[3])
	}
}
