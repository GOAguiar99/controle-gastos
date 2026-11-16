package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	cookieName = "cg_session"
	sessionTTL = 30 * 24 * time.Hour
)

// Auth is a single-password login with an HMAC-signed session cookie.
type Auth struct {
	PasswordHash []byte // bcrypt; empty = auth disabled
	Secret       []byte

	mu       sync.Mutex
	failures map[string][]time.Time
}

func (a *Auth) Enabled() bool { return len(a.PasswordHash) > 0 }

func (a *Auth) sign(payload string) string {
	m := hmac.New(sha256.New, a.Secret)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (a *Auth) newSession(w http.ResponseWriter, r *http.Request) {
	exp := strconv.FormatInt(time.Now().Add(sessionTTL).Unix(), 10)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: exp + "." + a.sign(exp), Path: "/",
		MaxAge: int(sessionTTL.Seconds()), HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
}

func (a *Auth) clearSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode})
}

func (a *Auth) valid(r *http.Request) bool {
	if !a.Enabled() {
		return true
	}
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	exp, sig, ok := strings.Cut(c.Value, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(a.sign(exp))) {
		return false
	}
	n, err := strconv.ParseInt(exp, 10, 64)
	return err == nil && time.Now().Unix() < n
}

// check verifies the password with a simple per-IP lockout (5 failures / 15 min).
func (a *Auth) check(r *http.Request, password string) (ok, locked bool) {
	ip := clientIP(r)
	a.mu.Lock()
	if a.failures == nil {
		a.failures = map[string][]time.Time{}
	}
	recent := a.failures[ip][:0]
	for _, t := range a.failures[ip] {
		if time.Since(t) < 15*time.Minute {
			recent = append(recent, t)
		}
	}
	a.failures[ip] = recent
	if len(recent) >= 5 {
		a.mu.Unlock()
		return false, true
	}
	a.mu.Unlock()

	if bcrypt.CompareHashAndPassword(a.PasswordHash, []byte(password)) == nil {
		a.mu.Lock()
		delete(a.failures, ip)
		a.mu.Unlock()
		return true, false
	}
	a.mu.Lock()
	a.failures[ip] = append(a.failures[ip], time.Now())
	a.mu.Unlock()
	return false, false
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func clientIP(r *http.Request) string {
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return ip
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ip, _, _ := strings.Cut(xff, ",")
		return strings.TrimSpace(ip)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// middleware enforces login and a same-origin check on state-changing requests.
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		path := r.URL.Path
		// /mcp/{token} carries its own secret (checked in mcpGate) and is
		// called by Claude from other origins, so it skips login and the
		// same-origin check.
		if strings.HasPrefix(path, "/mcp/") {
			next.ServeHTTP(w, r)
			return
		}
		public := path == "/login" || path == "/healthz" || strings.HasPrefix(path, "/static/")
		if r.Method == http.MethodPost {
			if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r) {
				http.Error(w, "origem inválida", http.StatusForbidden)
				return
			}
		}
		if !public && !s.auth.valid(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(origin string, r *http.Request) bool {
	o := strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return o == host
}
