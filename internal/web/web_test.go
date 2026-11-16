package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"controle-gastos/internal/db"
)

func TestParseMoney(t *testing.T) {
	cases := map[string]int64{
		"1.234,56": 123456, "1234,56": 123456, "1234.56": 123456, "R$ 10": 1000,
		"0,1": 10, "1.000.000": 100000000, " 99,999 ": 10000,
	}
	for in, want := range cases {
		got, err := ParseMoney(in)
		if err != nil || got != want {
			t.Errorf("ParseMoney(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := ParseMoney("abc"); err == nil {
		t.Error("expected error")
	}
}

func TestBRL(t *testing.T) {
	for in, want := range map[int64]string{0: "R$ 0,00", 123456: "R$ 1.234,56", -50: "−R$ 0,50", 100000000: "R$ 1.000.000,00"} {
		if got := brl(in); got != want {
			t.Errorf("brl(%d) = %q want %q", in, got, want)
		}
	}
}

func newTestServer(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	st, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hash, _ := bcrypt.GenerateFromPassword([]byte("senha-de-teste"), bcrypt.MinCost)
	srv, err := New(st, &Auth{PasswordHash: hash, Secret: []byte(strings.Repeat("k", 32))}, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	jar := &cookieJar{}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return ts, client
}

type cookieJar struct{ c []*http.Cookie }

func (j *cookieJar) SetCookies(_ *url.URL, cs []*http.Cookie) { j.c = append(j.c, cs...) }
func (j *cookieJar) Cookies(*url.URL) []*http.Cookie          { return j.c }

func TestPagesRequireLoginAndRender(t *testing.T) {
	ts, client := newTestServer(t)

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

	for _, p := range []string{"/", "/?mes=2027-02", "/receitas?mes=2026-11", "/gastos?mes=2026-11", "/recorrentes",
		"/recorrentes?tipo=income&editar=1", "/dividas", "/config", "/lancamentos/1/editar", "/export/lancamentos.csv", "/export/backup.db"} {
		res, err := client.Get(ts.URL + p)
		if err != nil || res.StatusCode != http.StatusOK {
			t.Errorf("GET %s: %v %v", p, res.StatusCode, err)
		}
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
