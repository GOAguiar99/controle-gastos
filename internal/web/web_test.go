package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"controle-gastos/internal/app"
	"controle-gastos/internal/db/dbtest"
	"controle-gastos/internal/mcpserver"
)

type testEnv struct {
	ts  *httptest.Server
	idp *fakeIDP
	app *app.App
}

func newTestServer(t *testing.T) (*httptest.Server, *http.Client, *testEnv) {
	t.Helper()
	idp := newFakeIDP(t)
	a := app.NewApp(dbtest.Open(t), time.UTC)
	a.SetClock(func() time.Time { return time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC) })
	a.AllowEmail = app.AllowList("ana@x.com,beto@x.com")
	ts := httptest.NewUnstartedServer(nil)
	cfg := Config{
		Google: &GoogleConfig{ClientID: idp.clientID, ClientSecret: "s", Issuer: idp.URL, BaseURL: "http://" + ts.Listener.Addr().String()},
		Secret: []byte(strings.Repeat("k", 32)),
	}
	srv, err := New(context.Background(), a, cfg, mcpserver.Handler(ServiceFrom))
	if err != nil {
		t.Fatal(err)
	}
	ts.Config.Handler = srv.Handler()
	ts.Start()
	t.Cleanup(ts.Close)
	return ts, newClient(), &testEnv{ts: ts, idp: idp, app: a}
}

func newClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

var anaUser = idpUser{Sub: "g-ana", Email: "ana@x.com", Name: "Ana Souza", Verified: true}

// googleLogin runs "Entrar com Google" against the fake provider and returns
// the app's answer to the callback.
func (e *testEnv) googleLogin(t *testing.T, client *http.Client, u idpUser, tamper func(map[string]any)) *http.Response {
	t.Helper()
	e.idp.as(u, tamper)
	res, err := client.Get(e.ts.URL + "/auth/google")
	if err != nil || res.StatusCode != http.StatusFound {
		t.Fatalf("start: %v %v", res.StatusCode, err)
	}
	res, err = client.Get(res.Header.Get("Location")) // provider authorize -> back to callback
	if err != nil || res.StatusCode != http.StatusFound {
		t.Fatalf("authorize: %v %v", res.StatusCode, err)
	}
	res, err = client.Get(res.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (e *testEnv) mustLogin(t *testing.T, client *http.Client, u idpUser) {
	t.Helper()
	if res := e.googleLogin(t, client, u, nil); res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/" {
		t.Fatalf("login: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
}

func TestPagesRequireLoginAndRender(t *testing.T) {
	ts, client, env := newTestServer(t)

	res, err := client.Get(ts.URL + "/")
	if err != nil || res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/login" {
		t.Fatalf("expected redirect to login, got %v %v", res.StatusCode, err)
	}
	env.mustLogin(t, client, anaUser)

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
	// the loan (debt id 2) came with a suggested installment recurrence (id 3), shown with a nav badge
	res, _ = client.Get(ts.URL + "/recorrentes")
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "Novas parcelas para adicionar") || !strings.Contains(string(body), `class="badge"`) {
		t.Errorf("suggestion banner/badge missing")
	}
	post("/recorrentes/3/aceitar", nil)
	// a second recurrence for the loan is refused
	res, _ = client.PostForm(ts.URL+"/recorrentes", url.Values{"kind": {"expense"}, "description": {"Financiamento"}, "new_category": {"Financiamento"},
		"amount": {"4651,45"}, "day": {"16"}, "start": {"2026-11"}, "interval": {"1"}, "end_mode": {"never"}, "debt_id": {"2"}})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("duplicate loan recurrence: %d", res.StatusCode)
	}

	for _, p := range []string{"/", "/?mes=2027-02", "/receitas?mes=2026-11", "/gastos?mes=2026-11", "/recorrentes",
		"/recorrentes?tipo=income&editar=1", "/recorrentes?editar=3", "/dividas", "/dividas?editar=2", "/perfil", "/perfil/preferencias", "/perfil/agentes", "/perfil/compartilhar",
		"/lancamentos/1/editar", "/export/lancamentos.csv"} {
		res, err := client.Get(ts.URL + p)
		if err != nil || res.StatusCode != http.StatusOK {
			t.Errorf("GET %s: %v %v", p, res.StatusCode, err)
		}
	}

	// Dec/2026 projected parcel = parcela 12 of the SAC table (915,15 + 3.605,00 + 79,88)
	res, _ = client.Get(ts.URL + "/gastos?mes=2026-12")
	body, _ = io.ReadAll(res.Body)
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

func TestGoogleLoginRejections(t *testing.T) {
	ts, _, env := newTestServer(t)
	env.app.Signup = app.SignupClosed // unknown accounts are refused instead of waiting for approval
	for name, c := range map[string]struct {
		u      idpUser
		tamper func(map[string]any)
		want   string
	}{
		"not allowed":    {idpUser{Sub: "g-eve", Email: "eve@x.com", Verified: true}, nil, "não tem acesso"},
		"unverified":     {idpUser{Sub: "g-ana", Email: "ana@x.com"}, nil, "verificado"},
		"wrong audience": {anaUser, func(c map[string]any) { c["aud"] = "other-client" }, "não foi possível"},
		"wrong nonce":    {anaUser, func(c map[string]any) { c["nonce"] = "x" }, "não foi possível"},
		"expired":        {anaUser, func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }, "não foi possível"},
		"wrong issuer":   {anaUser, func(c map[string]any) { c["iss"] = "https://evil.example" }, "não foi possível"},
	} {
		client := newClient()
		res := env.googleLogin(t, client, c.u, c.tamper)
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusForbidden || !strings.Contains(string(body), c.want) {
			t.Errorf("%s: %d, want 403 with %q", name, res.StatusCode, c.want)
		}
		if res, _ := client.Get(ts.URL + "/"); res.StatusCode != http.StatusSeeOther {
			t.Errorf("%s: signed in anyway", name)
		}
	}

	// a forged callback (state not issued to this browser) is refused
	client := newClient()
	res, _ := client.Get(ts.URL + "/auth/google/callback?code=x&state=y")
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("forged callback: %d", res.StatusCode)
	}
}

func TestLogout(t *testing.T) {
	ts, client, env := newTestServer(t)
	env.mustLogin(t, client, anaUser)
	u, _ := url.Parse(ts.URL)
	stolen := client.Jar.Cookies(u)
	if res, _ := client.PostForm(ts.URL+"/logout", nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout: %d", res.StatusCode)
	}
	// the old cookie no longer works anywhere (revoked server-side)
	other := newClient()
	other.Jar.SetCookies(u, stolen)
	if res, _ := other.Get(ts.URL + "/"); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("revoked session still works: %d", res.StatusCode)
	}
}

func TestMCPEndpoint(t *testing.T) {
	ts, client, env := newTestServer(t)
	env.mustLogin(t, client, anaUser)

	// creating a link shows its URL once
	res, _ := client.PostForm(ts.URL+"/config/mcp", url.Values{"label": {"celular"}})
	body, _ := io.ReadAll(res.Body)
	m := regexp.MustCompile(`value="(http[^"]+/mcp/[^"]+)"`).FindSubmatch(body)
	if res.StatusCode != http.StatusOK || m == nil {
		t.Fatalf("create link: %d", res.StatusCode)
	}
	link := string(m[1])
	if res, _ := client.Get(ts.URL + "/perfil/agentes"); true {
		b, _ := io.ReadAll(res.Body)
		if strings.Contains(string(b), link) || !strings.Contains(string(b), "celular") {
			t.Fatal("config must list the link by name but never show its secret again")
		}
	}

	initialize := func(u string) *http.Response {
		t.Helper()
		body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
		req, _ := http.NewRequest("POST", u, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Origin", "https://claude.ai") // connectors call from another origin, without the session cookie
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	for _, u := range []string{ts.URL + "/mcp/errado", link[:len(link)-1], ts.URL + "/mcp/"} {
		if res := initialize(u); res.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s: %d, want 404", u, res.StatusCode)
		}
	}
	res = initialize(link)
	body, _ = io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "controle-gastos") {
		t.Fatalf("initialize: %d %s", res.StatusCode, body)
	}

	// each link only reaches its owner's ledger
	beto := newClient()
	env.mustLogin(t, beto, idpUser{Sub: "g-beto", Email: "beto@x.com", Verified: true})
	res, _ = beto.PostForm(ts.URL+"/config/mcp", nil)
	body, _ = io.ReadAll(res.Body)
	betoLink := string(regexp.MustCompile(`value="(http[^"]+/mcp/[^"]+)"`).FindSubmatch(body)[1])
	client.PostForm(ts.URL+"/lancamentos", url.Values{"kind": {"expense"}, "date": {"2026-11-10"}, "amount": {"50,00"}, "new_category": {"Segredo da Ana"}})
	if n := countTx(t, link); n != 1 {
		t.Errorf("ana's link sees %d transactions, want 1", n)
	}
	if n := countTx(t, betoLink); n != 0 {
		t.Errorf("beto's link sees %d of ana's transactions", n)
	}

	// revoking kills the link
	ids := regexp.MustCompile(`/config/mcp/(\d+)/revogar`).FindAllSubmatch(mustGet(t, client, ts.URL+"/perfil/agentes"), -1)
	if len(ids) != 1 {
		t.Fatalf("links listed: %d", len(ids))
	}
	if res, _ := beto.PostForm(ts.URL+"/config/mcp/"+string(ids[0][1])+"/revogar", nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("beto revoked ana's link: %d", res.StatusCode)
	}
	if res, _ := client.PostForm(ts.URL+"/config/mcp/"+string(ids[0][1])+"/revogar", nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("revoke: %d", res.StatusCode)
	}
	if res := initialize(link); res.StatusCode != http.StatusNotFound {
		t.Fatalf("revoked link: %d", res.StatusCode)
	}
}

func mustGet(t *testing.T, c *http.Client, u string) []byte {
	t.Helper()
	res, err := c.Get(u)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %v", u, err)
	}
	b, _ := io.ReadAll(res.Body)
	return b
}

// countTx lists Nov/2026 transactions through an MCP link.
func countTx(t *testing.T, link string) int {
	t.Helper()
	ctx := context.Background()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil).
		Connect(ctx, &mcp.StreamableClientTransport{Endpoint: link}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_transactions", Arguments: map[string]any{"from": "2026-11"}})
	if err != nil || res.IsError {
		t.Fatalf("list_transactions: %v %+v", err, res)
	}
	var out struct {
		Transactions []any `json:"transactions"`
	}
	b, _ := json.Marshal(res.StructuredContent)
	json.Unmarshal(b, &out)
	return len(out.Transactions)
}

// The expense form asks how it was paid; the Análise tab splits the month by it.
func TestExpensePaymentMethodForm(t *testing.T) {
	ts, client, env := newTestServer(t)
	env.mustLogin(t, client, anaUser)
	page := string(mustGet(t, client, ts.URL+"/gastos?mes=2026-11"))
	if !strings.Contains(page, `name="payment_method"`) || !strings.Contains(page, `<option value="" selected>Automático`) {
		t.Fatal("new-expense form has no payment method (with Automático by default)")
	}
	if strings.Contains(string(mustGet(t, client, ts.URL+"/receitas?mes=2026-11")), `name="payment_method"`) {
		t.Error("income form asks for a payment method")
	}
	for _, pay := range []string{"credito", "pix"} {
		res, err := client.PostForm(ts.URL+"/lancamentos", url.Values{"kind": {"expense"}, "date": {"2026-11-10"},
			"amount": {"50,00"}, "new_category": {"Alimentação"}, "payment_method": {pay}})
		if err != nil || res.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST %s: %v %v", pay, res.StatusCode, err)
		}
	}
	res, _ := client.PostForm(ts.URL+"/lancamentos", url.Values{"kind": {"expense"}, "date": {"2026-11-10"},
		"amount": {"1,00"}, "new_category": {"Alimentação"}, "payment_method": {"cartao"}})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid payment method: %d", res.StatusCode)
	}
	an := string(mustGet(t, client, ts.URL+"/gastos?mes=2026-11&ver=analise"))
	for _, want := range []string{"Por forma de pagamento", "Crédito", "Pix", "Cartão de crédito"} {
		if !strings.Contains(an, want) {
			t.Errorf("análise misses %q", want)
		}
	}
}

// Cards: register one and set a fatura total; the cards module can be turned off.
func TestCardsAndStatementImport(t *testing.T) {
	ts, client, env := newTestServer(t)
	env.mustLogin(t, client, anaUser)
	post := func(path string, v url.Values) *http.Response {
		t.Helper()
		res, err := client.PostForm(ts.URL+path, v)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := post("/cartoes", url.Values{"name": {"Nubank"}, "closing_day": {"3"}, "due_day": {"10"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("save card: %d", res.StatusCode)
	}
	cards := string(mustGet(t, client, ts.URL+"/cartoes"))
	if !strings.Contains(cards, "Nubank") || !strings.Contains(cards, "fecha dia 3") {
		t.Fatal("card not listed")
	}
	// the expense form now offers the card and installments
	if g := string(mustGet(t, client, ts.URL+"/gastos")); !strings.Contains(g, `name="card_id"`) || !strings.Contains(g, `name="installments"`) {
		t.Fatal("expense form without card fields")
	}
	if res := post("/cartoes/1/fatura?mes=2026-11", url.Values{"total": {"1.000,00"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("total: %d", res.StatusCode)
	}
	if f := string(mustGet(t, client, ts.URL+"/cartoes/1/fatura?mes=2026-11")); !strings.Contains(f, "R$ 1.000,00") {
		t.Fatal("declared total not shown")
	}

	// importing is done by the assistant over MCP: the page says so, and the
	// old web import routes are gone
	f := string(mustGet(t, client, ts.URL+"/cartoes/1/fatura?mes=2026-11"))
	if !strings.Contains(f, "Importa esta fatura do Nubank") || strings.Contains(f, `name="text"`) {
		t.Error("statement page should point to the assistant, without a paste form")
	}
	if res := post("/cartoes/1/importar?mes=2026-11", url.Values{"text": {"28/10 Padaria 12,50"}}); res.StatusCode == http.StatusOK || res.StatusCode == http.StatusSeeOther {
		t.Errorf("web import still answers: %d", res.StatusCode)
	}
	// the November expenses list shows the whole fatura in one row, linked to it
	post("/lancamentos", url.Values{"kind": {"expense"}, "date": {"2026-10-20"}, "amount": {"30,00"}, "new_category": {"Lazer"},
		"payment_method": {"credito"}, "card_id": {"1"}, "description": {"Cinema"}})
	g := string(mustGet(t, client, ts.URL+"/gastos?mes=2026-11"))
	if !strings.Contains(g, "Fatura Nubank · nov/2026") || !strings.Contains(g, `href="/cartoes/1/fatura?mes=2026-11"`) ||
		!strings.Contains(g, "R$ 1.000,00") || strings.Contains(g, "Cinema") {
		t.Error("expense list should show the fatura as one row")
	}
	if strings.Contains(string(mustGet(t, client, ts.URL+"/gastos?mes=2026-10")), "Cinema") {
		t.Error("an October purchase on the November fatura counts in November")
	}
	f = string(mustGet(t, client, ts.URL+"/cartoes/1/fatura?mes=2026-11"))
	for _, want := range []string{"Por categoria", "Lazer", "Não detalhado", "Maiores compras", "Cinema"} {
		if !strings.Contains(f, want) {
			t.Errorf("statement page misses %q", want)
		}
	}
	// each purchase has a category picker; changing it remembers the merchant
	if !strings.Contains(f, `action="/lancamentos/`) || !strings.Contains(f, `/categoria"`) {
		t.Error("statement page without per-purchase category picker")
	}
	cinema := regexp.MustCompile(`id="compra-(\d+)"[^<]*<div class="e-date"><strong>[^<]*</strong></div>\s*<div class="e-main">\s*<a[^>]*>Cinema`).FindStringSubmatch(f)
	if cinema == nil {
		t.Fatal("cinema row not found")
	}
	res := post("/lancamentos/"+cinema[1]+"/categoria", url.Values{"new_category": {"Cultura"}, "back": {"/cartoes/1/fatura?mes=2026-11"}})
	if res.StatusCode != http.StatusSeeOther || !strings.Contains(res.Header.Get("Location"), "Cultura") {
		t.Fatalf("recategorize: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if p := string(mustGet(t, client, ts.URL+"/perfil/preferencias")); !strings.Contains(p, "Memória de categorias") || !strings.Contains(p, "Cinema") {
		t.Error("memory not listed in preferences")
	}
	if res := post("/config/memorias/apagar", url.Values{"merchant": {"cinema"}}); res.StatusCode != http.StatusSeeOther {
		t.Errorf("forget: %d", res.StatusCode)
	}
	if c := string(mustGet(t, client, ts.URL+"/cartoes")); !strings.Contains(c, `id="card-hist-1"`) || !strings.Contains(c, `id="card-history"`) {
		t.Error("cards page without the fatura history chart")
	}

	// turning the module off hides the menu and the card fields
	if res := post("/config/cartoes", url.Values{}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("turn off: %d", res.StatusCode)
	}
	g = string(mustGet(t, client, ts.URL+"/gastos"))
	if strings.Contains(g, `href="/cartoes"`) || strings.Contains(g, `name="card_id"`) {
		t.Error("cards still offered with the module off")
	}
	res, _ = client.Get(ts.URL + "/cartoes")
	if res.StatusCode != http.StatusSeeOther || !strings.Contains(res.Header.Get("Location"), "/perfil/preferencias") {
		t.Errorf("cards page with module off: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if p := string(mustGet(t, client, ts.URL+"/perfil/preferencias")); !strings.Contains(p, `name="enabled"  data-autosubmit-check`) {
		t.Error("preference not shown as off")
	}
	post("/config/cartoes", url.Values{"enabled": {"on"}})
	if g := string(mustGet(t, client, ts.URL+"/gastos")); !strings.Contains(g, `href="/cartoes"`) {
		t.Error("menu not back after turning the module on")
	}
}
