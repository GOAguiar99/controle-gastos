package app

import (
	"errors"
	"testing"
	"time"

	"controle-gastos/internal/db"
	"controle-gastos/internal/finance"
)

func carol() db.GoogleUser {
	return db.GoogleUser{Sub: "g-carol", Email: "carolina@x.com", EmailVerified: true, Name: "Carolina"}
}

func TestShareLedger(t *testing.T) {
	a, d, now := newApp(t)
	ana, _ := a.SignIn(ctx, ana())
	txID, err := a.For(ctx, ana).AddTx(TxIn{Kind: finance.Expense, Date: d0(), Amount: 5000, Category: CategoryRef{Name: "Mercado", Create: true}})
	if err != nil {
		t.Fatal(err)
	}
	mustInvalid(t, a.InviteToLedger(ctx, ana, "não é email"), "inválido")
	mustInvalid(t, a.InviteToLedger(ctx, ana, "ANA@x.com"), "já está")
	if err := a.InviteToLedger(ctx, ana, "Carolina@X.com"); err != nil {
		t.Fatal(err)
	}

	// someone else signing in doesn't get in (and still waits for approval)
	if _, err := a.SignIn(ctx, eve()); !errors.Is(err, ErrPending) {
		t.Fatalf("uninvited: %v", err)
	}
	// the invited email is approved and lands in ana's ledger, seeing her data
	c, err := a.SignIn(ctx, carol())
	if err != nil || c.LedgerID != ana.LedgerID {
		t.Fatalf("invited sign-in: %+v %v", c, err)
	}
	if e, err := a.For(ctx, c).Tx(txID); err != nil || e.Amount != 5000 {
		t.Fatalf("member can't read the shared ledger: %v", err)
	}
	// the invite is used up
	if ok, _ := d.HasOpenInvite(ctx, "carolina@x.com", *now); ok {
		t.Fatal("invite still open after use")
	}

	// members can't manage people; the owner can't leave
	mustInvalid(t, a.InviteToLedger(ctx, c, "zed@x.com"), "dono")
	mustInvalid(t, a.RemoveMember(ctx, c, ana.User.ID), "dono")
	mustInvalid(t, a.LeaveLedger(ctx, ana), "dono")
	mcp := ana
	mcp.Source = SourceMCP
	mustInvalid(t, a.InviteToLedger(ctx, mcp, "zed@x.com"), "assistente")

	// removing ends access at once and the next sign-in gets a fresh ledger
	tok, _ := a.StartSession(ctx, c.User.ID, "", "")
	if err := a.RemoveMember(ctx, ana, c.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := a.ResumeSession(ctx, tok); ok {
		t.Fatal("removed member's session still works")
	}
	c2, err := a.SignIn(ctx, carol())
	if err != nil || c2.LedgerID == ana.LedgerID || c2.LedgerID == 0 {
		t.Fatalf("after removal: %+v %v", c2, err)
	}
	if _, err := a.For(ctx, c2).Tx(txID); err == nil {
		t.Fatal("removed member still reads ana's data")
	}
}

func TestInviteRules(t *testing.T) {
	a, _, now := newApp(t)
	ana, _ := a.SignIn(ctx, ana())

	// expiry
	if err := a.InviteToLedger(ctx, ana, "carolina@x.com"); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(InviteTTL + time.Hour)
	if _, err := a.SignIn(ctx, carol()); !errors.Is(err, ErrPending) {
		t.Fatalf("expired invite still let her in: %v", err)
	}

	// cap: members + open invites
	for i, e := range []string{"b1@x.com", "b2@x.com", "b3@x.com", "b4@x.com"} {
		if err := a.InviteToLedger(ctx, ana, e); err != nil {
			t.Fatalf("invite %d: %v", i, err)
		}
	}
	mustInvalid(t, a.InviteToLedger(ctx, ana, "b5@x.com"), "máximo")

	// someone with their own ledger accepts in Config: only if it's empty
	beto, _ := a.SignIn(ctx, db.GoogleUser{Sub: "g-beto", Email: "beto@x.com", EmailVerified: true})
	sh, _ := a.Sharing(ctx, ana)
	if err := a.CancelInvite(ctx, ana, sh.Invites[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := a.InviteToLedger(ctx, ana, "beto@x.com"); err != nil {
		t.Fatal(err)
	}
	bs, _ := a.Sharing(ctx, beto)
	if len(bs.ForMe) != 1 {
		t.Fatalf("beto doesn't see the invite: %+v", bs.ForMe)
	}
	// another person (with an empty ledger they could give up) can't use beto's invite
	a.AllowEmail = AllowList("ana@x.com, zed@x.com")
	zed, err := a.SignIn(ctx, db.GoogleUser{Sub: "g-zed", Email: "zed@x.com", EmailVerified: true})
	if err != nil {
		t.Fatal(err)
	}
	mustInvalid(t, a.AcceptInvite(ctx, zed, bs.ForMe[0].ID), "inválido")
	if _, err := a.For(ctx, beto).AddTx(TxIn{Kind: finance.Expense, Date: d0(), Amount: 100, Category: CategoryRef{Name: "Mercado", Create: true}}); err != nil {
		t.Fatal(err)
	}
	mustInvalid(t, a.AcceptInvite(ctx, beto, bs.ForMe[0].ID), "tem dados")
	if err := a.DeclineInvite(ctx, beto, bs.ForMe[0].ID); err != nil {
		t.Fatal(err)
	}
	mustInvalid(t, a.AcceptInvite(ctx, beto, bs.ForMe[0].ID), "inválido")
}

func TestSharedLedgerSurvivesOwnerDeletion(t *testing.T) {
	a, d, _ := newApp(t)
	ana, _ := a.SignIn(ctx, ana())
	a.InviteToLedger(ctx, ana, "carolina@x.com")
	c, err := a.SignIn(ctx, carol())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteAccount(ctx, ana, "ana@x.com"); err != nil {
		t.Fatal(err)
	}
	role, ok, _ := d.MemberRoleOf(ctx, c.LedgerID, c.User.ID)
	if !ok || role != db.MemberOwner {
		t.Fatalf("member not promoted: %q %v", role, ok)
	}
	// carolina leaving would orphan it: she's the owner now
	mustInvalid(t, a.LeaveLedger(ctx, c), "dono")
}

func d0() time.Time { return time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC) }

func TestCreatedBy(t *testing.T) {
	a, d, _ := newApp(t)
	ana, _ := a.SignIn(ctx, ana())
	a.InviteToLedger(ctx, ana, "carolina@x.com")
	c, err := a.SignIn(ctx, carol())
	if err != nil {
		t.Fatal(err)
	}
	in := TxIn{Kind: finance.Expense, Date: d0(), Amount: 100, Category: CategoryRef{Name: "Mercado", Create: true}}
	byAna, _ := a.For(ctx, ana).AddTx(in)
	byCarol, _ := a.For(ctx, c).AddTx(in)
	bulk, err := a.For(ctx, c).AddTxs([]TxIn{in})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := a.For(ctx, ana).SaveRecurrence(RecIn{Kind: finance.Expense, Category: CategoryRef{Name: "Mercado"}, Amount: 900, Day: 5,
		Interval: 1, Start: finance.NewMonth(2026, 11), EndMode: "never", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := a.For(ctx, c).ConfirmOccurrence(rec, finance.NewMonth(2026, 11), 0, time.Time{}, SourceManual)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]int64{byAna: ana.User.ID, byCarol: c.User.ID, bulk[0]: c.User.ID, confirmed: c.User.ID}
	for id, who := range want {
		e, err := a.For(ctx, ana).Tx(id)
		if err != nil || e.CreatedByID != who || e.CreatedBy == "" {
			t.Errorf("tx %d: created_by %d %q, want %d (%v)", id, e.CreatedByID, e.CreatedBy, who, err)
		}
	}
	// editing keeps the author
	if _, err := a.For(ctx, ana).UpdateTx(byCarol, TxIn{Date: d0(), Amount: 200, Category: CategoryRef{Name: "Mercado"}}); err != nil {
		t.Fatal(err)
	}
	if e, _ := a.For(ctx, ana).Tx(byCarol); e.CreatedByID != c.User.ID {
		t.Errorf("edit changed the author to %d", e.CreatedByID)
	}
	// bulk loaders (no acting user) leave it unknown
	id, err := d.Ledger(ana.LedgerID).InsertTx(db.TxInput{Kind: finance.Expense, Date: d0(), Amount: 1, CategoryID: mustCat(t, a, ana), Source: "import"})
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := a.For(ctx, ana).Tx(id); e.CreatedByID != 0 {
		t.Errorf("import got an author: %d", e.CreatedByID)
	}
	// if the author deletes their account, the entry stays, author unknown
	if err := a.DeleteAccount(ctx, c, "carolina@x.com"); err != nil {
		t.Fatal(err)
	}
	if e, err := a.For(ctx, ana).Tx(byCarol); err != nil || e.CreatedByID != 0 {
		t.Errorf("after author deletion: %+v %v", e, err)
	}
}

func mustCat(t *testing.T, a *App, act Actor) int64 {
	t.Helper()
	cats, err := a.For(ctx, act).Categories(finance.Expense)
	if err != nil || len(cats) == 0 {
		t.Fatal(err)
	}
	return cats[0].ID
}
