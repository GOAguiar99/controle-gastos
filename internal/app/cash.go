package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"controle-gastos/internal/db"
	"controle-gastos/internal/finance"
)

// CDIRate is the market_rates name of the CDI (% a.a., base 252).
const CDIRate = "cdi"

// CashSetup is how the cash is split into accounts and what it yields.
type CashSetup struct {
	Accounts []db.CashAccount
	CDI      float64   // % a.a.
	CDIAsOf  time.Time // date the rate refers to
	HasCDI   bool
	// PctOfCDI is the yield of the cash as a whole: the accounts' % of the CDI
	// weighted by their last balances (0 = no accounts or nothing yields).
	PctOfCDI float64
	Annual   float64 // gross % a.a. the cash yields: CDI × PctOfCDI
	Monthly  int64   // gross yield of the last informed balances over 30 days
}

// Cash returns the accounts and the yield the projection uses.
func (s *Service) Cash() (CashSetup, error) {
	var c CashSetup
	var err error
	if c.Accounts, err = s.st.CashAccounts(); err != nil {
		return c, err
	}
	if c.CDI, c.CDIAsOf, c.HasCDI, err = s.st.MarketRate(CDIRate); err != nil {
		return c, err
	}
	var total, weighted float64
	for _, a := range c.Accounts {
		if a.Balance > 0 {
			total += float64(a.Balance)
			weighted += float64(a.Balance) * a.CDIPct
		}
	}
	if total > 0 {
		c.PctOfCDI = weighted / total
	}
	if c.HasCDI {
		c.Annual = c.CDI * c.PctOfCDI / 100
		c.Monthly = int64(math.Round(total * finance.AnnualToPeriod(c.Annual, 30)))
	}
	return c, nil
}

func (c CashSetup) yieldDesc() string {
	return fmt.Sprintf("Rendimento estimado (%s%% do CDI)", strings.Replace(strconv.FormatFloat(math.Round(c.PctOfCDI*10)/10, 'f', -1, 64), ".", ",", 1))
}

// withYield adds the estimated yield of the cash to entries, for cash flows.
func (s *Service) withYield(es []finance.Entry, snap finance.Snapshot, end time.Time) ([]finance.Entry, error) {
	c, err := s.Cash()
	if err != nil || c.Annual <= 0 {
		return es, err
	}
	ys := finance.Yield(es, snap, s.Today(), end, c.Annual, c.yieldDesc())
	out := append(append([]finance.Entry(nil), es...), ys...)
	finance.SortEntries(out)
	return out, nil
}

// CashAccountIn creates (ID == 0) or updates an account.
type CashAccountIn struct {
	ID     int64
	Name   string
	CDIPct float64 // % do CDI; 0 = não rende
}

func (s *Service) SaveCashAccount(in CashAccountIn) (int64, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return 0, invalid("dê um nome à conta")
	}
	if in.CDIPct < 0 || in.CDIPct > 300 || math.IsNaN(in.CDIPct) {
		return 0, invalid("percentual do CDI inválido (0 a 300)")
	}
	accs, err := s.st.CashAccounts()
	if err != nil {
		return 0, err
	}
	found := in.ID == 0
	for _, a := range accs {
		if a.ID != in.ID && fold(a.Name) == fold(in.Name) {
			return 0, invalid("já existe uma conta chamada %s", a.Name)
		}
		found = found || a.ID == in.ID
	}
	if !found {
		return 0, invalid("conta não encontrada")
	}
	return s.st.SaveCashAccount(db.CashAccount{ID: in.ID, Name: in.Name, CDIPct: in.CDIPct})
}

func (s *Service) DeleteCashAccount(id int64) (db.CashAccount, error) {
	a, err := s.cashAccount(id)
	if err != nil {
		return a, err
	}
	return a, s.st.DeleteCashAccount(id)
}

func (s *Service) cashAccount(id int64) (db.CashAccount, error) {
	accs, err := s.st.CashAccounts()
	if err != nil {
		return db.CashAccount{}, err
	}
	for _, a := range accs {
		if a.ID == id {
			return a, nil
		}
	}
	return db.CashAccount{}, invalid("conta não encontrada")
}

// CashAccountByName finds an account, accent/case-insensitive.
func (s *Service) CashAccountByName(name string) (db.CashAccount, error) {
	accs, err := s.st.CashAccounts()
	if err != nil {
		return db.CashAccount{}, err
	}
	var names []string
	for _, a := range accs {
		if fold(a.Name) == fold(name) {
			return a, nil
		}
		names = append(names, a.Name)
	}
	return db.CashAccount{}, invalid("conta %q não existe (contas: %s)", name, strings.Join(names, ", "))
}

// SetCashBalances informs the balance of every account on date (zero =
// today); the cash ("caixa hoje") becomes their sum. Accounts left out keep
// their last balance.
func (s *Service) SetCashBalances(date time.Time, balances map[int64]int64, note string) (int64, error) {
	if date.IsZero() {
		date = s.Today()
	}
	accs, err := s.st.CashAccounts()
	if err != nil {
		return 0, err
	}
	if len(accs) == 0 {
		return 0, invalid("cadastre as contas do caixa antes (ou informe o saldo total)")
	}
	all := map[int64]int64{}
	var total int64
	for _, a := range accs {
		v, ok := balances[a.ID]
		if !ok {
			v = a.Balance
		}
		all[a.ID] = v
		total += v
	}
	for id := range balances {
		if _, ok := all[id]; !ok {
			return 0, invalid("conta %d não encontrada", id)
		}
	}
	return total, s.st.SetCashBalances(date, all, note)
}

// ---------- CDI from the Banco Central ----------

// cdiURL is the BCB SGS series 4389 (CDI anualizada, base 252), last value.
var cdiURL = "https://api.bcb.gov.br/dados/serie/bcdata.sgs.4389/dados/ultimos/1?formato=json"

// RefreshCDI fetches the latest CDI from the Banco Central and stores it.
func (a *App) RefreshCDI(ctx context.Context, client *http.Client) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cdiURL, nil)
	if err != nil {
		return err
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("CDI: HTTP %d", res.StatusCode)
	}
	var rows []struct{ Data, Valor string }
	if err := json.NewDecoder(http.MaxBytesReader(nil, res.Body, 64<<10)).Decode(&rows); err != nil || len(rows) == 0 {
		return fmt.Errorf("CDI: resposta inválida: %v", err)
	}
	v, err := strconv.ParseFloat(strings.Replace(rows[len(rows)-1].Valor, ",", ".", 1), 64)
	if err != nil || v <= 0 || v > 100 {
		return fmt.Errorf("CDI: valor inválido %q", rows[len(rows)-1].Valor)
	}
	asOf, err := time.Parse("02/01/2006", rows[len(rows)-1].Data)
	if err != nil {
		return fmt.Errorf("CDI: data inválida %q", rows[len(rows)-1].Data)
	}
	return a.db.SetMarketRate(ctx, CDIRate, v, asOf)
}

// KeepCDIFresh refreshes the CDI now and every 12 hours until ctx ends.
func (a *App) KeepCDIFresh(ctx context.Context, logf func(string, ...any)) {
	client := &http.Client{Timeout: 20 * time.Second}
	for {
		if err := a.RefreshCDI(ctx, client); err != nil && ctx.Err() == nil {
			logf("CDI: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(12 * time.Hour):
		}
	}
}
