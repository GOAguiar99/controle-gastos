// Package web serves the server-rendered UI.
package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"controle-gastos/internal/db"
	"controle-gastos/internal/finance"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

type Server struct {
	st   *db.Store
	auth *Auth
	loc  *time.Location
	tpl  map[string]*template.Template
	now  func() time.Time
}

func New(st *db.Store, auth *Auth, loc *time.Location) (*Server, error) {
	s := &Server{st: st, auth: auth, loc: loc, now: time.Now, tpl: map[string]*template.Template{}}
	pages := []string{"dashboard", "list", "recurrences", "debts", "config", "edit_tx", "login"}
	for _, p := range pages {
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/partials.html", "templates/"+p+".html")
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", p, err)
		}
		s.tpl[p] = t
	}
	return s, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheStatic(http.FileServerFS(static))))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.logout)

	mux.HandleFunc("GET /{$}", s.dashboard)
	mux.HandleFunc("POST /caixa", s.saveCash)

	mux.HandleFunc("GET /receitas", s.list(finance.Income))
	mux.HandleFunc("GET /gastos", s.list(finance.Expense))
	mux.HandleFunc("POST /lancamentos", s.createTx)
	mux.HandleFunc("GET /lancamentos/{id}/editar", s.editTxPage)
	mux.HandleFunc("POST /lancamentos/{id}", s.updateTx)
	mux.HandleFunc("POST /lancamentos/{id}/excluir", s.deleteTx)
	mux.HandleFunc("POST /previstos/confirmar", s.confirmOccurrence)
	mux.HandleFunc("POST /previstos/pular", s.skipOccurrence)
	mux.HandleFunc("POST /previstos/restaurar", s.unskipOccurrence)

	mux.HandleFunc("GET /recorrentes", s.recurrencesPage)
	mux.HandleFunc("POST /recorrentes", s.saveRecurrence)
	mux.HandleFunc("POST /recorrentes/{id}", s.saveRecurrence)
	mux.HandleFunc("POST /recorrentes/{id}/excluir", s.deleteRecurrence)

	mux.HandleFunc("GET /dividas", s.debtsPage)
	mux.HandleFunc("POST /dividas", s.saveDebt)
	mux.HandleFunc("POST /dividas/{id}", s.saveDebt)
	mux.HandleFunc("POST /dividas/{id}/excluir", s.deleteDebt)

	mux.HandleFunc("GET /config", s.configPage)
	mux.HandleFunc("POST /config/limites", s.saveLimits)
	mux.HandleFunc("POST /config/categorias", s.addCategory)
	mux.HandleFunc("POST /config/categorias/{id}", s.updateCategory)
	mux.HandleFunc("GET /export/lancamentos.csv", s.exportCSV)
	mux.HandleFunc("GET /export/backup.db", s.exportDB)

	return s.middleware(mux)
}

func cacheStatic(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		h.ServeHTTP(w, r)
	})
}

// today is the current calendar date in the configured timezone, as UTC midnight.
func (s *Server) today() time.Time { return finance.DateOnly(s.now().In(s.loc)) }

type page map[string]any

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data page) {
	data["Nav"] = name
	data["AuthEnabled"] = s.auth.Enabled()
	if _, ok := data["Flash"]; !ok {
		data["Flash"] = r.URL.Query().Get("msg")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tpl[name].Execute(w, data); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	log.Printf("error: %v", err)
	http.Error(w, "Erro interno: "+err.Error(), http.StatusInternalServerError)
}

func badRequest(w http.ResponseWriter, msg string) { http.Error(w, msg, http.StatusBadRequest) }

// back redirects to a local path supplied by the form, or fallback.
func back(w http.ResponseWriter, r *http.Request, fallback string) {
	to := r.FormValue("back")
	if !strings.HasPrefix(to, "/") || strings.HasPrefix(to, "//") || strings.Contains(to, "\\") {
		to = fallback
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (s *Server) monthParam(r *http.Request) finance.Month {
	if m, err := finance.ParseMonth(r.URL.Query().Get("mes")); err == nil {
		return m
	}
	return finance.MonthOf(s.today())
}

func pathID(r *http.Request) (int64, error) { return strconv.ParseInt(r.PathValue("id"), 10, 64) }

func formID(r *http.Request, key string) int64 {
	n, _ := strconv.ParseInt(r.FormValue(key), 10, 64)
	return n
}

func formInt(r *http.Request, key string, def int) int {
	n, err := strconv.Atoi(r.FormValue(key))
	if err != nil {
		return def
	}
	return n
}

// ParseMoney accepts "1.234,56", "1234,56", "1234.56", "R$ 1.234" → cents.
func ParseMoney(s string) (int64, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "R$"))
	s = strings.ReplaceAll(s, " ", "")
	if s == "" {
		return 0, fmt.Errorf("valor vazio")
	}
	if strings.Contains(s, ",") {
		s = strings.ReplaceAll(s, ".", "")
		s = strings.Replace(s, ",", ".", 1)
	} else if strings.Count(s, ".") > 1 {
		s = strings.ReplaceAll(s, ".", "")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("valor inválido %q", s)
	}
	return int64(math.Round(f * 100)), nil
}

// ---------- template helpers ----------

func brl(cents int64) string {
	neg := cents < 0
	if neg {
		cents = -cents
	}
	reais := strconv.FormatInt(cents/100, 10)
	var b strings.Builder
	for i, c := range reais {
		if i > 0 && (len(reais)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	out := fmt.Sprintf("R$ %s,%02d", b.String(), cents%100)
	if neg {
		return "−" + out
	}
	return out
}

// moneyInput formats cents for an <input>, e.g. "1234,56".
func moneyInput(cents int64) string {
	if cents == 0 {
		return ""
	}
	return strings.Replace(fmt.Sprintf("%.2f", float64(cents)/100), ".", ",", 1)
}

func pct(f float64) string { return fmt.Sprintf("%.0f%%", f*100) }

// barPct is a/b as an integer percentage clamped to [0, 100] for CSS widths.
func barPct(a, b int64) int {
	if b <= 0 || a <= 0 {
		return 0
	}
	p := int(a * 100 / b)
	if p > 100 {
		return 100
	}
	return p
}

type option struct {
	V int
	L string
}

var intervalOptions = []option{{1, "Mensal"}, {2, "Bimestral"}, {3, "Trimestral"}, {6, "Semestral"}, {12, "Anual"}}

var weekdays = [...]string{"dom", "seg", "ter", "qua", "qui", "sex", "sáb"}

var funcs = template.FuncMap{
	"brl":    brl,
	"money":  moneyInput,
	"pct":    pct,
	"barPct": barPct,
	"ratio": func(a, b int64) float64 {
		if b == 0 {
			return 0
		}
		return float64(a) / float64(b)
	},
	"dm":         func(t time.Time) string { return t.Format("02/01") },
	"dmy":        func(t time.Time) string { return t.Format("02/01/2006") },
	"iso":        func(t time.Time) string { return t.Format("2006-01-02") },
	"weekday":    func(t time.Time) string { return weekdays[t.Weekday()] },
	"neg":        func(v int64) bool { return v < 0 },
	"json":       func(v any) (template.JS, error) { b, err := json.Marshal(v); return template.JS(b), err },
	"add":        func(a, b int) int { return a + b },
	"isZeroTime": func(t time.Time) bool { return t.IsZero() },
	"intervals":  func() []option { return intervalOptions },
	"kindLabel": func(k finance.Kind) string {
		if k == finance.Income {
			return "Receita"
		}
		return "Gasto"
	},
	"bucketLabel": func(b finance.Bucket) string {
		switch b {
		case finance.Card:
			return "Cartão"
		case finance.Fixed:
			return "Fixos"
		}
		return "Outros"
	},
}
