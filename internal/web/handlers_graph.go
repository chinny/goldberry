package web

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/chinny/goldberry/internal/service"
	"github.com/chinny/goldberry/internal/store"
)

type graphLink struct {
	Label string
	Href  string
	On    bool
	Class string // the line class, for a jar's swatch
}

type graphData struct {
	Kid                store.User
	History            service.BalanceHistory
	Chart              chart
	Ranges             []graphLink
	Shows              []graphLink
	Show               string // carried by the custom-range form
	Custom             bool
	FromLabel, ToLabel string // "Sep 10", "Today"
}

// Single is the one line shown, or nil when every jar is.
func (d graphData) Single() *service.HistorySeries {
	if len(d.History.Series) != 1 {
		return nil
	}
	return &d.History.Series[0]
}

func (s *Server) kidGraph(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	s.renderGraph(w, r, *c.User, "/graph", "/")
}

func (s *Server) adminGraph(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	kv, ok := s.loadKid(w, r)
	if !ok {
		return
	}
	s.renderGraph(w, r, kv.Kid, "/admin/kids/"+kv.Kid.ID+"/graph", "/admin/kids/"+kv.Kid.ID)
}

// renderGraph shows one kid's balance over a range. The kid comes from the
// session for kids and from the path (after the admin check) for parents.
func (s *Server) renderGraph(w http.ResponseWriter, r *http.Request, kid store.User, base, back string) {
	ctx, q := r.Context(), r.URL.Query()
	errMsg := ""
	rng, err := s.svc.ResolveRange(ctx, q.Get("range"), q.Get("from"), q.Get("to"))
	if err != nil {
		if errMsg = userMessage(err); errMsg == "" {
			s.serverError(w, r, err)
			return
		}
		if rng, err = s.svc.ResolveRange(ctx, service.RangeMonth, "", ""); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	h, err := s.svc.BalanceHistory(ctx, kid.ID, rng, q.Get("show"))
	if err != nil {
		s.fail(w, r, back, err)
		return
	}
	c := rc(r)
	d := graphData{Kid: kid, History: h, Chart: buildChart(h, c.Cur, c.House.Location(), false),
		Show: h.Show, Custom: rng.Preset == service.RangeCustom}
	loc := c.House.Location()
	day := func(t time.Time) string {
		if t.In(loc).Year() != time.Now().In(loc).Year() {
			return t.In(loc).Format("Jan 2, 2006")
		}
		return t.In(loc).Format("Jan 2")
	}
	d.FromLabel, d.ToLabel = day(rng.From), day(rng.To.Add(-time.Nanosecond))
	if rng.ToDate == time.Now().In(loc).Format(time.DateOnly) {
		d.ToLabel = "Today"
	}

	// Each link keeps the other choice: a range keeps the lines shown, and
	// the lines keep the range (custom dates included).
	rangeQ := url.Values{"range": {rng.Preset}}
	if d.Custom {
		rangeQ = url.Values{"from": {rng.FromDate}, "to": {rng.ToDate}}
	}
	for _, p := range service.RangePresets {
		d.Ranges = append(d.Ranges, graphLink{Label: p.Label, On: p.Key == rng.Preset,
			Href: base + "?" + url.Values{"range": {p.Key}, "show": {h.Show}}.Encode()})
	}
	showLink := func(label, show, class string) graphLink {
		q := url.Values{"show": {show}}
		for k, v := range rangeQ {
			q[k] = v
		}
		return graphLink{Label: label, Href: base + "?" + q.Encode(), On: h.Show == show, Class: class}
	}
	d.Shows = append(d.Shows, showLink("Total", service.ShowTotal, ""))
	if len(h.Jars) > 1 {
		d.Shows = append(d.Shows, showLink("Every jar", service.ShowJars, ""))
		for _, j := range h.Jars {
			d.Shows = append(d.Shows, showLink(j.Name, j.ID, lineClass(service.HistorySeries{Kind: j.Kind})))
		}
	}

	title := "My money over time"
	v := s.newView(w, r, title, d)
	v.Nav, v.Back, v.Err = "graph", back, errMsg
	if c.User.IsAdmin() {
		v.Title, v.Nav = kid.DisplayName+"’s money over time", "kid:"+kid.ID
	}
	v.Form.Set("from", rng.FromDate)
	v.Form.Set("to", rng.ToDate)
	if errMsg != "" { // keep what they typed
		v.Form.Set("from", q.Get("from"))
		v.Form.Set("to", q.Get("to"))
	}
	status := http.StatusOK
	if errMsg != "" {
		status = http.StatusUnprocessableEntity
	}
	s.render(w, r, status, "graph", v)
}

// graphPreview is the small graph on a home page.
type graphPreview struct {
	Chart chart
	Href  string
}

// previewChart is the last month's total, for the small graph on a home page.
// A failure only costs the preview, never the page.
func (s *Server) previewChart(ctx context.Context, c *reqCtx, kidID, href string) *graphPreview {
	rng, err := s.svc.ResolveRange(ctx, service.RangeMonth, "", "")
	if err != nil {
		s.log.Warn("graph preview", "err", err)
		return nil
	}
	h, err := s.svc.BalanceHistory(ctx, kidID, rng, service.ShowTotal)
	if err != nil {
		s.log.Warn("graph preview", "err", err)
		return nil
	}
	return &graphPreview{Chart: buildChart(h, c.Cur, c.House.Location(), true), Href: href}
}
