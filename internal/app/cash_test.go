package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"controle-gastos/internal/db/dbtest"
	"controle-gastos/internal/finance"
)

func TestCashAccountsAndYield(t *testing.T) {
	s := newSvc(t) // today 2026-11-16
	_, err := s.SetCashBalances(time.Time{}, map[int64]int64{1: 1}, "")
	mustInvalid(t, err, "cadastre as contas")
	conta, err := s.SaveCashAccount(CashAccountIn{Name: "Conta", CDIPct: 100})
	if err != nil {
		t.Fatal(err)
	}
	cdb, _ := s.SaveCashAccount(CashAccountIn{Name: "CDB", CDIPct: 120})
	_, err = s.SaveCashAccount(CashAccountIn{Name: "conta", CDIPct: 50})
	mustInvalid(t, err, "já existe")
	_, err = s.SaveCashAccount(CashAccountIn{Name: "X", CDIPct: 301})
	mustInvalid(t, err, "percentual")

	total, err := s.SetCashBalances(time.Time{}, map[int64]int64{conta: 1000000, cdb: 3000000}, "")
	if err != nil || total != 4000000 {
		t.Fatalf("set: %d %v", total, err)
	}
	if snap, _ := s.Store().LatestSnapshot(); snap.Amount != 4000000 || !snap.Date.Equal(d("2026-11-16")) {
		t.Fatalf("snapshot: %+v", snap)
	}
	// only one account updated: the other keeps its balance
	if total, _ = s.SetCashBalances(d("2026-11-16"), map[int64]int64{conta: 2000000}, ""); total != 5000000 {
		t.Fatalf("partial: %d", total)
	}

	// no CDI yet: no yield
	c, _ := s.Cash()
	if c.HasCDI || c.Annual != 0 || c.PctOfCDI != 112 { // (2×100 + 3×120)/5
		t.Fatalf("no cdi: %+v", c)
	}
	if err := s.Store().SetMarketRate(CDIRate, 14.9, d("2026-11-14")); err != nil {
		t.Fatal(err)
	}
	c, _ = s.Cash()
	if !c.HasCDI || c.Annual < 16.68 || c.Annual > 16.69 || c.Monthly < 60000 || c.Monthly > 65000 {
		t.Fatalf("cash: %+v", c)
	}
	// the projection grows with the yield, which isn't the month's income
	cf, err := s.CashProjection(12, "")
	if err != nil {
		t.Fatal(err)
	}
	if last := cf.Months[len(cf.Months)-1]; last.Closing < 5700000 || last.Closing > 5900000 {
		t.Fatalf("a year at ~16,7%% a.a. on R$ 50 mil: %d", last.Closing)
	}
	o, _ := s.Overview(0)
	if o.Summary.Income != 0 {
		t.Fatalf("yield counted as income: %+v", o.Summary)
	}
	if _, err := s.DeleteCashAccount(cdb); err != nil {
		t.Fatal(err)
	}
	_, err = s.DeleteCashAccount(cdb)
	mustInvalid(t, err, "não encontrada")
	_ = finance.Income
}

func TestRefreshCDI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"data":"21/11/2026","valor":"14.90"}]`))
	}))
	defer srv.Close()
	old := cdiURL
	cdiURL = srv.URL
	defer func() { cdiURL = old }()
	d := dbtest.Open(t)
	a := NewApp(d, time.UTC)
	if err := a.RefreshCDI(context.Background(), srv.Client()); err != nil {
		t.Fatal(err)
	}
	id, _ := d.CreateLedger(context.Background(), "x")
	v, asOf, ok, err := d.Ledger(id).MarketRate(CDIRate)
	if err != nil || !ok || v != 14.9 || asOf.Format("2006-01-02") != "2026-11-21" {
		t.Fatalf("rate: %v %v %v %v", v, asOf, ok, err)
	}
}
