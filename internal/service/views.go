package service

import (
	"context"

	"github.com/chinny/goldberry/internal/store"
)

// KidView is everything a kid's home or an admin's kid page shows.
type KidView struct {
	Kid       store.User
	Jars      []JarView
	Pending   []store.WithdrawalRequest
	Recent    []store.LedgerEntry
	Total     int64 // sum of balances
	Held      int64 // sum of pending holds
	Split     []store.SplitRule
	Schedules []ScheduleView
}

// JarView is a jar with its balance, split share and the locks on it today.
type JarView struct {
	store.JarBalance
	BPS   int             // share of the split, in basis points
	Locks []store.JarLock // locks on money leaving this jar
}

// Locked reports whether any lock is on the jar; Hard whether a parent's is.
func (j JarView) Locked() bool { return len(j.Locks) > 0 }

func (j JarView) Hard() bool {
	for _, l := range j.Locks {
		if l.Hard() {
			return true
		}
	}
	return false
}

// SelfLock returns the kid's own lock on the jar, if any.
func (j JarView) SelfLock() *store.JarLock {
	for _, l := range j.Locks {
		if !l.Hard() {
			return &l
		}
	}
	return nil
}

// ParentLock returns a parent's lock on the jar, if any.
func (j JarView) ParentLock() *store.JarLock {
	for _, l := range j.Locks {
		if l.Hard() {
			return &l
		}
	}
	return nil
}

// RequestLock is the strictest lock on asking for money from this jar (a
// whole-jar lock; route locks leave requests open), or nil.
func (j JarView) RequestLock() *store.JarLock {
	var soft *store.JarLock
	for _, l := range j.Locks {
		if l.ToJarID != "" {
			continue
		}
		if l.Hard() {
			return &l
		}
		if soft == nil {
			soft = &l
		}
	}
	return soft
}

// Percent is the jar's split share as a whole percentage (rounded).
func (j JarView) Percent() int { return (j.BPS + 50) / 100 }

// SplitSummary is "70 / 20 / 10".
func (v KidView) SplitSummary() string {
	bal := make([]store.JarBalance, len(v.Jars))
	for i, j := range v.Jars {
		bal[i] = j.JarBalance
	}
	return SplitSummary(bal, v.Split)
}

// Jar returns the kid's jar by ID.
func (v KidView) Jar(id string) (JarView, bool) {
	for _, j := range v.Jars {
		if j.ID == id {
			return j, true
		}
	}
	return JarView{}, false
}

// Available is the total a kid can still ask for.
func (v KidView) Available() int64 { return v.Total - v.Held }

// KidOverview loads a kid's jars, pending requests and recent ledger.
func (s *Service) KidOverview(ctx context.Context, kidID string, recent int) (KidView, error) {
	kid, err := getKid(ctx, s.Store, kidID)
	if err != nil {
		return KidView{}, err
	}
	v := KidView{Kid: kid}
	jars, err := s.Store.JarBalances(ctx, kidID)
	if err != nil {
		return v, err
	}
	if v.Split, err = s.Store.ListSplitRules(ctx, kidID); err != nil {
		return v, err
	}
	locks, err := s.KidLocks(ctx, s.Store, kidID)
	if err != nil {
		return v, err
	}
	bps := map[string]int{}
	for _, r := range v.Split {
		bps[r.JarID] = r.BasisPoints
	}
	for _, j := range jars {
		jv := JarView{JarBalance: j, BPS: bps[j.ID]}
		for _, l := range locks {
			if l.JarID == j.ID {
				jv.Locks = append(jv.Locks, l)
			}
		}
		v.Jars = append(v.Jars, jv)
		v.Total += j.Balance
		v.Held += j.Held
	}
	if v.Schedules, err = s.Schedules(ctx, kidID); err != nil {
		return v, err
	}
	if v.Pending, err = s.Store.ListRequests(ctx, store.RequestFilter{KidID: kidID, Status: store.StatusPending}); err != nil {
		return v, err
	}
	if recent != 0 {
		v.Recent, err = s.Store.ListLedger(ctx, store.LedgerFilter{KidID: kidID, Limit: recent})
	}
	return v, err
}

// Dashboard is the admin home: the pending queue plus a card per kid.
type Dashboard struct {
	Pending []store.WithdrawalRequest
	Kids    []KidView
}

// AdminDashboard loads the admin home.
func (s *Service) AdminDashboard(ctx context.Context) (Dashboard, error) {
	var d Dashboard
	var err error
	if d.Pending, err = s.Store.ListRequests(ctx, store.RequestFilter{Status: store.StatusPending}); err != nil {
		return d, err
	}
	kids, err := s.Store.ListUsers(ctx, store.RoleKid)
	if err != nil {
		return d, err
	}
	for _, k := range kids {
		if k.Disabled() {
			continue
		}
		v, err := s.KidOverview(ctx, k.ID, 0)
		if err != nil {
			return d, err
		}
		d.Kids = append(d.Kids, v)
	}
	return d, nil
}

// LockActivity lists a kid's lock overrides, newest first (admins see these).
func (s *Service) LockActivity(ctx context.Context, kidID string, limit int) ([]store.LockOverride, error) {
	return s.Store.ListLockOverrides(ctx, kidID, limit)
}

// Ledger lists a kid's history, newest first.
func (s *Service) Ledger(ctx context.Context, kidID string, limit int) ([]store.LedgerEntry, error) {
	return s.Store.ListLedger(ctx, store.LedgerFilter{KidID: kidID, Limit: limit})
}

// Requests lists a kid's requests, newest first.
func (s *Service) Requests(ctx context.Context, kidID string, limit int) ([]store.WithdrawalRequest, error) {
	return s.Store.ListRequests(ctx, store.RequestFilter{KidID: kidID, Limit: limit})
}
