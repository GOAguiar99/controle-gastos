package app

import (
	"time"

	"controle-gastos/internal/db"
)

// DataExport is everything the app stores about a person and their ledger,
// for the data-portability right (LGPD art. 18, II and V). Field names are
// stable: this is a published format.
type DataExport struct {
	ExportedAt   time.Time        `json:"exported_at"`
	Account      ExportAccount    `json:"account"`
	Limits       ExportLimits     `json:"limits"`
	Categories   []ExportCategory `json:"categories"`
	Transactions []ExportTx       `json:"transactions"`
	Recurrences  []ExportRec      `json:"recurrences"`
	Debts        []ExportDebt     `json:"debts"`
	Cash         []ExportCash     `json:"cash_snapshots"`
}

type ExportAccount struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

type ExportLimits struct {
	CardLimitCents  int64 `json:"card_limit_cents"`
	MinSavingsCents int64 `json:"min_savings_cents"`
}

type ExportCategory struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Bucket string `json:"bucket"`
}

type ExportTx struct {
	ID           int64  `json:"id"`
	Date         string `json:"date"`
	Kind         string `json:"kind"`
	AmountCents  int64  `json:"amount_cents"`
	Category     string `json:"category"`
	Description  string `json:"description"`
	RecurrenceID int64  `json:"recurrence_id,omitempty"`
	DebtID       int64  `json:"debt_id,omitempty"`
}

type ExportRec struct {
	ID             int64  `json:"id"`
	Kind           string `json:"kind"`
	Description    string `json:"description"`
	Category       string `json:"category"`
	AmountCents    int64  `json:"amount_cents"`
	Day            int    `json:"day"`
	IntervalMonths int    `json:"interval_months"`
	Start          string `json:"start"`
	EndMode        string `json:"end_mode"`
	Count          int    `json:"count,omitempty"`
	End            string `json:"end,omitempty"`
	Active         bool   `json:"active"`
	DebtID         int64  `json:"debt_id,omitempty"`
}

type ExportDebt struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	InitialCents int64   `json:"initial_cents"`
	System       string  `json:"system"`
	MonthlyRate  float64 `json:"monthly_rate_pct,omitempty"`
	Term         int     `json:"term,omitempty"`
	PaidCents    int64   `json:"paid_cents"`
}

type ExportCash struct {
	Date        string `json:"date"`
	AmountCents int64  `json:"amount_cents"`
	Note        string `json:"note,omitempty"`
}

// ExportData collects all of the actor's data. The Service is already bound
// to the actor's ledger, so nothing outside it can be included.
func (s *Service) ExportData(user db.User) (DataExport, error) {
	out := DataExport{ExportedAt: s.now().UTC(), Account: ExportAccount{Email: user.Email, Name: user.Name}}
	lim := s.st.Limits()
	out.Limits = ExportLimits{CardLimitCents: lim.CardLimit, MinSavingsCents: lim.MinSavings}
	cats, err := s.st.Categories("")
	if err != nil {
		return out, err
	}
	for _, c := range cats {
		out.Categories = append(out.Categories, ExportCategory{c.ID, c.Name, string(c.Kind), string(c.Bucket)})
	}
	txs, err := s.st.AllTransactions()
	if err != nil {
		return out, err
	}
	for _, e := range txs {
		out.Transactions = append(out.Transactions, ExportTx{e.ID, e.Date.Format("2006-01-02"), string(e.Kind), e.Amount, e.Category, e.Description, e.RecurrenceID, e.DebtID})
	}
	recs, err := s.st.Recurrences()
	if err != nil {
		return out, err
	}
	for _, r := range recs {
		x := ExportRec{ID: r.ID, Kind: string(r.Kind), Description: r.Description, Category: r.Category, AmountCents: r.Amount,
			Day: r.Day, IntervalMonths: r.Interval, Start: r.Start.String(), EndMode: r.EndMode, Count: r.Count, Active: r.Active, DebtID: r.DebtID}
		if r.EndMode == "until" {
			x.End = r.End.String()
		}
		out.Recurrences = append(out.Recurrences, x)
	}
	debts, err := s.st.Debts(s.Today())
	if err != nil {
		return out, err
	}
	for _, d := range debts {
		out.Debts = append(out.Debts, ExportDebt{d.ID, d.Name, d.Initial, d.Loan.System, d.Loan.MonthlyRate, d.Loan.Term, d.Paid})
	}
	snaps, err := s.st.Snapshots()
	if err != nil {
		return out, err
	}
	for _, c := range snaps {
		out.Cash = append(out.Cash, ExportCash{c.Date.Format("2006-01-02"), c.Amount, c.Note})
	}
	return out, nil
}
