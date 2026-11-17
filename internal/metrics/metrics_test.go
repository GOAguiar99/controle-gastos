package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func scrape(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("scrape: %d", rec.Code)
	}
	return rec.Body.String()
}

// Labels come from the route pattern, never the URL: the MCP token in
// /mcp/{token} must not appear in any metric.
func TestInstrumentLabelsByPatternNotURL(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp/{token}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusAccepted) })
	mux.HandleFunc("GET /lancamentos/{id}/editar", func(w http.ResponseWriter, r *http.Request) {})
	// a middleware that copies the request, like the app's actor middleware
	copying := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/bloqueado" {
				http.Error(w, "no", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r.WithContext(r.Context()))
		})
	}
	h := Instrument(copying(Route(mux)))
	for _, p := range []string{"/mcp/segredo-do-token-xyz", "/lancamentos/42/editar", "/nao-existe", "/bloqueado"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", p, nil))
	}
	out := scrape(t)
	for _, bad := range []string{"segredo-do-token-xyz", "/lancamentos/42"} {
		if strings.Contains(out, bad) {
			t.Errorf("métrica vazou a URL %q", bad)
		}
	}
	for _, want := range []string{
		`code="202",route="/mcp/{token}"`,
		`code="200",route="GET /lancamentos/{id}/editar"`,
		`code="404",route="unmatched"`,
		`code="429",route="rejected"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("faltou série com %s", want)
		}
	}
}

func TestToolAndAuditCounters(t *testing.T) {
	ObserveTool("get_overview", "ok", 10*time.Millisecond)
	ObserveTool("add_transactions", "invalid", time.Millisecond)
	CountAudit("login_failure")
	out := scrape(t)
	for _, want := range []string{
		`cg_mcp_tool_calls_total{outcome="ok",tool="get_overview"} 1`,
		`cg_mcp_tool_calls_total{outcome="invalid",tool="add_transactions"} 1`,
		`cg_audit_events_total{event="login_failure"} 1`,
		`cg_mcp_tool_duration_seconds_bucket{tool="get_overview"`,
		"go_goroutines",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("faltou %s", want)
		}
	}
}

func TestCheckAddr(t *testing.T) {
	for addr, ok := range map[string]bool{
		"127.0.0.1:9091": true, "localhost:9091": true, "[::1]:9091": true,
		":9091": false, "0.0.0.0:9091": false, "10.0.0.5:9091": false, "sem-porta": false,
	} {
		if err := CheckAddr(addr); (err == nil) != ok {
			t.Errorf("CheckAddr(%q) = %v, queria ok=%v", addr, err, ok)
		}
	}
}
