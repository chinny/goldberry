// Package storetest is the store contract suite (plan §9.1): every behaviour
// test — balances, holds, reversals, idempotency, the double-submit race —
// runs against both SQLite and Postgres. This suite, not the interface, is the
// real definition of "pluggable".
package storetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/chinny/goldberry/internal/notify"
	"github.com/chinny/goldberry/internal/service"
	"github.com/chinny/goldberry/internal/store"
)

// Opener returns a fresh, empty, migrated store for one test.
type Opener func(t *testing.T) store.Store

// Clock is a settable test clock.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

func NewClock() *Clock { return &Clock{t: time.Date(2026, 3, 7, 15, 0, 0, 0, time.UTC)} }

func (c *Clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *Clock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// Fixture is a household with two admins and two kids.
type Fixture struct {
	Svc         *service.Service
	Store       store.Store
	Clock       *Clock
	Mom, Dad    store.User
	Ava, Leo    store.User
	AvaPIN      string
	MomPassword string
}

// NewFixture sets up a household on a fresh store.
func NewFixture(t *testing.T, open Opener) *Fixture {
	t.Helper()
	ctx := context.Background()
	st := open(t)
	clk := NewClock()
	svc := service.New(st, nil)
	svc.Now = clk.Now
	f := &Fixture{Svc: svc, Store: st, Clock: clk, AvaPIN: "2468", MomPassword: "correct horse battery"}
	var err error
	f.Mom, err = svc.Setup(ctx, service.SetupInput{HouseholdName: "The Chins", Currency: "USD", Timezone: "America/New_York",
		Username: "Mom", DisplayName: "Mom", Password: f.MomPassword})
	must(t, err)
	f.Dad, err = svc.CreateAdmin(ctx, f.Mom, service.AdminInput{Username: "dad", DisplayName: "Dad", Password: "another long one"})
	must(t, err)
	f.Ava, err = svc.CreateKid(ctx, f.Mom, service.KidInput{Username: "ava", DisplayName: "Ava", PIN: f.AvaPIN})
	must(t, err)
	f.Leo, err = svc.CreateKid(ctx, f.Mom, service.KidInput{Username: "leo", DisplayName: "Leo", PIN: "1357"})
	must(t, err)
	return f
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// Deposit adds funds to a kid's first jar.
func (f *Fixture) Deposit(t *testing.T, kid store.User, amount int64) store.LedgerEntry {
	t.Helper()
	e, err := f.Svc.PostEntry(context.Background(), f.Mom, service.EntryInput{KidID: kid.ID, Amount: amount, Comment: "chores"})
	must(t, err)
	return e
}

// Jar returns a kid's first jar with balances.
func (f *Fixture) Jar(t *testing.T, kid store.User) store.JarBalance {
	t.Helper()
	jars, err := f.Store.JarBalances(context.Background(), kid.ID)
	must(t, err)
	if len(jars) == 0 {
		t.Fatal("no jars")
	}
	return jars[0]
}

func (f *Fixture) wantJar(t *testing.T, kid store.User, balance, held int64) {
	t.Helper()
	j := f.Jar(t, kid)
	if j.Balance != balance || j.Held != held {
		t.Fatalf("%s jar: balance %d held %d, want %d / %d", kid.Username, j.Balance, j.Held, balance, held)
	}
}

// Run runs the whole contract suite.
func Run(t *testing.T, open Opener) {
	tests := []struct {
		name string
		fn   func(*testing.T, Opener)
	}{
		{"Setup", testSetup},
		{"DepositsAndBalances", testDeposits},
		{"Idempotency", testIdempotency},
		{"RemoveRefusedBelowZero", testRemove},
		{"Reversal", testReversal},
		{"RequestHolds", testRequestHolds},
		{"ApproveLowerAmount", testApproveLower},
		{"DenyAndCancel", testDenyCancel},
		{"FirstDecisionWins", testFirstDecisionWins},
		{"PendingCap", testPendingCap},
		{"DoubleSubmitRace", testDoubleSubmitRace},
		{"SameKeyRace", testSameKeyRace},
		{"Expiry", testExpiry},
		{"LoginThrottleAndLock", testLoginLock},
		{"Sessions", testSessions},
		{"LastAdmin", testLastAdmin},
		{"Notifications", testNotifications},
		{"Timestamps", testTimestamps},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { tc.fn(t, open) })
	}
}

func testSetup(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	if _, err := f.Svc.Setup(ctx, service.SetupInput{HouseholdName: "x", Currency: "USD", Timezone: "UTC",
		Username: "evil", DisplayName: "Evil", Password: "0123456789ab"}); !errors.Is(err, service.ErrAlreadySetUp) {
		t.Fatalf("second setup: %v", err)
	}
	h, cur, err := f.Svc.Household(ctx)
	must(t, err)
	if h.Name != "The Chins" || cur.Code != "USD" || h.PINLength != 4 || h.RequestExpiryDays != 14 {
		t.Fatalf("household %+v", h)
	}
	if u, err := f.Store.GetUserByUsername(ctx, "MOM"); err != nil || u.ID != f.Mom.ID {
		t.Fatalf("usernames are case-insensitive: %v", err)
	}
	if _, err := f.Svc.CreateKid(ctx, f.Mom, service.KidInput{Username: "ava", DisplayName: "Ava 2", PIN: "1111"}); err == nil {
		t.Fatal("duplicate username accepted")
	}
	if _, err := f.Svc.CreateKid(ctx, f.Ava, service.KidInput{Username: "sib", DisplayName: "Sib", PIN: "1111"}); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("kid created a kid: %v", err)
	}
	f.wantJar(t, f.Ava, 0, 0)
}

func testDeposits(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	f.Deposit(t, f.Ava, 500)
	f.Deposit(t, f.Ava, 1025)
	f.Deposit(t, f.Leo, 100)
	f.wantJar(t, f.Ava, 1525, 0)
	f.wantJar(t, f.Leo, 100, 0)
	entries, err := f.Store.ListLedger(ctx, store.LedgerFilter{KidID: f.Ava.ID})
	must(t, err)
	if len(entries) != 2 || entries[0].Amount != 1025 || entries[0].ActorName != "Mom" || entries[0].JarName != "Spend" {
		t.Fatalf("ledger %+v", entries)
	}
	if _, err := f.Svc.PostEntry(ctx, f.Mom, service.EntryInput{KidID: f.Ava.ID, Amount: 0}); err == nil {
		t.Fatal("zero amount accepted")
	}
	if _, err := f.Svc.PostEntry(ctx, f.Mom, service.EntryInput{KidID: f.Mom.ID, Amount: 10}); err == nil {
		t.Fatal("deposit to an admin accepted")
	}
}

func testIdempotency(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	in := service.EntryInput{KidID: f.Ava.ID, Amount: 500, IdempotencyKey: "form-1"}
	a, err := f.Svc.PostEntry(ctx, f.Mom, in)
	must(t, err)
	b, err := f.Svc.PostEntry(ctx, f.Mom, in)
	must(t, err)
	if a.ID != b.ID {
		t.Fatalf("double submit made two entries: %s %s", a.ID, b.ID)
	}
	f.wantJar(t, f.Ava, 500, 0)
	// The same key can't be replayed against another kid.
	if _, err := f.Svc.PostEntry(ctx, f.Mom, service.EntryInput{KidID: f.Leo.ID, Amount: 500, IdempotencyKey: "form-1"}); err == nil {
		t.Fatal("key reused across kids")
	}
}

func testRemove(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	f.Deposit(t, f.Ava, 2500)
	_, err := f.Svc.PostEntry(ctx, f.Mom, service.EntryInput{KidID: f.Ava.ID, Amount: 3000, Remove: true})
	var ins *service.ErrInsufficient
	if !errors.As(err, &ins) || ins.Available != 2500 {
		t.Fatalf("want insufficient, got %v", err)
	}
	if ins.Error() != "Only $25.00 is available in Spend." {
		t.Fatalf("message %q", ins.Error())
	}
	_, err = f.Svc.PostEntry(ctx, f.Mom, service.EntryInput{KidID: f.Ava.ID, Amount: 1000, Remove: true, Comment: "candy"})
	must(t, err)
	f.wantJar(t, f.Ava, 1500, 0)

	h, _, _ := f.Svc.Household(ctx)
	must(t, f.Svc.UpdateHousehold(ctx, f.Mom, service.HouseholdInput{Name: h.Name, Timezone: h.Timezone, PINLength: 4,
		AllowNegative: true, RequestExpiryDays: 14}))
	_, err = f.Svc.PostEntry(ctx, f.Mom, service.EntryInput{KidID: f.Ava.ID, Amount: 5000, Remove: true, Comment: "window"})
	must(t, err)
	f.wantJar(t, f.Ava, -3500, 0)
}

func testReversal(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	e := f.Deposit(t, f.Ava, 5000)
	f.Deposit(t, f.Ava, 200)
	r1, err := f.Svc.Reverse(ctx, f.Dad, e.ID)
	must(t, err)
	if r1.Amount != -5000 || r1.Kind != store.KindReversal || r1.ReversesID != e.ID {
		t.Fatalf("reversal %+v", r1)
	}
	r2, err := f.Svc.Reverse(ctx, f.Dad, e.ID)
	must(t, err)
	if r2.ID != r1.ID {
		t.Fatal("second reverse posted another entry")
	}
	f.wantJar(t, f.Ava, 200, 0)
	if _, err := f.Svc.Reverse(ctx, f.Dad, r1.ID); !errors.Is(err, service.ErrCannotReverse) {
		t.Fatalf("reversed a reversal: %v", err)
	}
	got, err := f.Store.GetLedgerEntry(ctx, e.ID)
	must(t, err)
	if !got.Reversed() || got.ReversedByID != r1.ID {
		t.Fatalf("original not marked reversed: %+v", got)
	}
	// Reversing a deposit that's already been spent would go negative.
	d := f.Deposit(t, f.Leo, 1000)
	_, err = f.Svc.PostEntry(ctx, f.Mom, service.EntryInput{KidID: f.Leo.ID, Amount: 800, Remove: true})
	must(t, err)
	var ins *service.ErrInsufficient
	if _, err := f.Svc.Reverse(ctx, f.Mom, d.ID); !errors.As(err, &ins) {
		t.Fatalf("want insufficient, got %v", err)
	}
	if _, err := f.Svc.Reverse(ctx, f.Ava, d.ID); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("kid reversed: %v", err)
	}
}

func request(t *testing.T, f *Fixture, kid store.User, amount int64, key string) (store.WithdrawalRequest, error) {
	t.Helper()
	return f.Svc.CreateRequest(context.Background(), kid, service.RequestInput{Amount: amount, Reason: "Movie with Jess", IdempotencyKey: key})
}

func testRequestHolds(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	f.Deposit(t, f.Ava, 2500)
	r, err := request(t, f, f.Ava, 800, "")
	must(t, err)
	if r.Status != store.StatusPending || r.ExpiresAt == nil || !r.ExpiresAt.Equal(f.Clock.Now().Add(14*24*time.Hour)) {
		t.Fatalf("request %+v", r)
	}
	f.wantJar(t, f.Ava, 2500, 800)
	var ins *service.ErrInsufficient
	if _, err := request(t, f, f.Ava, 1800, ""); !errors.As(err, &ins) || ins.Available != 1700 {
		t.Fatalf("over-request across requests: %v", err)
	}
	// Holds also block admin debits of held money.
	if _, err := f.Svc.PostEntry(ctx, f.Mom, service.EntryInput{KidID: f.Ava.ID, Amount: 2000, Remove: true}); !errors.As(err, &ins) {
		t.Fatalf("admin debit spent held money: %v", err)
	}
	approved, err := f.Svc.DecideRequest(ctx, f.Mom, r.ID, service.Decision{Approve: true})
	must(t, err)
	if approved.Status != store.StatusApproved || approved.ApprovedAmount != 800 {
		t.Fatalf("approved %+v", approved)
	}
	f.wantJar(t, f.Ava, 1700, 0)
	entries, err := f.Store.ListLedger(ctx, store.LedgerFilter{KidID: f.Ava.ID, Limit: 1})
	must(t, err)
	if entries[0].RequestID != r.ID || entries[0].Amount != -800 || entries[0].Kind != store.KindWithdrawal {
		t.Fatalf("settling entry %+v", entries[0])
	}
	if _, err := f.Svc.CreateRequest(ctx, f.Mom, service.RequestInput{Amount: 1, Reason: "x"}); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("admin made a request: %v", err)
	}
}

func testApproveLower(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	f.Deposit(t, f.Leo, 3150)
	r, err := request(t, f, f.Leo, 2000, "")
	must(t, err)
	if _, err := f.Svc.DecideRequest(ctx, f.Dad, r.ID, service.Decision{Approve: true, ApprovedAmount: 2500}); err == nil {
		t.Fatal("approved more than asked")
	}
	got, err := f.Svc.DecideRequest(ctx, f.Dad, r.ID, service.Decision{Approve: true, ApprovedAmount: 1500, Note: "half now"})
	must(t, err)
	if got.Amount != 2000 || got.ApprovedAmount != 1500 {
		t.Fatalf("request keeps both numbers: %+v", got)
	}
	f.wantJar(t, f.Leo, 1650, 0)
	stored, err := f.Store.GetRequest(ctx, r.ID)
	must(t, err)
	if stored.ApprovedAmount != 1500 || stored.DecidedByName != "Dad" || stored.DecisionNote != "half now" || stored.DecidedAt == nil {
		t.Fatalf("stored %+v", stored)
	}
}

func testDenyCancel(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	f.Deposit(t, f.Ava, 1000)
	r1, err := request(t, f, f.Ava, 600, "")
	must(t, err)
	r2, err := request(t, f, f.Ava, 400, "")
	must(t, err)
	f.wantJar(t, f.Ava, 1000, 1000)
	_, err = f.Svc.DecideRequest(ctx, f.Mom, r1.ID, service.Decision{Approve: false, Note: "not tonight"})
	must(t, err)
	f.wantJar(t, f.Ava, 1000, 400)
	if err := f.Svc.CancelRequest(ctx, f.Leo, r2.ID); err == nil {
		t.Fatal("sibling cancelled a request")
	}
	must(t, f.Svc.CancelRequest(ctx, f.Ava, r2.ID))
	f.wantJar(t, f.Ava, 1000, 0)
	var done *service.ErrAlreadyDecided
	if err := f.Svc.CancelRequest(ctx, f.Ava, r1.ID); !errors.As(err, &done) {
		t.Fatalf("cancel a denied request: %v", err)
	}
	entries, err := f.Store.ListLedger(ctx, store.LedgerFilter{KidID: f.Ava.ID})
	must(t, err)
	if len(entries) != 1 {
		t.Fatalf("deny or cancel posted ledger entries: %d", len(entries))
	}
}

func testFirstDecisionWins(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	f.Deposit(t, f.Ava, 1000)
	r, err := request(t, f, f.Ava, 500, "")
	must(t, err)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, admin := range []store.User{f.Mom, f.Dad} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.Svc.DecideRequest(ctx, admin, r.ID, service.Decision{Approve: i == 0})
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		var done *service.ErrAlreadyDecided
		switch {
		case err == nil:
			ok++
		case errors.As(err, &done):
			if done.Request.DecidedByName == "" {
				t.Errorf("loser not told who decided: %v", err)
			}
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d decisions won, want exactly 1", ok)
	}
	entries, err := f.Store.ListLedger(ctx, store.LedgerFilter{KidID: f.Ava.ID})
	must(t, err)
	if n := len(entries); n > 2 {
		t.Fatalf("%d ledger entries; approval posted twice", n)
	}
}

func testPendingCap(t *testing.T, open Opener) {
	f := NewFixture(t, open)
	f.Deposit(t, f.Ava, 10000)
	for i := range service.MaxPendingRequests {
		_, err := request(t, f, f.Ava, 100, fmt.Sprint("k", i))
		must(t, err)
	}
	if _, err := request(t, f, f.Ava, 100, "k-last"); !errors.Is(err, service.ErrTooManyPending) {
		t.Fatalf("sixth request: %v", err)
	}
}

// testDoubleSubmitRace is the race from plan §5.3: $25 available, many
// concurrent "Request $20" taps with different keys. Exactly one may hold.
func testDoubleSubmitRace(t *testing.T, open Opener) {
	f := NewFixture(t, open)
	f.Deposit(t, f.Ava, 2500)
	const n = 8
	var wg sync.WaitGroup
	results := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = request(t, f, f.Ava, 2000, fmt.Sprint("tap-", i))
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range results {
		var ins *service.ErrInsufficient
		switch {
		case err == nil:
			ok++
		case errors.As(err, &ins):
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d requests held, want 1", ok)
	}
	j := f.Jar(t, f.Ava)
	if j.Held != 2000 || j.Available() < 0 {
		t.Fatalf("held %d against %d", j.Held, j.Balance)
	}
}

func testSameKeyRace(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	f.Deposit(t, f.Ava, 5000)
	var wg sync.WaitGroup
	ids := make([]string, 6)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := request(t, f, f.Ava, 1000, "same-form")
			if err != nil {
				t.Errorf("tap %d: %v", i, err)
				return
			}
			ids[i] = r.ID
		}()
	}
	wg.Wait()
	for _, id := range ids[1:] {
		if id != ids[0] {
			t.Fatalf("same key made different requests: %v", ids)
		}
	}
	n, err := f.Store.CountPendingRequests(ctx, f.Ava.ID)
	must(t, err)
	if n != 1 {
		t.Fatalf("%d pending, want 1", n)
	}
}

func testExpiry(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	f.Deposit(t, f.Ava, 1000)
	r, err := request(t, f, f.Ava, 300, "")
	must(t, err)
	f.Clock.Advance(13 * 24 * time.Hour)
	n, err := f.Svc.ExpireRequests(ctx)
	must(t, err)
	if n != 0 {
		t.Fatal("expired early")
	}
	f.Clock.Advance(24 * time.Hour)
	n, err = f.Svc.ExpireRequests(ctx)
	must(t, err)
	if n != 1 {
		t.Fatalf("expired %d, want 1", n)
	}
	got, err := f.Store.GetRequest(ctx, r.ID)
	must(t, err)
	if got.Status != store.StatusExpired {
		t.Fatalf("status %s", got.Status)
	}
	f.wantJar(t, f.Ava, 1000, 0)
	for _, u := range []store.User{f.Ava, f.Mom, f.Dad} {
		ns, err := f.Store.ListNotifications(ctx, u.ID, 1)
		must(t, err)
		if len(ns) == 0 || ns[0].Kind != notify.RequestExpired {
			t.Errorf("%s not told about expiry", u.Username)
		}
	}
	// Expiry 0 = never.
	h, _, _ := f.Svc.Household(ctx)
	must(t, f.Svc.UpdateHousehold(ctx, f.Mom, service.HouseholdInput{Name: h.Name, Timezone: h.Timezone, PINLength: 4, RequestExpiryDays: 0}))
	r2, err := request(t, f, f.Ava, 100, "")
	must(t, err)
	if r2.ExpiresAt != nil {
		t.Fatal("expiry set with request_expiry_days = 0")
	}
}

func testLoginLock(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	if _, err := f.Svc.Login(ctx, "nobody", "0000", false, ""); !errors.Is(err, service.ErrBadCredentials) {
		t.Fatalf("unknown user: %v", err)
	}
	for i := 1; i <= 9; i++ {
		_, err := f.Svc.Login(ctx, "ava", "0000", false, "")
		var th *service.ThrottledError
		if i < 3 && !errors.Is(err, service.ErrBadCredentials) || i >= 3 && !errors.As(err, &th) {
			t.Fatalf("attempt %d: %v", i, err)
		}
		// While backing off, even the right PIN is refused.
		if i == 3 {
			var th *service.ThrottledError
			if _, err := f.Svc.Login(ctx, "ava", f.AvaPIN, false, ""); !errors.As(err, &th) {
				t.Fatalf("not throttled after 3: %v", err)
			}
		}
		f.Clock.Advance(20 * time.Minute)
	}
	if _, err := f.Svc.Login(ctx, "ava", "0000", false, ""); !errors.Is(err, service.ErrLocked) {
		t.Fatalf("10th failure: %v", err)
	}
	f.Clock.Advance(48 * time.Hour)
	if _, err := f.Svc.Login(ctx, "ava", f.AvaPIN, false, ""); !errors.Is(err, service.ErrLocked) {
		t.Fatalf("lock lifted on a timer: %v", err)
	}
	ns, err := f.Store.ListNotifications(ctx, f.Mom.ID, 0)
	must(t, err)
	kinds := map[string]int{}
	for _, n := range ns {
		kinds[n.Kind]++
	}
	if kinds[notify.KidLocked] != 1 || kinds[notify.SignInFailures] != 1 {
		t.Fatalf("admin notifications %v", kinds)
	}
	must(t, f.Svc.Unlock(ctx, f.Dad, f.Ava.ID))
	if _, err := f.Svc.Login(ctx, "ava", f.AvaPIN, true, "tablet"); err != nil {
		t.Fatalf("after unlock: %v", err)
	}
	// Admins back off but never hard-lock.
	for range 15 {
		_, _ = f.Svc.Login(ctx, "mom", "wrong password!", false, "")
		f.Clock.Advance(time.Hour)
	}
	if _, err := f.Svc.Login(ctx, "mom", f.MomPassword, false, ""); err != nil {
		t.Fatalf("admin locked out: %v", err)
	}
}

func testSessions(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	res, err := f.Svc.Login(ctx, "Ava", f.AvaPIN, false, "tablet")
	must(t, err)
	u, _, err := f.Svc.Authenticate(ctx, res.Token)
	must(t, err)
	if u.ID != f.Ava.ID {
		t.Fatal("wrong user")
	}
	// Sliding: activity keeps a kid's 12 h session alive.
	f.Clock.Advance(11 * time.Hour)
	_, _, err = f.Svc.Authenticate(ctx, res.Token)
	must(t, err)
	f.Clock.Advance(11 * time.Hour)
	_, _, err = f.Svc.Authenticate(ctx, res.Token)
	must(t, err)
	f.Clock.Advance(13 * time.Hour)
	if _, _, err := f.Svc.Authenticate(ctx, res.Token); err == nil {
		t.Fatal("idle session survived")
	}

	res, err = f.Svc.Login(ctx, "ava", f.AvaPIN, true, "")
	must(t, err)
	must(t, f.Svc.RevokeSessions(ctx, f.Mom, f.Ava.ID))
	if _, _, err := f.Svc.Authenticate(ctx, res.Token); err == nil {
		t.Fatal("revoked session still valid")
	}
	res, err = f.Svc.Login(ctx, "dad", "another long one", false, "")
	must(t, err)
	must(t, f.Svc.Logout(ctx, res.Token))
	if _, _, err := f.Svc.Authenticate(ctx, res.Token); err == nil {
		t.Fatal("logged-out session still valid")
	}
	must(t, f.Svc.SetDisabled(ctx, f.Mom, f.Leo.ID, true))
	if _, err := f.Svc.Login(ctx, "leo", "1357", false, ""); !errors.Is(err, service.ErrBadCredentials) {
		t.Fatalf("disabled kid signed in: %v", err)
	}
}

func testLastAdmin(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	if err := f.Svc.SetDisabled(ctx, f.Mom, f.Mom.ID, true); err == nil {
		t.Fatal("disabled self")
	}
	must(t, f.Svc.SetDisabled(ctx, f.Mom, f.Dad.ID, true))
	dad, err := f.Store.GetUser(ctx, f.Dad.ID)
	must(t, err)
	if err := f.Svc.SetDisabled(ctx, dad, f.Mom.ID, true); err == nil {
		t.Fatal("disabled admin acted")
	}
	must(t, f.Svc.SetDisabled(ctx, f.Mom, f.Dad.ID, false))
	dad, err = f.Store.GetUser(ctx, f.Dad.ID)
	must(t, err)
	must(t, f.Svc.SetDisabled(ctx, dad, f.Mom.ID, true))
	if err := f.Svc.SetDisabled(ctx, dad, f.Dad.ID, true); err == nil {
		t.Fatal("last admin disabled")
	}
}

func testNotifications(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	f.Deposit(t, f.Ava, 1000)
	r, err := request(t, f, f.Ava, 500, "")
	must(t, err)
	for _, admin := range []store.User{f.Mom, f.Dad} {
		n, err := f.Svc.UnreadCount(ctx, admin)
		must(t, err)
		if n != 1 {
			t.Fatalf("%s unread %d", admin.Username, n)
		}
	}
	_, err = f.Svc.DecideRequest(ctx, f.Dad, r.ID, service.Decision{Approve: true})
	must(t, err)
	views, err := f.Svc.Notifications(ctx, f.Mom, 10)
	must(t, err)
	if len(views) != 1 || views[0].Request == nil || views[0].Request.Status != store.StatusApproved || views[0].Request.DecidedByName != "Dad" {
		t.Fatalf("mom's bell should show 'approved by Dad': %+v", views)
	}
	if views[0].Title != "Ava asked for $5.00" {
		t.Fatalf("title %q", views[0].Title)
	}
	kidViews, err := f.Svc.Notifications(ctx, f.Ava, 10)
	must(t, err)
	if len(kidViews) != 2 || kidViews[0].Title != "Dad approved $5.00" || kidViews[1].Title != "Mom added $10.00" {
		t.Fatalf("kid bell %+v", kidViews)
	}
	must(t, f.Svc.MarkRead(ctx, f.Mom))
	n, err := f.Svc.UnreadCount(ctx, f.Mom)
	must(t, err)
	if n != 0 {
		t.Fatalf("unread after mark-all %d", n)
	}
	must(t, f.Svc.MarkRead(ctx, f.Ava, kidViews[0].ID))
	if n, _ := f.Svc.UnreadCount(ctx, f.Ava); n != 1 {
		t.Fatalf("kid unread %d, want 1", n)
	}
}

func testTimestamps(t *testing.T, open Opener) {
	ctx := context.Background()
	f := NewFixture(t, open)
	f.Clock.Advance(1234567 * time.Microsecond)
	e := f.Deposit(t, f.Ava, 100)
	got, err := f.Store.GetLedgerEntry(ctx, e.ID)
	must(t, err)
	if !got.CreatedAt.Equal(f.Clock.Now()) || got.CreatedAt.Location() != time.UTC {
		t.Fatalf("created_at %v, want %v", got.CreatedAt, f.Clock.Now())
	}
	if !got.EffectiveAt.Equal(e.EffectiveAt) {
		t.Fatal("effective_at round trip")
	}
}
