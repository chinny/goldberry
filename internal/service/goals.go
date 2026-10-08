package service

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/chinny/goldberry/internal/allowance"
	"github.com/chinny/goldberry/internal/money"
	"github.com/chinny/goldberry/internal/notify"
	"github.com/chinny/goldberry/internal/store"
)

// --- goals (plan §7.3) -----------------------------------------------------------

// GoalInput creates or edits a goal.
type GoalInput struct {
	Name     string
	Emoji    string
	Target   int64
	JarID    string // "" = the kid's Save jar (or first jar)
	Priority int
}

// GoalView is a goal with its progress: the jar's available balance fills
// goals in priority order, so moving money never strands it in a goal.
type GoalView struct {
	store.Goal
	JarName string
	Funded  int64
}

// Percent is progress as a whole percentage, 0–100.
func (g GoalView) Percent() int {
	if g.TargetAmount <= 0 {
		return 0
	}
	return int(min(g.Funded*100/g.TargetAmount, 100))
}

// Reached reports whether the jar currently covers the whole target.
func (g GoalView) Reached() bool { return g.Funded >= g.TargetAmount }

// Remaining is what's still needed.
func (g GoalView) Remaining() int64 { return max(g.TargetAmount-g.Funded, 0) }

// waterfall fills goals from each jar's available balance in priority order.
func waterfall(goals []store.Goal, jars []store.JarBalance) []GoalView {
	left := map[string]int64{}
	names := map[string]string{}
	for _, j := range jars {
		left[j.ID] = max(j.Available(), 0)
		names[j.ID] = j.Name
	}
	out := make([]GoalView, 0, len(goals))
	for _, g := range goals { // already in priority order
		funded := min(left[g.JarID], g.TargetAmount)
		left[g.JarID] -= funded
		out = append(out, GoalView{Goal: g, JarName: names[g.JarID], Funded: funded})
	}
	return out
}

// Goals returns a kid's active goals with progress.
func (s *Service) Goals(ctx context.Context, r store.Reader, kidID string) ([]GoalView, error) {
	goals, err := r.ListGoals(ctx, kidID, false)
	if err != nil || len(goals) == 0 {
		return nil, err
	}
	jars, err := r.JarBalances(ctx, kidID)
	if err != nil {
		return nil, err
	}
	return waterfall(goals, jars), nil
}

// GoalImpact is how a goal's progress would change if amount left a jar.
type GoalImpact struct {
	Name          string
	Before, After int
}

// GoalImpacts reports the goals in jarID whose progress drops if amount
// leaves it (the gauntlet shows these: "Switch 64% → 22%").
func (s *Service) GoalImpacts(ctx context.Context, kidID, jarID string, amount int64) ([]GoalImpact, error) {
	goals, err := s.Store.ListGoals(ctx, kidID, false)
	if err != nil || len(goals) == 0 {
		return nil, err
	}
	jars, err := s.Store.JarBalances(ctx, kidID)
	if err != nil {
		return nil, err
	}
	before := waterfall(goals, jars)
	for i := range jars {
		if jars[i].ID == jarID {
			jars[i].Balance -= amount
		}
	}
	after := waterfall(goals, jars)
	var out []GoalImpact
	for i := range before {
		if before[i].JarID == jarID && after[i].Percent() != before[i].Percent() {
			out = append(out, GoalImpact{Name: before[i].Name, Before: before[i].Percent(), After: after[i].Percent()})
		}
	}
	return out, nil
}

func (s *Service) goalFields(ctx context.Context, r store.Reader, kidID string, in GoalInput) (store.Goal, error) {
	name, err := cleanText("name", in.Name, true)
	if err != nil {
		return store.Goal{}, err
	}
	if utf8.RuneCountInString(name) > 40 {
		return store.Goal{}, invalid("name", "Keep goal names under 40 characters.")
	}
	if in.Target <= 0 || in.Target > money.MaxAmount {
		return store.Goal{}, invalid("target", "How much does it cost?")
	}
	if utf8.RuneCountInString(in.Emoji) > 8 {
		return store.Goal{}, invalid("emoji", "Pick one emoji (or leave it empty).")
	}
	jarID := in.JarID
	if jarID == "" {
		jars, err := r.ListJars(ctx, kidID)
		if err != nil {
			return store.Goal{}, err
		}
		if len(jars) == 0 {
			return store.Goal{}, errors.New("kid has no jars")
		}
		jarID = jars[0].ID
		for _, j := range jars {
			if j.Kind == store.JarSave {
				jarID = j.ID
				break
			}
		}
	} else if _, err := jarFor(ctx, r, kidID, jarID); err != nil {
		return store.Goal{}, err
	}
	return store.Goal{KidID: kidID, JarID: jarID, Name: name, Emoji: in.Emoji, TargetAmount: in.Target, Priority: in.Priority}, nil
}

// goalOwner resolves whose goal an actor may touch: kids only their own.
func goalOwner(actor store.User, kidID string) (string, error) {
	switch {
	case actor.Disabled():
		return "", ErrForbidden
	case actor.IsKid():
		return actor.ID, nil
	case actor.IsAdmin():
		return kidID, nil
	}
	return "", ErrForbidden
}

// CreateGoal adds a goal. Kids add their own; admins add for any kid.
func (s *Service) CreateGoal(ctx context.Context, actor store.User, kidID string, in GoalInput) (store.Goal, error) {
	kidID, err := goalOwner(actor, kidID)
	if err != nil {
		return store.Goal{}, err
	}
	if _, err := getKid(ctx, s.Store, kidID); err != nil {
		return store.Goal{}, err
	}
	g, err := s.goalFields(ctx, s.Store, kidID, in)
	if err != nil {
		return g, err
	}
	err = s.Store.Tx(ctx, kidID, func(tx store.Tx) error {
		existing, err := tx.ListGoals(ctx, kidID, false)
		if err != nil {
			return err
		}
		if in.Priority == 0 { // new goals go to the back of the line
			for _, e := range existing {
				g.Priority = max(g.Priority, e.Priority+1)
			}
		}
		g.ID, g.CreatedBy, g.CreatedAt = store.NewID(), actor.ID, s.now()
		return tx.CreateGoal(ctx, g)
	})
	return g, err
}

// UpdateGoal edits a goal. Kids edit their own; admins edit any.
func (s *Service) UpdateGoal(ctx context.Context, actor store.User, goalID string, in GoalInput) error {
	old, err := s.Store.GetGoal(ctx, goalID)
	if err != nil {
		return err
	}
	if kid, err := goalOwner(actor, old.KidID); err != nil || kid != old.KidID {
		return ErrNotFound
	}
	g, err := s.goalFields(ctx, s.Store, old.KidID, in)
	if err != nil {
		return err
	}
	g.ID, g.CreatedBy, g.CreatedAt, g.ArchivedAt = old.ID, old.CreatedBy, old.CreatedAt, old.ArchivedAt
	if g.TargetAmount == old.TargetAmount {
		g.ReachedAt = old.ReachedAt // a new target is a new milestone to celebrate
	}
	return s.Store.Tx(ctx, old.KidID, func(tx store.Tx) error { return tx.UpdateGoal(ctx, g) })
}

// ArchiveGoal removes a goal from view. Its history stays.
func (s *Service) ArchiveGoal(ctx context.Context, actor store.User, goalID string) error {
	g, err := s.Store.GetGoal(ctx, goalID)
	if err != nil {
		return err
	}
	if kid, err := goalOwner(actor, g.KidID); err != nil || kid != g.KidID {
		return ErrNotFound
	}
	return s.Store.Tx(ctx, g.KidID, func(tx store.Tx) error {
		now := s.now()
		g.ArchivedAt = &now
		return tx.UpdateGoal(ctx, g)
	})
}

// CheckGoals marks goals that are now fully funded and tells the kid and the
// admins, once per goal. The scheduler calls it every minute.
func (s *Service) CheckGoals(ctx context.Context) (int, error) {
	kids, err := s.Store.ListUsers(ctx, store.RoleKid)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, k := range kids {
		views, err := s.Goals(ctx, s.Store, k.ID)
		if err != nil {
			return n, err
		}
		for _, v := range views {
			if !v.Reached() || v.ReachedAt != nil {
				continue
			}
			err := s.Store.Tx(ctx, k.ID, func(tx store.Tx) error {
				g, err := tx.GetGoal(ctx, v.ID)
				if err != nil || g.ReachedAt != nil || g.ArchivedAt != nil {
					return err
				}
				now := s.now()
				g.ReachedAt = &now
				if err := tx.UpdateGoal(ctx, g); err != nil {
					return err
				}
				admins, err := notify.Admins(ctx, tx)
				if err != nil {
					return err
				}
				n++
				return notify.Send(ctx, tx, now, append([]string{k.ID}, admins...), notify.GoalReached, "", notify.Payload{
					KidID: k.ID, KidName: k.DisplayName, Amount: g.TargetAmount, Jar: v.JarName, Text: g.Name,
				})
			})
			if err != nil {
				return n, err
			}
		}
	}
	return n, nil
}

// --- interest (plan §7.4) ----------------------------------------------------------

// SetInterest sets a kid's monthly interest on a jar, in basis points
// (100 = 1% a month), with an optional monthly cap.
func (s *Service) SetInterest(ctx context.Context, actor store.User, kidID, jarID string, bps int, monthlyCap int64, active bool) error {
	if err := requireAdmin(actor); err != nil {
		return err
	}
	if bps < 0 || bps > 2000 {
		return invalid("rate", "Use a monthly rate between 0%% and 20%%.")
	}
	if monthlyCap < 0 || monthlyCap > money.MaxAmount {
		return invalid("cap", "The cap can't be negative.")
	}
	return s.Store.Tx(ctx, kidID, func(tx store.Tx) error {
		if _, err := jarFor(ctx, tx, kidID, jarID); err != nil {
			return err
		}
		if err := tx.PutInterestRule(ctx, store.InterestRule{KidID: kidID, JarID: jarID, MonthlyBPS: bps,
			MonthlyCap: monthlyCap, Active: active && bps > 0}); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor.ID, "interest.set", kidID, map[string]any{"jar": jarID, "bps": bps, "cap": monthlyCap})
	})
}

// InterestRules returns a kid's interest rules by jar ID.
func (s *Service) InterestRules(ctx context.Context, kidID string) (map[string]store.InterestRule, error) {
	rules, err := s.Store.ListInterestRules(ctx, kidID)
	if err != nil {
		return nil, err
	}
	out := map[string]store.InterestRule{}
	for _, r := range rules {
		out[r.JarID] = r
	}
	return out, nil
}

// monthInterest is one month's interest on a jar: the average end-of-day
// balance over the month (household time zone) times the monthly rate,
// floored to a cent and capped. Averaging daily removes the end-of-month
// deposit trick: money added on the 30th earns almost nothing.
func monthInterest(start int64, entries []store.DatedAmount, from allowance.Date, loc *time.Location, bps int, monthlyCap int64) (interest, avg int64, days int) {
	bal, i := start, 0
	var sum int64
	for d := from; d.Month == from.Month; d = d.AddDays(1) {
		end := d.AddDays(1).Start(loc)
		for i < len(entries) && entries[i].EffectiveAt.Before(end) {
			bal += entries[i].Amount
			i++
		}
		sum += bal
		days++
	}
	if days == 0 || sum <= 0 {
		return 0, 0, days
	}
	avg = sum / int64(days)
	interest = sum * int64(bps) / (int64(days) * 10000)
	if monthlyCap > 0 {
		interest = min(interest, monthlyCap)
	}
	return interest, avg, days
}

// PostInterest pays last month's interest on every active rule. It runs every
// minute; the key interest:<kid>:<jar>:<YYYY-MM> makes repeats no-ops.
func (s *Service) PostInterest(ctx context.Context) (int, error) {
	h, cur, err := s.Household(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	rules, err := s.Store.ListInterestRules(ctx, "")
	if err != nil {
		return 0, err
	}
	loc, today := h.Location(), s.today(h)
	thisMonth := allowance.Date{Year: today.Year, Month: today.Month, Day: 1}
	lastMonth := thisMonth.AddDays(-1)
	lastMonth.Day = 1
	label := lastMonth.Start(loc).Format("January")
	n := 0
	for _, r := range rules {
		if !r.Active || r.MonthlyBPS <= 0 {
			continue
		}
		key := fmt.Sprintf("interest:%s:%s:%04d-%02d", r.KidID, r.JarID, lastMonth.Year, int(lastMonth.Month))
		if _, err := s.Store.GetLedgerEntryByKey(ctx, key); err == nil {
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return n, err
		}
		kid, err := getKid(ctx, s.Store, r.KidID)
		if err != nil || kid.Disabled() {
			continue
		}
		err = s.Store.Tx(ctx, r.KidID, func(tx store.Tx) error {
			jar, err := jarFor(ctx, tx, r.KidID, r.JarID)
			var gone *UserError
			if errors.As(err, &gone) {
				return nil // the jar was archived: nothing to pay
			}
			if err != nil {
				return err
			}
			from, to := lastMonth.Start(loc), thisMonth.Start(loc)
			start, err := tx.JarBalanceBefore(ctx, jar.ID, from)
			if err != nil {
				return err
			}
			entries, err := tx.JarEntriesBetween(ctx, jar.ID, from, to)
			if err != nil {
				return err
			}
			amount, avg, days := monthInterest(start, entries, lastMonth, loc, r.MonthlyBPS, r.MonthlyCap)
			if amount <= 0 {
				return nil
			}
			now := s.now()
			comment := fmt.Sprintf("Interest · %s — avg %s × %s%% · %d days", label, cur.Format(avg), pctString(r.MonthlyBPS), days)
			ok, err := tx.InsertLedgerEntry(ctx, store.LedgerEntry{
				ID: store.NewID(), KidID: r.KidID, JarID: jar.ID, Amount: amount, Kind: store.KindInterest, Comment: comment,
				IdempotencyKey: key, EffectiveAt: to.UTC(), CreatedAt: now,
			})
			if err != nil || !ok {
				return err
			}
			n++
			return notify.Send(ctx, tx, now, []string{r.KidID}, notify.InterestPosted, "", notify.Payload{
				KidID: r.KidID, KidName: kid.DisplayName, Amount: amount, Jar: jar.Name, Text: comment,
			})
		})
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func pctString(bps int) string {
	if bps%100 == 0 {
		return fmt.Sprint(bps / 100)
	}
	return fmt.Sprintf("%.2g", float64(bps)/100)
}

// Projection is what a balance grows to after months of monthly interest if
// left alone (the number that's the lesson, plan §7.4).
func Projection(balance int64, bps int, monthlyCap int64, months int) int64 {
	for range months {
		if balance <= 0 {
			break
		}
		gain := balance * int64(bps) / 10000
		if monthlyCap > 0 {
			gain = min(gain, monthlyCap)
		}
		balance += gain
	}
	return balance
}
