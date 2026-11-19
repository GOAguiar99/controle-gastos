package finance

import "testing"

func TestCardStatementAndDue(t *testing.T) {
	nubank := CreditCard{ClosingDay: 3, DueDay: 10} // fecha 3, vence 10 do mesmo mês
	itau := CreditCard{ClosingDay: 25, DueDay: 5}   // fecha 25, vence 5 do mês seguinte
	fim := CreditCard{ClosingDay: 31, DueDay: 31}   // fecha no último dia
	cases := []struct {
		c         CreditCard
		buy       string
		statement string
		due       string
	}{
		{nubank, "2026-11-02", "2026-11", "2026-11-10"},
		{nubank, "2026-11-03", "2026-12", "2026-12-10"}, // no dia do fechamento vai para a próxima
		{nubank, "2026-10-20", "2026-11", "2026-11-10"},
		{nubank, "2026-12-15", "2027-01", "2027-01-10"}, // virada de ano
		{itau, "2026-11-20", "2026-11", "2026-12-05"},
		{itau, "2026-12-26", "2027-01", "2027-02-05"},
		{fim, "2026-02-27", "2026-02", "2026-03-31"},
		{fim, "2026-02-28", "2026-03", "2026-04-30"}, // 31 vira o último dia do mês
	}
	for _, c := range cases {
		st := c.c.StatementFor(d(c.buy))
		if st.String() != c.statement {
			t.Errorf("%+v compra %s: fatura %s, want %s", c.c, c.buy, st, c.statement)
		}
		if due := c.c.Due(st); !due.Equal(d(c.due)) {
			t.Errorf("%+v fatura %s: vence %s, want %s", c.c, st, due.Format("2006-01-02"), c.due)
		}
	}
}

func TestStatementRest(t *testing.T) {
	c := CreditCard{ID: 1, Name: "Nubank", ClosingDay: 3, DueDay: 10}
	s := Statement{Card: c, Month: m("2026-11"), Items: 30000, Declared: 100000, HasTotal: true}
	if s.Total() != 100000 || s.Rest() != 70000 {
		t.Fatalf("total %d rest %d", s.Total(), s.Rest())
	}
	e := s.RestEntry()
	if e.Group() != Card || !e.CashDay().Equal(d("2026-11-10")) || !e.Date.Equal(d("2026-11-03")) {
		t.Fatalf("rest entry: %+v", e)
	}
	s.Items = 120000 // itens passaram do total informado: nada a completar
	if s.Total() != 120000 || s.Rest() != 0 {
		t.Fatalf("over: total %d rest %d", s.Total(), s.Rest())
	}
}

// Card purchases move the cash on the due date, not on the purchase date.
func TestCashUsesDueDate(t *testing.T) {
	snap := Snapshot{Set: true, Date: d("2026-11-01"), Amount: 100000}
	es := []Entry{
		{Kind: Expense, Date: d("2026-10-20"), CashDate: d("2026-11-10"), Amount: 30000, Payment: Credit},
		{Kind: Expense, Date: d("2026-11-05"), Amount: 1000, Payment: Pix},
	}
	if b := BalanceAt(es, snap, d("2026-11-09")); b != 99000 {
		t.Fatalf("antes do vencimento: %d", b)
	}
	if b := BalanceAt(es, snap, d("2026-11-10")); b != 69000 {
		t.Fatalf("no vencimento: %d", b)
	}
	cf := ProjectCash(es, snap, d("2026-11-02"), 1)
	if cf.Months[0].Card != 30000 || cf.Months[0].Closing != 69000 {
		t.Fatalf("projeção: %+v", cf.Months[0])
	}
}
