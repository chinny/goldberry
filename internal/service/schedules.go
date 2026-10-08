package service

import (
	"context"
	"errors"
	"time"

	"github.com/chinny/goldberry/internal/allowance"
	"github.com/chinny/goldberry/internal/money"
	"github.com/chinny/goldberry/internal/notify"
	"github.com/chinny/goldberry/internal/store"
)

// CatchUpLimit caps how many missed allowances post after downtime (plan §7.2).
const CatchUpLimit = 8

// ScheduleInput describes a recurring allowance.
type ScheduleInput struct {
	Amount      int64
	Cadence     string // weekly | biweekly | monthly
	Weekday     int    // weekly: 0 = Sunday … 6 = Saturday
	DayOfMonth  int    // monthly
	Anchor      string // biweekly: "YYYY-MM-DD", a day it pays on
	TargetJarID string // "" = use the kid's split
	Comment     string // defaults to "Weekly allowance" etc.
}

// RuleOf converts a stored schedule into its calendar rule.
func RuleOf(sc store.Schedule) allowance.Rule {
	anchor, _ := allowance.ParseDate(sc.AnchorDate)
	return allowance.Rule{Cadence: allowance.Cadence(sc.Cadence), Weekday: time.Weekday(sc.Weekday),
		DayOfMonth: sc.DayOfMonth, Anchor: anchor}
}

func (s *Service) validSchedule(ctx context.Context, r store.Reader, kidID string, in ScheduleInput) (store.Schedule, error) {
	if in.Amount <= 0 || in.Amount > money.MaxAmount {
		return store.Schedule{}, invalid("amount", "Enter an amount above zero.")
	}
	anchor, err := allowance.ParseDate(in.Anchor)
	if err != nil {
		return store.Schedule{}, invalid("anchor", "%s", capitalize(err.Error()))
	}
	rule := allowance.Rule{Cadence: allowance.Cadence(in.Cadence), Weekday: time.Weekday(in.Weekday),
		DayOfMonth: in.DayOfMonth, Anchor: anchor}
	if err := rule.Validate(); err != nil {
		return store.Schedule{}, invalid("cadence", "%s", capitalize(err.Error()))
	}
	comment, err := cleanText("comment", in.Comment, false)
	if err != nil {
		return store.Schedule{}, err
	}
	if comment == "" {
		comment = map[allowance.Cadence]string{allowance.Weekly: "Weekly allowance", allowance.Biweekly: "Allowance",
			allowance.Monthly: "Monthly allowance"}[rule.Cadence]
	}
	if in.TargetJarID != "" {
		if _, err := jarFor(ctx, r, kidID, in.TargetJarID); err != nil {
			return store.Schedule{}, err
		}
	}
	return store.Schedule{KidID: kidID, Amount: in.Amount, Cadence: in.Cadence, Weekday: in.Weekday,
		DayOfMonth: in.DayOfMonth, AnchorDate: anchor.String(), TargetJarID: in.TargetJarID, CommentTemplate: comment}, nil
}

// CreateSchedule adds an allowance. It starts with the next paying day after
// today, so setting one up never pays out by surprise.
func (s *Service) CreateSchedule(ctx context.Context, actor store.User, kidID string, in ScheduleInput) (store.Schedule, error) {
	if err := requireAdmin(actor); err != nil {
		return store.Schedule{}, err
	}
	if _, err := getKid(ctx, s.Store, kidID); err != nil {
		return store.Schedule{}, err
	}
	h, _, err := s.Household(ctx)
	if err != nil {
		return store.Schedule{}, err
	}
	sc, err := s.validSchedule(ctx, s.Store, kidID, in)
	if err != nil {
		return sc, err
	}
	sc.ID, sc.Active, sc.CreatedBy, sc.CreatedAt = store.NewID(), true, actor.ID, s.now()
	sc.LastOccurrence = s.today(h).String()
	err = s.Store.Tx(ctx, kidID, func(tx store.Tx) error {
		if err := tx.CreateSchedule(ctx, sc); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor.ID, "schedule.create", kidID, map[string]any{"amount": sc.Amount, "cadence": sc.Cadence})
	})
	return sc, err
}

// UpdateSchedule changes an allowance's amount, timing or jar. Days already
// paid stay paid.
func (s *Service) UpdateSchedule(ctx context.Context, actor store.User, id string, in ScheduleInput) error {
	if err := requireAdmin(actor); err != nil {
		return err
	}
	old, err := s.Store.GetSchedule(ctx, id)
	if err != nil {
		return err
	}
	sc, err := s.validSchedule(ctx, s.Store, old.KidID, in)
	if err != nil {
		return err
	}
	sc.ID, sc.Active, sc.LastOccurrence, sc.CreatedBy, sc.CreatedAt = old.ID, old.Active, old.LastOccurrence, old.CreatedBy, old.CreatedAt
	return s.Store.Tx(ctx, old.KidID, func(tx store.Tx) error {
		if err := tx.UpdateSchedule(ctx, sc); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor.ID, "schedule.update", old.KidID, map[string]any{"amount": sc.Amount, "cadence": sc.Cadence})
	})
}

// SetScheduleActive pauses or resumes an allowance. Resuming doesn't back-pay
// the paused weeks ("no allowance while at camp").
func (s *Service) SetScheduleActive(ctx context.Context, actor store.User, id string, active bool) error {
	if err := requireAdmin(actor); err != nil {
		return err
	}
	sc, err := s.Store.GetSchedule(ctx, id)
	if err != nil {
		return err
	}
	h, _, err := s.Household(ctx)
	if err != nil {
		return err
	}
	return s.Store.Tx(ctx, sc.KidID, func(tx store.Tx) error {
		sc, err := tx.GetSchedule(ctx, id)
		if err != nil || sc.Active == active {
			return err
		}
		sc.Active = active
		if active {
			sc.LastOccurrence = s.today(h).String()
		}
		if err := tx.UpdateSchedule(ctx, sc); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor.ID, map[bool]string{true: "schedule.resume", false: "schedule.pause"}[active], sc.KidID, nil)
	})
}

// PostAllowance pays every active schedule's due days, oldest first. Each day
// posts with that day as its effective date and key sched:<id>:<date>, so a
// duplicate run (or a second replica) is a no-op. After downtime only the
// newest CatchUpLimit days are paid. The scheduler calls it every minute.
func (s *Service) PostAllowance(ctx context.Context) (int, error) {
	h, cur, err := s.Household(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil // not set up yet
	}
	if err != nil {
		return 0, err
	}
	scheds, err := s.Store.ListSchedules(ctx, "")
	if err != nil {
		return 0, err
	}
	loc, today := h.Location(), s.today(h)
	posted := 0
	for _, sc := range scheds {
		if !sc.Active {
			continue
		}
		last, _ := allowance.ParseDate(sc.LastOccurrence)
		if len(RuleOf(sc).Occurrences(last, today, CatchUpLimit)) == 0 {
			continue
		}
		n, err := s.postSchedule(ctx, sc.ID, today, loc, cur)
		posted += n
		if err != nil {
			return posted, err
		}
	}
	return posted, nil
}

func (s *Service) postSchedule(ctx context.Context, id string, today allowance.Date, loc *time.Location, cur money.Currency) (int, error) {
	posted := 0
	sc, err := s.Store.GetSchedule(ctx, id)
	if err != nil {
		return 0, err
	}
	kid, err := getKid(ctx, s.Store, sc.KidID)
	if err != nil || kid.Disabled() {
		return 0, err
	}
	err = s.Store.Tx(ctx, sc.KidID, func(tx store.Tx) error {
		sc, err := tx.GetSchedule(ctx, id) // re-read under the kid's lock
		if err != nil || !sc.Active {
			return err
		}
		last, _ := allowance.ParseDate(sc.LastOccurrence)
		for _, day := range RuleOf(sc).Occurrences(last, today, CatchUpLimit) {
			var parts []part
			if sc.TargetJarID != "" {
				if jar, err := jarFor(ctx, tx, sc.KidID, sc.TargetJarID); err == nil {
					parts = []part{{Jar: jar, Amount: sc.Amount}}
				}
			}
			if parts == nil {
				if parts, err = splitParts(ctx, tx, sc.KidID, sc.Amount); err != nil {
					return err
				}
			}
			batch := "sched:" + sc.ID + ":" + day.String()
			at, now := day.Start(loc).UTC(), s.now()
			inserted := false
			for _, p := range parts {
				ok, err := tx.InsertLedgerEntry(ctx, store.LedgerEntry{
					ID: store.NewID(), KidID: sc.KidID, JarID: p.Jar.ID, Amount: p.Amount, Kind: store.KindAllowance,
					Comment: sc.CommentTemplate, BatchID: batch, ScheduleID: sc.ID, IdempotencyKey: batch + ":" + p.Jar.ID,
					EffectiveAt: at, CreatedAt: now,
				})
				if err != nil {
					return err
				}
				inserted = inserted || ok
			}
			if inserted {
				posted++
				if err := notify.Send(ctx, tx, now, []string{sc.KidID}, notify.AllowancePosted, "", notify.Payload{
					KidID: sc.KidID, KidName: kid.DisplayName, Amount: sc.Amount, Jar: jarLabel(parts), Text: sc.CommentTemplate,
				}); err != nil {
					return err
				}
			}
			sc.LastOccurrence = day.String()
		}
		return tx.UpdateSchedule(ctx, sc)
	})
	if err != nil {
		return 0, err
	}
	if posted > 0 {
		s.Log.Info("allowance posted", "kid", kid.Username, "days", posted, "amount", cur.Format(sc.Amount))
	}
	return posted, nil
}

// ScheduleView is a schedule with its next paying day, for display.
type ScheduleView struct {
	store.Schedule
	Rule allowance.Rule
	Next allowance.Date
}

// Describe is "every Saturday" etc.
func (v ScheduleView) Describe() string { return v.Rule.Describe() }

// NextTime is the next paying day as a time, for formatting.
func (v ScheduleView) NextTime() time.Time { return v.Next.Start(time.UTC) }

// Schedules lists a kid's allowances with their next paying days.
func (s *Service) Schedules(ctx context.Context, kidID string) ([]ScheduleView, error) {
	h, _, err := s.Household(ctx)
	if err != nil {
		return nil, err
	}
	scheds, err := s.Store.ListSchedules(ctx, kidID)
	if err != nil {
		return nil, err
	}
	today := s.today(h)
	out := make([]ScheduleView, 0, len(scheds))
	for _, sc := range scheds {
		rule := RuleOf(sc)
		after := today
		if last, _ := allowance.ParseDate(sc.LastOccurrence); today.Before(last) {
			after = last
		}
		out = append(out, ScheduleView{Schedule: sc, Rule: rule, Next: rule.Next(after)})
	}
	return out, nil
}
