package web

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestTrustPageAfterLogin(t *testing.T) {
	ts, client, env := newTestServer(t)
	get := func(c *http.Client, path string) (*http.Response, string) {
		t.Helper()
		res, err := c.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		return res, string(b)
	}

	// public: people can read it before signing up
	res, body := get(newClient(), "/seguranca")
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Seus dados e segurança") ||
		!strings.Contains(body, "12 meses") || strings.Contains(body, `action="/seguranca"`) {
		t.Fatalf("public page: %d", res.StatusCode)
	}
	if res, _ := newClient().PostForm(ts.URL+"/seguranca", nil); res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/login" {
		t.Errorf("accept without a session: %d %s", res.StatusCode, res.Header.Get("Location"))
	}

	// first sign-in: every page leads to it until it is confirmed...
	if res := env.googleLogin(t, client, anaUser, nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("login: %d", res.StatusCode)
	}
	for _, p := range []string{"/", "/gastos", "/recorrentes", "/admin"} {
		if res, _ := get(client, p); res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/seguranca" {
			t.Errorf("GET %s before reading: %d %s", p, res.StatusCode, res.Header.Get("Location"))
		}
	}
	// ...but the account and the data stay reachable (export, delete)
	for _, p := range []string{"/perfil", "/export/dados.json", "/privacidade", "/como-usar"} {
		if res, _ := get(client, p); res.StatusCode != http.StatusOK {
			t.Errorf("GET %s before reading: %d", p, res.StatusCode)
		}
	}
	res, body = get(client, "/seguranca")
	if res.StatusCode != http.StatusOK || !strings.Contains(body, `action="/seguranca"`) || !strings.Contains(body, `class="topbar"`) {
		t.Fatalf("trust page for a new user: %d", res.StatusCode)
	}

	// confirming leads to the dashboard and is remembered
	acceptTrust(t, ts.URL, client)
	res, body = get(client, "/")
	if res.StatusCode != http.StatusOK || !strings.Contains(body, `href="/seguranca"`) {
		t.Fatalf("dashboard after reading: %d", res.StatusCode)
	}
	if _, body := get(client, "/seguranca"); strings.Contains(body, `action="/seguranca"`) {
		t.Error("asks to confirm again")
	}
	// a new session does not ask again
	again := newClient()
	env.mustLoginNoTrust(t, again)
	if res, _ := get(again, "/"); res.StatusCode != http.StatusOK {
		t.Errorf("second sign-in: %d", res.StatusCode)
	}
}

// mustLoginNoTrust signs Ana in again without confirming the security page.
func (e *testEnv) mustLoginNoTrust(t *testing.T, c *http.Client) {
	t.Helper()
	if res := e.googleLogin(t, c, anaUser, nil); res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/" {
		t.Fatalf("login: %d", res.StatusCode)
	}
}
