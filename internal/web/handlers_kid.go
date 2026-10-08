package web

import (
	"net/http"

	"github.com/chinny/goldberry/internal/service"
	"github.com/chinny/goldberry/internal/store"
)

func (s *Server) kidHome(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	kv, err := s.svc.KidOverview(r.Context(), c.User.ID, 8)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v := s.newView(w, r, "Hi, "+c.User.DisplayName, kv)
	v.Eyebrow, v.Nav = "Hi,", "home"
	v.Title = c.User.DisplayName
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
	} else if len(kv.Jars) > 0 {
		v.Form.Set("jar", kv.Jars[0].ID)
	}
	s.render(w, r, status, "kid_request", v)
}

func (s *Server) kidRequestSubmit(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	amount, err := c.Cur.Parse(r.PostFormValue("amount"))
	if err != nil {
		s.renderKidRequest(w, r, c, http.StatusUnprocessableEntity, "How much? "+err.Error()+".")
		return
	}
	req, err := s.svc.CreateRequest(r.Context(), *c.User, service.RequestInput{
		JarID: r.PostFormValue("jar"), Amount: amount, Reason: r.PostFormValue("reason"),
		IdempotencyKey: r.PostFormValue("key"),
	})
	if err != nil {
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
