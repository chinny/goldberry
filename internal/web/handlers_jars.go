package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/chinny/goldberry/internal/service"
	"github.com/chinny/goldberry/internal/store"
)

// --- moving money (admin) --------------------------------------------------------

func (s *Server) adminMovePage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	id := r.PathValue("id")
	s.renderMove(w, r, id, "/admin/kids/"+id+"/transfers", "/admin/kids/"+id, http.StatusOK, "")
}

func (s *Server) adminMoveSubmit(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	id := r.PathValue("id")
	s.moveSubmit(w, r, c, id, "/admin/kids/"+id+"/transfers", "/admin/kids/"+id)
}

// --- jars and the split -------------------------------------------------------------

func (s *Server) adminJarsPage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	s.renderJars(w, r, http.StatusOK, "")
}

func (s *Server) renderJars(w http.ResponseWriter, r *http.Request, status int, errMsg string) {
	kv, ok := s.loadKid(w, r)
	if !ok {
		return
	}
	v := s.newView(w, r, kv.Kid.DisplayName+"’s jars", kv)
	v.Nav, v.Back, v.Err = "kid:"+kv.Kid.ID, "/admin/kids/"+kv.Kid.ID, errMsg
	for _, j := range kv.Jars {
		v.Form.Set("split_"+j.ID, strconv.FormatFloat(float64(j.BPS)/100, 'f', -1, 64))
	}
	if r.Method == http.MethodPost {
		for k, vals := range r.PostForm {
			v.Form[k] = vals
		}
	}
	s.render(w, r, status, "admin_jars", v)
}

func (s *Server) jarsResult(w http.ResponseWriter, r *http.Request, err error, ok string) {
	if err != nil {
		if msg := userMessage(err); msg != "" {
			s.renderJars(w, r, http.StatusUnprocessableEntity, msg)
			return
		}
		s.fail(w, r, "/admin", err)
		return
	}
	s.flash(w, "ok", ok)
	s.redirectTo(w, r, "/admin/kids/"+url.PathEscape(r.PathValue("id"))+"/jars")
}

func (s *Server) adminJarAdd(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	j, err := s.svc.AddJar(r.Context(), *c.User, r.PathValue("id"), r.PostFormValue("name"))
	s.jarsResult(w, r, err, "Added "+j.Name+". Give it a share of the split if deposits should fill it.")
}

func (s *Server) adminSplit(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	bps := map[string]int{}
	for k, vals := range r.PostForm {
		id, ok := strings.CutPrefix(k, "split_")
		if !ok || len(vals) == 0 {
			continue
		}
		pct, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(vals[0]), "%"), 64)
		if err != nil {
			s.renderJars(w, r, http.StatusUnprocessableEntity, "Shares are percentages, like 70.")
			return
		}
		bps[id] = int(pct*100 + 0.5)
	}
	err := s.svc.SetSplit(r.Context(), *c.User, r.PathValue("id"), bps)
	s.jarsResult(w, r, err, "Split saved.")
}

func (s *Server) adminJarRename(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	err := s.svc.RenameJar(r.Context(), *c.User, r.PostFormValue("jar"), r.PostFormValue("name"))
	s.jarsResult(w, r, err, "Renamed.")
}

func (s *Server) adminJarArchive(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	err := s.svc.ArchiveJar(r.Context(), *c.User, r.PostFormValue("jar"))
	s.jarsResult(w, r, err, "Archived. Its history stays in the ledger.")
}

// --- locks (admin) ------------------------------------------------------------------

func (s *Server) adminLockSubmit(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	f := r.PostForm
	l, err := s.svc.SetLock(r.Context(), *c.User, service.LockInput{
		JarID: r.PathValue("id"), ToJarID: f.Get("to"), Reason: f.Get("reason"), Until: f.Get("until"),
	})
	if err != nil {
		s.fail(w, r, "/admin", err)
		return
	}
	s.back(w, r, "/admin/kids/"+l.KidID, "ok", "Locked "+l.JarName+". Only a parent can unlock it.")
}

func (s *Server) adminLockRemove(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	if err := s.svc.RemoveLock(r.Context(), *c.User, r.PathValue("id"), ""); err != nil {
		s.fail(w, r, "/admin", err)
		return
	}
	s.back(w, r, "/admin", "ok", "Lock removed.")
}

// --- allowance -----------------------------------------------------------------------

type allowanceData struct {
	service.KidView
	Schedule *service.ScheduleView
	Weekdays []time.Weekday
}

var weekdays = []time.Weekday{time.Sunday, time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday}

func (s *Server) adminAllowancePage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	s.renderAllowance(w, r, http.StatusOK, "")
}

func (s *Server) renderAllowance(w http.ResponseWriter, r *http.Request, status int, errMsg string) {
	kv, ok := s.loadKid(w, r)
	if !ok {
		return
	}
	d := allowanceData{KidView: kv, Weekdays: weekdays}
	schedID := r.URL.Query().Get("schedule")
	if r.Method == http.MethodPost {
		schedID = r.PostFormValue("schedule")
	}
	for i := range kv.Schedules {
		if kv.Schedules[i].ID == schedID {
			d.Schedule = &kv.Schedules[i]
		}
	}
	title := "New allowance"
	if d.Schedule != nil {
		title = "Edit allowance"
	}
	v := s.newView(w, r, title, d)
	v.Nav, v.Back, v.Err = "kid:"+kv.Kid.ID, "/admin/kids/"+kv.Kid.ID, errMsg
	switch {
	case r.Method == http.MethodPost:
		v.Form = r.PostForm
	case d.Schedule != nil:
		sc := d.Schedule
		v.Form.Set("amount", rc(r).Cur.Plain(sc.Amount))
		v.Form.Set("cadence", sc.Cadence)
		v.Form.Set("weekday", strconv.Itoa(sc.Weekday))
		v.Form.Set("day_of_month", strconv.Itoa(max(sc.DayOfMonth, 1)))
		v.Form.Set("anchor", sc.AnchorDate)
		v.Form.Set("jar", sc.TargetJarID)
		v.Form.Set("comment", sc.CommentTemplate)
	default:
		v.Form.Set("cadence", "weekly")
		v.Form.Set("weekday", strconv.Itoa(int(time.Saturday)))
		v.Form.Set("day_of_month", "1")
	}
	s.render(w, r, status, "admin_allowance", v)
}

func (s *Server) adminAllowanceSubmit(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	f := r.PostForm
	amount, err := c.Cur.Parse(f.Get("amount"))
	if err != nil {
		s.renderAllowance(w, r, http.StatusUnprocessableEntity, "Amount: "+err.Error()+".")
		return
	}
	wd, _ := strconv.Atoi(f.Get("weekday"))
	dom, _ := strconv.Atoi(f.Get("day_of_month"))
	in := service.ScheduleInput{Amount: amount, Cadence: f.Get("cadence"), Weekday: wd, DayOfMonth: dom,
		Anchor: f.Get("anchor"), TargetJarID: f.Get("jar"), Comment: f.Get("comment")}
	kidID := r.PathValue("id")
	if id := f.Get("schedule"); id != "" {
		err = s.svc.UpdateSchedule(r.Context(), *c.User, id, in)
	} else {
		_, err = s.svc.CreateSchedule(r.Context(), *c.User, kidID, in)
	}
	if err != nil {
		if msg := userMessage(err); msg != "" {
			s.renderAllowance(w, r, http.StatusUnprocessableEntity, msg)
			return
		}
		s.fail(w, r, "/admin/kids/"+kidID, err)
		return
	}
	s.flash(w, "ok", "Allowance saved. It starts on the next paying day.")
	s.redirectTo(w, r, "/admin/kids/"+url.PathEscape(kidID))
}

func (s *Server) adminSchedulePause(active bool) handler {
	return func(w http.ResponseWriter, r *http.Request, c *reqCtx) {
		if err := s.svc.SetScheduleActive(r.Context(), *c.User, r.PathValue("id"), active); err != nil {
			s.fail(w, r, "/admin", err)
			return
		}
		msg := "Allowance paused. Nothing posts until you resume it."
		if active {
			msg = "Allowance resumed. Paused weeks aren’t back-paid."
		}
		s.back(w, r, "/admin", "ok", msg)
	}
}

// --- shared --------------------------------------------------------------------------

// lockActivity loads the overrides an admin sees on a kid's page.
func (s *Server) lockActivity(r *http.Request, kidID string) []store.LockOverride {
	o, _ := s.svc.LockActivity(r.Context(), kidID, 10)
	return o
}
