package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/chinny/goldberry/internal/service"
)

type goalFormData struct {
	service.KidView
	Goal   *service.GoalView
	Action string
}

// renderGoalForm shows the new/edit goal form, for a kid (their own) or an
// admin (kidID from the URL).
func (s *Server) renderGoalForm(w http.ResponseWriter, r *http.Request, c *reqCtx, kidID string, status int, errMsg string) {
	kv, err := s.svc.KidOverview(r.Context(), kidID, 0)
	if err != nil {
		s.fail(w, r, "/", err)
		return
	}
	d := goalFormData{KidView: kv, Action: "/goals"}
	back := "/"
	if c.User.IsAdmin() {
		d.Action, back = "/admin/kids/"+url.PathEscape(kidID)+"/goals", "/admin/kids/"+url.PathEscape(kidID)
	}
	goalID := r.PathValue("goal")
	for i := range kv.Goals {
		if kv.Goals[i].ID == goalID {
			d.Goal = &kv.Goals[i]
		}
	}
	if goalID != "" && d.Goal == nil {
		s.notFound(w, r)
		return
	}
	title := "New goal"
	if d.Goal != nil {
		title, d.Action = "Edit goal", d.Action+"/"+url.PathEscape(goalID)
	}
	v := s.newView(w, r, title, d)
	v.Back, v.Err, v.Nav = back, errMsg, "home"
	if c.User.IsAdmin() {
		v.Nav = "kid:" + kidID
	}
	switch {
	case r.Method == http.MethodPost:
		v.Form = r.PostForm
	case d.Goal != nil:
		v.Form.Set("name", d.Goal.Name)
		v.Form.Set("emoji", d.Goal.Emoji)
		v.Form.Set("target", c.Cur.Plain(d.Goal.TargetAmount))
		v.Form.Set("jar", d.Goal.JarID)
	}
	s.render(w, r, status, "goal_form", v)
}

func (s *Server) goalSubmit(w http.ResponseWriter, r *http.Request, c *reqCtx, kidID, done string) {
	f := r.PostForm
	target, err := c.Cur.Parse(f.Get("target"))
	if err != nil {
		s.renderGoalForm(w, r, c, kidID, http.StatusUnprocessableEntity, "How much does it cost? "+err.Error()+".")
		return
	}
	in := service.GoalInput{Name: f.Get("name"), Emoji: strings.TrimSpace(f.Get("emoji")), Target: target, JarID: f.Get("jar")}
	if id := r.PathValue("goal"); id != "" {
		if old, err := s.svc.Store.GetGoal(r.Context(), id); err == nil {
			in.Priority = old.Priority
		}
		err = s.svc.UpdateGoal(r.Context(), *c.User, id, in)
	} else {
		_, err = s.svc.CreateGoal(r.Context(), *c.User, kidID, in)
	}
	if err != nil {
		if msg := userMessage(err); msg != "" {
			s.renderGoalForm(w, r, c, kidID, http.StatusUnprocessableEntity, msg)
			return
		}
		s.fail(w, r, done, err)
		return
	}
	s.flash(w, "ok", "Goal saved. Money in "+jarNameFor(r, s, kidID, in.JarID)+" fills it.")
	s.redirectTo(w, r, done)
}

func jarNameFor(r *http.Request, s *Server, kidID, jarID string) string {
	if jarID == "" {
		return "Save"
	}
	if j, err := s.svc.Store.GetJar(r.Context(), jarID); err == nil && j.KidID == kidID {
		return j.Name
	}
	return "the jar"
}

// Kid routes.
func (s *Server) kidGoalPage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	s.renderGoalForm(w, r, c, c.User.ID, http.StatusOK, "")
}

func (s *Server) kidGoalSubmit(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	s.goalSubmit(w, r, c, c.User.ID, "/")
}

// Admin routes.
func (s *Server) adminGoalPage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	s.renderGoalForm(w, r, c, r.PathValue("id"), http.StatusOK, "")
}

func (s *Server) adminGoalSubmit(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	id := r.PathValue("id")
	s.goalSubmit(w, r, c, id, "/admin/kids/"+url.PathEscape(id))
}

func (s *Server) goalArchive(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	if err := s.svc.ArchiveGoal(r.Context(), *c.User, r.PathValue("goal")); err != nil {
		s.fail(w, r, "/", err)
		return
	}
	s.back(w, r, "/", "ok", "Goal removed. The money stays in the jar.")
}

// --- interest ---------------------------------------------------------------------

func (s *Server) adminInterestSubmit(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	kidID := r.PathValue("id")
	back := "/admin/kids/" + url.PathEscape(kidID)
	f := r.PostForm
	pct, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(f.Get("rate")), "%"), 64)
	if err != nil {
		s.back(w, r, back, "err", "Rate is a percentage per month, like 1.")
		return
	}
	var capAmt int64
	if v := strings.TrimSpace(f.Get("cap")); v != "" {
		if capAmt, err = c.Cur.Parse(v); err != nil {
			s.back(w, r, back, "err", "Cap: "+err.Error()+".")
			return
		}
	}
	bps := int(pct*100 + 0.5)
	if err := s.svc.SetInterest(r.Context(), *c.User, kidID, f.Get("jar"), bps, capAmt, f.Get("active") == "1"); err != nil {
		s.fail(w, r, back, err)
		return
	}
	msg := "Interest turned off."
	if f.Get("active") == "1" && bps > 0 {
		msg = "Interest saved. It posts on the 1st for the month before, on the average daily balance."
	}
	s.back(w, r, back, "ok", msg)
}

// goalForRequest pre-fills "Ask to buy it" from a goal the kid owns.
func goalForRequest(kv service.KidView, id string) *service.GoalView {
	for i := range kv.Goals {
		if kv.Goals[i].ID == id {
			return &kv.Goals[i]
		}
	}
	return nil
}
