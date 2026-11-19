package app

import (
	"sort"
	"strconv"
	"time"

	"controle-gastos/internal/finance"
)

// catSlots is how many categories get their own color; the rest fold into "Outras".
const catSlots = 7

const otherCats = "Outras"

// CategoryPeriod is one composition (segmented bar) of the category totals.
type CategoryPeriod struct {
	Label  string    `json:"label"`
	Values []float64 `json:"values"` // per CategoryChart.Cats
	Total  float64   `json:"total"`
}

// CategoryChart feeds the Receitas/Gastos charts: totals per category per year and
// the composition of the selected month, its year and the realized history.
type CategoryChart struct {
	Cats      []string         `json:"cats"`
	Years     []string         `json:"years"`
	YearState []string         `json:"yearState"` // realizado | parcial | previsto
	Values    [][]float64      `json:"values"`    // [cat][year]
	YearReal  []float64        `json:"yearReal"`
	YearProj  []float64        `json:"yearProj"`
	YearMons  []int            `json:"yearMonths"` // months the yearly average divides by
	Periods   []CategoryPeriod `json:"periods"`
}

// buildCatChart expects entries of a single kind covering from first.. through end of lastYear.
func buildCatChart(entries []finance.Entry, first, sel finance.Month, today time.Time, lastYear int) CategoryChart {
	var c CategoryChart
	if len(entries) == 0 {
		return c
	}
	// Fixed color order: categories ranked by all-time total, so a color always means the same category.
	totals := map[string]int64{}
	for _, e := range entries {
		totals[e.Category] += e.Amount
	}
	names := make([]string, 0, len(totals))
	for n := range totals {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if totals[names[i]] != totals[names[j]] {
			return totals[names[i]] > totals[names[j]]
		}
		return names[i] < names[j]
	})
	slot := map[string]int{}
	for i, n := range names {
		if i < catSlots || len(names) == catSlots+1 {
			slot[n] = i
			c.Cats = append(c.Cats, n)
		}
	}
	if len(c.Cats) < len(names) {
		c.Cats = append(c.Cats, otherCats)
	}
	idx := func(cat string) int {
		if i, ok := slot[cat]; ok {
			return i
		}
		return len(c.Cats) - 1
	}

	firstYear, curYear := first.Year(), today.Year()
	nYears := lastYear - firstYear + 1
	c.Values = make([][]float64, len(c.Cats))
	for i := range c.Values {
		c.Values[i] = make([]float64, nYears)
	}
	c.YearReal = make([]float64, nYears)
	c.YearProj = make([]float64, nYears)
	for y := firstYear; y <= lastYear; y++ {
		state, mons := "realizado", 12
		switch {
		case y == curYear:
			state = "parcial"
		case y > curYear:
			state = "previsto"
		}
		if y == firstYear {
			mons = 12 - int(first.Mon()) + 1
		}
		c.Years = append(c.Years, strconv.Itoa(y))
		c.YearState = append(c.YearState, state)
		c.YearMons = append(c.YearMons, mons)
	}

	newPeriod := func(label string) CategoryPeriod {
		return CategoryPeriod{Label: label, Values: make([]float64, len(c.Cats))}
	}
	month := newPeriod(sel.Label())
	year := newPeriod(strconv.Itoa(sel.Year()))
	hist := newPeriod("Histórico até hoje")
	add := func(p *CategoryPeriod, i int, v float64) { p.Values[i] += v; p.Total += v }

	today = finance.DateOnly(today)
	for _, e := range entries {
		day := e.ReportDay()
		y := day.Year() - firstYear
		if y < 0 || y >= nYears {
			continue
		}
		i, v := idx(e.Category), reais(e.Amount)
		c.Values[i][y] += v
		if e.Projected {
			c.YearProj[y] += v
		} else {
			c.YearReal[y] += v
		}
		if e.Month() == sel {
			add(&month, i, v)
		}
		if day.Year() == sel.Year() {
			add(&year, i, v)
		}
		if !e.Projected && !day.After(today) {
			add(&hist, i, v)
		}
	}
	c.Periods = []CategoryPeriod{month, year, hist}
	return c
}
