package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chinny/goldberry/internal/config"
	"github.com/chinny/goldberry/internal/service"
	"github.com/chinny/goldberry/internal/store/sqlite"
)

const setupToken = "test-setup-token"

func newServer(t *testing.T) *httptest.Server {
	ts, _ := newServerWithService(t)
	return ts
}

func newServerWithService(t *testing.T) (*httptest.Server, *service.Service) {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "gb.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.DiscardHandler)
	cfg := config.Config{InsecureCookies: true, Timezone: "UTC"}
	svc := service.New(st, log)
	srv, err := New(ctx, svc, cfg, log, setupToken)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, svc
}

type browser struct {
	t    *testing.T
	base string
	c    *http.Client
	last string // last page body
	url  string // last final URL
}

func newBrowser(t *testing.T, ts *httptest.Server) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, base: ts.URL, c: &http.Client{Jar: jar}}
}

func (b *browser) do(req *http.Request, want int) string {
	b.t.Helper()
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	b.last, b.url = string(body), resp.Request.URL.Path+"?"+resp.Request.URL.RawQuery
	if want != 0 && resp.StatusCode != want {
		b.t.Fatalf("%s %s: status %d, want %d\n%s", req.Method, req.URL.Path, resp.StatusCode, want, firstLines(b.last))
	}
	return b.last
}

func (b *browser) get(path string, want int) string {
	b.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, b.base+path, nil)
	return b.do(req, want)
}

var csrfRE = regexp.MustCompile(`name="_csrf" value="([^"]+)"`)

// post submits a form, using the CSRF token from the last page.
func (b *browser) post(path string, form url.Values, want int) string {
	b.t.Helper()
	if form.Get("_csrf") == "" {
		m := csrfRE.FindStringSubmatch(b.last)
		if m == nil {
			b.t.Fatalf("no csrf token on last page (%s)", b.url)
		}
		form.Set("_csrf", m[1])
	}
	req, _ := http.NewRequest(http.MethodPost, b.base+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return b.do(req, want)
}

func (b *browser) mustSee(subs ...string) {
	b.t.Helper()
	for _, s := range subs {
		if !strings.Contains(b.last, s) {
			b.t.Fatalf("page %s missing %q\n%s", b.url, s, firstLines(b.last))
		}
	}
}

func (b *browser) mustNotSee(subs ...string) {
	b.t.Helper()
	for _, s := range subs {
		if strings.Contains(b.last, s) {
			b.t.Fatalf("page %s unexpectedly has %q", b.url, s)
		}
	}
}

func firstLines(s string) string {
	if i := strings.Index(s, `<main`); i > 0 {
		s = s[i:]
	}
	if len(s) > 3000 {
		s = s[:3000]
	}
	return s
}

func find(t *testing.T, re, s string) string {
	t.Helper()
	m := regexp.MustCompile(re).FindStringSubmatch(s)
	if m == nil {
		t.Fatalf("no match for %s", re)
	}
	return m[1]
}

// TestEndToEnd is the Phase 1–2 exit criteria: setup → add kid → deposit →
// kid signs in → request → admin approves from the bell → balances correct.
func TestEndToEnd(t *testing.T) {
	ts := newServer(t)
	mom := newBrowser(t, ts)

	// Open endpoints.
	mom.get("/healthz", 200)
	mom.get("/readyz", 200)
	mom.get("/metrics", 200)
	mom.mustSee("goldberry_http_requests_total")
	mom.get("/static/app.css", 200)

	// First run: everything redirects to /setup, which needs the token.
	mom.get("/", 200)
	if !strings.HasPrefix(mom.url, "/setup") {
		t.Fatalf("not redirected to setup: %s", mom.url)
	}
	setup := url.Values{"household_name": {"The Chins"}, "currency": {"USD"}, "timezone": {"America/New_York"},
		"display_name": {"Mom"}, "username": {"mom"}, "password": {"correct horse battery"}}
	setup.Set("setup_token", "wrong")
	mom.post("/setup", setup, 422)
	mom.mustSee("setup token doesn’t match")
	setup.Set("setup_token", setupToken)
	mom.post("/setup", setup, 200)
	if !strings.HasPrefix(mom.url, "/admin/users/new") {
		t.Fatalf("after setup landed on %s", mom.url)
	}
	mom.mustSee("Add a kid", "You’re set up")
	mom.get("/setup", 200) // now just redirects
	if strings.HasPrefix(mom.url, "/setup") {
		t.Fatal("setup reachable after setup")
	}

	// Add a kid and a second parent.
	mom.get("/admin/users/new?role=kid", 200)
	mom.post("/admin/users", url.Values{"role": {"kid"}, "display_name": {"Ava"}, "username": {"ava"}, "pin": {"12"}}, 422)
	mom.mustSee("PIN must be 4 digits")
	mom.post("/admin/users", url.Values{"role": {"kid"}, "display_name": {"Ava"}, "username": {"ava"}, "pin": {"2468"}}, 200)
	kidID := find(t, `/admin/kids/([0-9a-f-]{36})`, mom.url)
	mom.get("/admin/users/new?role=admin", 200)
	mom.post("/admin/users", url.Values{"role": {"admin"}, "display_name": {"Dad"}, "username": {"dad"}, "password": {"another long one"}}, 200)

	// Deposit $25 with a comment and a private note; a double submit is a no-op.
	mom.get("/admin/kids/"+kidID+"/funds", 200)
	key := find(t, `name="key" value="([^"]+)"`, mom.last)
	deposit := url.Values{"key": {key}, "direction": {"add"}, "amount": {"25.00"}, "comment": {"Mowed the lawn"}, "private_note": {"front only"}}
	mom.post("/admin/kids/"+kidID+"/entries", deposit, 200)
	mom.mustSee("$25.00", "Mowed the lawn", "front only")
	mom.get("/admin/kids/"+kidID+"/funds", 200)
	deposit.Del("_csrf")
	mom.post("/admin/kids/"+kidID+"/entries", deposit, 200)
	if n := strings.Count(mom.last, `amt-credit">&#43;$25.00`); n != 1 { // html/template escapes "+"
		t.Fatalf("double submit posted %d entries", n)
	}
	// Removing more than available is refused with the available amount.
	mom.get("/admin/kids/"+kidID+"/funds?direction=remove", 200)
	mom.post("/admin/kids/"+kidID+"/entries", url.Values{"key": {"k2"}, "direction": {"remove"}, "amount": {"30"}}, 422)
	mom.mustSee("Only $25.00 is available in Spend.")

	// Ava signs in on the family tablet with her PIN.
	ava := newBrowser(t, ts)
	ava.get("/login", 200)
	ava.post("/login", url.Values{"username": {"ava"}}, 200)
	ava.mustSee("Hi, Ava", "data-pin-form", `data-pin-length="4"`)
	ava.post("/login", url.Values{"username": {"ava"}, "secret": {"0000"}}, 401)
	ava.mustSee("PIN or password don&#39;t match")
	ava.post("/login", url.Values{"username": {"ava"}, "secret": {"2468"}, "remember": {"1"}}, 200)
	ava.mustSee("You can ask for", "$25.00", "Mowed the lawn")
	ava.mustNotSee("front only") // private notes never reach kids

	// Kids can't reach admin pages, or post without a CSRF token.
	ava.get("/admin", 403)
	ava.get("/admin/kids/"+kidID, 403)
	ava.post("/requests", url.Values{"_csrf": {"forged"}, "amount": {"1"}, "reason": {"x"}}, 403)

	// Ask for $20: it's held, and available drops to $5.
	ava.get("/requests/new", 200)
	ava.mustSee("You can ask for up to $25.00")
	reqKey := find(t, `name="key" value="([^"]+)"`, ava.last)
	ava.post("/requests", url.Values{"key": {reqKey}, "amount": {"20"}, "reason": {"Movie with Jess"}, "jar": {find(t, `name="jar" value="([^"]+)"`, ava.last)}}, 200)
	ava.mustSee("$5.00", "$20.00 waiting for approval", "Movie with Jess")
	ava.get("/requests/new", 200)
	ava.post("/requests", url.Values{"key": {"k3"}, "amount": {"6"}, "reason": {"Candy"}}, 422)
	ava.mustSee("Only $5.00 is available in Spend.")

	// Both parents see it in the bell; Mom approves $15 from the bell.
	mom.get("/notifications/count", 200)
	if strings.TrimSpace(mom.last) != "1" {
		t.Fatalf("mom unread = %q", mom.last)
	}
	mom.get("/notifications", 200)
	mom.mustSee("Ava asked for $20.00", "Approve", "Deny")
	reqID := find(t, `/admin/requests/([0-9a-f-]{36})/approve`, mom.last)
	mom.post("/admin/requests/"+reqID+"/approve", url.Values{"next": {"/notifications"}, "approved_amount": {"15"}, "note": {"$15, not $20"}}, 200)
	mom.mustSee("Approved $15.00 for Ava", "Approved $15.00 by Mom")

	dad := newBrowser(t, ts)
	dad.get("/login", 200)
	dad.post("/login", url.Values{"username": {"dad"}}, 200)
	dad.post("/login", url.Values{"username": {"dad"}, "secret": {"another long one"}}, 200)
	dad.get("/notifications", 200)
	dad.mustSee("Approved $15.00 by Mom")
	dad.mustNotSee(">Approve</button>")
	dad.post("/admin/requests/"+reqID+"/deny", url.Values{"next": {"/admin"}}, 200)
	dad.mustSee("Already approved by Mom.")

	// Ava: $25 − $15 = $10, nothing held, and she's told.
	ava.get("/", 200)
	ava.mustSee("$10.00")
	ava.mustNotSee("waiting for approval")
	ava.get("/notifications", 200)
	ava.mustSee("Mom approved $15.00", "$15, not $20", "Mom added $25.00")
	ava.get("/ledger", 200)
	ava.mustSee("Movie with Jess", "Approved", "−$15.00")

	// Reverse the deposit is refused (only $10 left); reverse the withdrawal works.
	mom.get("/admin/kids/"+kidID, 200)
	entryIDs := regexp.MustCompile(`/admin/entries/([0-9a-f-]{36})/reverse`).FindAllStringSubmatch(mom.last, -1)
	if len(entryIDs) != 2 {
		t.Fatalf("want 2 reversible entries, got %d", len(entryIDs))
	}
	mom.post("/admin/entries/"+entryIDs[1][1]+"/reverse", url.Values{"next": {"/admin/kids/" + kidID}}, 200)
	mom.mustSee("Only $10.00 is available")
	mom.post("/admin/entries/"+entryIDs[0][1]+"/reverse", url.Values{"next": {"/admin/kids/" + kidID}}, 200)
	mom.mustSee("Undone.", "$25.00")

	// Every admin page renders.
	for _, p := range []string{"/admin", "/admin/users", "/admin/users/" + kidID, "/admin/settings", "/admin/kids/" + kidID + "/funds"} {
		mom.get(p, 200)
	}
	mom.post("/admin/settings", url.Values{"name": {"The Chins"}, "timezone": {"America/New_York"}, "pin_length": {"4"}, "request_expiry_days": {"7"}}, 200)
	mom.mustSee("Settings saved.")
	mom.get("/no/such/page", 404)

	// Sign-out works and the old session is dead.
	ava.get("/", 200)
	ava.post("/logout", url.Values{}, 200)
	ava.get("/ledger", 200)
	if !strings.HasPrefix(ava.url, "/login") {
		t.Fatalf("signed-out kid reached %s", ava.url)
	}
}

func TestKidLockout(t *testing.T) {
	ts := newServer(t)
	mom := newBrowser(t, ts)
	mom.get("/setup", 200)
	mom.post("/setup", url.Values{"setup_token": {setupToken}, "household_name": {"H"}, "currency": {"USD"}, "timezone": {"UTC"},
		"display_name": {"Mom"}, "username": {"mom"}, "password": {"correct horse battery"}}, 200)
	mom.post("/admin/users", url.Values{"role": {"kid"}, "display_name": {"Leo"}, "username": {"leo"}, "pin": {"1357"}}, 200)
	kidID := find(t, `/admin/kids/([0-9a-f-]{36})`, mom.url)

	leo := newBrowser(t, ts)
	leo.get("/login", 200)
	leo.post("/login", url.Values{"username": {"leo"}}, 200)
	for range 3 {
		leo.post("/login", url.Values{"username": {"leo"}, "secret": {"0000"}}, 401)
	}
	leo.mustSee("Too many wrong tries")

	mom.get("/notifications", 200)
	mom.mustSee("Wrong PIN or password for Leo", "Unlock or reset PIN")
	mom.get("/admin/users/"+kidID, 200)
	mom.mustSee("recent wrong tries")
	mom.post("/admin/users/"+kidID+"/unlock", url.Values{}, 200)
	mom.mustSee("Unlocked.")
	leo.post("/login", url.Values{"username": {"leo"}, "secret": {"1357"}}, 200)
	leo.mustSee("Leo")
}

// TestJarsLocksAllowance covers Phase 4 through HTTP: split deposits, moves,
// a kid's self-lock and the server-enforced gauntlet, a parent lock, and an
// allowance posting from the scheduler job.
func TestJarsLocksAllowance(t *testing.T) {
	ts, svc := newServerWithService(t)
	var offset atomic.Int64 // test clock: real time plus a settable offset
	svc.Now = func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	advance := func(d time.Duration) { offset.Add(int64(d)) }

	mom := newBrowser(t, ts)
	mom.get("/setup", 200)
	mom.post("/setup", url.Values{"setup_token": {setupToken}, "household_name": {"H"}, "currency": {"USD"}, "timezone": {"UTC"},
		"display_name": {"Mom"}, "username": {"mom"}, "password": {"correct horse battery"}}, 200)
	mom.post("/admin/users", url.Values{"role": {"kid"}, "display_name": {"Ava"}, "username": {"ava"}, "pin": {"2468"}}, 200)
	kidID := find(t, `/admin/kids/([0-9a-f-]{36})`, mom.url)
	mom.mustSee("Spend", "Save", "Give", "70 / 20 / 10", "Set one up")

	// Split deposit: one row, three jars.
	mom.get("/admin/kids/"+kidID+"/funds", 200)
	mom.mustSee("Use Ava’s split")
	mom.post("/admin/kids/"+kidID+"/entries", url.Values{"key": {"k1"}, "direction": {"add"}, "jar": {"split"},
		"amount": {"100.00"}, "comment": {"Birthday"}}, 200)
	mom.mustSee("Added $100.00 across 3 jars", "$70.00", "$20.00", "$10.00", "3 jars")
	mom.get("/admin/kids/"+kidID+"/funds", 200)
	mom.post("/admin/kids/"+kidID+"/entries", url.Values{"key": {"k2"}, "direction": {"remove"}, "jar": {"split"}, "amount": {"1"}}, 422)
	mom.mustSee("Pick one jar to remove money from.")
	mom.get("/admin/kids/"+kidID, 200)
	jarIDs := regexp.MustCompile(`/admin/jars/([0-9a-f-]{36})/locks`).FindAllStringSubmatch(mom.last, -1)
	spend, save, give := jarIDs[0][1], jarIDs[1][1], jarIDs[2][1]

	// Mom locks Give; Ava can't ask from it.
	mom.post("/admin/jars/"+give+"/locks", url.Values{"reason": {"Give goes out with a parent"}, "next": {"/admin/kids/" + kidID}}, 200)
	mom.mustSee("Locked Give", "Your lock")

	ava := newBrowser(t, ts)
	ava.get("/login", 200)
	ava.post("/login", url.Values{"username": {"ava"}, "secret": {"2468"}}, 200)
	ava.mustSee("$70.00", "Locked by a parent", "Lock it")
	ava.get("/requests/new", 200)
	ava.post("/requests", url.Values{"key": {"r1"}, "jar": {give}, "amount": {"1"}, "reason": {"x"}}, 422)
	ava.mustSee("Give is locked by a parent")

	// Ava moves money, then locks Save.
	ava.get("/move", 200)
	ava.post("/transfers", url.Values{"key": {"m1"}, "from": {spend}, "to": {save}, "amount": {"30"}}, 200)
	ava.mustSee("Moved $30.00 from Spend to Save", "$40.00", "$50.00")
	ava.get("/jars/"+save+"/lock", 200)
	ava.post("/jars/"+save+"/locks", url.Values{"reason": {"Saving for a Switch"}}, 200)
	ava.mustSee("Locked Save", "You locked this")

	// Moving out of Save now goes through the gauntlet…
	ava.get("/move", 200)
	ava.post("/transfers", url.Values{"key": {"m2"}, "from": {save}, "to": {spend}, "amount": {"10"}}, 200)
	if !strings.HasPrefix(ava.url, "/gauntlet") {
		t.Fatalf("not sent to the gauntlet: %s", ava.url)
	}
	ava.mustSee("Past-you set this for a reason", "Saving for a Switch", "Press and hold", "data-gauntlet")
	override := find(t, `name="override" value="([^"]+)"`, ava.last)
	// …and the server refuses to skip the countdown.
	ava.post("/transfers", url.Values{"key": {"m2"}, "from": {save}, "to": {spend}, "amount": {"10"}, "override": {override}, "remove_lock": {"0"}}, 200)
	ava.mustSee("Not so fast: wait for the countdown")
	override = find(t, `name="override" value="([^"]+)"`, ava.last)
	advance(14 * time.Second)
	ava.post("/transfers", url.Values{"key": {"m2"}, "from": {save}, "to": {spend}, "amount": {"10"}, "override": {override}, "remove_lock": {"0"}}, 200)
	ava.mustSee("Moved $10.00 from Save to Spend", "You locked this")

	mom.get("/notifications", 200)
	mom.mustSee("Ava broke their own lock on Save", "Saving for a Switch")
	mom.get("/admin/kids/"+kidID, 200)
	mom.mustSee("Broken locks", "Broke once")

	// Allowance: set up, then the job pays the next Saturday split 70/20/10.
	mom.get("/admin/kids/"+kidID+"/allowance", 200)
	mom.post("/admin/kids/"+kidID+"/allowance", url.Values{"amount": {"10"}, "cadence": {"weekly"}, "weekday": {"6"}, "day_of_month": {"1"}}, 200)
	mom.mustSee("Allowance saved", "$10.00 every Saturday", "Pause")
	advance(8 * 24 * time.Hour)
	if n, err := svc.PostAllowance(context.Background()); err != nil || n != 1 {
		t.Fatalf("allowance posted %d, %v", n, err)
	}
	ava.get("/login", 200) // 8 days later her 12-hour session has expired
	ava.post("/login", url.Values{"username": {"ava"}, "secret": {"2468"}}, 200)
	ava.mustSee("Weekly allowance")
	ava.get("/notifications", 200)
	ava.mustSee("Allowance: &#43;$10.00")

	// Jars page: the split must make 100%. (Mom's 7-day session lapsed too.)
	mom.get("/login", 200)
	mom.post("/login", url.Values{"username": {"mom"}, "secret": {"correct horse battery"}}, 200)
	mom.get("/admin/kids/"+kidID+"/jars", 200)
	mom.post("/admin/kids/"+kidID+"/split", url.Values{"split_" + spend: {"50"}, "split_" + save: {"20"}, "split_" + give: {"10"}}, 422)
	mom.mustSee("make 100%")
	mom.post("/admin/kids/"+kidID+"/jars", url.Values{"name": {"Lego fund"}}, 200)
	mom.mustSee("Added Lego fund")
	mom.get("/admin", 200)
	mom.mustSee("every Saturday", "Give locked")
}
