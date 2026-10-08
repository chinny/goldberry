package storetest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chinny/goldberry/internal/notify"
	"github.com/chinny/goldberry/internal/service"
	"github.com/chinny/goldberry/internal/store"
)

var goalTests = []struct {
	name string
	fn   func(*testing.T, Opener)
}{
	{"GoalsWaterfall", testGoalsWaterfall},
	{"GoalReachedOnce", testGoalReached},
	{"AskToBuy", testAskToBuy},
	{"InterestAverageDailyBalance", testInterest},
	{"InterestCapAndProjection", testInterestCap},
}

func (f *Fixture) saveDeposit(t *testing.T, kid store.User, amount int64) {
	t.Helper()
	_, err := f.Svc.PostEntry(context.Background(), f.Mom, service.EntryInput{KidID: kid.ID,
		JarID: f.jars(t, kid)[store.JarSave].ID, Amount: amount, Comment: "Birthday"})
	must(t, err)
}

func testGoalsWaterfall(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	sw, err := f.Svc.CreateGoal(ctx, f.Ava, "", service.GoalInput{Name: "Nintendo Switch", Emoji: "🎮", Target: 30000})
	must(t, err)
	lego, err := f.Svc.CreateGoal(ctx, f.Ava, "", service.GoalInput{Name: "LEGO set", Target: 5000})
	must(t, err)
	if sw.JarID != f.jars(t, f.Ava)[store.JarSave].ID || lego.Priority <= sw.Priority {
		t.Fatalf("goal defaults: %+v %+v", sw, lego)
	}
	if _, err := f.Svc.CreateGoal(ctx, f.Ava, "", service.GoalInput{Name: "", Target: 1}); err == nil {
		t.Fatal("nameless goal")
	}
	f.saveDeposit(t, f.Ava, 19200)
	views, err := f.Svc.Goals(ctx, f.Store, f.Ava.ID)
	must(t, err)
	if views[0].Funded != 19200 || views[0].Percent() != 64 || views[1].Funded != 0 {
		t.Fatalf("waterfall %+v", views)
	}
	// Reprioritise: LEGO first fills completely, the Switch gets the rest.
	must(t, f.Svc.UpdateGoal(ctx, f.Ava, lego.ID, service.GoalInput{Name: "LEGO set", Target: 5000, Priority: -1}))
	views, err = f.Svc.Goals(ctx, f.Store, f.Ava.ID)
	must(t, err)
	if views[0].Name != "LEGO set" || !views[0].Reached() || views[1].Funded != 14200 {
		t.Fatalf("after reorder %+v", views)
	}
	// Held money doesn't count; moving money never strands it in a goal.
	_, err = f.Svc.CreateRequest(ctx, f.Ava, service.RequestInput{JarID: sw.JarID, Amount: 4200, Reason: "Book"})
	must(t, err)
	views, _ = f.Svc.Goals(ctx, f.Store, f.Ava.ID)
	if views[1].Funded != 10000 {
		t.Fatalf("holds not subtracted: %+v", views[1])
	}
	impacts, err := f.Svc.GoalImpacts(ctx, f.Ava.ID, sw.JarID, 10000)
	must(t, err)
	// $150 available: LEGO keeps its $50 (100%), the Switch drops from $100 (33%) to $0.
	if len(impacts) != 1 || impacts[0].Name != "Nintendo Switch" || impacts[0].Before != 33 || impacts[0].After != 0 {
		t.Fatalf("impacts %+v", impacts)
	}
	// A sibling can't touch Ava's goal; parents can.
	if err := f.Svc.UpdateGoal(ctx, f.Leo, sw.ID, service.GoalInput{Name: "Mine", Target: 1}); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("sibling edited a goal: %v", err)
	}
	must(t, f.Svc.ArchiveGoal(ctx, f.Dad, sw.ID))
	views, _ = f.Svc.Goals(ctx, f.Store, f.Ava.ID)
	if len(views) != 1 {
		t.Fatalf("archived goal still listed")
	}
}

func testGoalReached(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	_, err := f.Svc.CreateGoal(ctx, f.Mom, f.Ava.ID, service.GoalInput{Name: "LEGO set", Target: 5000})
	must(t, err)
	f.saveDeposit(t, f.Ava, 4999)
	n, err := f.Svc.CheckGoals(ctx)
	must(t, err)
	if n != 0 {
		t.Fatal("reached early")
	}
	f.saveDeposit(t, f.Ava, 1)
	n, err = f.Svc.CheckGoals(ctx)
	must(t, err)
	if n != 1 {
		t.Fatalf("reached %d", n)
	}
	n, _ = f.Svc.CheckGoals(ctx)
	if n != 0 {
		t.Fatal("notified twice")
	}
	for _, u := range []store.User{f.Ava, f.Mom, f.Dad} {
		ns, err := f.Store.ListNotifications(ctx, u.ID, 1)
		must(t, err)
		if ns[0].Kind != notify.GoalReached {
			t.Errorf("%s not told", u.Username)
		}
	}
}

func testAskToBuy(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	g, err := f.Svc.CreateGoal(ctx, f.Ava, "", service.GoalInput{Name: "LEGO set", Target: 5000})
	must(t, err)
	f.saveDeposit(t, f.Ava, 6000)
	other, err := f.Svc.CreateGoal(ctx, f.Leo, "", service.GoalInput{Name: "Leo's", Target: 1})
	must(t, err)
	if _, err := f.Svc.CreateRequest(ctx, f.Ava, service.RequestInput{GoalID: other.ID, Amount: 1, Reason: "x"}); err == nil {
		t.Fatal("asked to buy a sibling's goal")
	}
	r, err := f.Svc.CreateRequest(ctx, f.Ava, service.RequestInput{GoalID: g.ID, Amount: 5000, Reason: "LEGO set"})
	must(t, err)
	if r.JarID != g.JarID || r.GoalID != g.ID {
		t.Fatalf("request %+v", r)
	}
	_, err = f.Svc.DecideRequest(ctx, f.Mom, r.ID, service.Decision{Approve: true})
	must(t, err)
	got, err := f.Store.GetGoal(ctx, g.ID)
	must(t, err)
	if got.ArchivedAt == nil {
		t.Fatal("bought goal still open")
	}
	f.wantBalances(t, f.Ava, 0, 1000, 0)
}

// testInterest: 1%/month on the average end-of-day balance in household time.
func testInterest(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open) // Sat Mar 7 2026, New York
	save := f.jars(t, f.Ava)[store.JarSave]
	if err := f.Svc.SetInterest(ctx, f.Ava, f.Ava.ID, save.ID, 100, 0, true); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("kid set their own interest: %v", err)
	}
	must(t, f.Svc.SetInterest(ctx, f.Mom, f.Ava.ID, save.ID, 100, 0, true))
	f.saveDeposit(t, f.Ava, 31000) // from Mar 7: 25 of 31 days at $310 → avg $250
	// The 30th-of-the-month trick: a big deposit late in the month earns little.
	f.Clock.Advance(23*24*time.Hour + 10*time.Hour) // Mar 30, 21:00 local (EDT)
	f.saveDeposit(t, f.Ava, 1_000_000)
	n, err := f.Svc.PostInterest(ctx)
	must(t, err)
	if n != 0 {
		t.Fatal("paid March interest in March")
	}
	f.Clock.Advance(28 * time.Hour) // Apr 1, 01:00 local
	n, err = f.Svc.PostInterest(ctx)
	must(t, err)
	if n != 1 {
		t.Fatalf("posted %d", n)
	}
	// End-of-day balances: Mar 1–6 $0, Mar 7–29 $310 (23 days), Mar 30–31 $10,310
	// (2 days) → sum $27,750 → avg $895.16 → 1% = $8.95.
	es, err := f.Store.ListLedger(ctx, store.LedgerFilter{KidID: f.Ava.ID, JarID: save.ID, Limit: 1})
	must(t, err)
	if es[0].Kind != store.KindInterest || es[0].Amount != 895 || !strings.Contains(es[0].Comment, "avg $895.16 × 1% · 31 days") {
		t.Fatalf("interest entry %+v", es[0])
	}
	n, _ = f.Svc.PostInterest(ctx)
	if n != 0 {
		t.Fatal("paid twice")
	}
}

func testInterestCap(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	save := f.jars(t, f.Leo)[store.JarSave]
	f.saveDeposit(t, f.Leo, 100000)
	must(t, f.Svc.SetInterest(ctx, f.Mom, f.Leo.ID, save.ID, 500, 500, true)) // 5%, at most $5
	f.Clock.Advance(26 * 24 * time.Hour)                                      // into April
	_, err := f.Svc.PostInterest(ctx)
	must(t, err)
	if b := f.jars(t, f.Leo)[store.JarSave].Balance; b != 100500 {
		t.Fatalf("capped interest: balance %d", b)
	}
	// Zero rate earns nothing and posts nothing.
	must(t, f.Svc.SetInterest(ctx, f.Mom, f.Ava.ID, f.jars(t, f.Ava)[store.JarSave].ID, 0, 0, true))
	if got := service.Projection(10000, 100, 0, 12); got != 11266 {
		t.Fatalf("projection = %d, want 11266", got)
	}
	if got := service.Projection(10000, 1000, 50, 12); got != 10600 {
		t.Fatalf("capped projection = %d", got)
	}
}
