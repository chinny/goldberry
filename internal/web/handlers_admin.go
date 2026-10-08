package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/chinny/goldberry/internal/service"
	"github.com/chinny/goldberry/internal/store"
)

func (s *Server) adminHome(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	d, err := s.svc.AdminDashboard(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v := s.newView(w, r, "Dashboard", d)
	v.Eyebrow, v.Nav = c.House.Name, "home"
	s.render(w, r, http.StatusOK, "admin_home", v)
}

type adminKidData struct {
	service.KidView
	Ledger    []store.LedgerEntry
	JarFilter string
	Login     service.LoginState
	Overrides []store.LockOverride
}

func (s *Server) loadKid(w http.ResponseWriter, r *http.Request) (service.KidView, bool) {
	kv, err := s.svc.KidOverview(r.Context(), r.PathValue("id"), 0)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return kv, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return kv, false
	}
	return kv, true
}

func (s *Server) adminKid(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	kv, ok := s.loadKid(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	jarFilter := r.URL.Query().Get("jar")
	ledger, err := s.svc.Store.ListLedger(ctx, store.LedgerFilter{KidID: kv.Kid.ID, JarID: jarFilter, Limit: 200})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	states, err := s.svc.LoginStates(ctx, []store.User{kv.Kid})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v := s.newView(w, r, kv.Kid.DisplayName, adminKidData{KidView: kv, Ledger: ledger, JarFilter: jarFilter,
		Login: states[kv.Kid.ID], Overrides: s.lockActivity(r, kv.Kid.ID)})
	v.Nav, v.Back = "kid:"+kv.Kid.ID, "/admin"
	s.render(w, r, http.StatusOK, "admin_kid", v)
}

type fundsData struct {
	service.KidView
	Chips []string
}

var defaultChips = []string{"Chores", "Birthday", "Good report", "Spent at store"}

func (s *Server) adminFundsPage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	kv, ok := s.loadKid(w, r)
	if !ok {
		return
	}
	v := s.newView(w, r, kv.Kid.DisplayName+"’s money", fundsData{KidView: kv, Chips: defaultChips})
	v.Nav, v.Back = "kid:"+kv.Kid.ID, "/admin/kids/"+kv.Kid.ID
	v.Form.Set("direction", "add")
	v.Form.Set("jar", "split")
	if r.URL.Query().Get("direction") == "remove" {
		v.Form.Set("direction", "remove")
		v.Form.Set("jar", kv.Jars[0].ID)
	}
	s.render(w, r, http.StatusOK, "admin_funds", v)
}

func (s *Server) adminFundsSubmit(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	kv, ok := s.loadKid(w, r)
	if !ok {
		return
	}
	f := r.PostForm
	again := func(msg string) {
		v := s.newView(w, r, kv.Kid.DisplayName+"’s money", fundsData{KidView: kv, Chips: defaultChips})
		v.Nav, v.Back, v.Err, v.Form = "kid:"+kv.Kid.ID, "/admin/kids/"+kv.Kid.ID, msg, f
		s.render(w, r, http.StatusUnprocessableEntity, "admin_funds", v)
	}
	amount, err := c.Cur.Parse(f.Get("amount"))
	if err != nil {
		again("Amount: " + err.Error() + ".")
		return
	}
	remove := f.Get("direction") == "remove"
	jar := f.Get("jar")
	if jar == "split" && remove {
		again("Pick one jar to remove money from.")
		return
	}
	useSplit := jar == "split"
	if useSplit {
		jar = ""
	}
	es, err := s.svc.PostEntry(r.Context(), *c.User, service.EntryInput{
		KidID: kv.Kid.ID, JarID: jar, UseSplit: useSplit, Amount: amount, Remove: remove,
		Comment: f.Get("comment"), PrivateNote: f.Get("private_note"), IdempotencyKey: f.Get("key"),
	})
	if err != nil {
		if msg := userMessage(err); msg != "" {
			again(msg)
			return
		}
		s.serverError(w, r, err)
		return
	}
	verb := "Added"
	if remove {
		verb = "Removed"
	}
	var total int64
	for _, e := range es {
		total += abs(e.Amount)
	}
	where := ""
	if len(es) > 1 {
		where = " across " + strconv.Itoa(len(es)) + " jars"
	}
	s.flash(w, "ok", verb+" "+c.Cur.Format(total)+where+" · "+kv.Kid.DisplayName+" can see it now.")
	http.Redirect(w, r, "/admin/kids/"+kv.Kid.ID, http.StatusSeeOther)
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func (s *Server) adminReverse(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	es, err := s.svc.Reverse(r.Context(), *c.User, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, "/admin", err)
		return
	}
	s.back(w, r, "/admin/kids/"+es[0].KidID, "ok", "Undone. The original and the undo both stay in the ledger.")
}

func (s *Server) adminDecide(approve bool) handler {
	return func(w http.ResponseWriter, r *http.Request, c *reqCtx) {
		var lower int64
		if v := strings.TrimSpace(r.PostFormValue("approved_amount")); approve && v != "" {
			amt, err := c.Cur.Parse(v)
			if err != nil {
				s.back(w, r, "/admin", "err", "Approve amount: "+err.Error()+".")
				return
			}
			if amt == 0 {
				s.back(w, r, "/admin", "err", "To approve nothing, deny the request instead.")
				return
			}
			lower = amt
		}
		req, err := s.svc.DecideRequest(r.Context(), *c.User, r.PathValue("id"), service.Decision{
			Approve: approve, ApprovedAmount: lower, Note: r.PostFormValue("note"),
		})
		if err != nil {
			s.fail(w, r, "/admin", err)
			return
		}
		if id := r.PostFormValue("notification"); id != "" {
			_ = s.svc.MarkRead(r.Context(), *c.User, id)
		}
		msg := "Denied " + req.KidName + "’s request."
		if approve {
			msg = "Approved " + c.Cur.Format(req.ApprovedAmount) + " for " + req.KidName + ". Hand it over!"
		}
		s.back(w, r, "/admin", "ok", msg)
	}
}

// --- people ------------------------------------------------------------------

type peopleData struct {
	Admins []store.User
	Kids   []store.User
	Login  map[string]service.LoginState
}

func (s *Server) people(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	ctx := r.Context()
	users, err := s.svc.Store.ListUsers(ctx, "")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	states, err := s.svc.LoginStates(ctx, users)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d := peopleData{Login: states}
	for _, u := range users {
		if u.IsAdmin() {
			d.Admins = append(d.Admins, u)
		} else {
			d.Kids = append(d.Kids, u)
		}
	}
	v := s.newView(w, r, "People", d)
	v.Nav = "people"
	s.render(w, r, http.StatusOK, "people", v)
}

func (s *Server) userNewPage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	role := r.URL.Query().Get("role")
	if role != "admin" {
		role = "kid"
	}
	v := s.newView(w, r, map[string]string{"kid": "Add a kid", "admin": "Add a parent"}[role], role)
	v.Nav, v.Back = "people", "/admin/users"
	s.render(w, r, http.StatusOK, "user_new", v)
}

func (s *Server) userNewSubmit(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	f := r.PostForm
	role := f.Get("role")
	var u store.User
	var err error
	if role == "admin" {
		u, err = s.svc.CreateAdmin(r.Context(), *c.User, service.AdminInput{
			Username: f.Get("username"), DisplayName: f.Get("display_name"), Password: f.Get("password"), Email: f.Get("email")})
	} else {
		role = "kid"
		u, err = s.svc.CreateKid(r.Context(), *c.User, service.KidInput{
			Username: f.Get("username"), DisplayName: f.Get("display_name"), PIN: f.Get("pin")})
	}
	if err != nil {
		msg := userMessage(err)
		if msg == "" {
			s.serverError(w, r, err)
			return
		}
		v := s.newView(w, r, map[string]string{"kid": "Add a kid", "admin": "Add a parent"}[role], role)
		v.Nav, v.Back, v.Err, v.Form = "people", "/admin/users", msg, f
		v.Form.Del("pin")
		v.Form.Del("password")
		s.render(w, r, http.StatusUnprocessableEntity, "user_new", v)
		return
	}
	s.flash(w, "ok", "Added "+u.DisplayName+". They sign in as “"+u.Username+"”.")
	if u.IsKid() {
		http.Redirect(w, r, "/admin/kids/"+u.ID, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

type userData struct {
	Target   store.User
	Login    service.LoginState
	Sessions []store.Session
	Audit    []store.AuditEntry
	Names    map[string]string
	PINLen   int
}

func (s *Server) userPage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	ctx := r.Context()
	u, err := s.svc.Store.GetUser(ctx, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	states, err := s.svc.LoginStates(ctx, []store.User{u})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	sessions, err := s.svc.Store.ListSessions(ctx, u.ID, time.Now())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	audit, err := s.svc.Store.ListAudit(ctx, u.ID, 20)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	names := map[string]string{"": "System"}
	if all, err := s.svc.Store.ListUsers(ctx, ""); err == nil {
		for _, x := range all {
			names[x.ID] = x.DisplayName
		}
	}
	v := s.newView(w, r, u.DisplayName, userData{Target: u, Login: states[u.ID], Sessions: sessions, Audit: audit,
		Names: names, PINLen: c.House.PINLength})
	v.Nav, v.Back = "people", "/admin/users"
	if u.IsKid() {
		v.Nav = "kid:" + u.ID
	}
	s.render(w, r, http.StatusOK, "user", v)
}

func (s *Server) userSecret(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	id := r.PathValue("id")
	if err := s.svc.ResetSecret(r.Context(), c.User, id, strings.TrimSpace(r.PostFormValue("secret"))); err != nil {
		s.fail(w, r, "/admin/users/"+id, err)
		return
	}
	s.back(w, r, "/admin/users/"+id, "ok", "Saved. The account is unlocked too.")
}

func (s *Server) userUnlock(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	id := r.PathValue("id")
	if err := s.svc.Unlock(r.Context(), *c.User, id); err != nil {
		s.fail(w, r, "/admin/users/"+id, err)
		return
	}
	s.back(w, r, "/admin/users/"+id, "ok", "Unlocked.")
}

func (s *Server) userDisable(disabled bool) handler {
	return func(w http.ResponseWriter, r *http.Request, c *reqCtx) {
		id := r.PathValue("id")
		if err := s.svc.SetDisabled(r.Context(), *c.User, id, disabled); err != nil {
			s.fail(w, r, "/admin/users/"+id, err)
			return
		}
		msg := "Account enabled."
		if disabled {
			msg = "Account disabled and signed out everywhere."
		}
		s.back(w, r, "/admin/users/"+id, "ok", msg)
	}
}

func (s *Server) userRevoke(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	id := r.PathValue("id")
	if err := s.svc.RevokeSessions(r.Context(), *c.User, id); err != nil {
		s.fail(w, r, "/admin/users/"+id, err)
		return
	}
	if id == c.User.ID {
		s.clearCookie(w, sessionCookie)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.back(w, r, "/admin/users/"+id, "ok", "Signed out of every device.")
}

// --- settings ------------------------------------------------------------------

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	v := s.newView(w, r, "Settings", nil)
	v.Nav = "settings"
	h := c.House
	v.Form.Set("name", h.Name)
	v.Form.Set("timezone", h.Timezone)
	v.Form.Set("pin_length", strconv.Itoa(h.PINLength))
	v.Form.Set("request_expiry_days", strconv.Itoa(h.RequestExpiryDays))
	if h.AllowNegative {
		v.Form.Set("allow_negative", "1")
	}
	s.render(w, r, http.StatusOK, "settings", v)
}

func (s *Server) settingsSubmit(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	f := r.PostForm
	pinLen, _ := strconv.Atoi(f.Get("pin_length"))
	expiry, err := strconv.Atoi(strings.TrimSpace(f.Get("request_expiry_days")))
	if err != nil {
		expiry = -1
	}
	err = s.svc.UpdateHousehold(r.Context(), *c.User, service.HouseholdInput{
		Name: f.Get("name"), Timezone: f.Get("timezone"), PINLength: pinLen,
		AllowNegative: f.Get("allow_negative") == "1", RequestExpiryDays: expiry,
	})
	if err != nil {
		msg := userMessage(err)
		if msg == "" {
			s.serverError(w, r, err)
			return
		}
		v := s.newView(w, r, "Settings", nil)
		v.Nav, v.Err, v.Form = "settings", msg, f
		s.render(w, r, http.StatusUnprocessableEntity, "settings", v)
		return
	}
	s.flash(w, "ok", "Settings saved.")
	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}
