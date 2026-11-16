package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"controle-gastos/internal/app"
	"controle-gastos/internal/db"
	"controle-gastos/internal/db/dbtest"
)

var eveUser = idpUser{Sub: "g-eve", Email: "eve@x.com", Name: "Eve", Verified: true}

func TestPendingSignupHTTP(t *testing.T) {
	ts, eve, env := newTestServer(t)
	res := env.googleLogin(t, eve, eveUser, nil)
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "aguardando aprovação") {
		t.Fatalf("pending sign-up: %d", res.StatusCode)
	}
	for _, c := range res.Cookies() {
		if strings.Contains(c.Name, sessionValue) && c.MaxAge >= 0 {
			t.Error("pending sign-up got a session cookie")
		}
	}
	if res, _ := eve.Get(ts.URL + "/"); res.StatusCode != http.StatusSeeOther {
		t.Errorf("pending account reached the app: %d", res.StatusCode)
	}
}

func TestAdminHTTP(t *testing.T) {
	ts, ana, env := newTestServer(t)
	ctx := context.Background()
	env.mustLogin(t, ana, anaUser)
	beto := newClient()
	env.mustLogin(t, beto, betoUser)
	env.googleLogin(t, newClient(), eveUser, nil) // pending
	eve, err := env.app.SignIn(ctx, db.GoogleUser{Sub: eveUser.Sub, Email: eveUser.Email, EmailVerified: true})
	if eve.User.ID == 0 {
		t.Fatalf("eve: %v", err)
	}
	approve := ts.URL + "/admin/usuarios/" + itoa(eve.User.ID) + "/aprovar"

	// not an admin yet: the admin area doesn't exist for ana or beto
	for _, c := range []*http.Client{ana, beto} {
		if res, _ := c.Get(ts.URL + "/admin"); res.StatusCode != http.StatusNotFound {
			t.Errorf("GET /admin by non-admin: %d", res.StatusCode)
		}
		if res, _ := c.PostForm(approve, nil); res.StatusCode != http.StatusNotFound {
			t.Errorf("POST approve by non-admin: %d", res.StatusCode)
		}
	}
	res, _ := ana.Get(ts.URL + "/")
	if body, _ := io.ReadAll(res.Body); strings.Contains(string(body), `href="/admin"`) {
		t.Error("non-admin sees the admin link")
	}

	if _, err := env.app.SetRole(ctx, "ana@x.com", db.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	res, _ = ana.Get(ts.URL + "/admin")
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "eve@x.com") || !strings.Contains(string(body), "Pendente") {
		t.Fatalf("admin page: %d", res.StatusCode)
	}

	// cross-origin POSTs to the admin area are refused
	req, _ := http.NewRequest("POST", approve, nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if res, _ := ana.Do(req); res.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin admin POST: %d", res.StatusCode)
	}

	if res, _ := ana.PostForm(approve, nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("approve: %d", res.StatusCode)
	}
	env.mustLogin(t, newClient(), eveUser) // now gets in

	// an old admin session must sign in again
	env.app.SetClock(func() time.Time { return time.Date(2026, 11, 17, 1, 0, 0, 0, time.UTC) })
	res, _ = ana.Get(ts.URL + "/admin")
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/login") {
		t.Fatalf("stale admin: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if res, _ := ana.Get(ts.URL + "/"); res.StatusCode != http.StatusSeeOther {
		t.Error("stale admin session not ended")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// newPasswordServer is the self-hosted mode: no Google, email + password.
func newPasswordServer(t *testing.T) (*httptest.Server, *app.App) {
	t.Helper()
	a := app.NewApp(dbtest.Open(t), time.UTC)
	a.SetClock(func() time.Time { return time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC) })
	srv, err := New(context.Background(), a, Config{Password: true, Secret: []byte(strings.Repeat("k", 32))}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, a
}

func TestPasswordModeHTTP(t *testing.T) {
	ts, a := newPasswordServer(t)
	const pw = "correct horse battery"
	if _, err := a.SetPassword(context.Background(), "dona@casa.local", pw); err != nil {
		t.Fatal(err)
	}
	c := newClient()
	res, _ := c.Get(ts.URL + "/login")
	if body, _ := io.ReadAll(res.Body); !strings.Contains(string(body), `action="/auth/password"`) || strings.Contains(string(body), "/auth/google") {
		t.Error("login page doesn't offer just the password form")
	}
	if res, _ := c.Get(ts.URL + "/auth/google"); res.StatusCode != http.StatusNotFound {
		t.Errorf("Google route exists in password mode: %d", res.StatusCode)
	}

	login := func(c *http.Client, pass string) *http.Response {
		res, err := c.PostForm(ts.URL+"/auth/password", url.Values{"email": {"dona@casa.local"}, "password": {pass}})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := login(c, "wrong password!!"); res.StatusCode != http.StatusForbidden {
		t.Errorf("wrong password: %d", res.StatusCode)
	}
	if res := login(c, pw); res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/" {
		t.Fatalf("login: %d", res.StatusCode)
	}
	if res, _ := c.Get(ts.URL + "/config"); res.StatusCode != http.StatusOK {
		t.Errorf("app after password login: %d", res.StatusCode)
	}

	// lockout over HTTP
	other := newClient()
	for range app.MaxLoginFailures {
		login(other, "wrong password!!")
	}
	if res := login(other, pw); res.StatusCode != http.StatusForbidden {
		t.Errorf("locked account signed in: %d", res.StatusCode)
	}
}

func TestValidateBaseURL(t *testing.T) {
	for raw, wantOK := range map[string]bool{
		"https://gastos.exemplo.com": true, "https://gastos.exemplo.com/": true, "http://localhost:8080": true,
		"http://127.0.0.1:8080": true, "http://[::1]:8080": true,
		"http://gastos.exemplo.com": false, "http://192.168.0.10:8080": false, "gastos.exemplo.com": false,
		"https://gastos.exemplo.com/app": false, "https://u:p@gastos.exemplo.com": false, "": false,
	} {
		_, err := ValidateBaseURL(raw)
		if (err == nil) != wantOK {
			t.Errorf("ValidateBaseURL(%q) = %v, want ok=%v", raw, err, wantOK)
		}
	}
}
