package app

import (
	"context"
	"time"
)

// TrustVersion is the version of the "Seus dados e segurança" page
// (/seguranca). Bump it whenever that page changes in substance: every user
// sees it again, before the dashboard, and confirms it.
const TrustVersion = 1

// Inactive accounts are erased: approved accounts with no use of the app (web
// or MCP link) for InactivityMonths are deleted like a self-service account
// deletion. The clock starts no earlier than InactivityPolicyStart, so no
// account is erased before a full period has passed since the policy was
// announced.
const InactivityMonths = 12

var InactivityPolicyStart = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

// NeedsTrust reports whether the actor still has to read the current version
// of the security page.
func NeedsTrust(a Actor) bool { return a.User.TrustVersion < TrustVersion }

// AcceptTrust records that the actor read the current security page.
func (a *App) AcceptTrust(ctx context.Context, actor Actor) error {
	if !NeedsTrust(actor) {
		return nil
	}
	if err := a.db.SetTrustVersion(ctx, actor.User.ID, TrustVersion, a.now()); err != nil {
		return err
	}
	Audit("trust_accepted", "user_id", actor.User.ID, "version", TrustVersion)
	return nil
}

// InactivityCutoff is the moment before which an account's last use makes it
// inactive.
func (a *App) InactivityCutoff() time.Time { return a.now().AddDate(0, -InactivityMonths, 0) }

// PurgeInactive erases every inactive account (see InactivityMonths) and
// returns how many. With dryRun it only counts them. A shared ledger stays
// with its other members, as in DeleteAccount.
func (a *App) PurgeInactive(ctx context.Context, dryRun bool) (int, error) {
	ids, err := a.db.InactiveUsers(ctx, a.InactivityCutoff(), InactivityPolicyStart)
	if err != nil || dryRun {
		return len(ids), err
	}
	n := 0
	for _, id := range ids {
		ledger, err := a.db.DeleteUser(ctx, id)
		if err != nil {
			return n, err
		}
		Audit("account_deleted", "user_id", id, "ledger_deleted", ledger, "by", "inactivity")
		n++
	}
	return n, nil
}

// KeepPurgingInactive runs PurgeInactive now and once a day until ctx ends.
func (a *App) KeepPurgingInactive(ctx context.Context, logf func(string, ...any)) {
	for {
		if n, err := a.PurgeInactive(ctx, false); err != nil && ctx.Err() == nil {
			logf("contas inativas: %v", err)
		} else if n > 0 {
			logf("contas inativas: %d excluída(s)", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(24 * time.Hour):
		}
	}
}
