package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/chinny/goldberry/internal/buildinfo"
	"github.com/chinny/goldberry/internal/notify"
	"github.com/chinny/goldberry/internal/service"
	"github.com/chinny/goldberry/internal/store"
)

// view is the data every page template gets.
type view struct {
	*reqCtx
	Title     string
	Eyebrow   string
	Back      string // back-arrow target, if any
	Bare      bool   // full-screen page without the app shell (the gauntlet)
	Nav       string // active nav item
	FlashKind string
	Flash     string
	Err       string
	Form      url.Values
	Unread    int
	Kids      []store.User // admin sidebar
	Version   string
	Commit    string
	SourceURL string
	Now       time.Time
	D         any
}

var funcs = template.FuncMap{
	"add": func(a, b int) int { return a + b },
	"seq": func(n int) []int {
		s := make([]int, n)
		for i := range s {
			s[i] = i
		}
		return s
	},
	"lower":        strings.ToLower,
	"derefTimePtr": func(t time.Time) *time.Time { return &t },
	"deref":        func(r *store.WithdrawalRequest) store.WithdrawalRequest { return *r },
	"derefTime": func(t *time.Time) time.Time {
		if t == nil {
			return time.Time{}
		}
		return *t
	},
	// payloadKid pulls the kid ID out of a notification's payload.
	"payloadKid": func(n service.NotificationView) string {
		var p notify.Payload
		_ = json.Unmarshal(n.Payload, &p)
		return p.KidID
	},
	"initial": func(name string) string { return store.User{DisplayName: name}.Initial() },
	"rows":    groupLedger,
	"pct":     func(bps int) string { return strconv.FormatFloat(float64(bps)/100, 'f', -1, 64) },
	// pair passes the view plus one value to a partial.
	"pair": func(v *view, x any) map[string]any { return map[string]any{"V": v, "X": x} },
	// reqctx bundles what the "request-actions" partial needs.
	"reqctx": func(v *view, r store.WithdrawalRequest, next, notificationID string) map[string]any {
		return map[string]any{"V": v, "R": r, "Next": next, "N": notificationID}
	},
	"neg": func(v int64) int64 { return -v },
	"jarClass": func(k store.JarKind) string {
		switch k {
		case store.JarSpend, store.JarSave, store.JarGive:
			return string(k)
		}
		return "custom"
	},
}

func (s *Server) newView(w http.ResponseWriter, r *http.Request, title string, d any) *view {
	c := rc(r)
	v := &view{reqCtx: c, Title: title, D: d, Version: buildinfo.Version, Commit: buildinfo.ShortCommit(),
		SourceURL: buildinfo.SourceURL(), Now: time.Now(), Form: url.Values{}}
	v.FlashKind, v.Flash = s.takeFlash(w, r)
	if c.User != nil {
		ctx := r.Context()
		v.Unread, _ = s.svc.UnreadCount(ctx, *c.User)
		if c.User.IsAdmin() {
			kids, _ := s.svc.Store.ListUsers(ctx, store.RoleKid)
			for _, k := range kids {
				if !k.Disabled() {
					v.Kids = append(v.Kids, k)
				}
			}
		}
	}
	return v
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page string, v *view) {
	t, ok := s.pages[page]
	if !ok {
		s.serverError(w, r, fmt.Errorf("no template %q", page))
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", v); err != nil {
		s.log.Error("render", "page", page, "err", err)
		http.Error(w, "Something went wrong.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (s *Server) renderStatus(w http.ResponseWriter, r *http.Request, status int, title, msg string) {
	v := s.newView(w, r, title, msg)
	s.render(w, r, status, "status", v)
}

// --- template helpers (methods so they can use the household) -----------------

func (v *view) Money(amount int64) string  { return v.Cur.Format(amount) }
func (v *view) Signed(amount int64) string { return v.Cur.Signed(amount) }
func (v *view) Plain(amount int64) string  { return v.Cur.Plain(amount) }
func (v *view) Symbol() string             { return v.Cur.Symbol }

func (v *view) loc() *time.Location { return v.House.Location() }

// When is a short relative time: "just now", "12 min ago", "3 h ago",
// "yesterday", "Mon", "Mar 8", "Mar 8, 2025".
func (v *view) When(t time.Time) string {
	now := v.Now.In(v.loc())
	t = t.In(v.loc())
	d := now.Sub(t)
	y1, m1, d1 := now.Date()
	today := time.Date(y1, m1, d1, 0, 0, 0, 0, v.loc())
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d/time.Minute))
	case !t.Before(today):
		return fmt.Sprintf("%d h ago", int(d/time.Hour))
	case !t.Before(today.AddDate(0, 0, -1)):
		return "yesterday"
	case !t.Before(today.AddDate(0, 0, -6)):
		return t.Format("Mon")
	case t.Year() == now.Year():
		return t.Format("Jan 2")
	default:
		return t.Format("Jan 2, 2006")
	}
}

// Date is "Mar 8" in the household time zone.
func (v *view) Date(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.In(v.loc()).Format("Jan 2")
}

// Tone picks a stable avatar colour for a user.
func (v *view) Tone(u store.User) string { return v.ToneID(u.ID) }

// ToneID is Tone for a user ID.
func (v *view) ToneID(id string) string {
	h := fnv.New32a()
	h.Write([]byte(id))
	return []string{"gold", "river", "berry", "slate"}[h.Sum32()%4]
}

// Val returns a submitted form value, for re-filling a form after an error.
func (v *view) Val(k string) string { return v.Form.Get(k) }

// Checked reports whether a submitted checkbox/radio had this value.
func (v *view) Checked(k, val string) bool { return v.Form.Get(k) == val }

// NewKey is a fresh idempotency key for a form.
func (v *view) NewKey() string { return store.NewID() }

// IsAdmin is a nil-safe role check for templates.
func (v *view) IsAdmin() bool { return v.User != nil && v.User.IsAdmin() }
func (v *view) IsKid() bool   { return v.User != nil && v.User.IsKid() }

// ledgerRow is one line of a ledger: a single entry, or the entries of a split
// deposit or allowance (shared batch_id), or the two legs of a move between
// jars (shared transfer_id).
type ledgerRow struct {
	store.LedgerEntry       // the first entry: comment, kind, actor, dates
	Amount            int64 // the row's total (a move shows the amount moved)
	Jars              string
	Count             int
	IsMove            bool
	AllReversed       bool
}

// Reversible reports whether the row gets an Undo button.
func (r ledgerRow) Reversible() bool {
	return !r.AllReversed && !r.IsMove && r.Kind != store.KindReversal
}

func groupLedger(entries []store.LedgerEntry) []ledgerRow {
	var rows []ledgerRow
	for i := 0; i < len(entries); {
		e := entries[i]
		row := ledgerRow{LedgerEntry: e, Amount: e.Amount, Jars: e.JarName, Count: 1, AllReversed: e.Reversed()}
		j := i + 1
		for ; j < len(entries); j++ {
			n := entries[j]
			sameBatch := e.BatchID != "" && n.BatchID == e.BatchID
			sameMove := e.TransferID != "" && n.TransferID == e.TransferID
			if !sameBatch && !sameMove {
				break
			}
			row.Count++
			row.AllReversed = row.AllReversed && n.Reversed()
			if sameMove {
				out, in := e, n
				if out.Amount > 0 {
					out, in = n, e
				}
				row.IsMove, row.Amount, row.Jars = true, in.Amount, out.JarName+" → "+in.JarName
				row.LedgerEntry = out
			} else {
				row.Amount += n.Amount
			}
		}
		if row.Count > 1 && !row.IsMove {
			row.Jars = strconv.Itoa(row.Count) + " jars"
		}
		rows = append(rows, row)
		i = j
	}
	return rows
}
