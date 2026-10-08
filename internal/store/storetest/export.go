package storetest

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/chinny/goldberry/internal/service"
	"github.com/chinny/goldberry/internal/store"
)

// Busy fills a fixture with a bit of everything, for export and backup tests.
func Busy(t *testing.T, f *Fixture) {
	t.Helper()
	ctx := context.Background()
	es, err := f.Svc.PostEntry(ctx, f.Mom, service.EntryInput{KidID: f.Ava.ID, Amount: 10000, UseSplit: true, Comment: "Birthday", PrivateNote: "from Grandma"})
	must(t, err)
	_, err = f.Svc.Reverse(ctx, f.Mom, es[0].ID)
	must(t, err)
	f.Deposit(t, f.Ava, 5000)
	f.Deposit(t, f.Leo, 2500)
	j := f.jars(t, f.Ava)
	_, err = f.Svc.Transfer(ctx, f.Ava, service.TransferInput{FromJarID: j[store.JarSpend].ID, ToJarID: j[store.JarSave].ID, Amount: 1000})
	must(t, err)
	r, err := request(t, f, f.Ava, 800, "k")
	must(t, err)
	_, err = f.Svc.DecideRequest(ctx, f.Dad, r.ID, service.Decision{Approve: true, ApprovedAmount: 600, Note: "less"})
	must(t, err)
	_, err = request(t, f, f.Leo, 100, "")
	must(t, err)
	_, err = f.Svc.SetLock(ctx, f.Ava, service.LockInput{JarID: j[store.JarSave].ID, Reason: "Saving for a Switch", Until: "2026-12-25"})
	must(t, err)
	_, err = f.Svc.CreateGoal(ctx, f.Ava, "", service.GoalInput{Name: "Switch", Emoji: "🎮", Target: 30000})
	must(t, err)
	_, err = f.Svc.CreateSchedule(ctx, f.Mom, f.Ava.ID, service.ScheduleInput{Amount: 1000, Cadence: "weekly", Weekday: 6})
	must(t, err)
	must(t, f.Svc.SetInterest(ctx, f.Mom, f.Ava.ID, j[store.JarSave].ID, 100, 500, true))
}

func snapshot(t *testing.T, ctx context.Context, st store.Reader, kids ...store.User) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	for _, k := range kids {
		jars, err := st.JarBalances(ctx, k.ID)
		must(t, err)
		for _, j := range jars {
			out[k.Username+"/"+j.Name] = j.Balance
			out[k.Username+"/"+j.Name+"/held"] = j.Held
		}
		es, err := st.ListLedger(ctx, store.LedgerFilter{KidID: k.ID})
		must(t, err)
		out[k.Username+"/entries"] = int64(len(es))
	}
	return out
}

func testExportImport(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	Busy(t, f)
	sqlStore, ok := f.Store.(*store.SQL)
	if !ok {
		t.Skip("store doesn't export")
	}
	var dump bytes.Buffer
	n, err := sqlStore.Export(ctx, &dump, f.Clock.Now())
	must(t, err)
	if n < 30 {
		t.Fatalf("exported only %d rows", n)
	}
	want := snapshot(t, ctx, f.Store, f.Ava, f.Leo)

	t.Run("IntoEmpty", func(t *testing.T) {
		dst := open(t).(*store.SQL)
		m, err := dst.Import(ctx, bytes.NewReader(dump.Bytes()))
		must(t, err)
		if m != n {
			t.Fatalf("imported %d of %d rows", m, n)
		}
		got := snapshot(t, ctx, dst, f.Ava, f.Leo)
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: %d, want %d", k, got[k], v)
			}
		}
		svc := service.New(dst, nil)
		svc.Now = f.Clock.Now
		if _, err := svc.Login(ctx, "ava", f.AvaPIN, false, ""); err != nil {
			t.Fatalf("kid can't sign in after import: %v", err)
		}
		v, err := svc.KidOverview(ctx, f.Ava.ID, 0)
		must(t, err)
		if len(v.Goals) != 1 || len(v.Schedules) != 1 || v.Jars[1].SelfLock() == nil || v.Interest[v.Jars[1].ID].MonthlyCap != 500 {
			t.Fatalf("lost goals/schedules/locks/interest: %+v", v)
		}
		// A second import is refused: the database isn't empty any more.
		if _, err := dst.Import(ctx, bytes.NewReader(dump.Bytes())); !errors.Is(err, store.ErrNotEmpty) {
			t.Fatalf("second import: %v", err)
		}
	})
	if _, err := sqlStore.Import(ctx, bytes.NewReader([]byte(`{"nope":1}`))); err == nil {
		t.Fatal("garbage accepted")
	}
}
