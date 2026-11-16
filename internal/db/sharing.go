package db

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Ledger member roles: the owner invites and removes people; members use the
// ledger like the owner but can only leave it.
const (
	MemberOwner = "owner"
	MemberRole  = "member"
)

// MaxLedgerPeople caps members plus open invites of one ledger.
const MaxLedgerPeople = 5

// ErrLedgerFull is returned when a ledger already has MaxLedgerPeople.
var ErrLedgerFull = errors.New("ledger full")

// ErrInviteUnusable means the invite is not open, expired, or not for this email.
var ErrInviteUnusable = errors.New("invite unusable")

// ErrLedgerHasData means the user's own ledger has data and can't be dropped.
var ErrLedgerHasData = errors.New("ledger has data")

type Member struct {
	UserID   int64
	Email    string
	Name     string
	Role     string
	JoinedAt time.Time
}

type Invite struct {
	ID        int64
	LedgerID  int64
	Email     string
	CreatedAt time.Time
	ExpiresAt time.Time
	// for the invitee's view
	LedgerName  string
	InviterName string
}

func normEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

// MemberRoleOf is the user's role in ledgerID (ok=false when not a member).
func (d *DB) MemberRoleOf(ctx context.Context, ledgerID, userID int64) (string, bool, error) {
	var role string
	err := d.pool.QueryRow(ctx, `SELECT role FROM ledger_members WHERE ledger_id = $1 AND user_id = $2`, ledgerID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return role, err == nil, err
}

// Members lists the people of a ledger, owner first.
func (d *DB) Members(ctx context.Context, ledgerID int64) ([]Member, error) {
	rows, err := d.pool.Query(ctx, `SELECT u.id, u.email, u.name, m.role, m.joined_at FROM ledger_members m
		JOIN users u ON u.id = m.user_id WHERE m.ledger_id = $1 ORDER BY m.role = 'owner' DESC, m.joined_at`, ledgerID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Member, error) {
		var m Member
		err := r.Scan(&m.UserID, &m.Email, &m.Name, &m.Role, &m.JoinedAt)
		return m, err
	})
}

// CreateInvite invites email into ledgerID, within MaxLedgerPeople. Inviting
// an email that already has an open invite renews it.
func (d *DB) CreateInvite(ctx context.Context, ledgerID, by int64, email string, now, expires time.Time) (int64, error) {
	var id int64
	err := pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		// serialize invites/joins of this ledger
		if _, err := tx.Exec(ctx, `SELECT 1 FROM ledgers WHERE id = $1 FOR UPDATE`, ledgerID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE ledger_invites SET revoked_at = $3 WHERE ledger_id = $1 AND email = $2
			AND accepted_at IS NULL AND revoked_at IS NULL`, ledgerID, normEmail(email), now); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM ledger_members WHERE ledger_id = $1) +
			(SELECT count(*) FROM ledger_invites WHERE ledger_id = $1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > $2)`,
			ledgerID, now).Scan(&n); err != nil {
			return err
		}
		if n >= MaxLedgerPeople {
			return ErrLedgerFull
		}
		return tx.QueryRow(ctx, `INSERT INTO ledger_invites (ledger_id, email, invited_by, created_at, expires_at)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`, ledgerID, normEmail(email), by, now, expires).Scan(&id)
	})
	return id, err
}

// OpenInvites lists a ledger's invites still waiting.
func (d *DB) OpenInvites(ctx context.Context, ledgerID int64, now time.Time) ([]Invite, error) {
	return d.invites(ctx, `i.ledger_id = $1`, ledgerID, now)
}

// InvitesForEmail lists open invites addressed to email, from other ledgers.
func (d *DB) InvitesForEmail(ctx context.Context, email string, exceptLedger int64, now time.Time) ([]Invite, error) {
	return d.invites(ctx, `i.email = $1 AND i.ledger_id <> $3`, normEmail(email), now, exceptLedger)
}

func (d *DB) invites(ctx context.Context, where string, args ...any) ([]Invite, error) {
	rows, err := d.pool.Query(ctx, `SELECT i.id, i.ledger_id, i.email, i.created_at, i.expires_at, l.name, coalesce(u.name, '')
		FROM ledger_invites i JOIN ledgers l ON l.id = i.ledger_id LEFT JOIN users u ON u.id = i.invited_by
		WHERE `+where+` AND i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > $2 ORDER BY i.id`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Invite, error) {
		var x Invite
		err := r.Scan(&x.ID, &x.LedgerID, &x.Email, &x.CreatedAt, &x.ExpiresAt, &x.LedgerName, &x.InviterName)
		return x, err
	})
}

// HasOpenInvite reports whether some ledger invited email.
func (d *DB) HasOpenInvite(ctx context.Context, email string, now time.Time) (bool, error) {
	var ok bool
	err := d.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ledger_invites WHERE email = $1
		AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > $2)`, normEmail(email), now).Scan(&ok)
	return ok, err
}

// RevokeInvite cancels an open invite of ledgerID; ok=false if none.
func (d *DB) RevokeInvite(ctx context.Context, ledgerID, id int64, now time.Time) (bool, error) {
	tag, err := d.pool.Exec(ctx, `UPDATE ledger_invites SET revoked_at = $3 WHERE id = $1 AND ledger_id = $2
		AND accepted_at IS NULL AND revoked_at IS NULL`, id, ledgerID, now)
	return tag.RowsAffected() == 1, err
}

// DeclineInvite cancels an open invite addressed to email; ok=false if none.
func (d *DB) DeclineInvite(ctx context.Context, email string, id int64, now time.Time) (bool, error) {
	tag, err := d.pool.Exec(ctx, `UPDATE ledger_invites SET revoked_at = $3 WHERE id = $1 AND email = $2
		AND accepted_at IS NULL AND revoked_at IS NULL`, id, normEmail(email), now)
	return tag.RowsAffected() == 1, err
}

// JoinViaInvite moves userID into the invite's ledger as a member, in one
// transaction. The invite must be open, unexpired and addressed to email.
// The user's current membership is dropped first: as a plain member it is
// just left; as the only person of their own ledger, that ledger is deleted,
// but only if it holds no data (ErrLedgerHasData otherwise). An owner of a
// ledger shared with others can't join elsewhere.
func (d *DB) JoinViaInvite(ctx context.Context, userID, inviteID int64, email string, now time.Time) (ledgerID int64, err error) {
	err = pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT ledger_id FROM ledger_invites WHERE id = $1 AND email = $2
			AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > $3 FOR UPDATE`, inviteID, normEmail(email), now).Scan(&ledgerID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInviteUnusable
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM ledgers WHERE id = $1 FOR UPDATE`, ledgerID); err != nil {
			return err
		}
		var cur int64
		var role string
		var others int
		err = tx.QueryRow(ctx, `SELECT m.ledger_id, m.role, (SELECT count(*) FROM ledger_members o WHERE o.ledger_id = m.ledger_id AND o.user_id <> m.user_id)
			FROM ledger_members m WHERE m.user_id = $1`, userID).Scan(&cur, &role, &others)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		case cur == ledgerID:
			return ErrInviteUnusable // already in it
		case others == 0:
			// The tenant of a pooled connection is set when it is acquired
			// (PrepareConn), not per query: inside this transaction it is empty
			// and RLS would hide every row, making any ledger look empty. Scope
			// the check to cur explicitly, for this transaction only.
			var n int
			if err := tx.QueryRow(ctx, `SELECT set_config('app.ledger_id', $1, true)`, strconv.FormatInt(cur, 10)).Scan(new(string)); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM transactions) + (SELECT count(*) FROM recurrences)
				+ (SELECT count(*) FROM debts) + (SELECT count(*) FROM cash_snapshots)`).Scan(&n); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SELECT set_config('app.ledger_id', '', true)`); err != nil {
				return err
			}
			if n > 0 {
				return ErrLedgerHasData
			}
			if _, err := tx.Exec(ctx, `DELETE FROM ledgers WHERE id = $1`, cur); err != nil {
				return err
			}
		case role == MemberOwner:
			return ErrLedgerHasData
		default:
			if _, err := tx.Exec(ctx, `DELETE FROM ledger_members WHERE user_id = $1`, userID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO ledger_members (ledger_id, user_id, role) VALUES ($1, $2, 'member')`, ledgerID, userID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE ledger_invites SET accepted_at = $2 WHERE id = $1`, inviteID, now)
		return err
	})
	return ledgerID, err
}

// RemoveMember takes a member (not the owner) out of ledgerID and ends their
// sessions; their MCP links of that ledger go with the membership (FK cascade).
func (d *DB) RemoveMember(ctx context.Context, ledgerID, userID int64) (bool, error) {
	var ok bool
	err := pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM ledger_members WHERE ledger_id = $1 AND user_id = $2 AND role = 'member'`, ledgerID, userID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		ok = true
		_, err = tx.Exec(ctx, `UPDATE sessions SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID)
		return err
	})
	return ok, err
}
