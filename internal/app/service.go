// Package app is the application's API: every business rule and validation
// lives here. The web handlers and the MCP tools are thin adapters on top of
// it, so the UI and Claude always behave the same way.
package app

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"controle-gastos/internal/db"
	"controle-gastos/internal/finance"
)

// ValidationError is a user-facing message (pt-BR) about invalid input.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// IsValidation reports whether err is a ValidationError (bad input, not a bug).
func IsValidation(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}

func notFound(err error, what string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return invalid("%s não encontrada", what)
	}
	return err
}

// Sources recorded on transactions.
const (
	SourceManual = "manual"
	SourceMCP    = "mcp"
)

type Service struct {
	st  *db.Store
	loc *time.Location
	now func() time.Time
}

func New(st *db.Store, loc *time.Location) *Service {
	if loc == nil {
		loc = time.UTC
	}
	return &Service{st: st, loc: loc, now: time.Now}
}

// SetClock overrides the current time (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// Store gives read access for views that only render data. Writes must go
// through the Service methods.
func (s *Service) Store() *db.Store { return s.st }

// Today is the current calendar date in the configured timezone, as UTC midnight.
func (s *Service) Today() time.Time { return finance.DateOnly(s.now().In(s.loc)) }

func ParseKind(v string) (finance.Kind, error) {
	switch finance.Kind(v) {
	case finance.Income, finance.Expense:
		return finance.Kind(v), nil
	}
	return "", invalid("tipo inválido (use income ou expense)")
}

// ParseBucket is strict; "" means outros.
func ParseBucket(v string) (finance.Bucket, error) {
	switch finance.Bucket(v) {
	case finance.Card, finance.Fixed, finance.Other:
		return finance.Bucket(v), nil
	case "":
		return finance.Other, nil
	}
	return "", invalid("grupo inválido (use cartao, fixo ou outros)")
}

// ---------- categories ----------

func (s *Service) Categories(kind finance.Kind) ([]db.Category, error) { return s.st.Categories(kind) }

func (s *Service) AddCategory(name string, kind finance.Kind, bucket finance.Bucket) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" || (kind != finance.Income && kind != finance.Expense) {
		return 0, invalid("nome e tipo são obrigatórios")
	}
	if _, err := ParseBucket(string(bucket)); err != nil {
		return 0, err
	}
	if c, ok, err := s.findCategory(kind, name); err != nil || ok {
		return c.ID, err
	}
	return s.st.EnsureCategory(name, kind, bucket)
}

func (s *Service) SetCategoryBucket(id int64, bucket finance.Bucket) error {
	if _, err := ParseBucket(string(bucket)); err != nil {
		return err
	}
	if _, err := s.st.Category(id); err != nil {
		return notFound(err, "categoria")
	}
	return s.st.SetCategoryBucket(id, bucket)
}

// fold lowercases and strips Portuguese accents so "Saúde" matches "saude".
func fold(s string) string {
	r := strings.NewReplacer(
		"á", "a", "à", "a", "â", "a", "ã", "a", "ä", "a",
		"é", "e", "ê", "e", "è", "e", "ë", "e",
		"í", "i", "î", "i", "ì", "i", "ï", "i",
		"ó", "o", "ô", "o", "õ", "o", "ò", "o", "ö", "o",
		"ú", "u", "û", "u", "ù", "u", "ü", "u", "ç", "c",
	)
	return r.Replace(strings.ToLower(strings.Join(strings.Fields(s), " ")))
}

func (s *Service) findCategory(kind finance.Kind, name string) (db.Category, bool, error) {
	cats, err := s.st.Categories(kind)
	if err != nil {
		return db.Category{}, false, err
	}
	want := fold(name)
	for _, c := range cats {
		if fold(c.Name) == want {
			return c, true, nil
		}
	}
	return db.Category{}, false, nil
}

// CategoryRef picks a category by id or by name. A name that matches no
// existing category is only created when Create is set, so a typo doesn't
// silently spawn a near-duplicate.
type CategoryRef struct {
	ID     int64
	Name   string
	Create bool
}

func (s *Service) resolveCategory(kind finance.Kind, ref CategoryRef) (db.Category, error) {
	if name := strings.TrimSpace(ref.Name); name != "" && ref.ID == 0 {
		c, ok, err := s.findCategory(kind, name)
		if err != nil || ok {
			return c, err
		}
		if !ref.Create {
			cats, _ := s.st.Categories(kind)
			names := make([]string, len(cats))
			for i, c := range cats {
				names[i] = c.Name
			}
			return c, invalid("categoria %q não existe para %s; use uma existente (%s) ou peça para criá-la", name, kind, strings.Join(names, ", "))
		}
		id, err := s.st.EnsureCategory(name, kind, finance.Other)
		if err != nil {
			return c, err
		}
		return s.st.Category(id)
	}
	if ref.ID == 0 {
		return db.Category{}, invalid("escolha uma categoria")
	}
	c, err := s.st.Category(ref.ID)
	if err != nil || c.Kind != kind {
		return c, invalid("categoria inválida")
	}
	return c, nil
}

// ---------- limits ----------

func (s *Service) Limits() finance.Limits { return s.st.Limits() }

func (s *Service) SetLimits(lim finance.Limits) error {
	if lim.CardLimit < 0 || lim.MinSavings < 0 {
		return invalid("valores inválidos")
	}
	if err := s.st.SetSettingInt("card_limit_cents", lim.CardLimit); err != nil {
		return err
	}
	return s.st.SetSettingInt("min_savings_cents", lim.MinSavings)
}

// ---------- MCP access token ----------

const mcpTokenKey = "mcp_token"

// MCPToken returns the secret that forms the MCP URL, creating it on first use.
func (s *Service) MCPToken() (string, error) {
	if t := s.st.Setting(mcpTokenKey); t != "" {
		return t, nil
	}
	return s.RotateMCPToken()
}

// RotateMCPToken replaces the MCP secret; the previous URL stops working.
func (s *Service) RotateMCPToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	t := base64.RawURLEncoding.EncodeToString(b)
	return t, s.st.SetSetting(mcpTokenKey, t)
}
