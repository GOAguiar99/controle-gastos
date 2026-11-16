package app

import (
	"testing"
	"time"

	"controle-gastos/internal/finance"
)

func TestBuildCatChart(t *testing.T) {
	d := func(s string) time.Time { v, _ := time.Parse("2006-01-02", s); return v }
	es := []finance.Entry{
		{Kind: finance.Expense, Date: d("2025-03-10"), Amount: 10000, Category: "Mercado"},
		{Kind: finance.Expense, Date: d("2026-11-02"), Amount: 5000, Category: "Mercado"},
		{Kind: finance.Expense, Date: d("2026-11-20"), Amount: 2000, Category: "Luz", Projected: true},
		{Kind: finance.Expense, Date: d("2027-01-05"), Amount: 3000, Category: "Luz", Projected: true},
	}
	for i, n := range []string{"A", "B", "C", "D", "E", "F", "G"} { // 9 categories → 7 + Outras
		es = append(es, finance.Entry{Kind: finance.Expense, Date: d("2026-05-01"), Amount: int64(100 - i), Category: n})
	}
	c := buildCatChart(es, finance.MonthOf(d("2025-03-10")), finance.MonthOf(d("2026-11-16")), d("2026-11-16"), 2027)
	if len(c.Cats) != 8 || c.Cats[0] != "Mercado" || c.Cats[1] != "Luz" || c.Cats[7] != "Outras" {
		t.Fatalf("cats: %v", c.Cats)
	}
	if len(c.Years) != 3 || c.YearState[0] != "realizado" || c.YearState[1] != "parcial" || c.YearState[2] != "previsto" {
		t.Fatalf("years: %v %v", c.Years, c.YearState)
	}
	if c.YearMons[0] != 10 || c.YearMons[1] != 12 {
		t.Fatalf("months: %v", c.YearMons)
	}
	if c.Values[0][0] != 100 || c.Values[1][2] != 30 || c.Values[7][1] != 0.95+0.94 {
		t.Fatalf("values: %v", c.Values)
	}
	if c.YearReal[1] != 50+0.01*(100+99+98+97+96+95+94) || c.YearProj[1] != 20 {
		t.Fatalf("real/proj: %v %v", c.YearReal, c.YearProj)
	}
	// month nov/26: 50 real + 20 previsto; history until today excludes projections
	if m, h := c.Periods[0], c.Periods[2]; m.Total != 70 || h.Values[1] != 0 || h.Values[0] != 150 {
		t.Fatalf("periods: %+v", c.Periods)
	}
}
