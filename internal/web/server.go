// Package web serves the server-rendered UI.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
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

	"controle-gastos/internal/app"
	"controle-gastos/internal/db"
	"controle-gastos/internal/finance"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

type Server struct {
	svc  *app.Service
	st   *db.Store // read-only use for rendering; writes go through svc
	auth *Auth
	tpl  map[string]*template.Template
	mcp  http.Handler // MCP endpoint served at /mcp/{token}; nil = disabled
}

// assetVersion is a short hash of the embedded static files, appended to their
// URLs so browsers fetch new CSS/JS right after a deploy.
var assetVersion = func() string {
	h := sha256.New()
	fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := staticFS.ReadFile(p)
			h.Write(b)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:10]
}()

// New builds the web server. mcp, when non-nil, is served at the secret URL /mcp/{token}.
func New(svc *app.Service, auth *Auth, mcp http.Handler) (*Server, error) {
	s := &Server{svc: svc, st: svc.Store(), auth: auth, mcp: mcp, tpl: map[string]*template.Template{}}
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
	mux.HandleFunc("POST /config/mcp/rotar", s.rotateMCP)
	if s.mcp != nil {
		mux.Handle("/mcp/{token}", s.mcpGate(s.mcp))
	}

	return s.middleware(mux)
}

func cacheStatic(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		h.ServeHTTP(w, r)
	})
}

// today is the current calendar date in the configured timezone, as UTC midnight.
func (s *Server) today() time.Time { return s.svc.Today() }

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

// fail answers 400 with the message for invalid input, 500 otherwise.
func (s *Server) fail(w http.ResponseWriter, err error) {
	if app.IsValidation(err) {
		badRequest(w, err.Error())
		return
	}
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

// ---------- template helpers ----------

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
	"brl":    finance.BRL,
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
	"newDebt":    func() db.Debt { return db.Debt{} },
	"dict": func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	},
	"asset": func(name string) string { return "/static/" + name + "?v=" + assetVersion },
	// rate prints a % rate with a comma and no trailing zeros: 0,9631393
	"rate": func(f float64) string {
		return strings.Replace(strconv.FormatFloat(f, 'f', -1, 64), ".", ",", 1)
	},
	"pctDec": func(f float64) string { return strings.Replace(fmt.Sprintf("%.1f%%", f*100), ".", ",", 1) },
	// pctWidth is a CSS width (0..100) with one decimal so small progress still shows
	"pctWidth": func(f float64) string { return fmt.Sprintf("%.1f", math.Max(0, math.Min(100, f*100))) },
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
