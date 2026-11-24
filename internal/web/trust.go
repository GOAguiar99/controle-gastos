package web

import (
	"net/http"

	"controle-gastos/internal/app"
)

// trustPage is "Seus dados e segurança": how data is protected, who can see
// it, export, deletion and the inactivity rule. Public, so people can read it
// before signing up; after sign-in, users confirm each version once.
func (s *Server) trustPage(w http.ResponseWriter, r *http.Request) {
	data := page{"Title": "Seus dados e segurança", "Version": app.TrustVersion,
		"InactivityMonths": app.InactivityMonths, "PolicyStart": app.InactivityPolicyStart,
		"FirstPurge": app.InactivityPolicyStart.AddDate(0, app.InactivityMonths, 0)}
	if svcOf(r) != nil {
		data["NeedsTrust"] = app.NeedsTrust(actorOf(r))
	}
	s.render(w, r, "trust", data)
}

// acceptTrust records that the user read the current version and goes on to
// the dashboard.
func (s *Server) acceptTrust(w http.ResponseWriter, r *http.Request) {
	if err := s.app.AcceptTrust(r.Context(), actorOf(r)); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
