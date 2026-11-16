package finance

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ParseMoney accepts "1.234,56", "1234,56", "1234.56", "R$ 1.234" → cents.
func ParseMoney(s string) (int64, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "R$"))
	s = strings.ReplaceAll(s, " ", "")
	if s == "" {
		return 0, fmt.Errorf("valor vazio")
	}
	if strings.Contains(s, ",") {
		s = strings.ReplaceAll(s, ".", "")
		s = strings.Replace(s, ",", ".", 1)
	} else if strings.Count(s, ".") > 1 {
		s = strings.ReplaceAll(s, ".", "")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("valor inválido %q", s)
	}
	return int64(math.Round(f * 100)), nil
}

// FromReais converts a decimal amount in reais (e.g. 1234.56) to cents.
func FromReais(v float64) int64 { return int64(math.Round(v * 100)) }

// BRL formats cents as "R$ 1.234,56" (negative values get a leading "−").
func BRL(cents int64) string {
	neg := cents < 0
	if neg {
		cents = -cents
	}
	reais := strconv.FormatInt(cents/100, 10)
	var b strings.Builder
	for i, c := range reais {
		if i > 0 && (len(reais)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	out := fmt.Sprintf("R$ %s,%02d", b.String(), cents%100)
	if neg {
		return "−" + out
	}
	return out
}
