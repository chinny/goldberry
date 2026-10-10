package service

import (
	"context"
	"slices"
	"time"

	"github.com/chinny/goldberry/internal/store"
)

// Graph ranges: the presets plus a custom pair of dates.
const (
	RangeWeek    = "1w"
	RangeMonth   = "1m"
	RangeQuarter = "3m"
	RangeCustom  = "custom"
)

// RangePresets are the preset ranges, in display order.
var RangePresets = []struct{ Key, Label string }{
	{RangeWeek, "1W"}, {RangeMonth, "1M"}, {RangeQuarter, "3M"},
}

// History shows: the kid's total, every jar as its own line, or one jar's ID.
const (
	ShowTotal = "total"
	ShowJars  = "jars"
)

// HistoryRange is a resolved time window for a balance graph. From and To are
// instants, To exclusive; a range that runs to now ends a microsecond (the
// stored precision) after it, so an entry made this instant counts. FromDate and ToDate are the same window as household calendar
// days ("YYYY-MM-DD", both inclusive) for the custom-range form.
type HistoryRange struct {
	Preset   string
	From, To time.Time
	FromDate string
	ToDate   string
}

// ResolveRange turns a preset ("1w", "1m", "3m") or a custom pair of
// household dates into a window ending no later than now. An unknown preset
// with no dates is the last month.
func (s *Service) ResolveRange(ctx context.Context, preset, fromDate, toDate string) (HistoryRange, error) {
	h, err := s.Store.GetHousehold(ctx)
	if err != nil {
		return HistoryRange{}, err
	}
	return resolveRange(s.now(), h.Location(), preset, fromDate, toDate)
}

func resolveRange(now time.Time, loc *time.Location, preset, fromDate, toDate string) (HistoryRange, error) {
	local := now.In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	end := now.Add(time.Microsecond)
	r := HistoryRange{Preset: preset, To: end}
	if preset == RangeCustom || (preset == "" && (fromDate != "" || toDate != "")) {
		r.Preset = RangeCustom
		from, err := time.ParseInLocation(time.DateOnly, fromDate, loc)
		if err != nil {
			return r, invalid("from", "Pick a start date.")
		}
		to := today
		if toDate != "" {
			if to, err = time.ParseInLocation(time.DateOnly, toDate, loc); err != nil {
				return r, invalid("to", "Pick an end date.")
			}
		}
		if from.After(today) {
			return r, invalid("from", "That start date hasn’t happened yet.")
		}
		if from.After(to) {
			return r, invalid("from", "The start date has to be before the end date.")
		}
		r.From = from.UTC()
		if next := to.AddDate(0, 0, 1); next.Before(end) {
			r.To = next.UTC()
		}
	} else {
		switch preset {
		case RangeWeek:
			r.From = today.AddDate(0, 0, -6)
		case RangeQuarter:
			r.From = today.AddDate(0, -3, 0)
		default:
			r.Preset, r.From = RangeMonth, today.AddDate(0, -1, 0)
		}
		r.From = r.From.UTC()
	}
	r.FromDate = r.From.In(loc).Format(time.DateOnly)
	r.ToDate = r.To.Add(-time.Nanosecond).In(loc).Format(time.DateOnly)
	return r, nil
}

// HistoryPoint is one change on a graph line: the balance right after it.
// The entries behind it share Key (store.LedgerEntry.GroupKey), so a split
// deposit is one point.
type HistoryPoint struct {
	At      time.Time
	Balance int64
	Delta   int64
	Key     string
	Kind    store.EntryKind
	Comment string // the kid-visible comment; never the private note
}

// HistorySeries is one graph line: the total, or one jar.
type HistorySeries struct {
	Key       string // "total" or a jar ID
	Name      string
	Kind      store.JarKind // "" for the total
	Start     int64         // balance at the start of the range
	End       int64         // balance at the end of the range
	Low, High int64
	Points    []HistoryPoint
}

// Change is End − Start.
func (h HistorySeries) Change() int64 { return h.End - h.Start }

// BalanceHistory is a kid's balance over a range, ready to graph.
type BalanceHistory struct {
	Range  HistoryRange
	Show   string // "total", "jars" or a jar ID
	Jars   []store.Jar
	Series []HistorySeries
	// Entries are the changes behind the points, newest first: moves
	// between jars are left out of the total, since they don't change it.
	Entries []store.LedgerEntry
}

// BalanceHistory builds the graph lines for one kid over r. Handlers scope
// kidID from the session for kids, as with every other kid read.
func (s *Service) BalanceHistory(ctx context.Context, kidID string, r HistoryRange, show string) (BalanceHistory, error) {
	if _, err := getKid(ctx, s.Store, kidID); err != nil {
		return BalanceHistory{}, err
	}
	out := BalanceHistory{Range: r, Show: show}
	var err error
	if out.Jars, err = s.Store.ListJars(ctx, kidID); err != nil {
		return out, err
	}
	before, err := s.Store.BalancesBefore(ctx, kidID, r.From)
	if err != nil {
		return out, err
	}
	desc, err := s.Store.ListLedger(ctx, store.LedgerFilter{KidID: kidID, From: r.From, To: r.To})
	if err != nil {
		return out, err
	}
	asc := slices.Clone(desc)
	slices.Reverse(asc)

	switch {
	case show == ShowJars:
		for _, j := range out.Jars {
			out.Series = append(out.Series, buildSeries(j.ID, j.Name, j.Kind, before[j.ID], asc, onJar(j.ID)))
		}
		out.Entries = desc
	case show != ShowTotal && show != "" && slices.ContainsFunc(out.Jars, func(j store.Jar) bool { return j.ID == show }):
		j := out.Jars[slices.IndexFunc(out.Jars, func(j store.Jar) bool { return j.ID == show })]
		out.Series = []HistorySeries{buildSeries(j.ID, j.Name, j.Kind, before[j.ID], asc, onJar(j.ID))}
		out.Entries = filter(desc, onJar(j.ID))
	default:
		out.Show = ShowTotal
		var start int64
		for _, v := range before {
			start += v
		}
		notMove := func(e store.LedgerEntry) bool { return e.TransferID == "" }
		out.Series = []HistorySeries{buildSeries(ShowTotal, "Total", "", start, asc, notMove)}
		out.Entries = filter(desc, notMove)
	}
	return out, nil
}

func onJar(id string) func(store.LedgerEntry) bool {
	return func(e store.LedgerEntry) bool { return e.JarID == id }
}

func filter(entries []store.LedgerEntry, keep func(store.LedgerEntry) bool) []store.LedgerEntry {
	var out []store.LedgerEntry
	for _, e := range entries {
		if keep(e) {
			out = append(out, e)
		}
	}
	return out
}

// buildSeries walks entries (oldest first) from a starting balance. Entries
// sharing a GroupKey become one point; a group that nets to zero (a move,
// seen from the total) adds no point.
func buildSeries(key, name string, kind store.JarKind, start int64, asc []store.LedgerEntry, keep func(store.LedgerEntry) bool) HistorySeries {
	sr := HistorySeries{Key: key, Name: name, Kind: kind, Start: start, End: start, Low: start, High: start}
	entries := filter(asc, keep)
	for i := 0; i < len(entries); {
		first := entries[i]
		p := HistoryPoint{At: first.EffectiveAt, Key: first.GroupKey(), Kind: first.Kind}
		for ; i < len(entries) && entries[i].GroupKey() == p.Key; i++ {
			p.Delta += entries[i].Amount
			if p.Comment == "" {
				p.Comment = entries[i].Comment
			}
		}
		if p.Delta == 0 {
			continue
		}
		sr.End += p.Delta
		p.Balance = sr.End
		sr.Low, sr.High = min(sr.Low, sr.End), max(sr.High, sr.End)
		sr.Points = append(sr.Points, p)
	}
	return sr
}
