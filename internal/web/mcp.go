package web

import (
	"context"
	"net/http"
	"strings"

	"controle-gastos/internal/app"
)

// ServiceFrom is the Service of the MCP link owner's ledger, for the MCP
// tools; it is set on the request context by mcpGate.
func ServiceFrom(ctx context.Context) *app.Service { return svcFrom(ctx) }

// mcpGate serves the MCP endpoint only at /mcp/{token} for an active link;
// anything else is a plain 404, so the URL's existence leaks nothing.
func (s *Server) mcpGate(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, ok, err := s.app.MCPActor(r.Context(), r.PathValue("token"))
		if err != nil {
			logErr(err)
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, s.withActor(r, a))
	})
}

// mcpURL is the full connector URL for token, as seen from the browser.
func (s *Server) mcpURL(r *http.Request, token string) string {
	if s.cfg.BaseURL != "" {
		return strings.TrimRight(s.cfg.BaseURL, "/") + "/mcp/" + token
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/mcp/" + token
}

// createMCP makes a new link and shows it once: only its hash is kept, so the
// page is rendered directly (never put the token in a redirect URL).
func (s *Server) createMCP(w http.ResponseWriter, r *http.Request) {
	token, err := s.app.CreateMCPToken(r.Context(), actorOf(r), r.FormValue("label"))
	if err != nil {
		s.fail(w, err)
		return
	}
	data, err := s.profileData(r, tabAgents)
	if err != nil {
		s.fail(w, err)
		return
	}
	data["NewMCPURL"] = s.mcpURL(r, token)
	data["Flash"] = "Link criado. Copie agora: ele não será mostrado de novo."
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, "profile", data)
}

func (s *Server) revokeMCP(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		badRequest(w, "id inválido")
		return
	}
	if err := s.app.RevokeMCPToken(r.Context(), actorOf(r), id); err != nil {
		s.fail(w, err)
		return
	}
	profileDone(w, r, tabAgents, "Link revogado: ele parou de funcionar")
}
