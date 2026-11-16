package web

import (
	"crypto/subtle"
	"net/http"
	"net/url"
)

// mcpGate serves the MCP endpoint only at /mcp/{token} with the current
// secret; anything else is a plain 404, so the URL's existence leaks nothing.
func (s *Server) mcpGate(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want, err := s.svc.MCPToken()
		got := r.PathValue("token")
		if err != nil || got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

// mcpURL is the full connector URL as seen from the browser.
func (s *Server) mcpURL(r *http.Request) (string, error) {
	token, err := s.svc.MCPToken()
	if err != nil {
		return "", err
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + host + "/mcp/" + token, nil
}

func (s *Server) rotateMCP(w http.ResponseWriter, r *http.Request) {
	if _, err := s.svc.RotateMCPToken(); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/config?msg="+url.QueryEscape("Novo link do Claude gerado — o anterior parou de funcionar")+"#claude", http.StatusSeeOther)
}
