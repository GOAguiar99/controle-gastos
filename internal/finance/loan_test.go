package finance

import "testing"

// Numbers from an Itaú SAC statement (DDC): 380.000,00, 420 parcelas,
// 0,963139300% a.m., 1ª parcela 16/01/2026, saldo após a parcela 7 = 377.957,86.
func itauLoan() Loan {
	return Loan{
		System: LoanSAC, Principal: 38000000, MonthlyRate: 0.9631393, Term: 420,
		FirstDue: d("2026-01-16"), Fees: 7988, AnchorN: 7, AnchorBalance: 37795786,
	}
}

func TestSACMatchesBankProjection(t *testing.T) {
	s := itauLoan().Schedule()
	if len(s) != 420 {
		t.Fatalf("len %d", len(s))
	}
	want := []struct {
		n                        int
		amort, interest, balance int64
	}{
		{8, 91515, 364026, 37704271},
		{9, 91515, 363145, 37612756},
		{12, 91515, 360500, 37338211},
		{100, 91515, 282936, 29284891},
	}
	for _, w := range want {
		got := s[w.n-1]
		if abs(got.Amort-w.amort) > 1 || abs(got.Interest-w.interest) > 2 || abs(got.Balance-w.balance) > 50 {
			t.Errorf("parcela %d: got amort %d juros %d saldo %d; want %d %d %d", w.n, got.Amort, got.Interest, got.Balance, w.amort, w.interest, w.balance)
		}
	}
	last := s[419]
	if last.Balance != 0 || !last.Due.Equal(d("2060-12-16")) {
		t.Errorf("last: %+v", last)
	}
	if abs(last.Amort-91515) > 50 {
		t.Errorf("last amort %d", last.Amort)
	}
}

func TestLoanStatus(t *testing.T) {
	l := itauLoan()
	st := l.Status(11)
	// saldo após a parcela 11 (16/11/2026) no extrato: 374.297,26
	if abs(st.Balance-37429726) > 50 {
		t.Fatalf("balance %d", st.Balance)
	}
	if st.Next == nil || st.Next.N != 12 || !st.Next.Due.Equal(d("2026-12-16")) {
		t.Fatalf("next %+v", st.Next)
	}
	if st.ProgressPct <= 0.014 || st.ProgressPct >= 0.016 {
		t.Fatalf("progress %f", st.ProgressPct)
	}
	if st.InterestLeft <= 0 || st.InterestPaid <= 0 {
		t.Fatalf("interest %+v", st)
	}
	if in, ok := l.InstallmentFor(l.Schedule(), m("2026-12")); !ok || in.N != 12 {
		t.Fatalf("installment for dez/2026: %+v", in)
	}
}

func TestPrice(t *testing.T) {
	// 10.000 a 1% a.m. em 12x -> parcela 888,49
	l := Loan{System: LoanPrice, Principal: 1000000, MonthlyRate: 1, Term: 12, FirstDue: d("2026-01-10")}
	s := l.Schedule()
	if got := s[0].Amort + s[0].Interest; abs(got-88849) > 1 {
		t.Fatalf("pmt %d", got)
	}
	if s[11].Balance != 0 {
		t.Fatalf("final balance %d", s[11].Balance)
	}
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
