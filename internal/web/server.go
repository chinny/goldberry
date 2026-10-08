// Package web is Goldberry's HTTP layer: server-rendered html/template pages,
// HTMX for the few live bits, role middleware and CSRF (plan §11). Handlers
// call the service layer and never the store.
package web

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/chinny/goldberry/internal/auth"
	"github.com/chinny/goldberry/internal/config"
	"github.com/chinny/goldberry/internal/money"
	"github.com/chinny/goldberry/internal/service"
	"github.com/chinny/goldberry/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

const (
	sessionCookie = "gb_session"
	preCookie     = "gb_pre"
	flashCookie   = "gb_flash"
	csrfField     = "_csrf"
	csrfHeader    = "X-CSRF-Token"
)

// Server serves the Goldberry web app.
type Server struct {
	svc        *service.Service
	cfg        config.Config
	log        *slog.Logger
	setupToken string
	setUp      atomic.Bool
	csrfKey    []byte
	pages      map[string]*template.Template
	handler    http.Handler
	registry   *prometheus.Registry
	requests   *prometheus.CounterVec
	latency    *prometheus.HistogramVec
}

// New builds the server. setupToken guards /setup until the first admin exists.
func New(ctx context.Context, svc *service.Service, cfg config.Config, log *slog.Logger, setupToken string) (*Server, error) {
	s := &Server{svc: svc, cfg: cfg, log: log, setupToken: setupToken}
	var err error
	if s.csrfKey, err = loadCSRFKey(ctx, svc.Store); err != nil {
		return nil, fmt.Errorf("csrf key: %w", err)
	}
	if s.pages, err = parseTemplates(); err != nil {
		return nil, err
	}
	s.registry = prometheus.NewRegistry()
	s.registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	s.requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "goldberry_http_requests_total", Help: "HTTP requests by route and status.",
	}, []string{"route", "code"})
	s.latency = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "goldberry_http_request_duration_seconds", Help: "HTTP request latency by route.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5},
	}, []string{"route"})
	s.registry.MustRegister(s.requests, s.latency)
	s.handler = s.middleware(s.routes())
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// loadCSRFKey reads the per-install CSRF key, creating it on first start.
func loadCSRFKey(ctx context.Context, st store.Store) ([]byte, error) {
	const key = "csrf_key"
	v, err := st.GetSetting(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		v = base64.StdEncoding.EncodeToString(b)
		err = st.Tx(ctx, "", func(tx store.Tx) error {
			if existing, err := tx.GetSetting(ctx, key); err == nil {
				v = existing
				return nil
			}
			return tx.PutSetting(ctx, key, v, true)
		})
	}
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(v)
}

func parseTemplates() (map[string]*template.Template, error) {
	pages, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	out := map[string]*template.Template{}
	for _, p := range pages {
		name := strings.TrimSuffix(strings.TrimPrefix(p, "templates/"), ".html")
		if name == "layout" || name == "partials" {
			continue
		}
		t, err := template.New(name).Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/partials.html", p)
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", name, err)
		}
		out[name] = t
	}
	return out, nil
}

// --- request context ---------------------------------------------------------

type reqCtx struct {
	User    *store.User
	Session store.Session
	House   store.Household
	Cur     money.Currency
	CSRF    string
	token   string
}

type ctxKey struct{}

func rc(r *http.Request) *reqCtx {
	if c, ok := r.Context().Value(ctxKey{}).(*reqCtx); ok {
		return c
	}
	return &reqCtx{}
}

// --- middleware ----------------------------------------------------------------

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int)        { w.code = code; w.ResponseWriter.WriteHeader(code) }
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic", "path", r.URL.Path, "panic", rec)
				http.Error(sw, "Something went wrong.", http.StatusInternalServerError)
			}
			route := r.Pattern
			if route == "" {
				route = "unmatched"
			}
			s.requests.WithLabelValues(route, fmt.Sprint(sw.code)).Inc()
			s.latency.WithLabelValues(route).Observe(time.Since(start).Seconds())
			level := slog.LevelInfo
			if strings.HasPrefix(r.URL.Path, "/static/") || r.URL.Path == "/healthz" || r.URL.Path == "/readyz" ||
				r.URL.Path == "/metrics" || r.URL.Path == "/notifications/count" {
				level = slog.LevelDebug
			}
			s.log.Log(r.Context(), level, "http", "method", r.Method, "path", r.URL.Path, "status", sw.code,
				"dur_ms", time.Since(start).Milliseconds(), "ip", s.clientIP(r))
		}()

		h := sw.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; "+
			"form-action 'self'; frame-ancestors 'none'; base-uri 'none'; object-src 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")

		if isOpenPath(r.URL.Path) {
			next.ServeHTTP(sw, r)
			return
		}

		c := &reqCtx{}
		ctx := r.Context()
		if !s.setUp.Load() {
			need, err := s.svc.NeedsSetup(ctx)
			if err != nil {
				s.serverError(sw, r, err)
				return
			}
			if need {
				if r.URL.Path != "/setup" {
					http.Redirect(sw, r, "/setup", http.StatusSeeOther)
					return
				}
			} else {
				s.setUp.Store(true)
			}
		}
		if h, err := s.svc.Store.GetHousehold(ctx); err == nil {
			c.House, c.Cur = h, money.MustLookup(h.Currency)
		} else if !errors.Is(err, store.ErrNotFound) {
			s.serverError(sw, r, err)
			return
		}

		binding := ""
		if ck, err := r.Cookie(sessionCookie); err == nil && ck.Value != "" {
			if u, sess, err := s.svc.Authenticate(ctx, ck.Value); err == nil {
				c.User, c.Session, c.token, binding = &u, sess, ck.Value, ck.Value
			} else {
				s.clearCookie(sw, sessionCookie)
			}
		}
		if binding == "" {
			if ck, err := r.Cookie(preCookie); err == nil && len(ck.Value) >= 32 {
				binding = ck.Value
			} else if r.Method == http.MethodGet {
				binding = auth.NewToken()
				http.SetCookie(sw, s.cookie(preCookie, binding, 0))
			}
		}
		if binding != "" {
			c.CSRF = auth.CSRFToken(s.csrfKey, binding)
		}

		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			tok := r.Header.Get(csrfHeader)
			if tok == "" {
				tok = r.PostFormValue(csrfField)
			}
			if !auth.VerifyCSRF(s.csrfKey, binding, tok) {
				s.renderStatus(sw, r.WithContext(context.WithValue(ctx, ctxKey{}, c)), http.StatusForbidden,
					"That form expired", "Go back, reload the page and try again.")
				return
			}
		}
		next.ServeHTTP(sw, r.WithContext(context.WithValue(ctx, ctxKey{}, c)))
	})
}

func isOpenPath(p string) bool {
	return strings.HasPrefix(p, "/static/") || p == "/healthz" || p == "/readyz" || p == "/metrics" || p == "/favicon.ico"
}

func (s *Server) cookie(name, value string, maxAge time.Duration) *http.Cookie {
	//nolint:gosec // Secure is on unless the operator opts out with GOLDBERRY_INSECURE_COOKIES
	c := &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: !s.cfg.InsecureCookies, SameSite: http.SameSiteLaxMode}
	if maxAge > 0 {
		c.MaxAge = int(maxAge / time.Second)
	}
	return c
}

func (s *Server) clearCookie(w http.ResponseWriter, name string) {
	c := s.cookie(name, "", 0) //nolint:gosec // see cookie
	c.MaxAge = -1
	http.SetCookie(w, c)
}

// clientIP honours X-Forwarded-For only from GOLDBERRY_TRUSTED_PROXIES.
func (s *Server) clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		host = ap.Addr().String()
		trusted := false
		for _, p := range s.cfg.TrustedProxies {
			if p.Contains(ap.Addr()) {
				trusted = true
				break
			}
		}
		if trusted {
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				parts := strings.Split(xff, ",")
				return strings.TrimSpace(parts[len(parts)-1])
			}
		}
	}
	return host
}

// --- role wrappers -------------------------------------------------------------

type handler func(w http.ResponseWriter, r *http.Request, c *reqCtx)

func (s *Server) signedIn(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c := rc(r)
		if c.User == nil {
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		h(w, r, c)
	}
}

func (s *Server) admin(h handler) http.HandlerFunc {
	return s.signedIn(func(w http.ResponseWriter, r *http.Request, c *reqCtx) {
		if !c.User.IsAdmin() {
			s.renderStatus(w, r, http.StatusForbidden, "That’s for grown-ups", "Ask a parent if you need something here.")
			return
		}
		h(w, r, c)
	})
}

func (s *Server) kid(h handler) http.HandlerFunc {
	return s.signedIn(func(w http.ResponseWriter, r *http.Request, c *reqCtx) {
		if !c.User.IsKid() {
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
			return
		}
		h(w, r, c)
	})
}

// --- flash & redirects -----------------------------------------------------------

func (s *Server) flash(w http.ResponseWriter, kind, msg string) {
	http.SetCookie(w, s.cookie(flashCookie, base64.RawURLEncoding.EncodeToString([]byte(kind+"|"+msg)), time.Minute))
}

func (s *Server) takeFlash(w http.ResponseWriter, r *http.Request) (kind, msg string) {
	ck, err := r.Cookie(flashCookie)
	if err != nil {
		return "", ""
	}
	s.clearCookie(w, flashCookie)
	b, err := base64.RawURLEncoding.DecodeString(ck.Value)
	if err != nil {
		return "", ""
	}
	kind, msg, _ = strings.Cut(string(b), "|")
	return kind, msg
}

// safeNext returns a same-site path to go back to, or def.
func safeNext(next, def string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return def
	}
	return next
}

// redirectTo sends the browser to a same-site path built from request data
// (like a kid ID from the URL); anything else goes to "/".
func (s *Server) redirectTo(w http.ResponseWriter, r *http.Request, path string) {
	http.Redirect(w, r, safeNext(path, "/"), http.StatusSeeOther) //nolint:gosec // safeNext allows same-site paths only
}

// back redirects to the form's "next" field (or def) with a flash message.
func (s *Server) back(w http.ResponseWriter, r *http.Request, def, kind, msg string) {
	if msg != "" {
		s.flash(w, kind, msg)
	}
	http.Redirect(w, r, safeNext(r.PostFormValue("next"), def), http.StatusSeeOther) //nolint:gosec // safeNext allows same-site paths only
}

// userMessage returns a safe message for expected errors, or "".
func userMessage(err error) string {
	var ue *service.UserError
	var ins *service.ErrInsufficient
	var done *service.ErrAlreadyDecided
	var th *service.ThrottledError
	var locked *service.ErrJarLocked
	var needs *service.ErrNeedsOverride
	switch {
	case errors.As(err, &ue), errors.As(err, &ins), errors.As(err, &done), errors.As(err, &th),
		errors.As(err, &locked), errors.As(err, &needs):
		return err.Error()
	case errors.Is(err, service.ErrTooManyPending), errors.Is(err, service.ErrCannotReverse),
		errors.Is(err, service.ErrCannotReverseMove), errors.Is(err, service.ErrOverrideTooSoon),
		errors.Is(err, service.ErrOverrideInvalid),
		errors.Is(err, service.ErrBadCredentials), errors.Is(err, service.ErrLocked), errors.Is(err, service.ErrForbidden):
		msg := err.Error()
		return strings.ToUpper(msg[:1]) + msg[1:] + "."
	}
	return ""
}

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request failed", "path", r.URL.Path, "err", err)
	s.renderStatus(w, r, http.StatusInternalServerError, "Something went wrong", "It’s been logged. Try again in a moment.")
}

// fail handles a service error from an action: expected ones go back with a
// message; not-found is a 404; anything else is a 500.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, def string, err error) {
	if msg := userMessage(err); msg != "" {
		s.back(w, r, def, "err", msg)
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	s.serverError(w, r, err)
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.renderStatus(w, r, http.StatusNotFound, "Nothing here", "That page doesn’t exist, or it isn’t yours to see.")
}
