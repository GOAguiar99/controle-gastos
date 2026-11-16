package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"controle-gastos/internal/db"
	"controle-gastos/internal/finance"
)

// Session lifetimes: a login lasts at most SessionTTL and ends earlier after
// SessionIdle without use.
const (
	SessionTTL  = 30 * 24 * time.Hour
	SessionIdle = 7 * 24 * time.Hour
	// sessions are marked as used at most this often (one write per request is wasteful)
	sessionTouchEvery = 5 * time.Minute
	maxMCPTokens      = 20
)

// Actor is who is acting and on which ledger: a signed-in user (web) or the
// owner of an MCP link.
type Actor struct {
	User     db.User
	LedgerID int64
	Source   string    // SourceManual (web) or SourceMCP
	AuthAt   time.Time // when the web session was created (fresh sign-in check for admins)
}

// Sign-up policies (APP_SIGNUP).
const (
	SignupApproval = "approval" // unknown accounts wait for an admin (default)
	SignupClosed   = "closed"   // only the allow list and existing accounts
)

// ErrPending is returned by SignIn for a sign-up waiting for approval. It is
// not a failure: the web shows a "waiting" page and creates no session.
var ErrPending = errors.New("cadastro aguardando aprovação")

// App is the entry point above the per-ledger Service: sign-in, sessions, MCP
// links, and building the Service for an actor.
type App struct {
	db  *db.DB
	loc *time.Location
	now func() time.Time
	// AllowEmail lists accounts that are approved on sign-in (nil = none).
	AllowEmail func(email string) bool
	// Signup is SignupApproval or SignupClosed (empty = approval).
	Signup string
	// MaxPending caps sign-ups waiting for approval (0 = 200).
	MaxPending int
}

func NewApp(d *db.DB, loc *time.Location) *App {
	if loc == nil {
		loc = time.UTC
	}
	return &App{db: d, loc: loc, now: time.Now}
}

// SetClock overrides the current time (tests).
func (a *App) SetClock(now func() time.Time) { a.now = now }

// Today is the current calendar date in the configured timezone, as UTC midnight.
func (a *App) Today() time.Time { return finance.DateOnly(a.now().In(a.loc)) }

// For returns the Service of the actor's ledger. Queries use ctx and can only
// reach that ledger's rows.
func (a *App) For(ctx context.Context, actor Actor) *Service {
	return New(a.db.Ledger(actor.LedgerID).WithContext(ctx).WithUser(actor.User.ID), a.loc).withClock(a.now)
}

// AllowList parses "a@x.com, b@y.com" into an AllowEmail func; "*" allows
// every verified Google account.
func AllowList(list string) func(string) bool {
	set := map[string]bool{}
	for _, e := range strings.Split(list, ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			set[e] = true
		}
	}
	return func(email string) bool { return set["*"] || set[strings.ToLower(email)] }
}

// SignIn turns a verified Google identity into a user with a ledger. Accounts
// in AllowEmail are approved on the spot; any other new account waits for an
// admin (ErrPending, no ledger yet) or, with SignupClosed, is refused. An
// approved user keeps their ledger, claims one reserved for the email, or
// gets a new one.
func (a *App) SignIn(ctx context.Context, g db.GoogleUser) (Actor, error) {
	if g.Sub == "" || !g.EmailVerified || g.Email == "" {
		return Actor{}, invalid("a conta Google precisa ter um e-mail verificado")
	}
	auto := a.AllowEmail != nil && a.AllowEmail(g.Email)
	if !auto {
		// an invite to a ledger is approval: the owner vouched for this email
		invited, err := a.db.HasOpenInvite(ctx, g.Email, a.now())
		if err != nil {
			return Actor{}, err
		}
		auto = invited
	}
	if !auto {
		exists, err := a.db.UserExists(ctx, g.Sub)
		if err != nil {
			return Actor{}, err
		}
		if !exists {
			if a.Signup == SignupClosed {
				return Actor{}, invalid("esta conta Google não tem acesso a este app")
			}
			max := a.MaxPending
			if max <= 0 {
				max = 200
			}
			if n, err := a.db.CountPending(ctx); err != nil || n >= max {
				if err == nil {
					Audit("signup_refused", "reason", "pending_cap")
					err = invalid("novos cadastros estão temporariamente fechados; tente mais tarde")
				}
				return Actor{}, err
			}
		}
	}
	u, err := a.db.UpsertGoogleUser(ctx, g, auto)
	if err != nil {
		return Actor{}, err
	}
	return a.activate(ctx, u)
}

// activate checks the account state and returns its actor, creating the
// ledger on the first sign-in of an approved account.
func (a *App) activate(ctx context.Context, u db.User) (Actor, error) {
	if u.Suspended {
		return Actor{}, invalid("esta conta está suspensa; fale com o suporte")
	}
	if !u.Approved {
		Audit("signup_pending", "user_id", u.ID)
		return Actor{User: u}, ErrPending
	}
	actor := Actor{User: u, Source: SourceManual, AuthAt: a.now()}
	id, ok, err := a.db.UserLedger(ctx, u.ID)
	if err == nil && !ok {
		id, ok, err = a.joinInvitedLedger(ctx, u)
	}
	if err == nil && !ok {
		id, ok, err = a.db.ClaimLedger(ctx, u.ID, u.Email)
	}
	if err == nil && !ok {
		name := "Minha carteira"
		if first, _, _ := strings.Cut(strings.TrimSpace(u.Name), " "); first != "" {
			name = "Carteira de " + first
		}
		if id, err = a.db.CreateLedger(ctx, name); err == nil {
			err = a.db.AddMember(ctx, id, u.ID)
		}
	}
	actor.LedgerID = id
	return actor, err
}

// newSecret returns a random URL-safe token and the hash that is stored.
func newSecret() (token string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, hashSecret(token), nil
}

func hashSecret(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// StartSession creates a login session and returns the cookie value.
func (a *App) StartSession(ctx context.Context, userID int64, ip, userAgent string) (string, error) {
	token, hash, err := newSecret()
	if err != nil {
		return "", err
	}
	now := a.now()
	return token, a.db.CreateSession(ctx, userID, hash, now, now.Add(SessionTTL), ip, userAgent)
}

// ResumeSession returns the actor of a live session (ok=false when the token
// is unknown, revoked, expired or idle for too long).
func (a *App) ResumeSession(ctx context.Context, token string) (Actor, bool, error) {
	if token == "" {
		return Actor{}, false, nil
	}
	now := a.now()
	s, ok, err := a.db.LiveSession(ctx, hashSecret(token), now, now.Add(-SessionIdle))
	if err != nil || !ok {
		return Actor{}, false, err
	}
	if now.Sub(s.LastSeenAt) > sessionTouchEvery {
		if err := a.db.TouchSession(ctx, s.ID, now); err != nil {
			return Actor{}, false, err
		}
	}
	actor, ok, err := a.actor(ctx, s.UserID, SourceManual)
	actor.AuthAt = s.CreatedAt
	return actor, ok, err
}

// actor loads the user on every request, so suspension, approval and role
// changes apply at once.
func (a *App) actor(ctx context.Context, userID int64, source string) (Actor, bool, error) {
	u, err := a.db.User(ctx, userID)
	if err != nil || u.Suspended || !u.Approved {
		return Actor{}, false, err
	}
	id, ok, err := a.db.UserLedger(ctx, userID)
	if err != nil || !ok {
		return Actor{}, false, err
	}
	return Actor{User: u, LedgerID: id, Source: source}, true, nil
}

// EndSession revokes the session (logout).
func (a *App) EndSession(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return a.db.RevokeSession(ctx, hashSecret(token))
}

// CreateMCPToken creates a secret MCP link for the actor and returns the
// token. It is shown once: only its hash is stored.
func (a *App) CreateMCPToken(ctx context.Context, actor Actor, label string) (string, error) {
	label = strings.TrimSpace(label)
	if len([]rune(label)) > 60 {
		return "", invalid("nome do link muito longo (máximo 60 caracteres)")
	}
	if label == "" {
		label = "Claude"
	}
	ts, err := a.db.MCPTokens(ctx, actor.User.ID, actor.LedgerID)
	if err != nil {
		return "", err
	}
	if len(ts) >= maxMCPTokens {
		return "", invalid("limite de %d links atingido; revogue um antes de criar outro", maxMCPTokens)
	}
	token, hash, err := newSecret()
	if err != nil {
		return "", err
	}
	id, err := a.db.CreateMCPToken(ctx, actor.User.ID, actor.LedgerID, hash, label)
	if err == nil {
		Audit("mcp_link_created", "user_id", actor.User.ID, "link_id", id)
	}
	return token, err
}

func (a *App) MCPTokens(ctx context.Context, actor Actor) ([]db.MCPToken, error) {
	return a.db.MCPTokens(ctx, actor.User.ID, actor.LedgerID)
}

func (a *App) RevokeMCPToken(ctx context.Context, actor Actor, id int64) error {
	ok, err := a.db.RevokeMCPToken(ctx, actor.User.ID, id)
	if err == nil && !ok {
		Audit("authz_denied", "user_id", actor.User.ID, "action", "revoke_mcp_link", "link_id", id)
		return invalid("link não encontrado")
	}
	if err == nil {
		Audit("mcp_link_revoked", "user_id", actor.User.ID, "link_id", id)
	}
	return err
}

// MCPActor resolves the token of an MCP link (ok=false when unknown or revoked).
func (a *App) MCPActor(ctx context.Context, token string) (Actor, bool, error) {
	if token == "" {
		return Actor{}, false, nil
	}
	userID, ledgerID, ok, err := a.db.MCPTokenOwner(ctx, hashSecret(token))
	if err != nil || !ok {
		return Actor{}, false, err
	}
	u, err := a.db.User(ctx, userID)
	if err != nil || u.Suspended || !u.Approved {
		return Actor{}, false, err
	}
	return Actor{User: u, LedgerID: ledgerID, Source: SourceMCP}, true, nil
}

// DeleteAccount erases the actor's account (LGPD art. 18, VI): their ledger
// with all its financial data when nobody else shares it, their sessions and
// MCP links. confirm must be the account's email, typed by the user.
func (a *App) DeleteAccount(ctx context.Context, actor Actor, confirm string) error {
	if !strings.EqualFold(strings.TrimSpace(confirm), actor.User.Email) {
		return invalid("digite o seu e-mail exatamente para confirmar a exclusão")
	}
	ledger, err := a.db.DeleteUser(ctx, actor.User.ID)
	if err != nil {
		return err
	}
	Audit("account_deleted", "user_id", actor.User.ID, "ledger_deleted", ledger)
	return nil
}

// SetSuspended suspends or reactivates an account (operators, via the CLI).
func (a *App) SetSuspended(ctx context.Context, idOrEmail string, suspended bool) (db.User, error) {
	u, err := a.db.FindUser(ctx, idOrEmail)
	if err != nil {
		return u, notFound(err, "conta")
	}
	if err := a.db.SetSuspended(ctx, u.ID, suspended); err != nil {
		return u, err
	}
	ev := "account_reactivated"
	if suspended {
		ev = "account_suspended"
	}
	Audit(ev, "user_id", u.ID, "by", "operator-cli")
	u.Suspended = suspended
	return u, nil
}

// DeleteUserByOperator erases an account on request received outside the app
// (e.g. an LGPD request by email), after the operator verified the requester.
func (a *App) DeleteUserByOperator(ctx context.Context, idOrEmail string) (db.User, error) {
	u, err := a.db.FindUser(ctx, idOrEmail)
	if err != nil {
		return u, notFound(err, "conta")
	}
	ledger, err := a.db.DeleteUser(ctx, u.ID)
	if err == nil {
		Audit("account_deleted", "user_id", u.ID, "ledger_deleted", ledger, "by", "operator-cli")
	}
	return u, err
}
