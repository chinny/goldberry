package service

import (
	"context"

	"github.com/chinny/goldberry/internal/store"
)

// KidView is everything a kid's home or an admin's kid page shows.
type KidView struct {
	Kid     store.User
	Jars    []store.JarBalance
	Pending []store.WithdrawalRequest
	Recent  []store.LedgerEntry
	Total   int64 // sum of balances
	Held    int64 // sum of pending holds
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
	if v.Jars, err = s.Store.JarBalances(ctx, kidID); err != nil {
		return v, err
	}
	for _, j := range v.Jars {
		v.Total += j.Balance
		v.Held += j.Held
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

// Ledger lists a kid's history, newest first.
func (s *Service) Ledger(ctx context.Context, kidID string, limit int) ([]store.LedgerEntry, error) {
	return s.Store.ListLedger(ctx, store.LedgerFilter{KidID: kidID, Limit: limit})
}

// Requests lists a kid's requests, newest first.
func (s *Service) Requests(ctx context.Context, kidID string, limit int) ([]store.WithdrawalRequest, error) {
	return s.Store.ListRequests(ctx, store.RequestFilter{KidID: kidID, Limit: limit})
}
