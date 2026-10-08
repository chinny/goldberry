package web

import (
	"context"
	"io/fs"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	static, _ := fs.Sub(staticFS, "static")
	files := http.FileServerFS(static)
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheForever(files)))
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/static/icon.svg", http.StatusMovedPermanently)
	})

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.registry, promhttp.HandlerOpts{}))

	mux.HandleFunc("GET /setup", s.setupPage)
	mux.HandleFunc("POST /setup", s.setupSubmit)
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.loginSubmit)
	mux.HandleFunc("POST /logout", s.signedIn(s.logout))

	// Kid routes sit at the root: a kid's home screen is the product.
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /ledger", s.kid(s.kidLedger))
	mux.HandleFunc("GET /requests/new", s.kid(s.kidRequestPage))
	mux.HandleFunc("POST /requests", s.kid(s.kidRequestSubmit))
	mux.HandleFunc("POST /requests/{id}/cancel", s.kid(s.kidRequestCancel))

	mux.HandleFunc("GET /admin", s.admin(s.adminHome))
	mux.HandleFunc("GET /admin/kids/{id}", s.admin(s.adminKid))
	mux.HandleFunc("GET /admin/kids/{id}/funds", s.admin(s.adminFundsPage))
	mux.HandleFunc("POST /admin/kids/{id}/entries", s.admin(s.adminFundsSubmit))
	mux.HandleFunc("POST /admin/entries/{id}/reverse", s.admin(s.adminReverse))
	mux.HandleFunc("POST /admin/requests/{id}/approve", s.admin(s.adminDecide(true)))
	mux.HandleFunc("POST /admin/requests/{id}/deny", s.admin(s.adminDecide(false)))
	mux.HandleFunc("GET /admin/users", s.admin(s.people))
	mux.HandleFunc("GET /admin/users/new", s.admin(s.userNewPage))
	mux.HandleFunc("POST /admin/users", s.admin(s.userNewSubmit))
	mux.HandleFunc("GET /admin/users/{id}", s.admin(s.userPage))
	mux.HandleFunc("POST /admin/users/{id}/secret", s.admin(s.userSecret))
	mux.HandleFunc("POST /admin/users/{id}/unlock", s.admin(s.userUnlock))
	mux.HandleFunc("POST /admin/users/{id}/disable", s.admin(s.userDisable(true)))
	mux.HandleFunc("POST /admin/users/{id}/enable", s.admin(s.userDisable(false)))
	mux.HandleFunc("POST /admin/users/{id}/revoke", s.admin(s.userRevoke))
	mux.HandleFunc("GET /admin/settings", s.admin(s.settingsPage))
	mux.HandleFunc("POST /admin/settings", s.admin(s.settingsSubmit))

	mux.HandleFunc("GET /notifications", s.signedIn(s.notificationsPage))
	mux.HandleFunc("GET /notifications/count", s.signedIn(s.notificationsCount))
	mux.HandleFunc("POST /notifications/read", s.signedIn(s.notificationsRead))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { s.notFound(w, r) })
	return mux
}

func cacheForever(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Assets are embedded in the binary, so they change only with a new
		// build; an hour is long enough to matter and short enough to upgrade.
		w.Header().Set("Cache-Control", "public, max-age=3600")
		h.ServeHTTP(w, r)
	})
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.svc.Store.Ping(ctx); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("ok\n"))
}

// home sends each role to its own home.
func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	c := rc(r)
	switch {
	case c.User == nil:
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	case c.User.IsAdmin():
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	default:
		s.kidHome(w, r, c)
	}
}
