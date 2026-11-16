package web

import (
	"errors"
	"net/http"
	"net/url"

	"controle-gastos/internal/app"
)

// adminPage lists accounts for an admin: identity and status, no financial
// data. Everyone else gets a plain 404.
func (s *Server) adminPage(w http.ResponseWriter, r *http.Request) {
	users, err := s.app.AdminUsers(r.Context(), actorOf(r))
	if s.adminDenied(w, r, err) {
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, r, "admin", page{"Title": "Admin", "Users": users, "Me": actorOf(r).User.ID})
}

func (s *Server) adminAction(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a, ctx := actorOf(r), r.Context()
	var msg string
	switch r.PathValue("action") {
	case "aprovar":
		err, msg = s.app.Approve(ctx, a, id), "Cadastro aprovado. A carteira é criada no primeiro login."
	case "rejeitar":
		err, msg = s.app.Reject(ctx, a, id), "Cadastro recusado e apagado."
	case "suspender":
		err, msg = s.app.Suspend(ctx, a, id), "Conta suspensa."
	case "reativar":
		err, msg = s.app.Reactivate(ctx, a, id), "Conta reativada."
	default:
		http.NotFound(w, r)
		return
	}
	if s.adminDenied(w, r, err) {
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

// adminDenied answers the admin authorization errors: 404 for non-admins,
// and a forced re-login when the admin's sign-in is too old.
func (s *Server) adminDenied(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case errors.Is(err, app.ErrForbidden):
		http.NotFound(w, r)
		return true
	case errors.Is(err, app.ErrReauth):
		if c, cerr := r.Cookie(s.sessionCookie()); cerr == nil {
			if err := s.app.EndSession(r.Context(), c.Value); err != nil {
				logErr(err)
			}
		}
		s.setCookie(w, s.sessionCookie(), "", -1)
		http.Redirect(w, r, "/login?msg="+url.QueryEscape("Por segurança, entre de novo para usar o Admin."), http.StatusSeeOther)
		return true
	}
	return false
}
