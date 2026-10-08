package web

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/chinny/goldberry/internal/service"
	"github.com/chinny/goldberry/internal/store"
)

type setupData struct {
	Timezone string
}

func (s *Server) setupPage(w http.ResponseWriter, r *http.Request) {
	if s.setUp.Load() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	v := s.newView(w, r, "Set up Goldberry", setupData{Timezone: s.cfg.Timezone})
	v.Form.Set("currency", "USD")
	v.Form.Set("timezone", s.cfg.Timezone)
	s.render(w, r, http.StatusOK, "setup", v)
}

func (s *Server) setupSubmit(w http.ResponseWriter, r *http.Request) {
	if s.setUp.Load() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	f := r.PostForm
	fail := func(msg string) {
		v := s.newView(w, r, "Set up Goldberry", setupData{Timezone: s.cfg.Timezone})
		v.Form, v.Err = f, msg
		v.Form.Del("password")
		v.Form.Del("setup_token")
		s.render(w, r, http.StatusUnprocessableEntity, "setup", v)
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(f.Get("setup_token"))), []byte(s.setupToken)) != 1 {
		fail("That setup token doesn’t match. Copy it from the container log (docker logs goldberry).")
		return
	}
	ctx := r.Context()
	_, err := s.svc.Setup(ctx, service.SetupInput{
		HouseholdName: f.Get("household_name"), Currency: f.Get("currency"), Timezone: f.Get("timezone"),
		Username: f.Get("username"), DisplayName: f.Get("display_name"), Password: f.Get("password"),
	})
	if errors.Is(err, service.ErrAlreadySetUp) {
		s.setUp.Store(true)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err != nil {
		if msg := userMessage(err); msg != "" {
			fail(msg)
			return
		}
		s.serverError(w, r, err)
		return
	}
	s.setUp.Store(true)
	s.log.Info("setup complete", "admin", f.Get("username"))
	res, err := s.svc.Login(ctx, f.Get("username"), f.Get("password"), false, deviceLabel(r))
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.setSession(w, res)
	s.flash(w, "ok", "You’re set up. Now add your kids.")
	http.Redirect(w, r, "/admin/users/new?role=kid", http.StatusSeeOther)
}

type loginData struct {
	Step      string // username | password | pin
	Username  string
	Name      string
	Tone      store.User
	PINLength int
	Next      string
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	c := rc(r)
	if c.User != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	v := s.newView(w, r, "Sign in", loginData{Step: "username", Next: safeNext(r.URL.Query().Get("next"), "")})
	s.render(w, r, http.StatusOK, "login", v)
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	username := strings.TrimSpace(r.PostFormValue("username"))
	d := loginData{Username: username, Next: safeNext(r.PostFormValue("next"), ""), PINLength: rc(r).House.PINLength}
	secretStep := func(status int, msg string) {
		d.Step = "password"
		if u, err := s.svc.Store.GetUserByUsername(ctx, username); err == nil && u.IsKid() && !u.Disabled() {
			d.Step, d.Name, d.Tone = "pin", u.DisplayName, u
		}
		v := s.newView(w, r, "Sign in", d)
		v.Err = msg
		s.render(w, r, status, "login", v)
	}
	if username == "" {
		v := s.newView(w, r, "Sign in", loginData{Step: "username", Next: d.Next})
		v.Err = "Type your username."
		s.render(w, r, http.StatusUnprocessableEntity, "login", v)
		return
	}
	secret, hasSecret := r.PostForm["secret"]
	if !hasSecret {
		secretStep(http.StatusOK, "")
		return
	}
	res, err := s.svc.Login(ctx, username, strings.TrimSpace(secret[0]), r.PostFormValue("remember") == "1", deviceLabel(r))
	if err != nil {
		if msg := userMessage(err); msg != "" {
			secretStep(http.StatusUnauthorized, msg)
			return
		}
		s.serverError(w, r, err)
		return
	}
	s.setSession(w, res)
	s.clearCookie(w, preCookie)
	def := "/"
	if res.User.IsAdmin() {
		def = "/admin"
	}
	http.Redirect(w, r, safeNext(d.Next, def), http.StatusSeeOther)
}

func (s *Server) setSession(w http.ResponseWriter, res service.LoginResult) {
	var maxAge time.Duration
	if res.User.IsAdmin() || res.Session.Remember {
		maxAge = res.Session.ExpiresAt.Sub(res.Session.CreatedAt)
	}
	http.SetCookie(w, s.cookie(sessionCookie, res.Token, maxAge))
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	if err := s.svc.Logout(r.Context(), c.token); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.clearCookie(w, sessionCookie)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// deviceLabel names a device from its User-Agent, for the sessions list.
func deviceLabel(r *http.Request) string {
	ua := r.UserAgent()
	for _, d := range []struct{ needle, label string }{
		{"iPad", "iPad"}, {"iPhone", "iPhone"}, {"Android", "Android"}, {"CrOS", "Chromebook"},
		{"Macintosh", "Mac"}, {"Windows", "Windows PC"}, {"Linux", "Linux"},
	} {
		if strings.Contains(ua, d.needle) {
			return d.label
		}
	}
	return "Browser"
}
