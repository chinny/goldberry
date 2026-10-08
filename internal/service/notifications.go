package service

import (
	"context"
	"errors"

	"github.com/chinny/goldberry/internal/notify"
	"github.com/chinny/goldberry/internal/store"
)

// NotificationView is a rendered notification, with its request's live state
// so an admin's bell shows Approve/Deny only while it's still pending.
type NotificationView struct {
	store.Notification
	notify.Message
	Request *store.WithdrawalRequest
}

// Notifications lists a user's notifications, newest first.
func (s *Service) Notifications(ctx context.Context, user store.User, limit int) ([]NotificationView, error) {
	_, cur, err := s.Household(ctx)
	if err != nil {
		return nil, err
	}
	ns, err := s.Store.ListNotifications(ctx, user.ID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]NotificationView, 0, len(ns))
	reqs := map[string]*store.WithdrawalRequest{}
	for _, n := range ns {
		v := NotificationView{Notification: n, Message: notify.Render(n, cur)}
		if n.RequestID != "" {
			r, ok := reqs[n.RequestID]
			if !ok {
				got, err := s.Store.GetRequest(ctx, n.RequestID)
				if err != nil && !errors.Is(err, store.ErrNotFound) {
					return nil, err
				}
				if err == nil {
					r = &got
				}
				reqs[n.RequestID] = r
			}
			v.Request = r
		}
		out = append(out, v)
	}
	return out, nil
}

// UnreadCount is the bell badge.
func (s *Service) UnreadCount(ctx context.Context, user store.User) (int, error) {
	return s.Store.CountUnread(ctx, user.ID)
}

// MarkRead marks some (or, with no ids, all) of a user's notifications read.
func (s *Service) MarkRead(ctx context.Context, user store.User, ids ...string) error {
	return s.Store.Tx(ctx, "", func(tx store.Tx) error {
		return tx.MarkNotificationsRead(ctx, user.ID, ids, s.now())
	})
}
