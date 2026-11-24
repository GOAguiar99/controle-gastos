package web

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"controle-gastos/internal/db"
)

func TestAgo(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		time.Minute:              "agora há pouco",
		20 * time.Minute:         "há 20 min",
		time.Hour:                "há 1 hora",
		5 * time.Hour:            "há 5 horas",
		26 * time.Hour:           "há 1 dia",
		3 * 24 * time.Hour:       "há 3 dias",
		45 * 24 * time.Hour:      "há 1 mês",
		200 * 24 * time.Hour:     "há 6 meses",
		2 * 366 * 24 * time.Hour: "há 2 anos",
		-10 * time.Minute:        "agora há pouco", // clock skew
	} {
		if got := ago(now.Add(-d), now); got != want {
			t.Errorf("ago(-%v) = %q, want %q", d, got, want)
		}
	}
	if got := ago(time.Time{}, now); got != "nunca" {
		t.Errorf("zero: %q", got)
	}
}

func get(t *testing.T, c *http.Client, u string) string {
	t.Helper()
	res, err := c.Get(u)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %v %v", u, res.StatusCode, err)
	}
	b, _ := io.ReadAll(res.Body)
	return string(b)
}

// "Último acesso" shows in Perfil → Conta, in Perfil → Compartilhar for each
// member, and in /admin for every account.
func TestLastSeenHTTP(t *testing.T) {
	ts, ana, env := newTestServer(t)
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	env.app.SetClock(func() time.Time { return now })
	env.app.AllowEmail = func(e string) bool { return e == "ana@x.com" }
	env.mustLogin(t, ana, anaUser)
	env.googleLogin(t, newClient(), eveUser, nil) // pending: never used the app

	if res, _ := ana.PostForm(ts.URL+"/config/compartilhar", url.Values{"email": {"carolina@x.com"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("invite: %d", res.StatusCode)
	}
	carol := newClient()
	env.mustLogin(t, carol, carolUser)
	get(t, carol, ts.URL+"/") // carolina uses the app at 12:00

	now = now.Add(2 * time.Hour)
	body := get(t, ana, ts.URL+"/perfil/compartilhar")
	if !strings.Contains(body, "Último acesso") || !strings.Contains(body, "há 2 horas") {
		t.Error("sharing page doesn't show carolina's last access")
	}
	body = get(t, ana, ts.URL+"/perfil")
	if !strings.Contains(body, "último acesso") {
		t.Error("account page doesn't show the user's last access")
	}

	now = now.Add(3 * 24 * time.Hour)
	if _, err := env.app.SetRole(context.Background(), "ana@x.com", db.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	env.mustLogin(t, ana, anaUser) // admins need a fresh sign-in
	body = get(t, ana, ts.URL+"/admin")
	if !strings.Contains(body, "Último acesso") || !strings.Contains(body, "há 3 dias") || !strings.Contains(body, "nunca") {
		t.Errorf("admin page: want carolina 'há 3 dias' and eve 'nunca':\n%s", body)
	}
}
