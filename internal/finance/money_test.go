package finance

import "testing"

func TestParseMoney(t *testing.T) {
	cases := map[string]int64{
		"1.234,56": 123456, "1234,56": 123456, "1234.56": 123456, "R$ 10": 1000,
		"0,1": 10, "1.000.000": 100000000, " 99,999 ": 10000,
	}
	for in, want := range cases {
		got, err := ParseMoney(in)
		if err != nil || got != want {
			t.Errorf("ParseMoney(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := ParseMoney("abc"); err == nil {
		t.Error("expected error")
	}
}

func TestBRL(t *testing.T) {
	for in, want := range map[int64]string{0: "R$ 0,00", 123456: "R$ 1.234,56", -50: "−R$ 0,50", 100000000: "R$ 1.000.000,00"} {
		if got := BRL(in); got != want {
			t.Errorf("BRL(%d) = %q want %q", in, got, want)
		}
	}
	if FromReais(1234.565) != 123457 || FromReais(0.1) != 10 {
		t.Error("FromReais rounding")
	}
}
