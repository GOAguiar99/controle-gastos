package web

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// fakeIDP is a minimal OpenID Connect provider standing in for Google: the
// authorize endpoint approves at once and redirects back with a code, and the
// token endpoint returns an RS256-signed ID token for the configured user.
type fakeIDP struct {
	*httptest.Server
	key      *rsa.PrivateKey
	clientID string

	mu    sync.Mutex
	user  idpUser
	codes map[string]string // code -> nonce
	// tamper, when set, edits the claims before signing (negative tests)
	tamper func(c map[string]any)
}

type idpUser struct {
	Sub, Email, Name string
	Verified         bool
}

func newFakeIDP(t *testing.T) *fakeIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeIDP{key: key, clientID: "client-test", codes: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": p.URL, "authorization_endpoint": p.URL + "/authorize", "token_endpoint": p.URL + "/token",
			"jwks_uri": p.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("client_id") != p.clientID {
			http.Error(w, "bad authorize request", http.StatusBadRequest)
			return
		}
		code := rand.Text()
		p.mu.Lock()
		p.codes[code] = q.Get("nonce")
		p.mu.Unlock()
		back, _ := url.Parse(q.Get("redirect_uri"))
		v := back.Query()
		v.Set("code", code)
		v.Set("state", q.Get("state"))
		back.RawQuery = v.Encode()
		http.Redirect(w, r, back.String(), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		p.mu.Lock()
		nonce, ok := p.codes[r.FormValue("code")]
		delete(p.codes, r.FormValue("code"))
		u, tamper := p.user, p.tamper
		p.mu.Unlock()
		if !ok || r.FormValue("code_verifier") == "" {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		claims := map[string]any{
			"iss": p.URL, "aud": p.clientID, "sub": u.Sub, "email": u.Email, "email_verified": u.Verified,
			"name": u.Name, "nonce": nonce, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
		}
		if tamper != nil {
			tamper(claims)
		}
		sig, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", "k1"))
		raw, err := jwt.Signed(sig).Claims(claims).Serialize()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 3600, "id_token": raw})
	})
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return p
}

func (p *fakeIDP) as(u idpUser, tamper func(map[string]any)) {
	p.mu.Lock()
	p.user, p.tamper = u, tamper
	p.mu.Unlock()
}
