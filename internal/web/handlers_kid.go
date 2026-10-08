package web

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/chinny/goldberry/internal/service"
	"github.com/chinny/goldberry/internal/store"
)

func (s *Server) kidHome(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	kv, err := s.svc.KidOverview(r.Context(), c.User.ID, 8)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v := s.newView(w, r, c.User.DisplayName, kv)
	v.Eyebrow, v.Nav = "Hi,", "home"
	s.render(w, r, http.StatusOK, "kid_home", v)
}

type kidLedgerData struct {
	Entries  []store.LedgerEntry
	Requests []store.WithdrawalRequest
}

func (s *Server) kidLedger(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	ctx := r.Context()
	entries, err := s.svc.Ledger(ctx, c.User.ID, 200)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	reqs, err := s.svc.Requests(ctx, c.User.ID, 50)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v := s.newView(w, r, "Activity", kidLedgerData{Entries: entries, Requests: reqs})
	v.Nav, v.Back = "activity", "/"
	s.render(w, r, http.StatusOK, "kid_ledger", v)
}

// --- ask for money -------------------------------------------------------------

func (s *Server) kidRequestPage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	s.renderKidRequest(w, r, c, http.StatusOK, "")
}

func (s *Server) renderKidRequest(w http.ResponseWriter, r *http.Request, c *reqCtx, status int, errMsg string) {
	kv, err := s.svc.KidOverview(r.Context(), c.User.ID, 0)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v := s.newView(w, r, "Ask for money", kv)
	v.Nav, v.Back, v.Err = "ask", "/", errMsg
	if r.Method == http.MethodPost {
		v.Form = r.PostForm
	} else {
		v.Form.Set("jar", r.URL.Query().Get("jar"))
		if v.Form.Get("jar") == "" && len(kv.Jars) > 0 {
			v.Form.Set("jar", kv.Jars[0].ID)
		}
	}
	s.render(w, r, status, "kid_request", v)
}

func (s *Server) kidRequestSubmit(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	f := r.PostForm
	amount, err := c.Cur.Parse(f.Get("amount"))
	if err != nil {
		s.renderKidRequest(w, r, c, http.StatusUnprocessableEntity, "How much? "+err.Error()+".")
		return
	}
	req, err := s.svc.CreateRequest(r.Context(), *c.User, service.RequestInput{
		JarID: f.Get("jar"), Amount: amount, Reason: f.Get("reason"), IdempotencyKey: f.Get("key"),
		Override: f.Get("override"), RemoveLock: f.Get("remove_lock") == "1",
	})
	if err != nil {
		if s.toGauntlet(w, r, err, "request", f) {
			return
		}
		if msg := userMessage(err); msg != "" {
			s.renderKidRequest(w, r, c, http.StatusUnprocessableEntity, msg)
			return
		}
		s.serverError(w, r, err)
		return
	}
	s.flash(w, "ok", "Sent! "+c.Cur.Format(req.Amount)+" is held until a grown-up says yes or no.")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) kidRequestCancel(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	if err := s.svc.CancelRequest(r.Context(), *c.User, r.PathValue("id")); err != nil {
		s.fail(w, r, "/", err)
		return
	}
	s.back(w, r, "/", "ok", "Request cancelled. The money is yours to ask for again.")
}

// --- moving money between jars ----------------------------------------------------

type moveData struct {
	service.KidView
	Action string
}

func (s *Server) renderMove(w http.ResponseWriter, r *http.Request, kidID, action, back string, status int, errMsg string) {
	kv, err := s.svc.KidOverview(r.Context(), kidID, 0)
	if err != nil {
		s.fail(w, r, back, err)
		return
	}
	v := s.newView(w, r, "Move between jars", moveData{KidView: kv, Action: action})
	v.Back, v.Err = back, errMsg
	v.Nav = "move"
	if rc(r).User.IsAdmin() {
		v.Nav = "kid:" + kidID
	}
	if r.Method == http.MethodPost {
		v.Form = r.PostForm
	} else {
		q := r.URL.Query()
		v.Form.Set("from", q.Get("from"))
		v.Form.Set("to", q.Get("to"))
		if v.Form.Get("from") == "" && len(kv.Jars) > 1 {
			v.Form.Set("from", kv.Jars[0].ID)
			v.Form.Set("to", kv.Jars[1].ID)
		}
	}
	s.render(w, r, status, "move", v)
}

func (s *Server) kidMovePage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	s.renderMove(w, r, c.User.ID, "/transfers", "/", http.StatusOK, "")
}

func (s *Server) kidMoveSubmit(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	s.moveSubmit(w, r, c, c.User.ID, "/transfers", "/")
}

func (s *Server) moveSubmit(w http.ResponseWriter, r *http.Request, c *reqCtx, kidID, action, back string) {
	f := r.PostForm
	amount, err := c.Cur.Parse(f.Get("amount"))
	if err != nil {
		s.renderMove(w, r, kidID, action, back, http.StatusUnprocessableEntity, "How much? "+err.Error()+".")
		return
	}
	pair, err := s.svc.Transfer(r.Context(), *c.User, service.TransferInput{
		KidID: kidID, FromJarID: f.Get("from"), ToJarID: f.Get("to"), Amount: amount, IdempotencyKey: f.Get("key"),
		Override: f.Get("override"), RemoveLock: f.Get("remove_lock") == "1",
	})
	if err != nil {
		if s.toGauntlet(w, r, err, "transfer", f) {
			return
		}
		if msg := userMessage(err); msg != "" {
			s.renderMove(w, r, kidID, action, back, http.StatusUnprocessableEntity, msg)
			return
		}
		s.serverError(w, r, err)
		return
	}
	msg := "Moved " + c.Cur.Format(amount)
	if len(pair) == 2 {
		msg += " from " + pair[0].JarName + " to " + pair[1].JarName
	}
	s.flash(w, "ok", msg+".")
	s.redirectTo(w, r, back)
}

// --- self-locks and the gauntlet ---------------------------------------------------

type lockFormData struct {
	service.KidView
	Jar service.JarView
}

func (s *Server) kidLockPage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	s.renderLockForm(w, r, c, http.StatusOK, "")
}

func (s *Server) renderLockForm(w http.ResponseWriter, r *http.Request, c *reqCtx, status int, errMsg string) {
	kv, err := s.svc.KidOverview(r.Context(), c.User.ID, 0)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	jar, ok := kv.Jar(r.PathValue("id"))
	if !ok {
		s.notFound(w, r)
		return
	}
	v := s.newView(w, r, "Lock "+jar.Name, lockFormData{KidView: kv, Jar: jar})
	v.Back, v.Nav, v.Err = "/", "home", errMsg
	if r.Method == http.MethodPost {
		v.Form = r.PostForm
	}
	s.render(w, r, status, "kid_lock", v)
}

func (s *Server) kidLockSubmit(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	f := r.PostForm
	l, err := s.svc.SetLock(r.Context(), *c.User, service.LockInput{
		JarID: r.PathValue("id"), ToJarID: f.Get("to"), Reason: f.Get("reason"), Until: f.Get("until"),
	})
	if err != nil {
		if msg := userMessage(err); msg != "" {
			s.renderLockForm(w, r, c, http.StatusUnprocessableEntity, msg)
			return
		}
		s.fail(w, r, "/", err)
		return
	}
	s.flash(w, "ok", "Locked "+l.JarName+". Future-you will thank you.")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) kidLockRemove(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	err := s.svc.RemoveLock(r.Context(), *c.User, r.PathValue("id"), r.PostFormValue("override"))
	if err != nil {
		if errors.Is(err, service.ErrOverrideTooSoon) || errors.Is(err, service.ErrOverrideInvalid) {
			s.flash(w, "err", userMessage(err))
			http.Redirect(w, r, "/gauntlet?"+url.Values{"then": {"remove"}, "jar": {r.PostFormValue("jar")},
				"lock": {r.PathValue("id")}}.Encode(), http.StatusSeeOther)
			return
		}
		s.fail(w, r, "/", err)
		return
	}
	s.back(w, r, "/", "ok", "Lock removed. Your parents can see that you did.")
}

// toGauntlet sends a kid to the lock-breaking screen when their own lock is in
// the way (or the gauntlet token was early or stale), carrying the form along.
func (s *Server) toGauntlet(w http.ResponseWriter, r *http.Request, err error, then string, f url.Values) bool {
	var needs *service.ErrNeedsOverride
	jar := ""
	switch {
	case errors.As(err, &needs):
		jar = needs.JarID
	case errors.Is(err, service.ErrOverrideTooSoon), errors.Is(err, service.ErrOverrideInvalid):
		s.flash(w, "err", userMessage(err))
		jar = f.Get("jar")
		if then == "transfer" {
			jar = f.Get("from")
		}
	default:
		return false
	}
	q := url.Values{"then": {then}, "jar": {jar}, "amount": {f.Get("amount")}, "key": {f.Get("key")}}
	switch then {
	case "request":
		q.Set("reason", f.Get("reason"))
	case "transfer":
		q.Set("to", f.Get("to"))
	}
	http.Redirect(w, r, "/gauntlet?"+q.Encode(), http.StatusSeeOther)
	return true
}

type gauntletData struct {
	Then     string // request | transfer | remove
	Action   string
	Jar      service.JarView
	To       service.JarView
	Locks    []store.JarLock
	Token    string
	Amount   int64
	AmountIn string
	Reason   string
	Key      string
	LockID   string
	After    int64
}

func (s *Server) gauntletPage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	q := r.URL.Query()
	kv, err := s.svc.KidOverview(r.Context(), c.User.ID, 0)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	jar, ok := kv.Jar(q.Get("jar"))
	if !ok {
		s.notFound(w, r)
		return
	}
	token, locks, err := s.svc.StartOverride(r.Context(), *c.User, jar.ID)
	if err != nil {
		s.fail(w, r, "/", err)
		return
	}
	d := gauntletData{Then: q.Get("then"), Jar: jar, Locks: locks, Token: token, AmountIn: q.Get("amount"),
		Reason: q.Get("reason"), Key: q.Get("key"), LockID: q.Get("lock")}
	d.Amount, _ = c.Cur.Parse(d.AmountIn)
	switch d.Then {
	case "request":
		d.Action = "/requests"
	case "transfer":
		d.Action = "/transfers"
		d.To, _ = kv.Jar(q.Get("to"))
	default:
		d.Then = "remove"
		if d.LockID == "" {
			d.LockID = locks[0].ID
		}
		d.Action = "/locks/" + d.LockID + "/remove"
	}
	d.After = jar.Available() - d.Amount
	v := s.newView(w, r, "Past-you set this for a reason", d)
	v.Bare = true
	s.render(w, r, http.StatusOK, "gauntlet", v)
}
