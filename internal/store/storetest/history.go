package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chinny/goldberry/internal/service"
	"github.com/chinny/goldberry/internal/store"
)

var historyTests = []struct {
	name string
	fn   func(*testing.T, Opener)
}{
	{"LedgerDateRange", testLedgerDateRange},
	{"BalanceHistory", testBalanceHistory},
	{"HistoryRanges", testHistoryRanges},
}

// historyFixture posts, in America/New_York:
//
//	Mar 7  +10.00 Spend
//	Mar 9  +10.00 split 70/20/10 (one batch)
//	Mar 10  3.00 moved Spend → Save
//	Mar 11 −2.00 Spend
//
// and a deposit for Leo, who must never show up in Ava's history.
func historyFixture(t *testing.T, open Opener) (*Fixture, map[store.JarKind]store.JarBalance) {
	t.Helper()
	ctx := context.Background()
	f := NewFixture(t, open)
	f.Deposit(t, f.Ava, 1000)
	f.Clock.Advance(48 * time.Hour)
	_, err := f.Svc.PostEntry(ctx, f.Mom, service.EntryInput{KidID: f.Ava.ID, Amount: 1000, UseSplit: true, Comment: "Allowance"})
	must(t, err)
	f.Clock.Advance(24 * time.Hour)
	j := f.jars(t, f.Ava)
	_, err = f.Svc.Transfer(ctx, f.Ava, service.TransferInput{FromJarID: j[store.JarSpend].ID, ToJarID: j[store.JarSave].ID, Amount: 300})
	must(t, err)
	f.Clock.Advance(24 * time.Hour)
	_, err = f.Svc.PostEntry(ctx, f.Mom, service.EntryInput{KidID: f.Ava.ID, Amount: 200, Remove: true, Comment: "Comic"})
	must(t, err)
	f.Deposit(t, f.Leo, 5000)
	return f, j
}

func testLedgerDateRange(t *testing.T, open Opener) {
	ctx := context.Background()
	f, j := historyFixture(t, open)
	ny, _ := time.LoadLocation("America/New_York")
	mar8 := time.Date(2026, 3, 8, 0, 0, 0, 0, ny).UTC()
	mar10 := time.Date(2026, 3, 10, 0, 0, 0, 0, ny).UTC()

	all, err := f.Store.ListLedger(ctx, store.LedgerFilter{KidID: f.Ava.ID, From: mar8})
	must(t, err)
	if len(all) != 6 { // 3 split parts, 2 move legs, 1 removal
		t.Fatalf("from Mar 8: %d entries, want 6", len(all))
	}
	win, err := f.Store.ListLedger(ctx, store.LedgerFilter{KidID: f.Ava.ID, From: mar8, To: mar10})
	must(t, err)
	if len(win) != 3 || win[0].BatchID == "" || win[0].GroupKey() != win[0].BatchID {
		t.Fatalf("Mar 8–9: %+v", win)
	}
	upTo, err := f.Store.ListLedger(ctx, store.LedgerFilter{KidID: f.Ava.ID, To: mar8})
	must(t, err)
	if len(upTo) != 1 || upTo[0].Amount != 1000 {
		t.Fatalf("before Mar 8: %+v", upTo)
	}

	before, err := f.Store.BalancesBefore(ctx, f.Ava.ID, mar10)
	must(t, err)
	if len(before) != 3 || before[j[store.JarSpend].ID] != 1700 || before[j[store.JarSave].ID] != 200 || before[j[store.JarGive].ID] != 100 {
		t.Fatalf("balances before Mar 10: %v", before)
	}
	none, err := f.Store.BalancesBefore(ctx, f.Ava.ID, mar8.Add(-72*time.Hour))
	must(t, err)
	if len(none) != 0 {
		t.Fatalf("balances before any entry: %v", none)
	}
}

func testBalanceHistory(t *testing.T, open Opener) {
	ctx := context.Background()
	f, j := historyFixture(t, open)
	rng, err := f.Svc.ResolveRange(ctx, service.RangeCustom, "2026-03-08", "2026-03-11")
	must(t, err)

	total, err := f.Svc.BalanceHistory(ctx, f.Ava.ID, rng, "")
	must(t, err)
	if total.Show != service.ShowTotal || len(total.Series) != 1 {
		t.Fatalf("total: %+v", total)
	}
	s := total.Series[0]
	// The split is one point and the move, which doesn't change the total, none.
	if s.Start != 1000 || s.End != 1800 || s.Low != 1000 || s.High != 2000 || len(s.Points) != 2 ||
		s.Points[0].Delta != 1000 || s.Points[0].Balance != 2000 || s.Points[0].Comment != "Allowance" ||
		s.Points[1].Delta != -200 || s.Points[1].Balance != 1800 {
		t.Fatalf("total series: %+v", s)
	}
	for _, e := range total.Entries {
		if e.TransferID != "" {
			t.Fatal("total lists a move between jars")
		}
	}
	if len(total.Entries) != 4 || total.Entries[0].Amount != -200 {
		t.Fatalf("total entries (newest first): %+v", total.Entries)
	}

	jars, err := f.Svc.BalanceHistory(ctx, f.Ava.ID, rng, service.ShowJars)
	must(t, err)
	want := map[store.JarKind][2]int64{store.JarSpend: {1000, 1200}, store.JarSave: {0, 500}, store.JarGive: {0, 100}}
	if len(jars.Series) != 3 || len(jars.Entries) != 6 {
		t.Fatalf("every jar: %d series, %d entries", len(jars.Series), len(jars.Entries))
	}
	for _, sr := range jars.Series {
		if w := want[sr.Kind]; sr.Start != w[0] || sr.End != w[1] {
			t.Fatalf("%s: %d → %d, want %v", sr.Name, sr.Start, sr.End, w)
		}
	}

	save, err := f.Svc.BalanceHistory(ctx, f.Ava.ID, rng, j[store.JarSave].ID)
	must(t, err)
	if len(save.Series) != 1 || save.Series[0].End != 500 || len(save.Series[0].Points) != 2 || len(save.Entries) != 2 {
		t.Fatalf("save: %+v", save)
	}

	// A sibling's jar isn't a line on Ava's graph: it falls back to the total.
	leoJars, err := f.Store.ListJars(ctx, f.Leo.ID)
	must(t, err)
	other, err := f.Svc.BalanceHistory(ctx, f.Ava.ID, rng, leoJars[0].ID)
	must(t, err)
	if other.Show != service.ShowTotal || other.Series[0].End != 1800 {
		t.Fatalf("sibling's jar: %+v", other)
	}
	if _, err := f.Svc.BalanceHistory(ctx, f.Mom.ID, rng, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("history for an admin: %v", err)
	}
}

func testHistoryRanges(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open) // now: Mar 7 2026, 10:00 in New York
	ny, _ := time.LoadLocation("America/New_York")
	now := f.Clock.Now().Add(time.Microsecond) // a range to now ends just after it

	for _, tc := range []struct {
		preset, from, to string
		wantFrom         time.Time
		wantTo           time.Time
		wantPreset       string
	}{
		{"1w", "", "", time.Date(2026, 3, 1, 0, 0, 0, 0, ny), now, "1w"},
		{"1m", "", "", time.Date(2026, 2, 7, 0, 0, 0, 0, ny), now, "1m"},
		{"3m", "", "", time.Date(2025, 12, 7, 0, 0, 0, 0, ny), now, "3m"},
		{"bogus", "", "", time.Date(2026, 2, 7, 0, 0, 0, 0, ny), now, "1m"},
		{"custom", "2026-01-01", "2026-01-31", time.Date(2026, 1, 1, 0, 0, 0, 0, ny), time.Date(2026, 2, 1, 0, 0, 0, 0, ny), "custom"},
		{"", "2026-03-01", "", time.Date(2026, 3, 1, 0, 0, 0, 0, ny), now, "custom"},
		{"custom", "2026-03-01", "2026-03-20", time.Date(2026, 3, 1, 0, 0, 0, 0, ny), now, "custom"}, // capped at now
	} {
		r, err := f.Svc.ResolveRange(ctx, tc.preset, tc.from, tc.to)
		must(t, err)
		if !r.From.Equal(tc.wantFrom) || !r.To.Equal(tc.wantTo) || r.Preset != tc.wantPreset {
			t.Errorf("%q %q %q: %s → %s (%s), want %s → %s", tc.preset, tc.from, tc.to, r.From, r.To, r.Preset, tc.wantFrom, tc.wantTo)
		}
	}
	r, err := f.Svc.ResolveRange(ctx, "custom", "2026-01-01", "2026-01-31")
	must(t, err)
	if r.FromDate != "2026-01-01" || r.ToDate != "2026-01-31" {
		t.Errorf("custom dates: %s – %s", r.FromDate, r.ToDate)
	}

	var ue *service.UserError
	for _, bad := range [][2]string{{"2026-03-05", "2026-03-01"}, {"2026-04-01", ""}, {"yesterday", ""}, {"", "2026-03-01"}} {
		if _, err := f.Svc.ResolveRange(ctx, "custom", bad[0], bad[1]); !errors.As(err, &ue) {
			t.Errorf("custom %v: %v", bad, err)
		}
	}
}
