package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"controle-gastos/internal/app"
	"controle-gastos/internal/db"
	"controle-gastos/internal/mcpserver"
)

func newTestServer(t *testing.T) (*httptest.Server, *http.Client, *app.Service) {
	t.Helper()
	st, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hash, _ := bcrypt.GenerateFromPassword([]byte("senha-de-teste"), bcrypt.MinCost)
	svc := app.New(st, time.UTC)
	srv, err := New(svc, &Auth{PasswordHash: hash, Secret: []byte(strings.Repeat("k", 32))}, mcpserver.Handler(svc))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	jar := &cookieJar{}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return ts, client, svc
}

type cookieJar struct{ c []*http.Cookie }

func (j *cookieJar) SetCookies(_ *url.URL, cs []*http.Cookie) { j.c = append(j.c, cs...) }
func (j *cookieJar) Cookies(*url.URL) []*http.Cookie          { return j.c }

func TestPagesRequireLoginAndRender(t *testing.T) {
	ts, client, _ := newTestServer(t)

	res, err := client.Get(ts.URL + "/")
	if err != nil || res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/login" {
		t.Fatalf("expected redirect to login, got %v %v", res.StatusCode, err)
	}
	res, _ = client.PostForm(ts.URL+"/login", url.Values{"password": {"errada"}})
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", res.StatusCode)
	}
	res, _ = client.PostForm(ts.URL+"/login", url.Values{"password": {"senha-de-teste"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("login: %d", res.StatusCode)
	}

	post := func(path string, v url.Values) {
		t.Helper()
		res, err := client.PostForm(ts.URL+path, v)
		if err != nil || res.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST %s: %v %v", path, res.StatusCode, err)
		}
	}
	post("/caixa", url.Values{"amount": {"1.000,00"}})
	post("/lancamentos", url.Values{"kind": {"expense"}, "date": {"2026-11-10"}, "amount": {"50,00"}, "new_category": {"Mercado"}})
	post("/lancamentos", url.Values{"kind": {"income"}, "date": {"2026-11-05"}, "amount": {"5000"}, "new_category": {"Empresa"},
		"repeat": {"on"}, "interval": {"1"}, "end_mode": {"count"}, "count": {"12"}})
	post("/recorrentes", url.Values{"kind": {"expense"}, "description": {"IPVA"}, "new_category": {"Impostos"}, "amount": {"1200"},
		"day": {"31"}, "start": {"2026-11"}, "interval": {"12"}, "end_mode": {"never"}})
	post("/dividas", url.Values{"name": {"Fulano"}, "initial": {"1000"}})
	post("/dividas", url.Values{"name": {"Banco"}, "initial": {"380.000,00"}, "system": {"sac"}, "rate": {"0,9631393"},
		"term": {"420"}, "first_due": {"2026-01-16"}, "fees": {"79,88"}, "anchor_n": {"7"}, "anchor_balance": {"377.957,86"}})
	// recurrence paying the loan (debt id 2); its projected parcel comes from the SAC table
	post("/recorrentes", url.Values{"kind": {"expense"}, "description": {"Financiamento"}, "new_category": {"Financiamento"},
		"amount": {"4651,45"}, "day": {"16"}, "start": {"2026-11"}, "interval": {"1"}, "end_mode": {"never"}, "debt_id": {"2"}})

	for _, p := range []string{"/", "/?mes=2027-02", "/receitas?mes=2026-11", "/gastos?mes=2026-11", "/recorrentes",
		"/recorrentes?tipo=income&editar=1", "/recorrentes?editar=3", "/dividas", "/dividas?editar=2", "/config",
		"/lancamentos/1/editar", "/export/lancamentos.csv", "/export/backup.db"} {
		res, err := client.Get(ts.URL + p)
		if err != nil || res.StatusCode != http.StatusOK {
			t.Errorf("GET %s: %v %v", p, res.StatusCode, err)
		}
	}

	// Dec/2026 projected parcel = parcela 12 of the SAC table (915,15 + 3.605,00 + 79,88)
	res, _ = client.Get(ts.URL + "/gastos?mes=2026-12")
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "R$ 4.600,03") {
		t.Errorf("expected SAC installment R$ 4.600,03 in dez/2026 gastos")
	}
	res, _ = client.Get(ts.URL + "/dividas")
	body, _ = io.ReadAll(res.Body)
	if !strings.Contains(string(body), "SAC") || !strings.Contains(string(body), "0/420") {
		t.Errorf("debts page missing loan info")
	}

	// cross-origin POST is rejected
	req, _ := http.NewRequest("POST", ts.URL+"/caixa", strings.NewReader("amount=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	res, _ = client.Do(req)
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin POST: %d", res.StatusCode)
	}
}

func TestMCPEndpoint(t *testing.T) {
	ts, client, svc := newTestServer(t)
	token, err := svc.MCPToken()
	if err != nil {
		t.Fatal(err)
	}
	initialize := func(path string) *http.Response {
		t.Helper()
		body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
		req, _ := http.NewRequest("POST", ts.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Origin", "https://claude.ai") // connectors call from another origin, without the session cookie
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	for _, p := range []string{"/mcp/errado", "/mcp/" + token[:len(token)-1], "/mcp/"} {
		if res := initialize(p); res.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s: %d, want 404", p, res.StatusCode)
		}
	}
	res := initialize("/mcp/" + token)
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "controle-gastos") {
		t.Fatalf("initialize: %d %s", res.StatusCode, body)
	}

	// the Config page shows the URL; rotating it kills the old one
	client.PostForm(ts.URL+"/login", url.Values{"password": {"senha-de-teste"}})
	res, _ = client.Get(ts.URL + "/config")
	body, _ = io.ReadAll(res.Body)
	if !strings.Contains(string(body), "/mcp/"+token) {
		t.Fatal("config page should show the MCP URL")
	}
	if res, _ := client.PostForm(ts.URL+"/config/mcp/rotar", nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("rotate: %d", res.StatusCode)
	}
	if res := initialize("/mcp/" + token); res.StatusCode != http.StatusNotFound {
		t.Fatalf("old token after rotate: %d", res.StatusCode)
	}
	newToken, _ := svc.MCPToken()
	if res := initialize("/mcp/" + newToken); res.StatusCode != http.StatusOK {
		t.Fatalf("new token: %d", res.StatusCode)
	}
}
