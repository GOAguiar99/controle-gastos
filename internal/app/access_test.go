package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"controle-gastos/internal/db"
)

func eve() db.GoogleUser {
	return db.GoogleUser{Sub: "g-eve", Email: "eve@x.com", EmailVerified: true, Name: "Eve"}
}

func mustAdmin(t *testing.T, a *App) Actor {
	t.Helper()
	act, err := a.SignIn(ctx, ana())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetRole(ctx, "ana@x.com", db.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	act.User.Role = db.RoleAdmin // as App.actor would load it on the next request
	return act
}

func TestApprovalFlow(t *testing.T) {
	a, d, _ := newApp(t)
	admin := mustAdmin(t, a)

	// an unknown account waits: no ledger, no actor
	p, err := a.SignIn(ctx, eve())
	if !errors.Is(err, ErrPending) || p.User.ID == 0 || p.LedgerID != 0 {
		t.Fatalf("pending sign-in: %+v %v", p, err)
	}
	if _, ok, _ := d.UserLedger(ctx, p.User.ID); ok {
		t.Fatal("pending account got a ledger")
	}
	if tok, err := a.StartSession(ctx, p.User.ID, "", ""); err == nil {
		if _, ok, _ := a.ResumeSession(ctx, tok); ok {
			t.Fatal("a session of a pending account works")
		}
	}
	if _, err := a.SetRole(ctx, "eve@x.com", db.RoleAdmin); err == nil {
		t.Fatal("pending account made admin")
	}
	// signing in again doesn't approve
	if _, err := a.SignIn(ctx, eve()); !errors.Is(err, ErrPending) {
		t.Fatalf("second pending sign-in: %v", err)
	}

	if err := a.Approve(ctx, admin, p.User.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.Approve(ctx, admin, p.User.ID); err == nil {
		t.Fatal("approved twice")
	}
	act, err := a.SignIn(ctx, eve())
	if err != nil || act.LedgerID == 0 {
		t.Fatalf("approved sign-in: %+v %v", act, err)
	}
	if err := a.Reject(ctx, admin, act.User.ID); err == nil {
		t.Fatal("rejected an active account (and its data)")
	}

	// the allow list approves on the spot, even a pending account
	a.AllowEmail = AllowList("ana@x.com, carl@x.com")
	carl := db.GoogleUser{Sub: "g-carl", Email: "carl@x.com", EmailVerified: true}
	if c, err := a.SignIn(ctx, carl); err != nil || c.LedgerID == 0 {
		t.Fatalf("allow-listed sign-in: %+v %v", c, err)
	}

	// reject erases a pending sign-up
	z, _ := a.SignIn(ctx, db.GoogleUser{Sub: "g-zed", Email: "zed@x.com", EmailVerified: true})
	if err := a.Reject(ctx, admin, z.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.User(ctx, z.User.ID); err == nil {
		t.Fatal("rejected sign-up still stored")
	}
}

func TestPendingCap(t *testing.T) {
	a, _, _ := newApp(t)
	a.MaxPending = 1
	if _, err := a.SignIn(ctx, eve()); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	_, err := a.SignIn(ctx, db.GoogleUser{Sub: "g-zed", Email: "zed@x.com", EmailVerified: true})
	mustInvalid(t, err, "temporariamente fechados")
	// the one already waiting still gets the waiting page; allow-listed people still get in
	if _, err := a.SignIn(ctx, eve()); !errors.Is(err, ErrPending) {
		t.Fatalf("existing pending: %v", err)
	}
	if _, err := a.SignIn(ctx, ana()); err != nil {
		t.Fatalf("allow-listed during cap: %v", err)
	}
}

func TestAdminAuthorization(t *testing.T) {
	a, _, now := newApp(t)
	admin := mustAdmin(t, a)
	beto, err := a.SignIn(ctx, db.GoogleUser{Sub: "g-beto", Email: "beto@x.com", EmailVerified: true})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := a.SignIn(ctx, eve())

	ops := map[string]func(Actor) error{
		"list":       func(x Actor) error { _, err := a.AdminUsers(ctx, x); return err },
		"approve":    func(x Actor) error { return a.Approve(ctx, x, p.User.ID) },
		"reject":     func(x Actor) error { return a.Reject(ctx, x, p.User.ID) },
		"suspend":    func(x Actor) error { return a.Suspend(ctx, x, beto.User.ID) },
		"reactivate": func(x Actor) error { return a.Reactivate(ctx, x, beto.User.ID) },
	}
	mcpAdmin := admin
	mcpAdmin.Source = SourceMCP
	stale := admin
	stale.AuthAt = now.Add(-AdminFreshness - time.Minute)
	for name, op := range ops {
		if err := op(beto); !errors.Is(err, ErrForbidden) {
			t.Errorf("%s by a normal user: %v", name, err)
		}
		if err := op(mcpAdmin); !errors.Is(err, ErrForbidden) {
			t.Errorf("%s through an MCP link: %v", name, err)
		}
		if err := op(stale); !errors.Is(err, ErrReauth) {
			t.Errorf("%s by a stale admin session: %v", name, err)
		}
	}

	// admins can't suspend themselves or other admins
	mustInvalid(t, a.Suspend(ctx, admin, admin.User.ID), "operador")
	if _, err := a.SetRole(ctx, "beto@x.com", db.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	mustInvalid(t, a.Suspend(ctx, admin, beto.User.ID), "operador")
	if _, err := a.SetRole(ctx, "beto@x.com", db.RoleUser); err != nil {
		t.Fatal(err)
	}

	// suspend takes effect: sign-in refused
	if err := a.Suspend(ctx, admin, beto.User.ID); err != nil {
		t.Fatal(err)
	}
	_, err = a.SignIn(ctx, db.GoogleUser{Sub: "g-beto", Email: "beto@x.com", EmailVerified: true})
	mustInvalid(t, err, "suspensa")
	if err := a.Reactivate(ctx, admin, beto.User.ID); err != nil {
		t.Fatal(err)
	}

	// the list carries no ledger data, only identity and status
	users, err := a.AdminUsers(ctx, admin)
	if err != nil || len(users) != 3 || users[0].Approved {
		t.Fatalf("admin list (pending first): %+v %v", users, err)
	}
}

func TestPasswordAccounts(t *testing.T) {
	a, _, now := newApp(t)
	mustInvalid(t, CheckPasswordPolicy("u@x.com", "curta"), "pelo menos")
	mustInvalid(t, CheckPasswordPolicy("usuario@exemplo.com", "usuario@exemplo.com"), "e-mail")
	mustInvalid(t, CheckPasswordPolicy("u@x.com", strings.Repeat("a", MaxPasswordLen+1)), "no máximo")

	const pw = "correct horse battery"
	u, err := a.SetPassword(ctx, "Dona@Casa.local", pw)
	if err != nil || u.GoogleSub != db.LocalSubPrefix+"dona@casa.local" || !u.Approved {
		t.Fatalf("set password: %+v %v", u, err)
	}
	act, err := a.PasswordSignIn(ctx, "dona@casa.local", pw, "")
	if err != nil || act.LedgerID == 0 {
		t.Fatalf("sign in: %+v %v", act, err)
	}

	// every failure looks the same
	_, errWrong := a.PasswordSignIn(ctx, "dona@casa.local", "wrong password!!", "")
	_, errUnknown := a.PasswordSignIn(ctx, "ninguem@casa.local", pw, "")
	if errWrong == nil || errUnknown == nil || errWrong.Error() != errUnknown.Error() {
		t.Fatalf("failures differ: %v / %v", errWrong, errUnknown)
	}

	// a success resets the counter
	for range MaxLoginFailures - 2 {
		a.PasswordSignIn(ctx, "dona@casa.local", "wrong password!!", "")
	}
	if _, err := a.PasswordSignIn(ctx, "dona@casa.local", pw, ""); err != nil {
		t.Fatalf("after failures below the limit: %v", err)
	}
	// MaxLoginFailures in a row lock it, even for the right password
	for range MaxLoginFailures {
		a.PasswordSignIn(ctx, "dona@casa.local", "wrong password!!", "")
	}
	if _, err := a.PasswordSignIn(ctx, "dona@casa.local", pw, ""); err == nil {
		t.Fatal("locked account signed in")
	}
	*now = now.Add(LockoutFor + time.Minute)
	if _, err := a.PasswordSignIn(ctx, "dona@casa.local", pw, ""); err != nil {
		t.Fatalf("after the lockout: %v", err)
	}

	// a Google account with the same email can't be used with a password
	if _, err := a.SignIn(ctx, db.GoogleUser{Sub: "g-dona", Email: "dona@casa.local", EmailVerified: true}); err != nil && !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	if _, err := a.PasswordSignIn(ctx, "dona@casa.local", "anything long enough", ""); err == nil {
		t.Fatal("cross-identity sign-in")
	}

	// setting the password again ends existing sessions
	tok, _ := a.StartSession(ctx, act.User.ID, "", "")
	if _, err := a.SetPassword(ctx, "dona@casa.local", "another long password"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := a.ResumeSession(ctx, tok); ok {
		t.Fatal("session survived a password reset")
	}
}

func TestPasswordHash(t *testing.T) {
	h, err := hashPassword("uma senha qualquer")
	if err != nil || !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("hash: %q %v", h, err)
	}
	if !verifyPassword("uma senha qualquer", h) || verifyPassword("outra senha qualquer", h) {
		t.Fatal("verify")
	}
	for _, bad := range []string{"", "plain", "$argon2i$v=19$m=19456,t=2,p=1$AA$AA", "$argon2id$v=19$m=99999999,t=2,p=1$AAAA$AAAA"} {
		if verifyPassword("x", bad) {
			t.Errorf("accepted malformed hash %q", bad)
		}
	}
}
