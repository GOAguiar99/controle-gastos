package app

import (
	"context"
	"testing"
	"time"

	"controle-gastos/internal/db"
	"controle-gastos/internal/db/dbtest"
	"controle-gastos/internal/finance"
)

func newApp(t *testing.T) (*App, *db.DB, *time.Time) {
	t.Helper()
	d := dbtest.Open(t)
	a := NewApp(d, time.UTC)
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	a.SetClock(func() time.Time { return now })
	a.AllowEmail = AllowList("ana@x.com, BETO@x.com")
	return a, d, &now
}

var ctx = context.Background()

func ana() db.GoogleUser {
	return db.GoogleUser{Sub: "g-ana", Email: "ana@x.com", EmailVerified: true, Name: "Ana Souza"}
}

func TestSignIn(t *testing.T) {
	a, d, _ := newApp(t)

	g := ana()
	g.EmailVerified = false
	_, err := a.SignIn(ctx, g)
	mustInvalid(t, err, "verificado")
	a.Signup = SignupClosed
	_, err = a.SignIn(ctx, db.GoogleUser{Sub: "g-eve", Email: "eve@x.com", EmailVerified: true})
	mustInvalid(t, err, "não tem acesso")
	a.Signup = SignupApproval

	// first sign-in creates a seeded ledger; the next one keeps it
	first, err := a.SignIn(ctx, ana())
	if err != nil || first.LedgerID == 0 {
		t.Fatalf("sign in: %+v %v", first, err)
	}
	if cats, _ := a.For(ctx, first).Categories(finance.Expense); len(cats) == 0 {
		t.Fatal("new ledger not seeded")
	}
	g = ana()
	g.Email = "ana@x.com"
	g.Name = "Ana S."
	again, err := a.SignIn(ctx, g)
	if err != nil || again.LedgerID != first.LedgerID || again.User.ID != first.User.ID || again.User.Name != "Ana S." {
		t.Fatalf("second sign in: %+v %v", again, err)
	}

	// a ledger reserved for an email (legacy data) is claimed on first sign-in
	legacy, _ := d.CreateLedger(ctx, "Legado")
	if err := d.SetClaimEmail(ctx, legacy, "Beto@X.com"); err != nil {
		t.Fatal(err)
	}
	beto, err := a.SignIn(ctx, db.GoogleUser{Sub: "g-beto", Email: "beto@x.com", EmailVerified: true})
	if err != nil || beto.LedgerID != legacy {
		t.Fatalf("claim: %+v %v", beto, err)
	}
	if err := d.SetClaimEmail(ctx, legacy, "outra@x.com"); err == nil {
		t.Fatal("a ledger with members can't be reserved again")
	}
}

func TestSessions(t *testing.T) {
	a, _, now := newApp(t)
	actor, _ := a.SignIn(ctx, ana())
	tok, err := a.StartSession(ctx, actor.User.ID, "1.2.3.4", "test")
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := a.ResumeSession(ctx, tok)
	if err != nil || !ok || got.User.ID != actor.User.ID || got.LedgerID != actor.LedgerID || got.Source != SourceManual {
		t.Fatalf("resume: %+v %v %v", got, ok, err)
	}
	if _, ok, _ := a.ResumeSession(ctx, tok+"x"); ok {
		t.Fatal("wrong token accepted")
	}

	// used within the idle window it stays alive; unused for longer it dies
	*now = now.Add(6 * 24 * time.Hour)
	if _, ok, _ := a.ResumeSession(ctx, tok); !ok {
		t.Fatal("session died while in use")
	}
	*now = now.Add(SessionIdle + time.Minute)
	if _, ok, _ := a.ResumeSession(ctx, tok); ok {
		t.Fatal("idle session still valid")
	}

	tok2, _ := a.StartSession(ctx, actor.User.ID, "", "")
	if err := a.EndSession(ctx, tok2); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := a.ResumeSession(ctx, tok2); ok {
		t.Fatal("logged-out session still valid")
	}

	// absolute expiry even when used every day
	tok3, _ := a.StartSession(ctx, actor.User.ID, "", "")
	for i := 0; i < 31; i++ {
		*now = now.Add(24 * time.Hour)
		a.ResumeSession(ctx, tok3)
	}
	if _, ok, _ := a.ResumeSession(ctx, tok3); ok {
		t.Fatal("session outlived SessionTTL")
	}
}

func TestMCPTokens(t *testing.T) {
	a, _, _ := newApp(t)
	ana, _ := a.SignIn(ctx, ana())
	beto, _ := a.SignIn(ctx, db.GoogleUser{Sub: "g-beto", Email: "beto@x.com", EmailVerified: true})

	tok, err := a.CreateMCPToken(ctx, ana, "  celular ")
	if err != nil || len(tok) < 40 {
		t.Fatalf("create: %q %v", tok, err)
	}
	got, ok, err := a.MCPActor(ctx, tok)
	if err != nil || !ok || got.User.ID != ana.User.ID || got.LedgerID != ana.LedgerID || got.Source != SourceMCP {
		t.Fatalf("resolve: %+v %v %v", got, ok, err)
	}
	list, _ := a.MCPTokens(ctx, ana)
	if len(list) != 1 || list[0].Label != "celular" || list[0].LastUsedAt.IsZero() {
		t.Fatalf("list: %+v", list)
	}
	if l, _ := a.MCPTokens(ctx, beto); len(l) != 0 {
		t.Fatal("beto sees ana's links")
	}
	mustInvalid(t, a.RevokeMCPToken(ctx, beto, list[0].ID), "não encontrado")
	if err := a.RevokeMCPToken(ctx, ana, list[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := a.MCPActor(ctx, tok); ok {
		t.Fatal("revoked link still works")
	}
	for i := 0; i < maxMCPTokens; i++ {
		if _, err := a.CreateMCPToken(ctx, ana, ""); err != nil {
			t.Fatal(err)
		}
	}
	_, err = a.CreateMCPToken(ctx, ana, "")
	mustInvalid(t, err, "limite")
}

// Two users' services never see each other's data.
func TestActorsAreIsolated(t *testing.T) {
	a, _, _ := newApp(t)
	ana, _ := a.SignIn(ctx, ana())
	beto, _ := a.SignIn(ctx, db.GoogleUser{Sub: "g-beto", Email: "beto@x.com", EmailVerified: true})
	in := TxIn{Kind: finance.Expense, Date: d("2026-11-10"), Amount: 5000, Category: CategoryRef{Name: "Mercado", Create: true}}
	id, err := a.For(ctx, ana).AddTx(in)
	if err != nil {
		t.Fatal(err)
	}
	sb := a.For(ctx, beto)
	if _, err := sb.Tx(id); err == nil {
		t.Fatal("beto read ana's transaction")
	}
	if es, _ := sb.ListTx(TxFilter{From: finance.NewMonth(2026, 1), To: finance.NewMonth(2026, 12)}); len(es) != 0 {
		t.Fatalf("beto lists ana's transactions: %d", len(es))
	}
}
