package app

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"

	"controle-gastos/internal/db"
)

// InviteTTL is how long an invite to a ledger stays valid.
const InviteTTL = 7 * 24 * time.Hour

// Sharing lets the owner of a ledger invite people by email. The invite is
// accepted by signing in with that exact email (Google-verified, or a CLI
// password account); it also counts as approval, so no admin step is needed.
// Everyone in a ledger sees and edits all of it. Account-level, so web-only
// (no MCP tool): an MCP link is a bearer URL driven by an LLM.

// Sharing is what the Config page shows about the actor's ledger.
type Sharing struct {
	Role      string // db.MemberOwner or db.MemberRole
	Members   []db.Member
	Invites   []db.Invite // open invites (owner only)
	ForMe     []db.Invite // invites to the actor's email from other ledgers
	MaxPeople int
}

func (a *App) memberRole(ctx context.Context, actor Actor) (string, error) {
	role, ok, err := a.db.MemberRoleOf(ctx, actor.LedgerID, actor.User.ID)
	if err == nil && !ok {
		err = invalid("você não faz parte desta carteira")
	}
	return role, err
}

func (a *App) requireOwner(ctx context.Context, actor Actor, action string) error {
	if actor.Source != SourceManual {
		return invalid("isso só pode ser feito pelo app, não pelo assistente")
	}
	role, err := a.memberRole(ctx, actor)
	if err != nil {
		return err
	}
	if role != db.MemberOwner {
		Audit("authz_denied", "user_id", actor.User.ID, "action", action)
		return invalid("só quem é dono da carteira pode fazer isso")
	}
	return nil
}

// Sharing describes the actor's ledger people and invites.
func (a *App) Sharing(ctx context.Context, actor Actor) (Sharing, error) {
	sh := Sharing{MaxPeople: db.MaxLedgerPeople}
	var err error
	if sh.Role, err = a.memberRole(ctx, actor); err != nil {
		return sh, err
	}
	if sh.Members, err = a.db.Members(ctx, actor.LedgerID); err != nil {
		return sh, err
	}
	if sh.Role == db.MemberOwner {
		if sh.Invites, err = a.db.OpenInvites(ctx, actor.LedgerID, a.now()); err != nil {
			return sh, err
		}
	}
	sh.ForMe, err = a.db.InvitesForEmail(ctx, actor.User.Email, actor.LedgerID, a.now())
	return sh, err
}

// InviteToLedger invites email to the actor's ledger (owner only).
func (a *App) InviteToLedger(ctx context.Context, actor Actor, email string) error {
	if err := a.requireOwner(ctx, actor, "invite"); err != nil {
		return err
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email || len(email) > 254 {
		return invalid("e-mail inválido")
	}
	if email == strings.ToLower(actor.User.Email) {
		return invalid("você já está nesta carteira")
	}
	members, err := a.db.Members(ctx, actor.LedgerID)
	if err != nil {
		return err
	}
	for _, m := range members {
		if strings.EqualFold(m.Email, email) {
			return invalid("essa pessoa já está nesta carteira")
		}
	}
	now := a.now()
	id, err := a.db.CreateInvite(ctx, actor.LedgerID, actor.User.ID, email, now, now.Add(InviteTTL))
	if errors.Is(err, db.ErrLedgerFull) {
		return invalid("a carteira já tem o máximo de %d pessoas (contando convites pendentes)", db.MaxLedgerPeople)
	}
	if err == nil {
		Audit("ledger_invite_created", "user_id", actor.User.ID, "ledger_id", actor.LedgerID, "invite_id", id)
	}
	return err
}

// CancelInvite revokes an open invite of the actor's ledger (owner only).
func (a *App) CancelInvite(ctx context.Context, actor Actor, id int64) error {
	if err := a.requireOwner(ctx, actor, "cancel_invite"); err != nil {
		return err
	}
	ok, err := a.db.RevokeInvite(ctx, actor.LedgerID, id, a.now())
	if err == nil && !ok {
		return invalid("convite não encontrado")
	}
	if err == nil {
		Audit("ledger_invite_revoked", "user_id", actor.User.ID, "invite_id", id)
	}
	return err
}

// RemoveMember takes someone out of the actor's ledger (owner only). They are
// signed out and lose their MCP links; next sign-in gives them their own ledger.
func (a *App) RemoveMember(ctx context.Context, actor Actor, userID int64) error {
	if err := a.requireOwner(ctx, actor, "remove_member"); err != nil {
		return err
	}
	if userID == actor.User.ID {
		return invalid("o dono não pode sair da própria carteira")
	}
	ok, err := a.db.RemoveMember(ctx, actor.LedgerID, userID)
	if err == nil && !ok {
		return invalid("pessoa não encontrada nesta carteira")
	}
	if err == nil {
		Audit("ledger_member_removed", "user_id", userID, "ledger_id", actor.LedgerID, "by", actor.User.ID)
	}
	return err
}

// LeaveLedger lets a member (not the owner) leave a shared ledger.
func (a *App) LeaveLedger(ctx context.Context, actor Actor) error {
	if actor.Source != SourceManual {
		return invalid("isso só pode ser feito pelo app, não pelo assistente")
	}
	role, err := a.memberRole(ctx, actor)
	if err != nil {
		return err
	}
	if role == db.MemberOwner {
		return invalid("o dono não pode sair da própria carteira; remova as outras pessoas ou exclua a conta")
	}
	if _, err := a.db.RemoveMember(ctx, actor.LedgerID, actor.User.ID); err != nil {
		return err
	}
	Audit("ledger_left", "user_id", actor.User.ID, "ledger_id", actor.LedgerID)
	return nil
}

// AcceptInvite moves the actor into the inviting ledger. Their own ledger is
// deleted only if they are alone in it and it has no data.
func (a *App) AcceptInvite(ctx context.Context, actor Actor, id int64) error {
	if actor.Source != SourceManual {
		return invalid("isso só pode ser feito pelo app, não pelo assistente")
	}
	ledger, err := a.db.JoinViaInvite(ctx, actor.User.ID, id, actor.User.Email, a.now())
	switch {
	case errors.Is(err, db.ErrInviteUnusable):
		return invalid("convite inválido ou expirado")
	case errors.Is(err, db.ErrLedgerHasData):
		return invalid("sua carteira atual tem dados. Exporte e exclua sua conta, ou peça para sair da carteira compartilhada, antes de entrar em outra")
	case err != nil:
		return err
	}
	Audit("ledger_joined", "user_id", actor.User.ID, "ledger_id", ledger, "invite_id", id)
	return nil
}

// DeclineInvite refuses an invite addressed to the actor.
func (a *App) DeclineInvite(ctx context.Context, actor Actor, id int64) error {
	ok, err := a.db.DeclineInvite(ctx, actor.User.Email, id, a.now())
	if err == nil && !ok {
		return invalid("convite não encontrado")
	}
	return err
}

// joinInvitedLedger puts a user without a ledger into the oldest ledger that
// invited their email (ok=false when none).
func (a *App) joinInvitedLedger(ctx context.Context, u db.User) (int64, bool, error) {
	invs, err := a.db.InvitesForEmail(ctx, u.Email, 0, a.now())
	if err != nil || len(invs) == 0 {
		return 0, false, err
	}
	id, err := a.db.JoinViaInvite(ctx, u.ID, invs[0].ID, u.Email, a.now())
	if err != nil {
		return 0, false, err
	}
	Audit("ledger_joined", "user_id", u.ID, "ledger_id", id, "invite_id", invs[0].ID)
	return id, true, nil
}

// IsShared reports whether more than one person uses the actor's ledger (the
// UI then shows who entered each transaction).
func (a *App) IsShared(ctx context.Context, actor Actor) bool {
	n, err := a.db.MemberCount(ctx, actor.LedgerID)
	return err == nil && n > 1
}
