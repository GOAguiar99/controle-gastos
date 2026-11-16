package db

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// User is someone who signed in with Google.
type User struct {
	ID         int64
	GoogleSub  string
	Email      string
	Name       string
	PictureURL string
}

// GoogleUser is the verified identity from a Google ID token.
type GoogleUser struct {
	Sub, Email, Name, Picture string
	EmailVerified             bool
}

const userCols = `u.id, u.google_sub, u.email, u.name, u.picture_url`

func scanUser(r pgx.Row) (User, error) {
	var u User
	err := r.Scan(&u.ID, &u.GoogleSub, &u.Email, &u.Name, &u.PictureURL)
	return u, err
}

// UpsertGoogleUser creates the user on first sign-in, or refreshes their
// profile (email, name, picture can change on Google's side).
func (d *DB) UpsertGoogleUser(ctx context.Context, g GoogleUser) (User, error) {
	return scanUser(d.pool.QueryRow(ctx, `INSERT INTO users AS u (google_sub, email, email_verified, name, picture_url)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (google_sub) DO UPDATE SET email = excluded.email, email_verified = excluded.email_verified,
			name = excluded.name, picture_url = excluded.picture_url, last_login_at = now()
		RETURNING `+userCols, g.Sub, g.Email, g.EmailVerified, g.Name, g.Picture))
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

func (d *DB) CreateSession(ctx context.Context, userID int64, tokenHash []byte, expires time.Time, ip, userAgent string) error {
	_, err := d.pool.Exec(ctx, `INSERT INTO sessions (user_id, token_hash, expires_at, ip, user_agent) VALUES ($1, $2, $3, $4, $5)`,
		userID, tokenHash, expires, ip, truncate(userAgent, 300))
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
