package finance

import (
	"math"
	"strings"
)

// BRLCode is the home currency: every stored amount is in reais.
const BRLCode = "BRL"

// Currency is a currency a transaction can be entered in, or the totals shown in.
type Currency struct {
	Code   string // ISO 4217
	Symbol string // as printed before the amount
	Name   string // pt-BR
}

// Currencies are BRL plus the ones the Banco Central publishes a PTAX for.
var Currencies = []Currency{
	{"BRL", "R$", "Real"},
	{"USD", "US$", "Dólar americano"},
	{"EUR", "€", "Euro"},
	{"GBP", "£", "Libra esterlina"},
	{"CAD", "C$", "Dólar canadense"},
	{"AUD", "A$", "Dólar australiano"},
	{"CHF", "CHF", "Franco suíço"},
	{"JPY", "¥", "Iene"},
	{"DKK", "kr DKK", "Coroa dinamarquesa"},
	{"NOK", "kr NOK", "Coroa norueguesa"},
	{"SEK", "kr SEK", "Coroa sueca"},
}

// CurrencyOf finds a currency by code (case-insensitive; "" = BRL).
func CurrencyOf(code string) (Currency, bool) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		code = BRLCode
	}
	for _, c := range Currencies {
		if c.Code == code {
			return c, true
		}
	}
	return Currency{}, false
}

// FormatMoney formats cents of a currency the Brazilian way: "US$ 1.234,56".
func FormatMoney(cents int64, code string) string {
	s := BRL(cents)
	c, ok := CurrencyOf(code)
	if !ok || c.Code == BRLCode {
		return s
	}
	return strings.Replace(s, "R$", c.Symbol, 1)
}

// ToBRL converts cents of a foreign currency at rate (reais per unit).
func ToBRL(cents int64, rate float64) int64 { return int64(math.Round(float64(cents) * rate)) }

// FromBRL converts reais (cents) to a foreign currency at rate (reais per unit).
func FromBRL(cents int64, rate float64) int64 {
	if rate <= 0 {
		return cents
	}
	return int64(math.Round(float64(cents) / rate))
}
