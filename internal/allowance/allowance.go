// Package allowance is the calendar maths for recurring allowance (plan §7.2).
// It works on civil dates (a day in the household's time zone, no clock time),
// so daylight-saving changes can't skip or double an occurrence.
package allowance

import (
	"errors"
	"fmt"
	"time"
)

// Date is a calendar day. Its zero value is "no date".
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

const layout = "2006-01-02"

// ParseDate reads "YYYY-MM-DD". An empty string is the zero Date.
func ParseDate(s string) (Date, error) {
	if s == "" {
		return Date{}, nil
	}
	t, err := time.Parse(layout, s)
	if err != nil {
		return Date{}, fmt.Errorf("want a date like 2026-03-07, got %q", s)
	}
	return DateOf(t), nil
}

// DateOf is the calendar day of t in t's own location.
func DateOf(t time.Time) Date {
	y, m, d := t.Date()
	return Date{y, m, d}
}

// Today is the current calendar day in loc.
func Today(now time.Time, loc *time.Location) Date { return DateOf(now.In(loc)) }

func (d Date) IsZero() bool { return d == Date{} }

func (d Date) String() string {
	if d.IsZero() {
		return ""
	}
	return d.utc().Format(layout)
}

func (d Date) utc() time.Time { return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC) }

// AddDays returns the date n days later (n may be negative).
func (d Date) AddDays(n int) Date { return DateOf(d.utc().AddDate(0, 0, n)) }

// Weekday is the day of the week.
func (d Date) Weekday() time.Weekday { return d.utc().Weekday() }

// Before reports whether d is earlier than e.
func (d Date) Before(e Date) bool { return d.utc().Before(e.utc()) }

// DaysUntil is the number of days from d to e.
func (d Date) DaysUntil(e Date) int { return int(e.utc().Sub(d.utc()).Hours() / 24) }

// Start is midnight at the start of d in loc: the effective time of an
// allowance posted for that day. time.Date normalizes a skipped midnight.
func (d Date) Start(loc *time.Location) time.Time {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, loc)
}

// Cadence is how often a schedule pays.
type Cadence string

const (
	Weekly   Cadence = "weekly"
	Biweekly Cadence = "biweekly"
	Monthly  Cadence = "monthly"
)

// Rule says which days a schedule pays on.
type Rule struct {
	Cadence    Cadence
	Weekday    time.Weekday // weekly
	Anchor     Date         // biweekly: a day it pays on; every 14 days from it
	DayOfMonth int          // monthly: 1–31, clamped to the month's last day
}

// Validate checks a rule is complete.
func (r Rule) Validate() error {
	switch r.Cadence {
	case Weekly:
		if r.Weekday < time.Sunday || r.Weekday > time.Saturday {
			return errors.New("pick a day of the week")
		}
	case Biweekly:
		if r.Anchor.IsZero() {
			return errors.New("pick the first day it pays")
		}
	case Monthly:
		if r.DayOfMonth < 1 || r.DayOfMonth > 31 {
			return errors.New("pick a day of the month from 1 to 31")
		}
	default:
		return errors.New("pick weekly, every two weeks or monthly")
	}
	return nil
}

// Matches reports whether the rule pays on d.
func (r Rule) Matches(d Date) bool {
	switch r.Cadence {
	case Weekly:
		return d.Weekday() == r.Weekday
	case Biweekly:
		if d.Before(r.Anchor) {
			return false
		}
		return r.Anchor.DaysUntil(d)%14 == 0
	case Monthly:
		last := Date{d.Year, d.Month + 1, 1}.AddDays(-1).Day // normalizes December
		return d.Day == min(r.DayOfMonth, last)
	}
	return false
}

// maxScan bounds how far back Occurrences looks, so a box that was off for
// years doesn't scan forever. It's far more than the catch-up cap needs.
const maxScan = 400

// Occurrences returns the paying days in (after, through], oldest first, at
// most limit of them: the newest ones when more are due (plan §7.2 caps
// catch-up at 8).
func (r Rule) Occurrences(after, through Date, limit int) []Date {
	start := after.AddDays(1)
	if floor := through.AddDays(-maxScan); start.Before(floor) {
		start = floor
	}
	var out []Date
	for d := start; !through.Before(d); d = d.AddDays(1) {
		if r.Matches(d) {
			out = append(out, d)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// Next returns the first paying day after d.
func (r Rule) Next(after Date) Date {
	for d, i := after.AddDays(1), 0; i < 62; d, i = d.AddDays(1), i+1 {
		if r.Matches(d) {
			return d
		}
	}
	return Date{}
}

// Describe is a short phrase like "every Saturday" or "on the 1st of each month".
func (r Rule) Describe() string {
	switch r.Cadence {
	case Weekly:
		return "every " + r.Weekday.String()
	case Biweekly:
		return "every other " + r.Anchor.Weekday().String()
	case Monthly:
		return "on the " + ordinal(r.DayOfMonth) + " of each month"
	}
	return ""
}

func ordinal(n int) string {
	suffix := "th"
	switch {
	case n%100 >= 11 && n%100 <= 13:
	case n%10 == 1:
		suffix = "st"
	case n%10 == 2:
		suffix = "nd"
	case n%10 == 3:
		suffix = "rd"
	}
	return fmt.Sprintf("%d%s", n, suffix)
}
