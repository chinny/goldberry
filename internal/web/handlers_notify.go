package web

import (
	"fmt"
	"net/http"
)

func (s *Server) notificationsPage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	ns, err := s.svc.Notifications(r.Context(), *c.User, 100)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v := s.newView(w, r, "Notifications", ns)
	v.Nav = "notifications"
	if c.User.IsAdmin() {
		v.Back = "/admin"
	} else {
		v.Back = "/"
	}
	s.render(w, r, http.StatusOK, "notifications", v)
}

// notificationsCount is the bell badge fragment HTMX polls every 30 s.
func (s *Server) notificationsCount(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	n, err := s.svc.UnreadCount(r.Context(), *c.User)
	if err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if n > 0 {
		fmt.Fprint(w, n) //nolint:gosec // an integer count, not user input
	}
}

func (s *Server) notificationsRead(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	_ = r.ParseForm()
	if err := s.svc.MarkRead(r.Context(), *c.User, r.PostForm["id"]...); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.back(w, r, "/notifications", "", "")
}
