package app

import (
	"errors"
	"testing"
	"time"

	"controle-gastos/internal/db"
)

func TestAcceptTrust(t *testing.T) {
	a, d, _ := newApp(t)
	act, err := a.SignIn(ctx, ana())
	if err != nil {
		t.Fatal(err)
	}
	if !NeedsTrust(act) {
		t.Fatal("a new account must read the security page")
	}
	if err := a.AcceptTrust(ctx, act); err != nil {
		t.Fatal(err)
	}
	u, _ := d.User(ctx, act.User.ID)
	if u.TrustVersion != TrustVersion || NeedsTrust(Actor{User: u}) {
		t.Fatalf("trust version = %d", u.TrustVersion)
	}
	// an older recorded version means the page changed: read it again
	if NeedsTrust(Actor{User: db.User{TrustVersion: TrustVersion - 1}}) == false {
		t.Fatal("outdated version must be shown again")
	}
}

func TestPurgeInactive(t *testing.T) {
	a, d, now := newApp(t)
	ana, err := a.SignIn(ctx, ana())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.SignIn(ctx, db.GoogleUser{Sub: "g-beto", Email: "beto@x.com", EmailVerified: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetRole(ctx, "beto@x.com", db.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	eve, err := a.SignIn(ctx, eve()) // pending: left to the admin
	if !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	start := time.Now() // accounts were created and signed in now (database clock)
	if InactivityPolicyStart.After(start) {
		start = InactivityPolicyStart
	}

	// 11 months without use: nothing to do
	*now = start.AddDate(0, 11, 0)
	if n, err := a.PurgeInactive(ctx, false); n != 0 || err != nil {
		t.Fatalf("purged %d %v", n, err)
	}
	// the policy start also holds the clock back
	old := InactivityPolicyStart
	InactivityPolicyStart = start.AddDate(0, 6, 0)
	*now = start.AddDate(0, 13, 0)
	n, err := a.PurgeInactive(ctx, false)
	InactivityPolicyStart = old
	if n != 0 || err != nil {
		t.Fatalf("purged before a full period since the policy: %d %v", n, err)
	}
	// 13 months: Ana goes (dry run first); the admin and the pending sign-up stay
	if n, err := a.PurgeInactive(ctx, true); n != 1 || err != nil {
		t.Fatalf("dry run: %d %v", n, err)
	}
	if _, err := d.User(ctx, ana.User.ID); err != nil {
		t.Fatal("dry run deleted the account")
	}
	if n, err := a.PurgeInactive(ctx, false); n != 1 || err != nil {
		t.Fatalf("purge: %d %v", n, err)
	}
	if _, err := d.User(ctx, ana.User.ID); err == nil {
		t.Fatal("inactive account still there")
	}
	if _, ok, _ := d.UserLedger(ctx, ana.User.ID); ok {
		t.Fatal("ledger membership still there")
	}
	if _, err := d.User(ctx, eve.User.ID); err != nil {
		t.Fatal("pending sign-up was deleted")
	}
	if _, err := d.FindUser(ctx, "beto@x.com"); err != nil {
		t.Fatal("admin was deleted")
	}
}
