package app

import (
	"sort"
	"strings"
	"time"

	"controle-gastos/internal/finance"
)

// CardIn creates (ID == 0) or updates a credit card.
type CardIn struct {
	ID         int64
	Name       string
	ClosingDay int
	DueDay     int
	Active     bool
}

// cardsSetting turns the cards module on ("1", the default) or off ("0").
const cardsSetting = "cards_enabled"

// CardsEnabled reports whether the cards module is on. Off, credit purchases
// leave the cash on the purchase date (no fatura), card screens and tools are
// hidden, and what was already billed on cards stays as it is.
func (s *Service) CardsEnabled() bool { return s.st.Setting(cardsSetting) != "0" }

// SetCardsEnabled turns the cards module on or off.
func (s *Service) SetCardsEnabled(on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return s.st.SetSetting(cardsSetting, v)
}

// errCardsOff is returned by card operations while the module is off.
func errCardsOff() error {
	return invalid("o módulo de cartões está desativado; ative em Perfil → Preferências (ou com set_cards_module)")
}

func (s *Service) requireCards() error {
	if !s.CardsEnabled() {
		return errCardsOff()
	}
	return nil
}

func (s *Service) Cards() ([]finance.CreditCard, error) {
	if !s.CardsEnabled() {
		return nil, nil
	}
	return s.st.Cards()
}

func (s *Service) Card(id int64) (finance.CreditCard, error) {
	if err := s.requireCards(); err != nil {
		return finance.CreditCard{}, err
	}
	c, err := s.st.Card(id)
	return c, notFound(err, "cartão")
}

// CardByName finds a card by name, accent/case-insensitive.
func (s *Service) CardByName(name string) (finance.CreditCard, error) {
	if err := s.requireCards(); err != nil {
		return finance.CreditCard{}, err
	}
	cards, err := s.st.Cards()
	if err != nil {
		return finance.CreditCard{}, err
	}
	var names []string
	for _, c := range cards {
		if fold(c.Name) == fold(name) {
			return c, nil
		}
		names = append(names, c.Name)
	}
	if len(names) == 0 {
		return finance.CreditCard{}, invalid("nenhum cartão cadastrado; cadastre um com dia de fechamento e de vencimento")
	}
	return finance.CreditCard{}, invalid("cartão %q não existe (cartões: %s)", name, strings.Join(names, ", "))
}

// SaveCard validates and saves a card. Changing the closing or due day moves
// the cash date of the purchases already billed; their statements are kept.
func (s *Service) SaveCard(in CardIn) (int64, error) {
	if err := s.requireCards(); err != nil {
		return 0, err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return 0, invalid("dê um nome ao cartão")
	}
	if in.ClosingDay < 1 || in.ClosingDay > 31 {
		return 0, invalid("dia de fechamento inválido (1 a 31)")
	}
	if in.DueDay < 1 || in.DueDay > 31 {
		return 0, invalid("dia de vencimento inválido (1 a 31)")
	}
	cards, err := s.st.Cards()
	if err != nil {
		return 0, err
	}
	var old finance.CreditCard
	for _, c := range cards {
		if c.ID != in.ID && fold(c.Name) == fold(in.Name) {
			return 0, invalid("já existe um cartão chamado %s", c.Name)
		}
		if c.ID == in.ID {
			old = c
		}
	}
	if in.ID != 0 && old.ID == 0 {
		return 0, invalid("cartão não encontrado")
	}
	c := finance.CreditCard{ID: in.ID, Name: in.Name, ClosingDay: in.ClosingDay, DueDay: in.DueDay, Active: in.Active || in.ID == 0}
	id, err := s.st.SaveCard(c)
	if err != nil {
		return 0, err
	}
	if in.ID != 0 && (old.DueDay != c.DueDay || old.ClosingDay != c.ClosingDay) {
		c.ID = id
		if err := s.st.RedateCard(c); err != nil {
			return id, err
		}
	}
	return id, nil
}

// DeleteCard removes a card without purchases; one with purchases can only be
// deactivated (its history stays on it).
func (s *Service) DeleteCard(id int64) (finance.CreditCard, error) {
	c, err := s.Card(id)
	if err != nil {
		return c, err
	}
	n, err := s.st.CardTxCount(id)
	if err != nil {
		return c, err
	}
	if n > 0 {
		return c, invalid("o cartão %s tem %d lançamento(s); desative-o em vez de excluir", c.Name, n)
	}
	return c, s.st.DeleteCard(id)
}

// cardOf is the card a credit transaction goes on (ok=false: none).
func (s *Service) cardOf(id int64, pay finance.PaymentMethod) (finance.CreditCard, bool) {
	if id == 0 || pay != finance.Credit || !s.CardsEnabled() {
		return finance.CreditCard{}, false
	}
	c, err := s.st.Card(id)
	return c, err == nil
}

// SetStatementTotal records the total the bank charges for a fatura (valor
// cheio). Items entered on it count against it: only the difference is added,
// as "Fatura cartão (não detalhado)". 0 clears it.
func (s *Service) SetStatementTotal(cardID int64, m finance.Month, cents int64) (StatementView, error) {
	if _, err := s.Card(cardID); err != nil {
		return StatementView{}, err
	}
	if m == 0 {
		return StatementView{}, invalid("mês da fatura inválido")
	}
	if cents < 0 {
		return StatementView{}, invalid("valor inválido")
	}
	if err := s.st.SetStatementTotal(cardID, m, cents); err != nil {
		return StatementView{}, err
	}
	return s.Statement(cardID, m)
}

// StatementView is one fatura with its items.
type StatementView struct {
	finance.Statement
	Closing, Due time.Time
	Open         bool            // still taking purchases
	Items        []finance.Entry // lançamentos desta fatura (reais e recorrências previstas)
	// Loose are credit expenses with no card dated in the statement's month
	// (e.g. old "Fatura cartão" totals): entering items too would count twice.
	Loose []finance.Entry
	// ByCategory splits the fatura total by category, largest first; the
	// undetailed part of a declared total is its own line.
	ByCategory []CategoryShare
	Top        []finance.Entry // largest purchases
}

// CategoryShare is one category's part of a fatura.
type CategoryShare struct {
	Name  string
	Total int64
	Count int
	Share float64 // of the fatura total, 0..1
}

// statementTop is how many of the largest purchases a fatura view lists.
const statementTop = 8

// splitByCategory fills ByCategory and Top from the items.
func (v *StatementView) splitByCategory() {
	idx := map[string]int{}
	for _, e := range v.Items {
		i, ok := idx[e.Category]
		if !ok {
			i = len(v.ByCategory)
			idx[e.Category] = i
			v.ByCategory = append(v.ByCategory, CategoryShare{Name: e.Category})
		}
		v.ByCategory[i].Total += e.Amount
		v.ByCategory[i].Count++
	}
	if rest := v.Rest(); rest > 0 {
		v.ByCategory = append(v.ByCategory, CategoryShare{Name: "Não detalhado", Total: rest})
	}
	total := v.Total()
	for i := range v.ByCategory {
		if total > 0 {
			v.ByCategory[i].Share = float64(v.ByCategory[i].Total) / float64(total)
		}
	}
	sort.SliceStable(v.ByCategory, func(i, j int) bool { return v.ByCategory[i].Total > v.ByCategory[j].Total })
	v.Top = append([]finance.Entry(nil), v.Items...)
	sort.SliceStable(v.Top, func(i, j int) bool { return v.Top[i].Amount > v.Top[j].Amount })
	if len(v.Top) > statementTop {
		v.Top = v.Top[:statementTop]
	}
}

// Statement returns fatura m of a card.
func (s *Service) Statement(cardID int64, m finance.Month) (StatementView, error) {
	c, err := s.Card(cardID)
	if err != nil {
		return StatementView{}, err
	}
	if m == 0 {
		m = c.OpenStatement(s.Today())
	}
	v := StatementView{Statement: finance.Statement{Card: c, Month: m}, Closing: c.Closing(m), Due: c.Due(m)}
	v.Open = !s.Today().After(v.Closing) && c.OpenStatement(s.Today()) == m
	if v.Items, err = s.st.StatementItems(cardID, m); err != nil {
		return v, err
	}
	// projected card recurrences billed on this statement
	es, _, err := s.st.Entries(m.Add(-1), m)
	if err != nil {
		return v, err
	}
	for _, e := range es {
		if e.Projected && e.CardID == cardID && e.Statement == m {
			v.Items = append(v.Items, e)
		}
		if e.ID != 0 && e.Kind == finance.Expense && e.Payment == finance.Credit && e.CardID == 0 && finance.MonthOf(e.Date) == m {
			v.Loose = append(v.Loose, e)
		}
	}
	finance.SortEntries(v.Items)
	var totals []finance.Entry
	for _, e := range v.Items {
		v.Statement.Items += e.Amount
		v.Count++
		if isBillTotal(e.Category) && e.ID != 0 {
			totals = append(totals, e)
		}
	}
	// a whole-fatura total next to detailed items counts the same spending twice
	if len(totals) > 0 && len(totals) < v.Count {
		v.Loose = append(v.Loose, totals...)
	}
	sts, err := s.st.Statements(map[int64]finance.CreditCard{c.ID: c}, m, m)
	if err != nil {
		return v, err
	}
	for _, st := range sts {
		v.Declared, v.HasTotal = st.Declared, st.HasTotal
	}
	v.splitByCategory()
	return v, nil
}

// CardStatements lists a card's faturas from the open one back n months,
// plus any future ones that already have parcels.
func (s *Service) CardStatements(cardID int64, n int) ([]finance.Statement, error) {
	c, err := s.Card(cardID)
	if err != nil {
		return nil, err
	}
	open := c.OpenStatement(s.Today())
	sts, err := s.st.Statements(map[int64]finance.CreditCard{c.ID: c}, open.Add(-n), open.Add(24))
	if err != nil {
		return nil, err
	}
	has := map[finance.Month]bool{}
	for _, st := range sts {
		has[st.Month] = true
	}
	for m := open; m >= open.Add(-2); m-- { // always show the open one and the last two
		if !has[m] {
			sts = append(sts, finance.Statement{Card: c, Month: m})
		}
	}
	sort.Slice(sts, func(i, j int) bool { return sts[i].Month > sts[j].Month })
	return sts, nil
}

// DeleteInstallmentsFrom deletes parcel n of an installment purchase and
// every later one (for a purchase returned or paid off early).
func (s *Service) DeleteInstallmentsFrom(txID int64) (int, error) {
	e, err := s.st.Tx(txID)
	if err != nil {
		return 0, notFound(err, "transação")
	}
	if e.InstallmentGroup == 0 {
		return 0, invalid("este lançamento não é parcelado")
	}
	return s.st.DeleteInstallmentsFrom(e.InstallmentGroup, e.InstallmentN)
}
