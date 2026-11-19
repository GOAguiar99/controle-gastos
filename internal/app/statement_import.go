package app

import (
	"encoding/csv"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"controle-gastos/internal/finance"
)

// SourceImport marks transactions entered by importing a card statement.
const SourceImport = "import"

// StatementLine is one line read from a pasted or uploaded card statement.
type StatementLine struct {
	Date         time.Time
	Description  string // without the "3/10" installment suffix
	Amount       int64  // cents, positive
	Credit       bool   // pagamento, estorno or other credit: not a purchase
	InstallmentN int    // parcela N de Installments (0 = à vista)
	Installments int
	Raw          string
}

var (
	reISODate  = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})`)
	reBRDate   = regexp.MustCompile(`^(\d{1,2})[/.\-](\d{1,2})(?:[/.\-](\d{2,4}))?\b`)
	reTextDate = regexp.MustCompile(`(?i)^(\d{1,2})\s*(?:de\s+)?(jan|fev|mar|abr|mai|jun|jul|ago|set|out|nov|dez)[a-zç]*\.?(?:\s*(?:de\s+)?(\d{4}))?\b`)
	reAmount   = regexp.MustCompile(`(-?\s*(?:R\$)?\s*-?\d{1,3}(?:\.\d{3})*,\d{2}|-?\s*(?:R\$)?\s*-?\d+(?:[.,]\d{1,2})?)\s*$`)
	reParcel   = regexp.MustCompile(`(?i)[\s\-–(]*(?:parc(?:ela)?\.?\s*)?(\d{1,2})\s*(?:/|de)\s*(\d{1,2})\)?\s*$`)
	reCredit   = regexp.MustCompile(`(?i)\b(pagamento|pgto|estorno|cr[ée]dito de|devolu[cç][aã]o|reembolso|cashback)\b`)
)

var ptMonths = map[string]time.Month{"jan": 1, "fev": 2, "mar": 3, "abr": 4, "mai": 5, "jun": 6, "jul": 7, "ago": 8, "set": 9, "out": 10, "nov": 11, "dez": 12}

// ParseStatement reads a card statement pasted as text or CSV: one purchase
// per line with a date, a description and an amount, in the formats bank
// apps export (Nubank CSV "date,title,amount", "12/11 Loja 3/10 R$ 99,90",
// "2026-11-12;Loja;-99,90"...). Dates without a year take the one that puts
// them on or before statement m. Lines it can't read are returned apart.
func ParseStatement(text string, m finance.Month) (lines []StatementLine, unread []string) {
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		l, ok := parseCSVLine(raw, m)
		if !ok {
			l, ok = parseTextLine(raw, m)
		}
		if !ok {
			if !looksLikeHeader(raw) {
				unread = append(unread, raw)
			}
			continue
		}
		l.Raw = raw
		lines = append(lines, l)
	}
	// Some exports show purchases as negative numbers: when most lines are
	// negative, flip the convention.
	neg := 0
	for _, l := range lines {
		if l.Credit {
			neg++
		}
	}
	flip := neg*2 > len(lines)
	for i := range lines {
		if flip {
			lines[i].Credit = !lines[i].Credit
		}
		if reCredit.MatchString(lines[i].Description) {
			lines[i].Credit = true
		}
	}
	return lines, unread
}

func looksLikeHeader(s string) bool {
	f := fold(s)
	return !strings.ContainsAny(s, "0123456789") || strings.Contains(f, "data") && (strings.Contains(f, "valor") || strings.Contains(f, "descri"))
}

// parseCSVLine reads "date<sep>description<sep>amount" in any field order.
func parseCSVLine(raw string, m finance.Month) (StatementLine, bool) {
	sep := rune(0)
	for _, c := range []rune{';', '\t', ','} {
		if strings.ContainsRune(raw, c) {
			sep = c
			break
		}
	}
	if sep == 0 {
		return StatementLine{}, false
	}
	r := csv.NewReader(strings.NewReader(raw))
	r.Comma, r.LazyQuotes, r.FieldsPerRecord = sep, true, -1
	fields, err := r.Read()
	if err != nil || len(fields) < 3 {
		return StatementLine{}, false
	}
	var l StatementLine
	var desc []string
	gotDate, gotAmount := false, false
	for i, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if !gotDate {
			if d, rest, ok := leadingDate(f, m); ok && strings.TrimSpace(rest) == "" {
				l.Date, gotDate = d, true
				continue
			}
		}
		// the amount is the last numeric field
		if i >= 1 && isAmount(f) && !gotAmountAfter(fields[i+1:]) {
			v, neg, ok := parseAmount(f)
			if ok {
				l.Amount, l.Credit, gotAmount = v, neg, true
				continue
			}
		}
		desc = append(desc, f)
	}
	if !gotDate || !gotAmount || l.Amount == 0 {
		return StatementLine{}, false
	}
	l.Description = strings.Join(desc, " ")
	splitParcel(&l)
	return l, true
}

func gotAmountAfter(rest []string) bool {
	for _, f := range rest {
		if isAmount(strings.TrimSpace(f)) {
			return true
		}
	}
	return false
}

func isAmount(f string) bool {
	m := reAmount.FindString(f)
	return m != "" && strings.TrimSpace(m) == strings.TrimSpace(f)
}

// parseTextLine reads "date description amount" separated by spaces.
func parseTextLine(raw string, m finance.Month) (StatementLine, bool) {
	d, rest, ok := leadingDate(raw, m)
	if !ok {
		return StatementLine{}, false
	}
	loc := reAmount.FindStringIndex(rest)
	if loc == nil {
		return StatementLine{}, false
	}
	v, neg, ok := parseAmount(rest[loc[0]:loc[1]])
	if !ok || v == 0 {
		return StatementLine{}, false
	}
	l := StatementLine{Date: d, Amount: v, Credit: neg, Description: strings.TrimSpace(rest[:loc[0]])}
	if l.Description == "" {
		return StatementLine{}, false
	}
	splitParcel(&l)
	return l, true
}

// leadingDate parses the date at the start of s and returns what follows.
func leadingDate(s string, m finance.Month) (time.Time, string, bool) {
	var y, mon, day int
	var rest string
	switch {
	case reISODate.MatchString(s):
		p := reISODate.FindStringSubmatch(s)
		y, _ = strconv.Atoi(p[1])
		mon, _ = strconv.Atoi(p[2])
		day, _ = strconv.Atoi(p[3])
		rest = s[len(p[0]):]
	case reBRDate.MatchString(s):
		p := reBRDate.FindStringSubmatch(s)
		day, _ = strconv.Atoi(p[1])
		mon, _ = strconv.Atoi(p[2])
		if p[3] != "" {
			y, _ = strconv.Atoi(p[3])
			if y < 100 {
				y += 2000
			}
		}
		rest = s[len(p[0]):]
	case reTextDate.MatchString(s):
		p := reTextDate.FindStringSubmatch(s)
		day, _ = strconv.Atoi(p[1])
		mon = int(ptMonths[strings.ToLower(p[2])])
		if p[3] != "" {
			y, _ = strconv.Atoi(p[3])
		}
		rest = s[len(p[0]):]
	default:
		return time.Time{}, "", false
	}
	if mon < 1 || mon > 12 || day < 1 || day > 31 {
		return time.Time{}, "", false
	}
	if y == 0 {
		// a statement holds purchases up to its closing month
		y = m.Year()
		if time.Month(mon) > m.Mon() {
			y--
		}
	}
	d := time.Date(y, time.Month(mon), day, 0, 0, 0, 0, time.UTC)
	if d.Day() != day {
		return time.Time{}, "", false
	}
	return d, rest, true
}

// parseAmount reads 1.234,56 / 1234,56 / 1234.56 / -R$ 12,30 (cents, negative?).
func parseAmount(s string) (int64, bool, bool) {
	s = strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(s), "R$", ""), " ", "")
	neg := strings.Contains(s, "-")
	s = strings.ReplaceAll(s, "-", "")
	switch {
	case strings.Contains(s, ","):
		s = strings.ReplaceAll(s, ".", "")
		s = strings.ReplaceAll(s, ",", ".")
	case strings.Count(s, ".") > 1:
		s = strings.ReplaceAll(s, ".", "")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0, false, false
	}
	return finance.FromReais(v), neg, true
}

// splitParcel moves a trailing "3/10" or "Parcela 3 de 10" out of the description.
func splitParcel(l *StatementLine) {
	p := reParcel.FindStringSubmatchIndex(l.Description)
	if p == nil {
		return
	}
	n, _ := strconv.Atoi(l.Description[p[2]:p[3]])
	of, _ := strconv.Atoi(l.Description[p[4]:p[5]])
	if of < 2 || n < 1 || n > of {
		return
	}
	desc := strings.TrimRight(strings.TrimSpace(l.Description[:p[0]]), " -–")
	if desc == "" {
		return
	}
	l.Description, l.InstallmentN, l.Installments = desc, n, of
}

// ---------- preview and import ----------

// Import row statuses.
const (
	ImportNew       = "novo"
	ImportDuplicate = "duplicado"
	ImportIgnore    = "ignorar"
)

// ImportRow is a statement line with what the app suggests doing with it.
type ImportRow struct {
	StatementLine
	Status     string // novo, duplicado ou ignorar
	Note       string
	CategoryID int64  // suggested from the last expense with the same description (0 = none)
	Category   string // its name
}

// ImportPreview is what importing a statement would do.
type ImportPreview struct {
	Card      finance.CreditCard
	Month     finance.Month
	Rows      []ImportRow
	Unread    []string
	New       int64 // soma das linhas novas
	NewCount  int
	Declared  int64 // valor informado da fatura, se houver
	Existing  int64 // soma dos lançamentos que a fatura já tem
	LooseNote string
}

// PreviewStatement reads a statement for card cardID, month m, and marks each
// line new, already entered (same card, value and date within 3 days, or the
// same parcel) or to ignore (payments and credits), suggesting a category.
func (s *Service) PreviewStatement(cardID int64, m finance.Month, text string) (ImportPreview, error) {
	c, err := s.Card(cardID)
	if err != nil {
		return ImportPreview{}, err
	}
	if m == 0 {
		return ImportPreview{}, invalid("mês da fatura inválido")
	}
	p := ImportPreview{Card: c, Month: m}
	lines, unread := ParseStatement(text, m)
	p.Unread = unread
	if len(lines) == 0 {
		return p, invalid("nenhuma linha reconhecida: cole uma compra por linha com data, descrição e valor")
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
		case l.Credit:
			r.Status, r.Note = ImportIgnore, "crédito, pagamento ou estorno"
		case l.InstallmentN > 0:
			if e, ok, err := s.st.InstallmentParcel(cardID, l.InstallmentN, l.Installments, l.Amount, m); err != nil {
				return p, err
			} else if ok && !used[e.ID] {
				used[e.ID] = true
				r.Status, r.Note = ImportDuplicate, "parcela já lançada"
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
			r.CategoryID, r.Category = cat.ID, cat.Name
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
		full[k] = catGuess{e.CategoryID, e.Category}
		if w := firstWord(k); w != "" {
			first[w] = catGuess{e.CategoryID, e.Category}
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

func descKey(s string) string {
	return strings.Join(strings.Fields(reNoise.ReplaceAllString(fold(s), " ")), " ")
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
