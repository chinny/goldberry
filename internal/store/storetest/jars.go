package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chinny/goldberry/internal/notify"
	"github.com/chinny/goldberry/internal/service"
	"github.com/chinny/goldberry/internal/store"
)

// jars returns a kid's jars by kind.
func (f *Fixture) jars(t *testing.T, kid store.User) map[store.JarKind]store.JarBalance {
	t.Helper()
	all, err := f.Store.JarBalances(context.Background(), kid.ID)
	must(t, err)
	out := map[store.JarKind]store.JarBalance{}
	for _, j := range all {
		out[j.Kind] = j
	}
	return out
}

func (f *Fixture) wantBalances(t *testing.T, kid store.User, spend, save, give int64) {
	t.Helper()
	j := f.jars(t, kid)
	if j[store.JarSpend].Balance != spend || j[store.JarSave].Balance != save || j[store.JarGive].Balance != give {
		t.Fatalf("%s jars = %d / %d / %d, want %d / %d / %d", kid.Username,
			j[store.JarSpend].Balance, j[store.JarSave].Balance, j[store.JarGive].Balance, spend, save, give)
	}
}

var jarTests = []struct {
	name string
	fn   func(*testing.T, Opener)
}{
	{"DefaultJars", testDefaultJars},
	{"SplitDeposit", testSplitDeposit},
	{"Transfer", testTransfer},
	{"ParentLock", testParentLock},
	{"SelfLockGauntlet", testSelfLockGauntlet},
	{"RouteLock", testRouteLock},
	{"JarManagement", testJarManagement},
	{"Allowance", testAllowance},
	{"AllowanceCatchUpAndPause", testAllowanceCatchUp},
	{"BackfillDefaultJars", testBackfill},
}

func testDefaultJars(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	v, err := f.Svc.KidOverview(ctx, f.Ava.ID, 0)
	must(t, err)
	if len(v.Jars) != 3 || v.Jars[0].Name != "Spend" || v.Jars[1].Name != "Save" || v.Jars[2].Name != "Give" {
		t.Fatalf("jars %+v", v.Jars)
	}
	if v.SplitSummary() != "70 / 20 / 10" {
		t.Fatalf("split %q", v.SplitSummary())
	}
}

func testSplitDeposit(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	in := service.EntryInput{KidID: f.Ava.ID, Amount: 1001, UseSplit: true, Comment: "Birthday", IdempotencyKey: "form-split"}
	es, err := f.Svc.PostEntry(ctx, f.Mom, in)
	must(t, err)
	if len(es) != 3 || es[0].BatchID == "" || es[0].BatchID != es[2].BatchID {
		t.Fatalf("entries %+v", es)
	}
	f.wantBalances(t, f.Ava, 701, 200, 100) // remainder cent to Spend
	again, err := f.Svc.PostEntry(ctx, f.Mom, in)
	must(t, err)
	if len(again) != 3 || again[0].ID != es[0].ID {
		t.Fatalf("double submit of a split posted again: %+v", again)
	}
	f.wantBalances(t, f.Ava, 701, 200, 100)
	if _, err := f.Svc.PostEntry(ctx, f.Mom, service.EntryInput{KidID: f.Ava.ID, Amount: 5, UseSplit: true, Remove: true}); err == nil {
		t.Fatal("split removal accepted")
	}
	// Undoing any part undoes the whole deposit.
	rs, err := f.Svc.Reverse(ctx, f.Dad, es[1].ID)
	must(t, err)
	if len(rs) != 3 {
		t.Fatalf("batch reversal posted %d entries", len(rs))
	}
	f.wantBalances(t, f.Ava, 0, 0, 0)
	ns, err := f.Store.ListNotifications(ctx, f.Ava.ID, 2)
	must(t, err)
	if ns[0].Kind != notify.EntryReversed || ns[1].Kind != notify.FundsAdded {
		t.Fatalf("notifications %v %v", ns[0].Kind, ns[1].Kind)
	}
}

func testTransfer(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	f.Deposit(t, f.Ava, 1000)
	j := f.jars(t, f.Ava)
	in := service.TransferInput{KidID: f.Ava.ID, FromJarID: j[store.JarSpend].ID, ToJarID: j[store.JarSave].ID, Amount: 300, IdempotencyKey: "mv-1"}
	pair, err := f.Svc.Transfer(ctx, f.Ava, in)
	must(t, err)
	if len(pair) != 2 || pair[0].TransferID == "" || pair[0].TransferID != pair[1].TransferID ||
		pair[0].Kind != store.KindTransferOut || pair[1].Kind != store.KindTransferIn {
		t.Fatalf("pair %+v", pair)
	}
	_, err = f.Svc.Transfer(ctx, f.Ava, in) // double submit
	must(t, err)
	f.wantBalances(t, f.Ava, 700, 300, 0)
	if _, err := f.Svc.Reverse(ctx, f.Mom, pair[0].ID); !errors.Is(err, service.ErrCannotReverseMove) {
		t.Fatalf("reversed half a move: %v", err)
	}

	var ins *service.ErrInsufficient
	in.Amount, in.IdempotencyKey = 701, ""
	if _, err := f.Svc.Transfer(ctx, f.Ava, in); !errors.As(err, &ins) {
		t.Fatalf("overdraw: %v", err)
	}
	in.ToJarID = in.FromJarID
	if _, err := f.Svc.Transfer(ctx, f.Ava, in); err == nil {
		t.Fatal("same-jar move accepted")
	}
	// A sibling can't move Ava's money: their own ID is forced.
	if _, err := f.Svc.Transfer(ctx, f.Leo, service.TransferInput{KidID: f.Ava.ID, FromJarID: j[store.JarSpend].ID,
		ToJarID: j[store.JarSave].ID, Amount: 1}); err == nil {
		t.Fatal("sibling moved money")
	}
	// Held money can't be moved.
	_, err = request(t, f, f.Ava, 600, "")
	must(t, err)
	in = service.TransferInput{FromJarID: j[store.JarSpend].ID, ToJarID: j[store.JarGive].ID, Amount: 200}
	if _, err := f.Svc.Transfer(ctx, f.Ava, in); !errors.As(err, &ins) || ins.Available != 100 {
		t.Fatalf("moved held money: %v", err)
	}
}

func testParentLock(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	f.Deposit(t, f.Ava, 1000)
	j := f.jars(t, f.Ava)
	give, spend := j[store.JarGive], j[store.JarSpend]
	_, err := f.Svc.Transfer(ctx, f.Mom, service.TransferInput{KidID: f.Ava.ID, FromJarID: spend.ID, ToJarID: give.ID, Amount: 500})
	must(t, err)
	// Locked until tomorrow (household time is America/New_York; the clock
	// reads 10:00 on Saturday Mar 7 there).
	lock, err := f.Svc.SetLock(ctx, f.Mom, service.LockInput{JarID: give.ID, Reason: "Give goes out with a parent", Until: "2026-03-08"})
	must(t, err)
	if !lock.Hard() {
		t.Fatal("admin lock should be hard")
	}
	var locked *service.ErrJarLocked
	if _, err := f.Svc.Transfer(ctx, f.Ava, service.TransferInput{FromJarID: give.ID, ToJarID: spend.ID, Amount: 1}); !errors.As(err, &locked) {
		t.Fatalf("kid moved out of a parent-locked jar: %v", err)
	}
	if _, err := f.Svc.CreateRequest(ctx, f.Ava, service.RequestInput{JarID: give.ID, Amount: 1, Reason: "x"}); !errors.As(err, &locked) {
		t.Fatalf("kid requested from a parent-locked jar: %v", err)
	}
	if _, _, err := f.Svc.StartOverride(ctx, f.Ava, give.ID); !errors.As(err, &locked) {
		t.Fatalf("gauntlet offered for a parent lock: %v", err)
	}
	if err := f.Svc.RemoveLock(ctx, f.Ava, lock.ID, ""); !errors.As(err, &locked) {
		t.Fatalf("kid removed a parent lock: %v", err)
	}
	// Locks are guardrails for kids, not parents.
	_, err = f.Svc.PostEntry(ctx, f.Mom, service.EntryInput{KidID: f.Ava.ID, JarID: give.ID, Amount: 100, Remove: true})
	must(t, err)
	// It lifts at local midnight, not UTC midnight: 23:30 Saturday local is still locked.
	f.Clock.Advance(13*time.Hour + 30*time.Minute)
	if _, err := f.Svc.Transfer(ctx, f.Ava, service.TransferInput{FromJarID: give.ID, ToJarID: spend.ID, Amount: 1}); !errors.As(err, &locked) {
		t.Fatalf("lock lifted early: %v", err)
	}
	f.Clock.Advance(time.Hour)
	_, err = f.Svc.Transfer(ctx, f.Ava, service.TransferInput{FromJarID: give.ID, ToJarID: spend.ID, Amount: 1})
	must(t, err)
}

func testSelfLockGauntlet(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	f.Deposit(t, f.Ava, 2000)
	j := f.jars(t, f.Ava)
	save, spend, give := j[store.JarSave], j[store.JarSpend], j[store.JarGive]
	_, err := f.Svc.Transfer(ctx, f.Ava, service.TransferInput{FromJarID: spend.ID, ToJarID: save.ID, Amount: 1500})
	must(t, err)
	if _, err := f.Svc.SetLock(ctx, f.Ava, service.LockInput{JarID: save.ID}); err == nil {
		t.Fatal("self-lock without a reason")
	}
	if _, err := f.Svc.SetLock(ctx, f.Ava, service.LockInput{JarID: f.jars(t, f.Leo)[store.JarSave].ID, Reason: "x"}); err == nil {
		t.Fatal("kid locked a sibling's jar")
	}
	lock, err := f.Svc.SetLock(ctx, f.Ava, service.LockInput{JarID: save.ID, Reason: "Saving for a Switch"})
	must(t, err)
	if lock.Hard() {
		t.Fatal("self-lock should be soft")
	}

	move := service.TransferInput{FromJarID: save.ID, ToJarID: spend.ID, Amount: 600}
	var needs *service.ErrNeedsOverride
	if _, err := f.Svc.Transfer(ctx, f.Ava, move); !errors.As(err, &needs) || needs.Locks[0].Reason != "Saving for a Switch" {
		t.Fatalf("want gauntlet, got %v", err)
	}
	token, locks, err := f.Svc.StartOverride(ctx, f.Ava, save.ID)
	must(t, err)
	if len(locks) != 1 {
		t.Fatalf("locks %+v", locks)
	}
	move.Override = token
	if _, err := f.Svc.Transfer(ctx, f.Ava, move); !errors.Is(err, service.ErrOverrideTooSoon) {
		t.Fatalf("skipped the countdown: %v", err)
	}
	f.Clock.Advance(12 * time.Second)
	if _, err := f.Svc.Transfer(ctx, f.Ava, move); !errors.Is(err, service.ErrOverrideTooSoon) {
		t.Fatalf("skipped the hold: %v", err)
	}
	f.Clock.Advance(2 * time.Second)
	// The token is for Save only, and only for Ava.
	if _, err := f.Svc.Transfer(ctx, f.Ava, service.TransferInput{FromJarID: give.ID, ToJarID: spend.ID, Amount: 1, Override: token}); err != nil {
		var ins *service.ErrInsufficient
		if !errors.As(err, &ins) { // Give is empty and unlocked; the token is just ignored
			t.Fatalf("give: %v", err)
		}
	}
	_, err = f.Svc.Transfer(ctx, f.Ava, move) // just this once
	must(t, err)
	f.wantBalances(t, f.Ava, 1100, 900, 0)
	move.Override = ""
	if _, err := f.Svc.Transfer(ctx, f.Ava, move); !errors.As(err, &needs) {
		t.Fatalf("lock gone after a one-time override: %v", err)
	}
	overrides, err := f.Svc.LockActivity(ctx, f.Ava.ID, 0)
	must(t, err)
	if len(overrides) != 1 || overrides[0].Action != "once" || overrides[0].LedgerEntryID == "" {
		t.Fatalf("overrides %+v", overrides)
	}
	ns, err := f.Store.ListNotifications(ctx, f.Mom.ID, 1)
	must(t, err)
	if ns[0].Kind != notify.LockOverridden {
		t.Fatalf("admin not told: %s", ns[0].Kind)
	}

	// Tokens expire, and removing the lock also takes the gauntlet.
	token, _, err = f.Svc.StartOverride(ctx, f.Ava, save.ID)
	must(t, err)
	f.Clock.Advance(11 * time.Minute)
	if err := f.Svc.RemoveLock(ctx, f.Ava, lock.ID, token); !errors.Is(err, service.ErrOverrideInvalid) {
		t.Fatalf("stale token: %v", err)
	}
	if err := f.Svc.RemoveLock(ctx, f.Ava, lock.ID, "forged.token"); !errors.Is(err, service.ErrOverrideInvalid) {
		t.Fatalf("forged token: %v", err)
	}
	token, _, err = f.Svc.StartOverride(ctx, f.Ava, save.ID)
	must(t, err)
	f.Clock.Advance(15 * time.Second)
	must(t, f.Svc.RemoveLock(ctx, f.Ava, lock.ID, token))
	_, err = f.Svc.Transfer(ctx, f.Ava, move)
	must(t, err)
	// A request through a self-lock can remove it in the same step.
	lock2, err := f.Svc.SetLock(ctx, f.Ava, service.LockInput{JarID: save.ID, Reason: "Really saving now"})
	must(t, err)
	token, _, err = f.Svc.StartOverride(ctx, f.Ava, save.ID)
	must(t, err)
	f.Clock.Advance(14 * time.Second)
	r, err := f.Svc.CreateRequest(ctx, f.Ava, service.RequestInput{JarID: save.ID, Amount: 100, Reason: "Book", Override: token, RemoveLock: true})
	must(t, err)
	got, err := f.Store.GetLock(ctx, lock2.ID)
	must(t, err)
	if got.RemovedAt == nil || r.JarID != save.ID {
		t.Fatal("lock not removed by the request")
	}
	// Admins remove any lock without the gauntlet.
	lock3, err := f.Svc.SetLock(ctx, f.Ava, service.LockInput{JarID: save.ID, Reason: "again"})
	must(t, err)
	must(t, f.Svc.RemoveLock(ctx, f.Dad, lock3.ID, ""))
}

func testRouteLock(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	_, err := f.Svc.PostEntry(ctx, f.Mom, service.EntryInput{KidID: f.Ava.ID, Amount: 1000, UseSplit: true})
	must(t, err)
	j := f.jars(t, f.Ava)
	save, spend, give := j[store.JarSave], j[store.JarSpend], j[store.JarGive]
	_, err = f.Svc.SetLock(ctx, f.Ava, service.LockInput{JarID: save.ID, ToJarID: spend.ID, Reason: "Not into Spend"})
	must(t, err)
	var needs *service.ErrNeedsOverride
	if _, err := f.Svc.Transfer(ctx, f.Ava, service.TransferInput{FromJarID: save.ID, ToJarID: spend.ID, Amount: 10}); !errors.As(err, &needs) {
		t.Fatalf("route not locked: %v", err)
	}
	_, err = f.Svc.Transfer(ctx, f.Ava, service.TransferInput{FromJarID: save.ID, ToJarID: give.ID, Amount: 10})
	must(t, err)
	_, err = f.Svc.CreateRequest(ctx, f.Ava, service.RequestInput{JarID: save.ID, Amount: 10, Reason: "ok"})
	must(t, err)
	if _, err := f.Svc.SetLock(ctx, f.Ava, service.LockInput{JarID: save.ID, ToJarID: save.ID, Reason: "x"}); err == nil {
		t.Fatal("route to itself accepted")
	}
}

func testJarManagement(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	jar, err := f.Svc.AddJar(ctx, f.Mom, f.Ava.ID, "Lego fund")
	must(t, err)
	if _, err := f.Svc.AddJar(ctx, f.Mom, f.Ava.ID, "lego FUND"); err == nil {
		t.Fatal("duplicate jar name")
	}
	if _, err := f.Svc.AddJar(ctx, f.Ava, f.Ava.ID, "Mine"); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("kid added a jar: %v", err)
	}
	j := f.jars(t, f.Ava)
	if err := f.Svc.SetSplit(ctx, f.Mom, f.Ava.ID, map[string]int{j[store.JarSpend].ID: 5000, j[store.JarSave].ID: 2000}); err == nil {
		t.Fatal("split under 100% accepted")
	}
	must(t, f.Svc.SetSplit(ctx, f.Mom, f.Ava.ID, map[string]int{j[store.JarSpend].ID: 5000, j[store.JarSave].ID: 2000,
		j[store.JarGive].ID: 1000, jar.ID: 2000}))
	es, err := f.Svc.PostEntry(ctx, f.Mom, service.EntryInput{KidID: f.Ava.ID, Amount: 1000, UseSplit: true})
	must(t, err)
	if len(es) != 4 {
		t.Fatalf("4-way split posted %d", len(es))
	}
	must(t, f.Svc.RenameJar(ctx, f.Mom, jar.ID, "Lego"))
	if err := f.Svc.ArchiveJar(ctx, f.Mom, jar.ID); err == nil {
		t.Fatal("archived a jar with money")
	}
	_, err = f.Svc.Transfer(ctx, f.Mom, service.TransferInput{KidID: f.Ava.ID, FromJarID: jar.ID, ToJarID: j[store.JarSpend].ID, Amount: 200})
	must(t, err)
	if err := f.Svc.ArchiveJar(ctx, f.Mom, jar.ID); err == nil {
		t.Fatal("archived a jar with a split share")
	}
	must(t, f.Svc.SetSplit(ctx, f.Mom, f.Ava.ID, map[string]int{j[store.JarSpend].ID: 7000, j[store.JarSave].ID: 2000, j[store.JarGive].ID: 1000}))
	must(t, f.Svc.ArchiveJar(ctx, f.Mom, jar.ID))
	v, err := f.Svc.KidOverview(ctx, f.Ava.ID, 0)
	must(t, err)
	if len(v.Jars) != 3 {
		t.Fatalf("archived jar still listed: %d", len(v.Jars))
	}
}

// testAllowance is Phase 4's exit criterion: Saturday's allowance posts split
// 70/20/10 with nobody touching it, including after a reboot on Friday night.
func testAllowance(t *testing.T, open Opener) {
	ctx := context.Background()
	t.Run("BeforeSetup", func(t *testing.T) { // the job runs before anyone sets up
		if n, err := service.New(open(t), nil).PostAllowance(ctx); err != nil || n != 0 {
			t.Fatalf("before setup: %d, %v", n, err)
		}
	})
	f := NewFixture(t, open) // Saturday Mar 7, 10:00 in New York
	sc, err := f.Svc.CreateSchedule(ctx, f.Mom, f.Ava.ID, service.ScheduleInput{Amount: 1000, Cadence: "weekly", Weekday: int(time.Saturday)})
	must(t, err)
	if sc.CommentTemplate != "Weekly allowance" {
		t.Fatalf("comment %q", sc.CommentTemplate)
	}
	if _, err := f.Svc.CreateSchedule(ctx, f.Ava, f.Ava.ID, service.ScheduleInput{Amount: 1, Cadence: "weekly"}); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("kid gave themselves an allowance: %v", err)
	}
	views, err := f.Svc.Schedules(ctx, f.Ava.ID)
	must(t, err)
	if views[0].Next.String() != "2026-03-14" || views[0].Describe() != "every Saturday" {
		t.Fatalf("next %s %q", views[0].Next, views[0].Describe())
	}
	n, err := f.Svc.PostAllowance(ctx)
	must(t, err)
	if n != 0 {
		t.Fatal("setting up an allowance paid out by surprise")
	}

	// Friday Mar 13, 23:00 local (EDT since Mar 8): the box goes down. It
	// comes back on Sunday.
	f.Clock.Advance(6*24*time.Hour + 12*time.Hour)
	n, err = f.Svc.PostAllowance(ctx)
	must(t, err)
	if n != 0 {
		t.Fatal("paid on Friday")
	}
	f.Clock.Advance(36 * time.Hour) // Sunday Mar 15, 11:00
	n, err = f.Svc.PostAllowance(ctx)
	must(t, err)
	if n != 1 {
		t.Fatalf("posted %d days after the reboot, want Saturday's 1", n)
	}
	f.wantBalances(t, f.Ava, 700, 200, 100)
	entries, err := f.Store.ListLedger(ctx, store.LedgerFilter{KidID: f.Ava.ID})
	must(t, err)
	ny, _ := time.LoadLocation("America/New_York")
	for _, e := range entries {
		if e.Kind != store.KindAllowance || e.ActorID != "" || e.ScheduleID != sc.ID ||
			!e.EffectiveAt.Equal(time.Date(2026, 3, 14, 0, 0, 0, 0, ny)) {
			t.Fatalf("entry %+v", e)
		}
	}
	// A duplicate run (or a second replica) is a no-op.
	n, err = f.Svc.PostAllowance(ctx)
	must(t, err)
	if n != 0 {
		t.Fatal("double-posted")
	}
	f.wantBalances(t, f.Ava, 700, 200, 100)
	ns, err := f.Store.ListNotifications(ctx, f.Ava.ID, 1)
	must(t, err)
	if ns[0].Kind != notify.AllowancePosted {
		t.Fatalf("kid not told: %s", ns[0].Kind)
	}
	// A fixed jar instead of the split.
	j := f.jars(t, f.Leo)
	_, err = f.Svc.CreateSchedule(ctx, f.Mom, f.Leo.ID, service.ScheduleInput{Amount: 500, Cadence: "monthly", DayOfMonth: 31, TargetJarID: j[store.JarSave].ID})
	must(t, err)
	f.Clock.Advance(17 * 24 * time.Hour) // Apr 1
	_, err = f.Svc.PostAllowance(ctx)
	must(t, err)
	f.wantBalances(t, f.Leo, 0, 500, 0) // Mar 31
}

func testAllowanceCatchUp(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	sc, err := f.Svc.CreateSchedule(ctx, f.Mom, f.Ava.ID, service.ScheduleInput{Amount: 100, Cadence: "weekly", Weekday: int(time.Monday)})
	must(t, err)
	f.Clock.Advance(12 * 7 * 24 * time.Hour) // off for 12 weeks
	n, err := f.Svc.PostAllowance(ctx)
	must(t, err)
	if n != service.CatchUpLimit {
		t.Fatalf("caught up %d, want %d", n, service.CatchUpLimit)
	}
	f.wantBalances(t, f.Ava, 8*70, 8*20, 8*10)

	// Paused weeks are never back-paid.
	must(t, f.Svc.SetScheduleActive(ctx, f.Mom, sc.ID, false))
	f.Clock.Advance(3 * 7 * 24 * time.Hour)
	n, err = f.Svc.PostAllowance(ctx)
	must(t, err)
	if n != 0 {
		t.Fatal("paid while paused")
	}
	must(t, f.Svc.SetScheduleActive(ctx, f.Mom, sc.ID, true))
	n, err = f.Svc.PostAllowance(ctx)
	must(t, err)
	if n != 0 {
		t.Fatal("back-paid the pause")
	}
	f.Clock.Advance(7 * 24 * time.Hour)
	n, err = f.Svc.PostAllowance(ctx)
	must(t, err)
	if n != 1 {
		t.Fatalf("after resume posted %d", n)
	}
	// Editing keeps days already paid.
	must(t, f.Svc.UpdateSchedule(ctx, f.Mom, sc.ID, service.ScheduleInput{Amount: 200, Cadence: "weekly", Weekday: int(time.Monday)}))
	n, err = f.Svc.PostAllowance(ctx)
	must(t, err)
	if n != 0 {
		t.Fatal("edit re-paid")
	}
}

func testBackfill(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	// A kid from before jars existed: one Spend jar taking 100%.
	old := store.User{ID: store.NewID(), Role: store.RoleKid, Username: "old", DisplayName: "Old", CreatedAt: f.Clock.Now()}
	spend := store.Jar{ID: store.NewID(), KidID: old.ID, Name: "Spend", Kind: store.JarSpend}
	must(t, f.Store.Tx(ctx, "", func(tx store.Tx) error {
		if err := tx.CreateUser(ctx, old); err != nil {
			return err
		}
		if err := tx.CreateJar(ctx, spend); err != nil {
			return err
		}
		return tx.PutSplitRule(ctx, store.SplitRule{KidID: old.ID, JarID: spend.ID, BasisPoints: 10000})
	}))
	n, err := f.Svc.EnsureDefaultJars(ctx)
	must(t, err)
	if n != 1 {
		t.Fatalf("backfilled %d kids, want 1", n)
	}
	v, err := f.Svc.KidOverview(ctx, old.ID, 0)
	must(t, err)
	if len(v.Jars) != 3 || v.SplitSummary() != "70 / 20 / 10" {
		t.Fatalf("after backfill: %d jars, split %q", len(v.Jars), v.SplitSummary())
	}
	n, err = f.Svc.EnsureDefaultJars(ctx)
	must(t, err)
	if n != 0 {
		t.Fatal("backfill ran twice")
	}
}
