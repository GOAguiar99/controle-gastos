package app

import (
	"regexp"
	"sort"
	"strings"
	"time"

	"controle-gastos/internal/finance"
)

// SourceImport marks transactions entered by importing a card statement.
const SourceImport = "import"

// StatementLine is one purchase of a card statement, as the assistant read it
// from the bill (the reading and categorizing happen on the MCP side).
type StatementLine struct {
	Date         time.Time
	Description  string // without the "3/10" installment suffix
	Amount       int64  // cents, positive; for installments, one parcel
	InstallmentN int    // parcela N de Installments (0 = à vista)
	Installments int
}

// ---------- preview and import ----------

// Import row statuses.
const (
	ImportNew       = "novo"
	ImportDuplicate = "duplicado"
)

// ImportRow is a statement line with what the app suggests doing with it.
type ImportRow struct {
	StatementLine
	Status     string // novo ou duplicado
	Note       string
	CategoryID int64  // suggested from the last expense with the same description (0 = none)
	Category   string // its name
	// SuggestedFrom is the past purchase the category came from; with no
	// history, NeedsResearch asks the assistant to look the name up.
	SuggestedFrom string
	NeedsResearch bool
	// ExistingID and ExistingAmount: the parcel already in the app this line
	// duplicates (its amount may differ by the cents the bank put on parcel 1).
	ExistingID     int64
	ExistingAmount int64
}

// pickParcel chooses the existing parcel a statement line repeats: same value
// first, else same description (the value differs by a few cents).
func pickParcel(cands []finance.Entry, l StatementLine, used map[int64]bool) (finance.Entry, bool) {
	for _, e := range cands {
		if !used[e.ID] && e.Amount == l.Amount {
			return e, true
		}
	}
	for _, e := range cands {
		if !used[e.ID] && descKey(e.Description) == descKey(l.Description) {
			return e, true
		}
	}
	return finance.Entry{}, false
}

// ImportPreview is what importing a statement would do.
type ImportPreview struct {
	Card      finance.CreditCard
	Month     finance.Month
	Rows      []ImportRow
	New       int64 // soma das linhas novas
	NewCount  int
	Declared  int64 // valor informado da fatura, se houver
	Existing  int64 // soma dos lançamentos que a fatura já tem
	LooseNote string
}

// PreviewStatement checks the lines of fatura m of card cardID before
// importing: each is new or already entered (same card, value and date within
// 3 days, or the same parcel), with a category suggested from history.
func (s *Service) PreviewStatement(cardID int64, m finance.Month, lines []StatementLine) (ImportPreview, error) {
	c, err := s.Card(cardID)
	if err != nil {
		return ImportPreview{}, err
	}
	if m == 0 {
		return ImportPreview{}, invalid("mês da fatura inválido")
	}
	p := ImportPreview{Card: c, Month: m}
	if len(lines) == 0 {
		return p, invalid("nenhuma linha informada")
	}
	for i, l := range lines {
		if l.Date.IsZero() || l.Amount <= 0 {
			return p, invalid("linha %d: data e valor positivo são obrigatórios (pagamentos e estornos não entram)", i+1)
		}
	}
	sv, err := s.Statement(cardID, m)
	if err != nil {
		return p, err
	}
	p.Declared, p.Existing = sv.Declared, sv.Statement.Items
	if len(sv.Loose) > 0 {
		var t int64
		for _, e := range sv.Loose {
			t += e.Amount
		}
		p.LooseNote = "Este mês já tem " + finance.BRL(t) + " em gastos no crédito sem cartão (ex.: \"Fatura cartão\" lançada como total). Importar os itens contaria em dobro: exclua o total ou vincule-o a este cartão."
	}
	// candidates for duplicates: the card's purchases around the statement
	lo, hi := lines[0].Date, lines[0].Date
	for _, l := range lines {
		if l.Date.Before(lo) {
			lo = l.Date
		}
		if l.Date.After(hi) {
			hi = l.Date
		}
	}
	existing, err := s.st.Transactions(lo.AddDate(0, 0, -3), hi.AddDate(0, 0, 3))
	if err != nil {
		return p, err
	}
	used := map[int64]bool{}
	guess, err := s.categoryGuesser()
	if err != nil {
		return p, err
	}
	for _, l := range lines {
		r := ImportRow{StatementLine: l, Status: ImportNew}
		switch {
		case l.InstallmentN > 0:
			cands, err := s.st.InstallmentParcels(cardID, l.InstallmentN, l.Installments, m)
			if err != nil {
				return p, err
			}
			if e, ok := pickParcel(cands, l, used); ok {
				used[e.ID] = true
				r.Status, r.Note, r.ExistingID, r.ExistingAmount = ImportDuplicate, "parcela já lançada", e.ID, e.Amount
				if e.Amount != l.Amount {
					r.Note = "parcela já lançada com " + finance.BRL(e.Amount) + "; o banco cobrou " + finance.BRL(l.Amount) + ": corrija o valor do lançamento existente"
				}
			}
		}
		if r.Status == ImportNew {
			for _, e := range existing {
				if used[e.ID] || e.Kind != finance.Expense || e.Amount != l.Amount || e.CardID != cardID {
					continue
				}
				if days := e.Date.Sub(l.Date).Hours() / 24; days >= -3 && days <= 3 {
					used[e.ID] = true
					r.Status, r.Note = ImportDuplicate, "já lançado em "+e.Date.Format("02/01")+" ("+e.Category+")"
					break
				}
			}
		}
		if cat, ok := guess(l.Description); ok {
			r.CategoryID, r.Category, r.SuggestedFrom = cat.ID, cat.Name, cat.From
		} else {
			r.NeedsResearch = true
		}
		if r.Status == ImportNew {
			p.New += l.Amount
			p.NewCount++
		}
		p.Rows = append(p.Rows, r)
	}
	return p, nil
}

type catGuess struct {
	ID   int64
	Name string
	From string // description of the past purchase it came from
}

// categoryGuesser suggests a category for a description: the one used by the
// latest expense with the same description, or else with the same first word.
func (s *Service) categoryGuesser() (func(string) (catGuess, bool), error) {
	txs, err := s.st.AllTransactions()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(txs, func(i, j int) bool { return txs[i].Date.Before(txs[j].Date) })
	full, first := map[string]catGuess{}, map[string]catGuess{}
	for _, e := range txs {
		if e.Kind != finance.Expense || e.Description == "" || e.StatementRest {
			continue
		}
		k := descKey(e.Description)
		g := catGuess{e.CategoryID, e.Category, e.Description}
		full[k] = g
		if w := firstWord(k); w != "" {
			first[w] = g
		}
	}
	return func(desc string) (catGuess, bool) {
		k := descKey(desc)
		if c, ok := full[k]; ok {
			return c, true
		}
		c, ok := first[firstWord(k)]
		return c, ok
	}, nil
}

var reNoise = regexp.MustCompile(`[^a-z ]+`)

// processorPrefixes are the payment intermediaries banks put before the
// merchant's name ("Vmt*Shelf", "Ifd*Pizzaria", "Dl*Uberrides").
var processorPrefixes = map[string]bool{"ifd": true, "vmt": true, "dl": true, "mp": true, "ebn": true, "jim": true,
	"com": true, "paygo": true, "asa": true, "efi": true, "pag": true, "pagseguro": true, "sumup": true, "stone": true, "ec": true}

// descKey normalizes a statement description for matching: accents, case,
// digits and punctuation out, intermediary prefixes dropped.
func descKey(s string) string {
	f := strings.Fields(reNoise.ReplaceAllString(fold(s), " "))
	for len(f) > 1 && processorPrefixes[f[0]] {
		f = f[1:]
	}
	return strings.Join(f, " ")
}

func firstWord(k string) string {
	w, _, _ := strings.Cut(k, " ")
	if len(w) < 3 {
		return ""
	}
	return w
}

// ImportItem is a statement line the user chose to import.
type ImportItem struct {
	Date         time.Time
	Description  string
	Amount       int64 // one parcel, for installment lines
	Category     CategoryRef
	InstallmentN int
	Installments int
}

// ImportStatement saves the chosen lines on fatura m of the card, all or
// nothing. A parcel line "3/10" also creates parcels 4..10 on the next faturas.
func (s *Service) ImportStatement(cardID int64, m finance.Month, items []ImportItem, source string) ([]int64, error) {
	if _, err := s.Card(cardID); err != nil {
		return nil, err
	}
	if m == 0 {
		return nil, invalid("mês da fatura inválido")
	}
	if source == "" {
		source = SourceImport
	}
	ins := make([]TxIn, len(items))
	for i, it := range items {
		ins[i] = TxIn{Kind: finance.Expense, Date: it.Date, Amount: it.Amount, Category: it.Category,
			Description: it.Description, Source: source, Payment: finance.Credit, CardID: cardID, Statement: m,
			Installments: it.Installments, InstallmentN: it.InstallmentN, ParcelAmount: true}
	}
	return s.AddTxs(ins)
}
