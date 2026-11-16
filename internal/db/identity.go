package db

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// User is someone who signs in, with Google or (self-hosted) a password.
type User struct {
	ID         int64
	GoogleSub  string // Google subject, or "local:<email>" for password accounts
	Email      string
	Name       string
	PictureURL string
	Suspended  bool   // set by an admin or operator; blocks sign-in, sessions and MCP links
	Approved   bool   // false = sign-up waiting for an admin (no ledger, no session)
	Role       string // RoleUser or RoleAdmin
	CreatedAt  time.Time
}

// Roles. Admins manage accounts; they never get access to other ledgers.
const (
	RoleUser  = "user"
	RoleAdmin = "admin"
)

// LocalSubPrefix marks password accounts (APP_AUTH=password) in google_sub, so
// they can never collide with a Google identity.
const LocalSubPrefix = "local:"

// GoogleUser is the verified identity from a Google ID token.
type GoogleUser struct {
	Sub, Email, Name, Picture string
	EmailVerified             bool
}

const userCols = `u.id, u.google_sub, u.email, u.name, u.picture_url, u.suspended_at IS NOT NULL, u.approved_at IS NOT NULL, u.role, u.created_at`

func scanUser(r pgx.Row) (User, error) {
	var u User
	err := r.Scan(&u.ID, &u.GoogleSub, &u.Email, &u.Name, &u.PictureURL, &u.Suspended, &u.Approved, &u.Role, &u.CreatedAt)
	return u, err
}

// UpsertGoogleUser creates the user on first sign-in, or refreshes their
// profile (email, name, picture can change on Google's side). A new user is
// pending unless autoApprove; autoApprove also approves a pending user.
func (d *DB) UpsertGoogleUser(ctx context.Context, g GoogleUser, autoApprove bool) (User, error) {
	return scanUser(d.pool.QueryRow(ctx, `INSERT INTO users AS u (google_sub, email, email_verified, name, picture_url, approved_at)
		VALUES ($1, $2, $3, $4, $5, CASE WHEN $6 THEN now() END)
		ON CONFLICT (google_sub) DO UPDATE SET email = excluded.email, email_verified = excluded.email_verified,
			name = excluded.name, picture_url = excluded.picture_url, last_login_at = now(),
			approved_at = coalesce(u.approved_at, excluded.approved_at)
		RETURNING `+userCols, g.Sub, g.Email, g.EmailVerified, g.Name, g.Picture, autoApprove))
}

// UserExists reports whether someone already signed in with this subject.
func (d *DB) UserExists(ctx context.Context, sub string) (bool, error) {
	var ok bool
	err := d.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE google_sub = $1)`, sub).Scan(&ok)
	return ok, err
}

// CountPending is the number of sign-ups waiting for approval.
func (d *DB) CountPending(ctx context.Context) (int, error) {
	var n int
	err := d.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE approved_at IS NULL`).Scan(&n)
	return n, err
}

func (d *DB) User(ctx context.Context, id int64) (User, error) {
	return scanUser(d.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users u WHERE u.id = $1`, id))
}

// UserLedger returns the ledger the user belongs to (ok=false when none).
func (d *DB) UserLedger(ctx context.Context, userID int64) (id int64, ok bool, err error) {
	err = d.pool.QueryRow(ctx, `SELECT ledger_id FROM ledger_members WHERE user_id = $1`, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

func (d *DB) AddMember(ctx context.Context, ledgerID, userID int64) error {
	_, err := d.pool.Exec(ctx, `INSERT INTO ledger_members (ledger_id, user_id) VALUES ($1, $2)`, ledgerID, userID)
	return err
}

// SetClaimEmail marks a ledger as waiting for its owner: the first verified
// sign-in with email joins it (see ClaimLedger). Only ledgers without members
// can be claimed.
func (d *DB) SetClaimEmail(ctx context.Context, ledgerID int64, email string) error {
	tag, err := d.pool.Exec(ctx, `UPDATE ledgers l SET claim_email = $2 WHERE id = $1
		AND NOT EXISTS (SELECT 1 FROM ledger_members m WHERE m.ledger_id = l.id)`, ledgerID, strings.ToLower(strings.TrimSpace(email)))
	if err == nil && tag.RowsAffected() == 0 {
		return errors.New("carteira inexistente ou que já tem membros")
	}
	return err
}

// ClaimLedger joins the user to the unclaimed ledger reserved for email, if any.
func (d *DB) ClaimLedger(ctx context.Context, userID int64, email string) (id int64, ok bool, err error) {
	err = pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `UPDATE ledgers l SET claim_email = NULL
			WHERE id = (SELECT id FROM ledgers WHERE claim_email = $1 ORDER BY id LIMIT 1)
			AND NOT EXISTS (SELECT 1 FROM ledger_members m WHERE m.ledger_id = l.id)
			RETURNING id`, strings.ToLower(email)).Scan(&id)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO ledger_members (ledger_id, user_id) VALUES ($1, $2)`, id, userID)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// ---------- sessions ----------

type Session struct {
	ID         int64
	UserID     int64
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

func (d *DB) CreateSession(ctx context.Context, userID int64, tokenHash []byte, now, expires time.Time, ip, userAgent string) error {
	_, err := d.pool.Exec(ctx, `INSERT INTO sessions (user_id, token_hash, created_at, last_seen_at, expires_at, ip, user_agent)
		VALUES ($1, $2, $3, $3, $4, $5, $6)`, userID, tokenHash, now, expires, ip, truncate(userAgent, 300))
	return err
}

// LiveSession returns the session for tokenHash when it is not revoked,
// not expired and was seen after idleSince.
func (d *DB) LiveSession(ctx context.Context, tokenHash []byte, now, idleSince time.Time) (Session, bool, error) {
	var s Session
	err := d.pool.QueryRow(ctx, `SELECT id, user_id, created_at, last_seen_at, expires_at FROM sessions
		WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > $2 AND last_seen_at > $3`, tokenHash, now, idleSince).
		Scan(&s.ID, &s.UserID, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, false, nil
	}
	return s, err == nil, err
}

func (d *DB) TouchSession(ctx context.Context, id int64, now time.Time) error {
	_, err := d.pool.Exec(ctx, `UPDATE sessions SET last_seen_at = $2 WHERE id = $1`, id, now)
	return err
}

func (d *DB) RevokeSession(ctx context.Context, tokenHash []byte) error {
	_, err := d.pool.Exec(ctx, `UPDATE sessions SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`, tokenHash)
	return err
}

// ---------- MCP tokens ----------

type MCPToken struct {
	ID         int64
	Label      string
	CreatedAt  time.Time
	LastUsedAt time.Time // zero = never used
}

func (d *DB) CreateMCPToken(ctx context.Context, userID, ledgerID int64, tokenHash []byte, label string) (int64, error) {
	var id int64
	err := d.pool.QueryRow(ctx, `INSERT INTO mcp_tokens (user_id, ledger_id, token_hash, label) VALUES ($1, $2, $3, $4) RETURNING id`,
		userID, ledgerID, tokenHash, label).Scan(&id)
	return id, err
}

// MCPTokens lists the user's active links in their ledger.
func (d *DB) MCPTokens(ctx context.Context, userID, ledgerID int64) ([]MCPToken, error) {
	rows, err := d.pool.Query(ctx, `SELECT id, label, created_at, last_used_at FROM mcp_tokens
		WHERE user_id = $1 AND ledger_id = $2 AND revoked_at IS NULL ORDER BY id`, userID, ledgerID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (MCPToken, error) {
		var t MCPToken
		var used *time.Time
		err := r.Scan(&t.ID, &t.Label, &t.CreatedAt, &used)
		if used != nil {
			t.LastUsedAt = *used
		}
		return t, err
	})
}

// RevokeMCPToken revokes one of the user's links; ok=false when it isn't theirs.
func (d *DB) RevokeMCPToken(ctx context.Context, userID, id int64) (bool, error) {
	tag, err := d.pool.Exec(ctx, `UPDATE mcp_tokens SET revoked_at = now() WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`, id, userID)
	return tag.RowsAffected() == 1, err
}

// MCPTokenOwner resolves an active link to its user and ledger, and records
// its use.
func (d *DB) MCPTokenOwner(ctx context.Context, tokenHash []byte) (userID, ledgerID int64, ok bool, err error) {
	err = d.pool.QueryRow(ctx, `UPDATE mcp_tokens SET last_used_at = now()
		WHERE token_hash = $1 AND revoked_at IS NULL RETURNING user_id, ledger_id`, tokenHash).Scan(&userID, &ledgerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, false, nil
	}
	return userID, ledgerID, err == nil, err
}

// ---------- account administration ----------

// FindUser looks a user up by numeric id or by email (operator CLI).
func (d *DB) FindUser(ctx context.Context, idOrEmail string) (User, error) {
	if id, err := strconv.ParseInt(idOrEmail, 10, 64); err == nil {
		return d.User(ctx, id)
	}
	return scanUser(d.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users u WHERE lower(u.email) = lower($1) ORDER BY u.id LIMIT 1`, idOrEmail))
}

// SetSuspended suspends or reactivates a user. Suspending also revokes their
// sessions; MCP links are refused while suspended (see app.MCPActor).
func (d *DB) SetSuspended(ctx context.Context, userID int64, suspended bool) error {
	return pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		q := `UPDATE users SET suspended_at = NULL WHERE id = $1`
		if suspended {
			q = `UPDATE users SET suspended_at = coalesce(suspended_at, now()) WHERE id = $1`
		}
		tag, err := tx.Exec(ctx, q, userID)
		if err == nil && tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		if err != nil || !suspended {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE sessions SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID)
		return err
	})
}

// DeleteUser erases a user: their ledger and all its financial data when they
// are its only member (foreign-key cascades), then the user with their
// sessions, memberships and MCP links. It reports the deleted ledger (0 when
// the ledger is shared and was kept).
func (d *DB) DeleteUser(ctx context.Context, userID int64) (ledgerID int64, err error) {
	err = pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		var others int
		err := tx.QueryRow(ctx, `SELECT m.ledger_id, (SELECT count(*) FROM ledger_members o WHERE o.ledger_id = m.ledger_id AND o.user_id <> m.user_id)
			FROM ledger_members m WHERE m.user_id = $1`, userID).Scan(&ledgerID, &others)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if ledgerID != 0 && others == 0 {
			if _, err := tx.Exec(ctx, `DELETE FROM ledgers WHERE id = $1`, ledgerID); err != nil {
				return err
			}
		} else if ledgerID != 0 {
			// a shared ledger stays with the others; if its owner leaves,
			// the longest-standing member becomes the owner
			if _, err := tx.Exec(ctx, `UPDATE ledger_members SET role = 'owner'
				WHERE ledger_id = $1 AND user_id = (SELECT user_id FROM ledger_members WHERE ledger_id = $1 AND user_id <> $2 ORDER BY joined_at LIMIT 1)
				AND NOT EXISTS (SELECT 1 FROM ledger_members WHERE ledger_id = $1 AND user_id <> $2 AND role = 'owner')`, ledgerID, userID); err != nil {
				return err
			}
			ledgerID = 0
		}
		tag, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		if err == nil && tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		return err
	})
	return ledgerID, err
}

// ListUsers is the admin's account list: pending first, then by sign-up date.
// It carries no financial data.
func (d *DB) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := d.pool.Query(ctx, `SELECT `+userCols+` FROM users u ORDER BY (u.approved_at IS NULL) DESC, u.created_at DESC, u.id DESC LIMIT 1000`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (User, error) { return scanUser(r) })
}

// Approve activates a pending sign-up; ok=false when it isn't pending.
func (d *DB) Approve(ctx context.Context, userID int64) (bool, error) {
	tag, err := d.pool.Exec(ctx, `UPDATE users SET approved_at = now() WHERE id = $1 AND approved_at IS NULL`, userID)
	return tag.RowsAffected() == 1, err
}

// SetRole grants or removes the admin role (operator CLI only).
func (d *DB) SetRole(ctx context.Context, userID int64, role string) error {
	tag, err := d.pool.Exec(ctx, `UPDATE users SET role = $2 WHERE id = $1`, userID, role)
	if err == nil && tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return err
}

// ---------- password accounts (APP_AUTH=password) ----------

// PasswordAccount is what a password sign-in needs to decide.
type PasswordAccount struct {
	User        User
	Hash        string
	LockedUntil time.Time // zero = not locked
}

// SetPassword creates or updates the password account for email (approved,
// verified) and revokes its sessions.
func (d *DB) SetPassword(ctx context.Context, email, hash string) (User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var u User
	err := pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		var err error
		u, err = scanUser(tx.QueryRow(ctx, `INSERT INTO users AS u (google_sub, email, email_verified, approved_at, password_hash)
			VALUES ($1, $2, true, now(), $3)
			ON CONFLICT (google_sub) DO UPDATE SET password_hash = excluded.password_hash, failed_logins = 0, locked_until = NULL
			RETURNING `+userCols, LocalSubPrefix+email, email, hash))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE sessions SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, u.ID)
		return err
	})
	return u, err
}

// PasswordAccountFor finds the password account of email (ok=false if none).
func (d *DB) PasswordAccountFor(ctx context.Context, email string) (PasswordAccount, bool, error) {
	var a PasswordAccount
	var locked *time.Time
	row := d.pool.QueryRow(ctx, `SELECT `+userCols+`, coalesce(u.password_hash, ''), u.locked_until FROM users u WHERE u.google_sub = $1`,
		LocalSubPrefix+strings.ToLower(strings.TrimSpace(email)))
	err := row.Scan(&a.User.ID, &a.User.GoogleSub, &a.User.Email, &a.User.Name, &a.User.PictureURL, &a.User.Suspended,
		&a.User.Approved, &a.User.Role, &a.User.CreatedAt, &a.Hash, &locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, false, nil
	}
	if locked != nil {
		a.LockedUntil = *locked
	}
	return a, err == nil && a.Hash != "", err
}

// LoginFailed counts a wrong password; at max failures the account is locked
// until lockUntil and the counter starts over. It reports whether it locked.
func (d *DB) LoginFailed(ctx context.Context, userID int64, max int, lockUntil time.Time) (bool, error) {
	var locked bool
	err := d.pool.QueryRow(ctx, `UPDATE users SET
			locked_until = CASE WHEN failed_logins + 1 >= $2 THEN $3 ELSE locked_until END,
			failed_logins = CASE WHEN failed_logins + 1 >= $2 THEN 0 ELSE failed_logins + 1 END
		WHERE id = $1 RETURNING locked_until IS NOT DISTINCT FROM $3`, userID, max, lockUntil).Scan(&locked)
	return locked, err
}

// LoginSucceeded clears the failure counter and any lock.
func (d *DB) LoginSucceeded(ctx context.Context, userID int64) error {
	_, err := d.pool.Exec(ctx, `UPDATE users SET failed_logins = 0, locked_until = NULL, last_login_at = now() WHERE id = $1`, userID)
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
