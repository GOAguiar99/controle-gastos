package web

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"controle-gastos/internal/db"
)

var carolUser = idpUser{Sub: "g-carol", Email: "carolina@x.com", Name: "Carolina", Verified: true}

func TestShareLedgerHTTP(t *testing.T) {
	ts, ana, env := newTestServer(t)
	env.app.AllowEmail = func(e string) bool { return e == "ana@x.com" } // carolina and beto aren't allow-listed
	env.mustLogin(t, ana, anaUser)
	_, _, _, _, cat := anaData(t, env) // "segredo da ana" in ana's ledger

	// alone in the ledger: no author clutter
	res, _ := ana.Get(ts.URL + "/gastos?mes=2026-11")
	if body, _ := io.ReadAll(res.Body); strings.Contains(string(body), " · por ") {
		t.Error("unshared ledger shows authors")
	}
	res, _ = ana.PostForm(ts.URL+"/config/compartilhar", url.Values{"email": {"carolina@x.com"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("invite: %d", res.StatusCode)
	}

	// carolina signs in with Google and lands in ana's ledger, no admin step
	carol := newClient()
	env.mustLogin(t, carol, carolUser)
	res, _ = carol.Get(ts.URL + "/gastos?mes=2026-11")
	if body, _ := io.ReadAll(res.Body); !strings.Contains(string(body), "segredo da ana") {
		t.Fatal("carolina doesn't see the shared ledger")
	}
	res, _ = carol.Get(ts.URL + "/config")
	if body, _ := io.ReadAll(res.Body); !strings.Contains(string(body), "Sair desta carteira") || strings.Contains(string(body), `action="/config/compartilhar"`) {
		t.Error("member's config shows owner controls")
	}
	if res, _ := carol.PostForm(ts.URL+"/config/compartilhar", url.Values{"email": {"zed@x.com"}}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("member invited someone: %d", res.StatusCode)
	}

	// entries say who entered them, once the ledger is shared
	res, _ = carol.PostForm(ts.URL+"/lancamentos", url.Values{"kind": {"expense"}, "date": {"2026-11-12"}, "amount": {"10,00"},
		"category_id": {itoa(cat)}, "description": {"pão de queijo"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("carolina adds an entry: %d", res.StatusCode)
	}
	res, _ = ana.Get(ts.URL + "/gastos?mes=2026-11")
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "por Ana Souza") {
		t.Error("ana's entry doesn't show its author")
	}
	if !strings.Contains(string(body), "pão de queijo") || !strings.Contains(string(body), "por Carolina") {
		t.Error("carolina's entry doesn't show its author")
	}

	// an uninvited person still only gets the waiting page
	res = env.googleLogin(t, newClient(), betoUser, nil)
	if body, _ := io.ReadAll(res.Body); !strings.Contains(string(body), "aguardando aprovação") {
		t.Error("uninvited account got in")
	}

	// a stranger's owner can't remove ana's members
	carolActor, _ := env.app.SignIn(context.Background(), db.GoogleUser{Sub: carolUser.Sub, Email: carolUser.Email, EmailVerified: true})
	stranger := newClient()
	env.app.AllowEmail = func(e string) bool { return e == "ana@x.com" || e == "zed@x.com" }
	env.mustLogin(t, stranger, idpUser{Sub: "g-zed", Email: "zed@x.com", Verified: true})
	if res, _ := stranger.PostForm(ts.URL+"/config/membros/"+itoa(carolActor.User.ID)+"/remover", nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("stranger removed ana's member: %d", res.StatusCode)
	}
	if res, _ := carol.Get(ts.URL + "/"); res.StatusCode != http.StatusOK {
		t.Fatalf("carolina lost access after stranger's attempt: %d", res.StatusCode)
	}

	// the owner removes her: she's out at once
	if res, _ := ana.PostForm(ts.URL+"/config/membros/"+itoa(carolActor.User.ID)+"/remover", nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("remove: %d", res.StatusCode)
	}
	if res, _ := carol.Get(ts.URL + "/"); res.StatusCode != http.StatusSeeOther {
		t.Errorf("removed member still in: %d", res.StatusCode)
	}
}
