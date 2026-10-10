package web

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/chinny/goldberry/internal/money"
	"github.com/chinny/goldberry/internal/service"
	"github.com/chinny/goldberry/internal/store"
)

func TestNiceStep(t *testing.T) {
	usd, jpy := money.MustLookup("USD"), money.MustLookup("JPY")
	for _, tc := range []struct {
		span int64
		cur  money.Currency
		want int64
	}{
		{0, usd, 100},       // nothing yet: $1 steps
		{250, usd, 100},     // never finer than a whole dollar
		{16900, usd, 10000}, // $169 in 3 steps: $100
		{50000, usd, 20000}, // $500: $200
		{70000, usd, 25000}, // $700: $250
		{700, usd, 300},     // $7: $2.50 would put cents on the axis, so $3
		{1200, jpy, 500},    // ¥1,200: ¥500
		{3, jpy, 1},         // ¥3: ¥1
		{123456789, usd, 50000000},
	} {
		if got := niceStep(tc.span, yIntervals, tc.cur); got != tc.want {
			t.Errorf("niceStep(%d, %s) = %d, want %d", tc.span, tc.cur.Code, got, tc.want)
		}
	}
	if floorTo(-150, 100) != -200 || ceilTo(150, 100) != 200 || floorTo(150, 100) != 100 || ceilTo(-150, 100) != -100 {
		t.Error("floorTo / ceilTo")
	}
}

func TestBuildChart(t *testing.T) {
	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	h := service.BalanceHistory{
		Range: service.HistoryRange{From: from, To: from.Add(10 * 24 * time.Hour), FromDate: "2026-03-01", ToDate: "2026-03-10"},
		Series: []service.HistorySeries{{Key: "total", Name: "Total", Start: 1000, End: 2500, Low: 1000, High: 3000,
			Points: []service.HistoryPoint{
				{At: from.Add(5 * 24 * time.Hour), Balance: 3000, Delta: 2000, Key: "k1", Kind: store.KindDeposit, Comment: "Birthday"},
				{At: from.Add(7 * 24 * time.Hour), Balance: 2500, Delta: -500, Key: "k2", Kind: store.KindWithdrawal},
			}}},
	}
	c := buildChart(h, money.MustLookup("USD"), time.UTC, false)
	// $0–$30 in $10 steps: four rows, top first, each tick at a row centre.
	if strings.Join(c.YTicks, " ") != "$30 $20 $10 $0" || c.Grid[0] != "37.5" || c.Grid[3] != "262.5" {
		t.Fatalf("y axis %v at %v", c.YTicks, c.Grid)
	}
	if strings.Join(c.XTicks, " ") != "Mar 2 Mar 4 Mar 7 Mar 9" {
		t.Fatalf("x axis %v", c.XTicks)
	}
	ln := c.Lines[0]
	if ln.Path != "M0 187.5 H500.0 V37.5 H700.0 V75.0 H1000.0" || ln.Area == "" || ln.Delta != 1500 || c.Legend {
		t.Fatalf("line %+v", ln)
	}
	if len(ln.Dots) != 2 || ln.Dots[0].Href != "#e-k1" || ln.Dots[0].Title != "Mar 6 · +$20.00 · Birthday → $30.00" ||
		!strings.Contains(ln.Dots[1].Title, "Money out") {
		t.Fatalf("dots %+v", ln.Dots)
	}
	if p := buildChart(h, money.MustLookup("USD"), time.UTC, true); len(p.Lines[0].Dots) != 0 {
		t.Fatal("a preview has dots")
	}
}

func TestBalanceGraph(t *testing.T) {
	ts, svc := newServerWithService(t)
	ctx := context.Background()
	mom, err := svc.Setup(ctx, service.SetupInput{HouseholdName: "Demo", Currency: "USD", Timezone: "UTC",
		Username: "mom", DisplayName: "Mom", Password: "correct horse battery"})
	if err != nil {
		t.Fatal(err)
	}
	ava, err := svc.CreateKid(ctx, mom, service.KidInput{Username: "ava", DisplayName: "Ava", PIN: "2468"})
	if err != nil {
		t.Fatal(err)
	}
	leo, err := svc.CreateKid(ctx, mom, service.KidInput{Username: "leo", DisplayName: "Leo", PIN: "1357"})
	if err != nil {
		t.Fatal(err)
	}
	// Ava got $20 ten days ago and $5 today; Leo has $99 of his own.
	svc.Now = func() time.Time { return time.Now().Add(-10 * 24 * time.Hour) }
	if _, err := svc.PostEntry(ctx, mom, service.EntryInput{KidID: ava.ID, Amount: 2000, Comment: "Birthday", PrivateNote: "from Gran"}); err != nil {
		t.Fatal(err)
	}
	svc.Now = time.Now
	if _, err := svc.PostEntry(ctx, mom, service.EntryInput{KidID: ava.ID, Amount: 500, UseSplit: true, Comment: "Allowance"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PostEntry(ctx, mom, service.EntryInput{KidID: leo.ID, Amount: 9900, Comment: "Leo's money"}); err != nil {
		t.Fatal(err)
	}

	kid := newBrowser(t, ts)
	kid.get("/login", 200)
	kid.post("/login", url.Values{"username": {"ava"}, "secret": {"2468"}}, 200)
	kid.mustSee("Last month", `class="chart chart-compact"`, `href="/graph"`)

	kid.get("/graph", 200) // a month by default: both changes, the total line
	kid.mustSee("My money over time", `aria-current="page">1M`, "$25.00", "Birthday", "Allowance", `href="#e-`, `id="e-`)
	kid.mustNotSee("from Gran", "Leo&#39;s money", "$99.00")
	kid.get("/graph?range=1w", 200) // the birthday is before this week
	kid.mustSee(`aria-current="page">1W`, "Allowance", "$20.00")
	kid.mustNotSee(">Birthday<")
	kid.get("/graph?range=1w&show=jars", 200)
	kid.mustSee("chart-legend", "line-spend", "line-save", "line-give")
	kid.get("/graph?range=custom&from=2099-01-01", 422)
	kid.mustSee("hasn’t happened yet", `value="2099-01-01"`)

	parent := newBrowser(t, ts)
	parent.get("/login", 200)
	parent.post("/login", url.Values{"username": {"mom"}, "secret": {"correct horse battery"}}, 200)
	parent.get("/admin/kids/"+ava.ID, 200)
	parent.mustSee("Last month", "/admin/kids/"+ava.ID+"/graph")
	parent.get("/admin/kids/"+ava.ID+"/graph?range=3m", 200)
	parent.mustSee("Ava’s money over time", "from Gran") // admins see private notes in the list
	parent.get("/admin/kids/"+mom.ID+"/graph", 404)
	kid.get("/admin/kids/"+leo.ID+"/graph", 403)
}
