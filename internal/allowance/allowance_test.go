package allowance

import (
	"slices"
	"testing"
	"time"
)

func d(s string) Date {
	v, err := ParseDate(s)
	if err != nil {
		panic(err)
	}
	return v
}

func strs(ds []Date) []string {
	out := make([]string, len(ds))
	for i, x := range ds {
		out[i] = x.String()
	}
	return out
}

func TestOccurrences(t *testing.T) {
	for _, tc := range []struct {
		name           string
		rule           Rule
		after, through string
		limit          int
		want           []string
	}{
		{"weekly saturday", Rule{Cadence: Weekly, Weekday: time.Saturday}, "2026-03-01", "2026-03-21", 0,
			[]string{"2026-03-07", "2026-03-14", "2026-03-21"}},
		{"after is exclusive", Rule{Cadence: Weekly, Weekday: time.Saturday}, "2026-03-07", "2026-03-13", 0, nil},
		{"through is inclusive", Rule{Cadence: Weekly, Weekday: time.Saturday}, "2026-03-06", "2026-03-07", 0,
			[]string{"2026-03-07"}},
		{"biweekly from anchor", Rule{Cadence: Biweekly, Anchor: d("2026-03-06")}, "2026-02-01", "2026-04-10", 0,
			[]string{"2026-03-06", "2026-03-20", "2026-04-03"}},
		{"monthly clamps the 31st", Rule{Cadence: Monthly, DayOfMonth: 31}, "2026-01-01", "2026-05-01", 0,
			[]string{"2026-01-31", "2026-02-28", "2026-03-31", "2026-04-30"}},
		{"monthly leap year", Rule{Cadence: Monthly, DayOfMonth: 30}, "2028-02-01", "2028-03-01", 0,
			[]string{"2028-02-29"}},
		{"monthly december rollover", Rule{Cadence: Monthly, DayOfMonth: 31}, "2026-12-01", "2027-01-31", 0,
			[]string{"2026-12-31", "2027-01-31"}},
		{"catch-up keeps the newest 8", Rule{Cadence: Weekly, Weekday: time.Monday}, "2026-01-01", "2026-06-01", 8,
			[]string{"2026-04-13", "2026-04-20", "2026-04-27", "2026-05-04", "2026-05-11", "2026-05-18", "2026-05-25", "2026-06-01"}},
		{"years off still bounded", Rule{Cadence: Monthly, DayOfMonth: 1}, "2010-01-01", "2026-03-01", 8,
			[]string{"2025-08-01", "2025-09-01", "2025-10-01", "2025-11-01", "2025-12-01", "2026-01-01", "2026-02-01", "2026-03-01"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := strs(tc.rule.Occurrences(d(tc.after), d(tc.through), tc.limit))
			if !slices.Equal(got, tc.want) && (len(got) != 0 || len(tc.want) != 0) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// Civil dates don’t care about DST: each Sunday appears exactly once across
// both US transitions, and the effective time is local midnight.
func TestDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	r := Rule{Cadence: Weekly, Weekday: time.Sunday}
	got := r.Occurrences(d("2026-02-28"), d("2026-11-08"), 0)
	if len(got) != 37 { // Mar 1 … Nov 8, 36 weeks apart
		t.Fatalf("got %d Sundays, want 37", len(got))
	}
	for _, x := range got {
		s := x.Start(ny)
		if s.Hour() != 0 || DateOf(s) != x {
			t.Fatalf("start of %s = %v", x, s)
		}
	}
	// "Today" flips at local midnight, not UTC midnight.
	now := time.Date(2026, 3, 8, 3, 30, 0, 0, time.UTC) // 23:30 Mar 7 in New York
	if Today(now, ny) != d("2026-03-07") {
		t.Fatalf("today = %s", Today(now, ny))
	}
}

func TestNextAndDescribe(t *testing.T) {
	sat := Rule{Cadence: Weekly, Weekday: time.Saturday}
	if got := sat.Next(d("2026-03-07")); got != d("2026-03-14") {
		t.Fatalf("next = %s", got)
	}
	if sat.Describe() != "every Saturday" {
		t.Fatal(sat.Describe())
	}
	for n, want := range map[int]string{1: "1st", 2: "2nd", 3: "3rd", 4: "4th", 11: "11th", 12: "12th", 13: "13th", 21: "21st", 22: "22nd", 31: "31st"} {
		if ordinal(n) != want {
			t.Errorf("ordinal(%d) = %s", n, ordinal(n))
		}
	}
	if (Rule{Cadence: Monthly, DayOfMonth: 0}).Validate() == nil || (Rule{Cadence: "daily"}).Validate() == nil ||
		(Rule{Cadence: Biweekly}).Validate() == nil || sat.Validate() != nil {
		t.Fatal("validate")
	}
	if _, err := ParseDate("03/07/2026"); err == nil {
		t.Fatal("bad date accepted")
	}
}
