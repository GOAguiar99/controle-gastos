package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"controle-gastos/internal/app"
	"controle-gastos/internal/db"
)

// GoogleConfig configures "Entrar com Google" (OpenID Connect, authorization
// code flow with PKCE).
type GoogleConfig struct {
	ClientID, ClientSecret string
	BaseURL                string // public URL of the app, e.g. https://gastos.example.com
	Issuer                 string // default https://accounts.google.com (tests use a fake)
}

// Config is the web server's security configuration.
type Config struct {
	Google *GoogleConfig // nil in password mode or with only DevLogin
	// Password enables email + password sign-in (APP_AUTH=password) for
	// self-hosted installs without Google. Accounts are created by the CLI.
	Password bool
	// BaseURL is the public URL (connector links); empty = from the request.
	BaseURL string
	// DevLogin adds a "sign in as dev" button with no Google account. Local
	// development only: main refuses it unless the server listens on loopback.
	DevLogin bool
	// SecureCookies sets Secure and the __Host- prefix on cookies (HTTPS).
	SecureCookies bool
	// Secret signs the short-lived OAuth state cookie (32+ bytes).
	Secret []byte
	// ControllerName and ContactEmail identify the data controller and the
	// channel for data-subject requests on the privacy and terms pages.
	ControllerName, ContactEmail string
}

type googleAuth struct {
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
}

func newGoogleAuth(ctx context.Context, c *GoogleConfig) (*googleAuth, error) {
	issuer := c.Issuer
	if issuer == "" {
		issuer = "https://accounts.google.com"
	}
	p, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, err
	}
	return &googleAuth{
		oauth: oauth2.Config{
			ClientID: c.ClientID, ClientSecret: c.ClientSecret, Endpoint: p.Endpoint(),
			RedirectURL: strings.TrimRight(c.BaseURL, "/") + "/auth/google/callback",
			Scopes:      []string{oidc.ScopeOpenID, "email", "profile"},
		},
		verifier: p.Verifier(&oidc.Config{ClientID: c.ClientID}),
	}, nil
}

const (
	oauthCookie  = "cg_oauth"
	oauthMaxAge  = 10 * time.Minute
	actorCtxKey  = ctxKey("actor")
	svcCtxKey    = ctxKey("svc")
	sessionValue = "cg_session"
)

type ctxKey string

func (s *Server) sessionCookie() string {
	if s.cfg.SecureCookies {
		return "__Host-" + sessionValue
	}
	return sessionValue
}

func (s *Server) setCookie(w http.ResponseWriter, name, value string, maxAge time.Duration) {
	c := &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode}
	if maxAge < 0 {
		c.MaxAge = -1
	} else {
		c.MaxAge = int(maxAge.Seconds())
	}
	http.SetCookie(w, c)
}

// oauthState travels in a signed cookie between /auth/google and the callback.
type oauthState struct {
	State, Nonce, Verifier string
	Exp                    int64
}

func randomString() string { return rand.Text() }

func (s *Server) sign(b []byte) string {
	m := hmac.New(sha256.New, s.cfg.Secret)
	m.Write(b)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (s *Server) googleStart(w http.ResponseWriter, r *http.Request) {
	st := oauthState{State: randomString(), Nonce: randomString(), Verifier: oauth2.GenerateVerifier(),
		Exp: time.Now().Add(oauthMaxAge).Unix()}
	b, _ := json.Marshal(st)
	s.setCookie(w, oauthCookie, base64.RawURLEncoding.EncodeToString(b)+"."+s.sign(b), oauthMaxAge)
	url := s.google.oauth.AuthCodeURL(st.State, oidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier),
		oauth2.SetAuthURLParam("prompt", "select_account"))
	http.Redirect(w, r, url, http.StatusFound)
}

func (s *Server) readOAuthState(r *http.Request) (oauthState, bool) {
	var st oauthState
	c, err := r.Cookie(oauthCookie)
	if err != nil {
		return st, false
	}
	payload, sig, ok := strings.Cut(c.Value, ".")
	b, err := base64.RawURLEncoding.DecodeString(payload)
	if !ok || err != nil || !hmac.Equal([]byte(sig), []byte(s.sign(b))) || json.Unmarshal(b, &st) != nil {
		return st, false
	}
	return st, time.Now().Unix() < st.Exp
}

var errLogin = errors.New("não foi possível entrar com o Google; tente de novo")

func (s *Server) googleCallback(w http.ResponseWriter, r *http.Request) {
	st, ok := s.readOAuthState(r)
	s.setCookie(w, oauthCookie, "", -1) // single use
	q := r.URL.Query()
	if !ok || q.Get("error") != "" || !hmac.Equal([]byte(q.Get("state")), []byte(st.State)) {
		s.loginFailed(w, r, errLogin)
		return
	}
	g, err := s.googleIdentity(r.Context(), q.Get("code"), st)
	if err != nil {
		s.loginFailed(w, r, err)
		return
	}
	s.signIn(w, r, g)
}

// googleIdentity exchanges the code and verifies the ID token: signature,
// issuer, audience, expiry and our nonce.
func (s *Server) googleIdentity(ctx context.Context, code string, st oauthState) (db.GoogleUser, error) {
	tok, err := s.google.oauth.Exchange(ctx, code, oauth2.VerifierOption(st.Verifier))
	if err != nil {
		return db.GoogleUser{}, errLogin
	}
	raw, _ := tok.Extra("id_token").(string)
	idt, err := s.google.verifier.Verify(ctx, raw)
	if err != nil || !hmac.Equal([]byte(idt.Nonce), []byte(st.Nonce)) {
		return db.GoogleUser{}, errLogin
	}
	var c struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		Picture       string `json:"picture"`
	}
	if err := idt.Claims(&c); err != nil {
		return db.GoogleUser{}, errLogin
	}
	return db.GoogleUser{Sub: idt.Subject, Email: c.Email, EmailVerified: c.EmailVerified, Name: c.Name, Picture: c.Picture}, nil
}

// devLogin signs in a fixed local user (Config.DevLogin only).
func (s *Server) devLogin(w http.ResponseWriter, r *http.Request) {
	s.signIn(w, r, db.GoogleUser{Sub: "dev", Email: "dev@localhost", EmailVerified: true, Name: "Dev"})
}

func (s *Server) signIn(w http.ResponseWriter, r *http.Request, g db.GoogleUser) {
	actor, err := s.app.SignIn(r.Context(), g)
	s.startSession(w, r, actor, err)
}

// passwordLogin is the self-hosted sign-in (Config.Password).
func (s *Server) passwordLogin(w http.ResponseWriter, r *http.Request) {
	actor, err := s.app.PasswordSignIn(r.Context(), r.FormValue("email"), r.FormValue("password"), clientIP(r))
	s.startSession(w, r, actor, err)
}

// startSession finishes any sign-in: a session for an active account, the
// "waiting for approval" page (no session) for a pending one.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, actor app.Actor, err error) {
	if errors.Is(err, app.ErrPending) {
		s.render(w, r, "pending", page{"Title": "Aguardando aprovação", "Email": actor.User.Email})
		return
	}
	if err != nil {
		s.loginFailed(w, r, err)
		return
	}
	token, err := s.app.StartSession(r.Context(), actor.User.ID, clientIP(r), r.UserAgent())
	if err != nil {
		s.fail(w, err)
		return
	}
	s.setCookie(w, s.sessionCookie(), token, app.SessionTTL)
	Audit("login_success", "user_id", actor.User.ID, "ip", clientIP(r))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) loginFailed(w http.ResponseWriter, r *http.Request, err error) {
	msg := errLogin.Error()
	reason := "error"
	if app.IsValidation(err) {
		msg, reason = err.Error(), "refused"
	} else if errors.Is(err, errLogin) {
		msg, reason = err.Error(), "invalid_oauth_response"
	} else {
		logErr(err)
	}
	Audit("login_failure", "reason", reason, "ip", clientIP(r))
	w.WriteHeader(http.StatusForbidden)
	s.render(w, r, "login", page{"Title": "Entrar", "Error": msg})
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.currentActor(r); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, r, "login", page{"Title": "Entrar"})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(s.sessionCookie()); err == nil {
		if err := s.app.EndSession(r.Context(), c.Value); err != nil {
			logErr(err)
		}
		Audit("logout", "ip", clientIP(r))
	}
	s.setCookie(w, s.sessionCookie(), "", -1)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// currentActor resolves the session cookie.
func (s *Server) currentActor(r *http.Request) (app.Actor, bool) {
	c, err := r.Cookie(s.sessionCookie())
	if err != nil {
		return app.Actor{}, false
	}
	a, ok, err := s.app.ResumeSession(r.Context(), c.Value)
	if err != nil {
		logErr(err)
	}
	return a, ok
}

// actorOf is the signed-in user of the request (set by the middleware).
func actorOf(r *http.Request) app.Actor {
	a, _ := r.Context().Value(actorCtxKey).(app.Actor)
	return a
}

// svcOf is the Service of the request's ledger (set by the middleware or the
// MCP gate). Every handler that reads or writes data goes through it.
func svcOf(r *http.Request) *app.Service { return svcFrom(r.Context()) }

func svcFrom(ctx context.Context) *app.Service {
	s, _ := ctx.Value(svcCtxKey).(*app.Service)
	return s
}

func (s *Server) withActor(r *http.Request, a app.Actor) *http.Request {
	ctx := context.WithValue(r.Context(), actorCtxKey, a)
	ctx = context.WithValue(ctx, svcCtxKey, s.app.For(ctx, a))
	return r.WithContext(ctx)
}

func clientIP(r *http.Request) string {
	// The app listens on loopback behind Caddy, which sets X-Forwarded-For;
	// only then is the header trustworthy.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			return strings.TrimSpace(first)
		}
	}
	return host
}

// maxBodyBytes caps form posts (the MCP handler has its own 1 MiB cap).
const maxBodyBytes = 1 << 20

// middleware sets security headers, limits request size and rate, rejects
// cross-origin state changes and requires a session everywhere but the public
// pages.
func (s *Server) middleware(next http.Handler) http.Handler {
	cop := http.NewCrossOriginProtection()
	guarded := cop.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		public := path == "/login" || path == "/healthz" || path == "/privacidade" || path == "/termos" || path == "/como-usar" ||
			strings.HasPrefix(path, "/static/") || strings.HasPrefix(path, "/auth/")
		if strings.HasPrefix(path, "/auth/") && !s.authLimit.allow(clientIP(r)) {
			Audit("rate_limited", "route", "auth", "ip", clientIP(r))
			tooMany(w)
			return
		}
		if public {
			next.ServeHTTP(w, r)
			return
		}
		a, ok := s.currentActor(r)
		if !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		// financial pages must not be kept in shared or back/forward caches
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, s.withActor(r, a))
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: https://*.googleusercontent.com; "+
			"style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors 'none'; form-action 'self' https://accounts.google.com; base-uri 'none'; object-src 'none'")
		if s.cfg.SecureCookies {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		// /mcp/{token} carries its own secret (checked in mcpGate) and is
		// called by Claude from other origins.
		if strings.HasPrefix(r.URL.Path, "/mcp/") {
			// per link: Claude's servers share IPs across users
			if !s.mcpLimit.allow(r.URL.Path) {
				tooMany(w)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		guarded.ServeHTTP(w, r)
	})
}

// ValidateBaseURL checks APP_BASE_URL: https, or plain http only on this
// machine (localhost/127.0.0.1/::1), since session cookies and passwords
// must never cross a network unencrypted. It reports whether it is https.
func ValidateBaseURL(raw string) (secure bool, err error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.User != nil {
		return false, errors.New("APP_BASE_URL inválida: use só esquema e host, ex.: https://gastos.exemplo.com")
	}
	switch u.Scheme {
	case "https":
		return true, nil
	case "http":
		if h := u.Hostname(); h == "localhost" || h == "127.0.0.1" || h == "::1" {
			return false, nil
		}
	}
	return false, errors.New("APP_BASE_URL precisa ser https (http só é aceito para localhost)")
}
