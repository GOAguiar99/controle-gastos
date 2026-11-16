package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"controle-gastos/internal/app"
	"controle-gastos/internal/db"
	"controle-gastos/internal/finance"
)

var betoUser = idpUser{Sub: "g-beto", Email: "beto@x.com", Name: "Beto", Verified: true}

// anaData creates one of everything in ana's ledger through the service and
// returns its ids.
func anaData(t *testing.T, env *testEnv) (actor app.Actor, tx, rec, debt, cat int64) {
	t.Helper()
	ctx := context.Background()
	actor, err := env.app.SignIn(ctx, db.GoogleUser{Sub: anaUser.Sub, Email: anaUser.Email, EmailVerified: true, Name: anaUser.Name})
	if err != nil {
		t.Fatal(err)
	}
	svc := env.app.For(ctx, actor)
	cat, err = svc.AddCategory("Mercado", finance.Expense, finance.Other)
	if err != nil {
		t.Fatal(err)
	}
	if tx, err = svc.AddTx(app.TxIn{Kind: finance.Expense, Date: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), Amount: 12345,
		Category: app.CategoryRef{ID: cat}, Description: "segredo da ana"}); err != nil {
		t.Fatal(err)
	}
	if rec, err = svc.SaveRecurrence(app.RecIn{Kind: finance.Expense, Category: app.CategoryRef{ID: cat}, Amount: 5000, Day: 5,
		Interval: 1, Start: finance.NewMonth(2026, 11), EndMode: "never", Active: true}); err != nil {
		t.Fatal(err)
	}
	d, err := svc.SaveDebt(0, db.DebtInput{Name: "Empréstimo da ana", Initial: 100000})
	if err != nil {
		t.Fatal(err)
	}
	return actor, tx, rec, d.ID, cat
}

func exportOf(t *testing.T, env *testEnv, a app.Actor) app.DataExport {
	t.Helper()
	x, err := env.app.For(context.Background(), a).ExportData(a.User)
	if err != nil {
		t.Fatal(err)
	}
	x.ExportedAt = time.Time{}
	return x
}

// TestCrossTenantHTTP signs in a second user and points every endpoint that
// takes an id at the first user's records: none may read or change them.
func TestCrossTenantHTTP(t *testing.T) {
	ts, ana, env := newTestServer(t)
	env.mustLogin(t, ana, anaUser)
	actor, tx, rec, debt, cat := anaData(t, env)
	before := exportOf(t, env, actor)

	beto := newClient()
	env.mustLogin(t, beto, betoUser)

	gets := []string{fmt.Sprintf("/lancamentos/%d/editar", tx)}
	for _, p := range gets {
		res, err := beto.Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode < 400 || strings.Contains(string(body), "segredo da ana") {
			t.Errorf("GET %s by beto: %d", p, res.StatusCode)
		}
	}

	month := url.Values{"rec_id": {fmt.Sprint(rec)}, "month": {"2026-11"}}
	posts := map[string]url.Values{
		fmt.Sprintf("/lancamentos/%d", tx):            {"date": {"2026-11-11"}, "amount": {"1,00"}, "category_id": {fmt.Sprint(cat)}, "description": {"hacked"}},
		fmt.Sprintf("/lancamentos/%d/excluir", tx):    nil,
		fmt.Sprintf("/recorrentes/%d", rec):           {"kind": {"expense"}, "category_id": {fmt.Sprint(cat)}, "amount": {"1,00"}, "day": {"1"}, "start": {"2026-11"}},
		fmt.Sprintf("/recorrentes/%d/excluir", rec):   nil,
		fmt.Sprintf("/recorrentes/%d/aceitar", rec):   nil,
		fmt.Sprintf("/recorrentes/%d/descartar", rec): nil,
		fmt.Sprintf("/dividas/%d", debt):              {"name": {"hacked"}, "initial": {"1,00"}},
		fmt.Sprintf("/dividas/%d/excluir", debt):      nil,
		fmt.Sprintf("/config/categorias/%d", cat):     {"bucket": {"fixo"}},
		"/previstos/confirmar":                        month,
		"/previstos/pular":                            month,
		"/previstos/restaurar":                        month,
		"/lancamentos":                                {"kind": {"expense"}, "date": {"2026-11-11"}, "amount": {"1,00"}, "category_id": {fmt.Sprint(cat)}},
	}
	for p, v := range posts {
		res, err := beto.PostForm(ts.URL+p, v)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode < 400 {
			t.Errorf("POST %s by beto: %d (expected a 4xx)", p, res.StatusCode)
		}
	}

	// beto's exports contain none of ana's data
	for _, p := range []string{"/export/lancamentos.csv", "/export/dados.json"} {
		res, err := beto.Get(ts.URL + p)
		if err != nil || res.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %v %v", p, res.StatusCode, err)
		}
		body, _ := io.ReadAll(res.Body)
		if strings.Contains(string(body), "segredo da ana") || strings.Contains(string(body), "ana@x.com") {
			t.Errorf("%s by beto leaks ana's data", p)
		}
	}

	if after := exportOf(t, env, actor); !reflect.DeepEqual(before, after) {
		t.Errorf("ana's data changed after beto's attempts:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestSecurityHeadersAndLimits(t *testing.T) {
	ts, client, env := newTestServer(t)
	env.mustLogin(t, client, anaUser)
	res, err := client.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"Content-Security-Policy": "frame-ancestors 'none'",
		"X-Frame-Options":         "DENY",
		"X-Content-Type-Options":  "nosniff",
		"Cache-Control":           "no-store",
	} {
		if got := res.Header.Get(k); !strings.Contains(got, want) {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	// oversized form bodies are refused
	big := url.Values{"description": {strings.Repeat("x", maxBodyBytes+10)}}
	res, err = client.PostForm(ts.URL+"/lancamentos", big)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode < 400 {
		t.Errorf("1 MiB+ body accepted: %d", res.StatusCode)
	}
	// legal pages are public
	anon := newClient()
	for _, p := range []string{"/privacidade", "/termos"} {
		if res, err := anon.Get(ts.URL + p); err != nil || res.StatusCode != http.StatusOK {
			t.Errorf("GET %s anonymous: %v %v", p, res.StatusCode, err)
		}
	}
}

func TestAuthRateLimit(t *testing.T) {
	ts, client, _ := newTestServer(t)
	limited := false
	for range 100 {
		res, err := client.Get(ts.URL + "/auth/google")
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("100 login starts from one IP were never rate limited")
	}
}

func TestCSVFormulaInjection(t *testing.T) {
	for in, want := range map[string]string{"=HYPERLINK(\"x\")": "'=HYPERLINK(\"x\")", "+1": "'+1", "@SUM(A1)": "'@SUM(A1)", "mercado": "mercado", "": ""} {
		if got := csvText(in); got != want {
			t.Errorf("csvText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExportJSONHasOwnData(t *testing.T) {
	ts, client, env := newTestServer(t)
	env.mustLogin(t, client, anaUser)
	anaData(t, env)
	res, err := client.Get(ts.URL + "/export/dados.json")
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("export: %v %v", res.StatusCode, err)
	}
	var x app.DataExport
	if err := json.NewDecoder(res.Body).Decode(&x); err != nil {
		t.Fatal(err)
	}
	if x.Account.Email != "ana@x.com" || len(x.Transactions) != 1 || len(x.Recurrences) != 1 || len(x.Debts) != 1 {
		t.Errorf("export incomplete: %+v", x)
	}
}

func TestDeleteAccount(t *testing.T) {
	ts, ana, env := newTestServer(t)
	env.mustLogin(t, ana, anaUser)
	actor, _, _, _, _ := anaData(t, env)
	link := createLink(t, ts, ana)

	// wrong confirmation keeps everything
	if res, _ := ana.PostForm(ts.URL+"/config/excluir-conta", url.Values{"confirm": {"outro@x.com"}}); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong confirmation: %d", res.StatusCode)
	}
	res, err := ana.PostForm(ts.URL+"/config/excluir-conta", url.Values{"confirm": {"ANA@x.com"}})
	if err != nil || res.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete: %v %v", res.StatusCode, err)
	}
	// session gone, MCP link gone, ledger data gone
	if res, _ := ana.Get(ts.URL + "/"); res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/login" {
		t.Errorf("session survived account deletion: %d", res.StatusCode)
	}
	if res, _ := http.Post(link, "application/json", strings.NewReader("{}")); res.StatusCode != http.StatusNotFound {
		t.Errorf("MCP link survived account deletion: %d", res.StatusCode)
	}
	if n, err := env.app.For(context.Background(), actor).Store().AllTransactions(); err != nil || len(n) != 0 {
		t.Errorf("ledger rows survived: %d %v", len(n), err)
	}
	// signing in again starts a fresh, empty account
	env.mustLogin(t, ana, anaUser)
	again, err := env.app.SignIn(context.Background(), db.GoogleUser{Sub: anaUser.Sub, Email: anaUser.Email, EmailVerified: true})
	if err != nil || again.User.ID == actor.User.ID || again.LedgerID == actor.LedgerID {
		t.Errorf("re-signup reused old identity: %+v %v", again, err)
	}
}

func TestSuspendedAccount(t *testing.T) {
	ts, ana, env := newTestServer(t)
	env.mustLogin(t, ana, anaUser)
	link := createLink(t, ts, ana)
	ctx := context.Background()
	if _, err := env.app.SetSuspended(ctx, "ana@x.com", true); err != nil {
		t.Fatal(err)
	}
	if res, _ := ana.Get(ts.URL + "/"); res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/login" {
		t.Errorf("suspended user's session still works: %d", res.StatusCode)
	}
	if res, _ := http.Post(link, "application/json", strings.NewReader("{}")); res.StatusCode != http.StatusNotFound {
		t.Errorf("suspended user's MCP link still works: %d", res.StatusCode)
	}
	if res := env.googleLogin(t, newClient(), anaUser, nil); res.StatusCode != http.StatusForbidden {
		t.Errorf("suspended user signed in: %d", res.StatusCode)
	}
	if _, err := env.app.SetSuspended(ctx, "ana@x.com", false); err != nil {
		t.Fatal(err)
	}
	env.mustLogin(t, newClient(), anaUser)
}

func createLink(t *testing.T, ts *httptest.Server, c *http.Client) string {
	t.Helper()
	res, err := c.PostForm(ts.URL+"/config/mcp", nil)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("create link: %v %v", res.StatusCode, err)
	}
	body, _ := io.ReadAll(res.Body)
	m := regexp.MustCompile(`value="(http[^"]+/mcp/[^"]+)"`).FindSubmatch(body)
	if m == nil {
		t.Fatal("no link on page")
	}
	return string(m[1])
}
