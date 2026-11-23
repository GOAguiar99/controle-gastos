package finance

import "testing"

func TestYield(t *testing.T) {
	snap := Snapshot{Set: true, Date: d("2026-11-10"), Amount: 1000000} // R$ 10.000
	es := []Entry{{Kind: Expense, Date: d("2026-12-05"), Amount: 400000}}
	ys := Yield(es, snap, d("2026-11-20"), d("2027-01-31"), 12, "x")
	if len(ys) != 4 || !ys[0].Date.Equal(d("2026-11-20")) || !ys[1].Date.Equal(d("2026-11-30")) || !ys[3].Date.Equal(d("2027-01-31")) {
		t.Fatalf("periods: %+v", ys)
	}
	// 10 days on R$ 10.000 at 12% a.a. ≈ R$ 31,10
	if ys[0].Amount < 3090 || ys[0].Amount > 3130 {
		t.Fatalf("first period: %d", ys[0].Amount)
	}
	// January earns on the balance after the December expense (and the yield so far)
	jan := ys[3].Amount
	if jan < 5500 || jan > 6000 {
		t.Fatalf("january on ~R$ 6.050: %d", jan)
	}
	// the yield moves the balance
	if b := BalanceAt(append(es, ys...), snap, d("2026-11-20")); b != 1000000+ys[0].Amount {
		t.Fatalf("today: %d", b)
	}
	if Yield(es, snap, d("2026-11-20"), d("2027-01-31"), 0, "x") != nil {
		t.Fatal("no rate, no yield")
	}
	// a negative balance earns nothing
	neg := Snapshot{Set: true, Date: d("2026-11-10"), Amount: -100}
	if ys := Yield(nil, neg, d("2026-11-20"), d("2026-12-31"), 12, "x"); len(ys) != 0 {
		t.Fatalf("negative: %+v", ys)
	}
}
