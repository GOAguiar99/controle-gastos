// Package web serves the server-rendered UI.
package web

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"controle-gastos/internal/app"
	"controle-gastos/internal/db"
	"controle-gastos/internal/finance"
	"controle-gastos/internal/metrics"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Server is the web UI. Each request gets the Service of the signed-in
// user's ledger (svcOf); views may read through its Store, writes go through
// the Service.
type Server struct {
	app    *app.App
	cfg    Config
	google *googleAuth // nil when only DevLogin is enabled
	tpl    map[string]*template.Template
	mcp    http.Handler // MCP endpoint served at /mcp/{token}; nil = disabled

	healthMu  sync.Mutex // guards the cached /healthz result
	healthAt  time.Time
	healthErr error

	authLimit *limiter // per client IP on /auth/*
	mcpLimit  *limiter // per MCP link
}

// Audit is app.Audit, for the web layer's security events.
var Audit = app.Audit

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

// New builds the web server. mcp, when non-nil, is served at the secret URLs
// /mcp/{token}; it finds the link owner's Service with ServiceFrom.
func New(ctx context.Context, a *app.App, cfg Config, mcp http.Handler) (*Server, error) {
	if cfg.Google == nil && !cfg.DevLogin && !cfg.Password {
		return nil, errors.New("configure o login com Google ou APP_AUTH=password")
	}
	if cfg.Google != nil && cfg.Password {
		return nil, errors.New("escolha um só modo de login: Google ou senha")
	}
	if cfg.BaseURL == "" && cfg.Google != nil {
		cfg.BaseURL = cfg.Google.BaseURL
	}
	if len(cfg.Secret) < 32 {
		return nil, errors.New("segredo do servidor curto demais (mínimo 32 bytes)")
	}
	s := &Server{app: a, cfg: cfg, mcp: mcp, tpl: map[string]*template.Template{},
		authLimit: newLimiter(30, 30), mcpLimit: newLimiter(120, 60)}
	if cfg.Google != nil {
		g, err := newGoogleAuth(ctx, cfg.Google)
		if err != nil {
			return nil, fmt.Errorf("login com Google: %w", err)
		}
		s.google = g
	}
	pages := []string{"dashboard", "list", "recurrences", "debts", "profile", "edit_tx", "login", "privacy", "terms", "pending", "admin", "guide", "cards", "statement"}
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
	mux.HandleFunc("GET /healthz", s.healthz)

	mux.HandleFunc("GET /login", s.loginPage)
	if s.google != nil {
		mux.HandleFunc("GET /auth/google", s.googleStart)
		mux.HandleFunc("GET /auth/google/callback", s.googleCallback)
	}
	if s.cfg.DevLogin {
		mux.HandleFunc("POST /auth/dev", s.devLogin)
	}
	if s.cfg.Password {
		mux.HandleFunc("POST /auth/password", s.passwordLogin)
	}
	mux.HandleFunc("POST /logout", s.logout)

	mux.HandleFunc("GET /{$}", s.dashboard)
	mux.HandleFunc("POST /caixa", s.saveCash)
	mux.HandleFunc("POST /previsao", s.saveForecast)

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
	mux.HandleFunc("POST /recorrentes/{id}/aceitar", s.acceptRecurrence)
	mux.HandleFunc("POST /recorrentes/{id}/descartar", s.dismissRecurrence)

	mux.HandleFunc("GET /dividas", s.debtsPage)
	mux.HandleFunc("POST /dividas", s.saveDebt)
	mux.HandleFunc("POST /dividas/{id}", s.saveDebt)
	mux.HandleFunc("POST /dividas/{id}/excluir", s.deleteDebt)
	mux.HandleFunc("GET /cartoes", s.cardsPage)
	mux.HandleFunc("POST /cartoes", s.saveCard)
	mux.HandleFunc("POST /cartoes/{id}", s.saveCard)
	mux.HandleFunc("POST /cartoes/{id}/excluir", s.deleteCard)
	mux.HandleFunc("GET /cartoes/{id}/fatura", s.statementPage)
	mux.HandleFunc("POST /cartoes/{id}/fatura", s.setStatementTotal)
	mux.HandleFunc("POST /lancamentos/{id}/parcelas/excluir", s.deleteInstallments)
	mux.HandleFunc("POST /lancamentos/{id}/categoria", s.recategorize)

	mux.HandleFunc("GET /perfil", s.profilePage(tabAccount))
	mux.HandleFunc("GET /perfil/preferencias", s.profilePage(tabPrefs))
	mux.HandleFunc("GET /perfil/agentes", s.profilePage(tabAgents))
	mux.HandleFunc("GET /perfil/compartilhar", s.profilePage(tabShare))
	// the old Config page lives in the profile now
	mux.Handle("GET /config", http.RedirectHandler("/perfil", http.StatusMovedPermanently))
	mux.HandleFunc("POST /config/limites", s.saveLimits)
	mux.HandleFunc("POST /config/cartoes", s.saveCardsModule)
	mux.HandleFunc("POST /config/memorias/apagar", s.forgetCategory)
	mux.HandleFunc("POST /config/contas", s.saveCashAccount)
	mux.HandleFunc("POST /config/contas/{id}", s.saveCashAccount)
	mux.HandleFunc("POST /config/contas/{id}/excluir", s.deleteCashAccount)
	mux.HandleFunc("POST /config/categorias", s.addCategory)
	mux.HandleFunc("GET /export/lancamentos.csv", s.exportCSV)
	mux.HandleFunc("GET /export/dados.json", s.exportJSON)
	mux.HandleFunc("POST /config/excluir-conta", s.deleteAccount)
	mux.HandleFunc("POST /config/compartilhar", s.inviteMember)
	mux.HandleFunc("POST /config/convites/{id}/cancelar", s.cancelInvite)
	mux.HandleFunc("POST /config/membros/{id}/remover", s.removeMember)
	mux.HandleFunc("POST /config/sair-da-carteira", s.leaveLedger)
	mux.HandleFunc("POST /convites/{id}/aceitar", s.acceptInvite)
	mux.HandleFunc("POST /convites/{id}/recusar", s.declineInvite)
	mux.HandleFunc("GET /privacidade", s.legalPage("privacy", "Política de Privacidade"))
	mux.HandleFunc("GET /como-usar", s.legalPage("guide", "Como usar"))
	mux.HandleFunc("GET /admin", s.adminPage)
	mux.HandleFunc("POST /admin/usuarios/{id}/{action}", s.adminAction)
	mux.HandleFunc("GET /termos", s.legalPage("terms", "Termos de Uso"))
	mux.HandleFunc("POST /config/mcp", s.createMCP)
	mux.HandleFunc("POST /config/mcp/{id}/revogar", s.revokeMCP)
	if s.mcp != nil {
		mux.Handle("/mcp/{token}", s.mcpGate(s.mcp))
	}

	return metrics.Instrument(s.middleware(metrics.Route(mux)))
}

// healthCacheTTL bounds how often a flood of /healthz (a public URL) can
// reach the database.
const healthCacheTTL = 2 * time.Second

// healthz answers 200 "ok" when the database responds within 2 s and 503
// otherwise; uptime monitors and the deploy check call it.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	s.healthMu.Lock()
	if time.Since(s.healthAt) > healthCacheTTL {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		s.healthErr = s.app.Ping(ctx)
		cancel()
		s.healthAt = time.Now()
		if s.healthErr != nil {
			logErr(fmt.Errorf("healthz: %w", s.healthErr))
		}
	}
	err := s.healthErr
	s.healthMu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		http.Error(w, "banco de dados indisponível", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ok"))
}

func cacheStatic(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		h.ServeHTTP(w, r)
	})
}

// today is the current calendar date in the configured timezone, as UTC midnight.
func (s *Server) today() time.Time { return s.app.Today() }

type page map[string]any

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data page) {
	data["Nav"] = name
	data["Contact"] = s.cfg.ContactEmail
	data["Controller"] = s.cfg.ControllerName
	if svc := svcOf(r); svc != nil {
		a := actorOf(r)
		data["User"] = a.User
		data["PendingRecs"] = svc.Store().PendingRecurrences()
		data["CardsOn"] = svc.CardsEnabled()
		data["Shared"] = s.app.IsShared(r.Context(), a)
		if app.IsAdmin(a) {
			data["IsAdmin"] = true
			data["PendingUsers"] = s.app.PendingCount(r.Context(), a)
		}
	} else {
		data["Google"] = s.google != nil
		data["DevLogin"] = s.cfg.DevLogin
		data["PasswordLogin"] = s.cfg.Password
	}
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
	logErr(err)
	http.Error(w, "Erro interno. Tente de novo; se continuar, avise o responsável.", http.StatusInternalServerError)
}

func logErr(err error) { log.Printf("error: %v", err) }

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

// brl formats cents as "R$ 1.234,56" wrapped in the money marker that the
// "Ocultar valores" toggle masks (app.css, privacy.js). In attributes
// html/template strips the tags; use brlText there to make that explicit.
func brl(cents int64) template.HTML {
	return template.HTML(`<span class="money"><span>` + template.HTMLEscapeString(finance.BRL(cents)) + `</span></span>`)
}

var funcs = template.FuncMap{
	"brl":     brl,
	"brlText": finance.BRL,
	"money":   moneyInput,
	"pct":     pct,
	"barPct":  barPct,
	"ratio": func(a, b int64) float64 {
		if b == 0 {
			return 0
		}
		return float64(a) / float64(b)
	},
	"dm":      func(t time.Time) string { return t.Format("02/01") },
	"dmy":     func(t time.Time) string { return t.Format("02/01/2006") },
	"iso":     func(t time.Time) string { return t.Format("2006-01-02") },
	"weekday": func(t time.Time) string { return weekdays[t.Weekday()] },
	"neg":     func(v int64) bool { return v < 0 },
	"json":    func(v any) (template.JS, error) { b, err := json.Marshal(v); return template.JS(b), err },
	"add":     func(a, b int) int { return a + b },
	"sub":     func(a, b int64) int64 { return a - b },
	// pctNum prints a percentage without trailing zeros, with a comma: 120 / 12,5
	"pctNum": func(f float64) string {
		return strings.Replace(strconv.FormatFloat(math.Round(f*10)/10, 'f', -1, 64), ".", ",", 1)
	},
	"pct2":       func(f float64) string { return strings.Replace(strconv.FormatFloat(f, 'f', 2, 64), ".", ",", 1) },
	"isZeroTime": func(t time.Time) bool { return t.IsZero() },
	"intervals":  func() []option { return intervalOptions },
	"newDebt":    func() db.Debt { return db.Debt{} },
	"newCard":    func() finance.CreditCard { return finance.CreditCard{} },
	"dict": func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	},
	"isLocal": func(u db.User) bool { return strings.HasPrefix(u.GoogleSub, db.LocalSubPrefix) },
	"initial": func(u db.User) string {
		for _, r := range strings.TrimSpace(u.Name + u.Email) {
			return strings.ToUpper(string(r))
		}
		return "?"
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
	"payMethods": func() []finance.PaymentMethod { return finance.PaymentMethods },
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
