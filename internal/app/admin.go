package app

import (
	"context"
	"errors"
	"time"

	"controle-gastos/internal/db"
)

// AdminFreshness is how recent an admin's sign-in must be to manage accounts:
// a stolen long-lived session cookie can't be used to approve or suspend.
const AdminFreshness = 12 * time.Hour

var (
	// ErrForbidden: the actor is not an admin. Callers answer 404, so the
	// admin area's existence leaks nothing.
	ErrForbidden = errors.New("acesso negado")
	// ErrReauth: the admin must sign in again (session older than AdminFreshness).
	ErrReauth = errors.New("entre de novo para continuar")
)

// requireAdmin is the server-side check behind every admin operation. The
// role is read from the database on each request (App.actor), never from the
// client or from an email comparison.
func (a *App) requireAdmin(actor Actor, action string) error {
	if actor.User.Role != db.RoleAdmin || actor.Source != SourceManual {
		Audit("authz_denied", "user_id", actor.User.ID, "action", action)
		return ErrForbidden
	}
	if actor.AuthAt.IsZero() || a.now().Sub(actor.AuthAt) > AdminFreshness {
		return ErrReauth
	}
	return nil
}

// IsAdmin reports whether the actor may see the admin area (for the nav).
func IsAdmin(actor Actor) bool { return actor.User.Role == db.RoleAdmin }

// AdminUsers lists accounts for the admin page: identity and status only,
// never financial data.
func (a *App) AdminUsers(ctx context.Context, actor Actor) ([]db.User, error) {
	if err := a.requireAdmin(actor, "list_users"); err != nil {
		return nil, err
	}
	return a.db.ListUsers(ctx)
}

// PendingCount is the number of sign-ups waiting (admins only; 0 otherwise).
func (a *App) PendingCount(ctx context.Context, actor Actor) int {
	if !IsAdmin(actor) {
		return 0
	}
	n, _ := a.db.CountPending(ctx)
	return n
}

// Approve activates a pending sign-up; its ledger is created on its next sign-in.
func (a *App) Approve(ctx context.Context, actor Actor, userID int64) error {
	if err := a.requireAdmin(actor, "approve"); err != nil {
		return err
	}
	ok, err := a.db.Approve(ctx, userID)
	if err == nil && !ok {
		return invalid("cadastro não encontrado ou já aprovado")
	}
	if err == nil {
		Audit("account_approved", "user_id", userID, "by", actor.User.ID)
	}
	return err
}

// Reject erases a pending sign-up (its name, email and photo). Only pending
// accounts: deleting an active account and its data stays a deliberate
// operator action (CLI delete-user) or the user's own choice.
func (a *App) Reject(ctx context.Context, actor Actor, userID int64) error {
	if err := a.requireAdmin(actor, "reject"); err != nil {
		return err
	}
	u, err := a.db.User(ctx, userID)
	if err != nil {
		return notFound(err, "conta")
	}
	if u.Approved {
		return invalid("só cadastros pendentes podem ser recusados")
	}
	if _, err := a.db.DeleteUser(ctx, userID); err != nil {
		return err
	}
	Audit("signup_rejected", "user_id", userID, "by", actor.User.ID)
	return nil
}

// Suspend blocks an account (sign-in, sessions, MCP links). Admins can't
// suspend themselves or another admin: that stays with the operator CLI.
func (a *App) Suspend(ctx context.Context, actor Actor, userID int64) error {
	return a.adminSuspend(ctx, actor, userID, true)
}

// Reactivate lifts a suspension.
func (a *App) Reactivate(ctx context.Context, actor Actor, userID int64) error {
	return a.adminSuspend(ctx, actor, userID, false)
}

func (a *App) adminSuspend(ctx context.Context, actor Actor, userID int64, suspend bool) error {
	action := "reactivate"
	if suspend {
		action = "suspend"
	}
	if err := a.requireAdmin(actor, action); err != nil {
		return err
	}
	u, err := a.db.User(ctx, userID)
	if err != nil {
		return notFound(err, "conta")
	}
	if u.ID == actor.User.ID || u.Role == db.RoleAdmin {
		return invalid("contas de administrador só podem ser alteradas pelo operador do servidor")
	}
	if err := a.db.SetSuspended(ctx, userID, suspend); err != nil {
		return err
	}
	ev := "account_reactivated"
	if suspend {
		ev = "account_suspended"
	}
	Audit(ev, "user_id", userID, "by", actor.User.ID)
	return nil
}

// SetRole grants or removes the admin role (operator CLI over SSH only).
func (a *App) SetRole(ctx context.Context, idOrEmail, role string) (db.User, error) {
	if role != db.RoleUser && role != db.RoleAdmin {
		return db.User{}, invalid("papel inválido")
	}
	u, err := a.db.FindUser(ctx, idOrEmail)
	if err != nil {
		return u, notFound(err, "conta")
	}
	if !u.Approved {
		return u, invalid("aprove a conta antes de torná-la administradora")
	}
	if err := a.db.SetRole(ctx, u.ID, role); err != nil {
		return u, err
	}
	Audit("role_changed", "user_id", u.ID, "role", role, "by", "operator-cli")
	u.Role = role
	return u, nil
}
