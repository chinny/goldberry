package service

import (
	"context"
	"errors"
	"time"

	"github.com/chinny/goldberry/internal/money"
	"github.com/chinny/goldberry/internal/notify"
	"github.com/chinny/goldberry/internal/store"
)

// ErrInsufficient is returned when available money would go below zero.
type ErrInsufficient struct {
	Available int64
	Jar       string
	Currency  money.Currency
}

func (e *ErrInsufficient) Error() string {
	return "Only " + e.Currency.Format(e.Available) + " is available in " + e.Jar + "."
}

var (
	ErrCannotReverse  = errors.New("a reversal can't itself be reversed; post a new entry instead")
	ErrTooManyPending = errors.New("you already have 5 requests waiting; cancel one or wait for a grown-up")
)

// ErrAlreadyDecided is returned when a request is no longer pending.
type ErrAlreadyDecided struct{ Request store.WithdrawalRequest }

func (e *ErrAlreadyDecided) Error() string {
	r := e.Request
	switch r.Status {
	case store.StatusApproved:
		return "Already approved by " + r.DecidedByName + "."
	case store.StatusDenied:
		return "Already denied by " + r.DecidedByName + "."
	case store.StatusCancelled:
		return "This request was cancelled."
	case store.StatusExpired:
		return "This request expired."
	}
	return "This request was already decided."
}

// EntryInput is an admin adding or removing funds (plan §7.1).
type EntryInput struct {
	KidID          string
	JarID          string // "" = the kid's first jar
	Amount         int64  // positive; Remove makes it a debit
	Remove         bool
	Comment        string // the kid sees this
	PrivateNote    string // admins only
	IdempotencyKey string
}

// PostEntry adds or removes funds. A removal that would take available
// below zero is refused unless the household allows negative balances.
// Reusing an idempotency key returns the original entry and posts nothing.
func (s *Service) PostEntry(ctx context.Context, actor store.User, in EntryInput) (store.LedgerEntry, error) {
	if err := requireAdmin(actor); err != nil {
		return store.LedgerEntry{}, err
	}
	if in.Amount <= 0 {
		return store.LedgerEntry{}, invalid("amount", "Enter an amount above zero.")
	}
	if in.Amount > money.MaxAmount {
		return store.LedgerEntry{}, invalid("amount", "%s", money.ErrTooLarge.Error())
	}
	comment, err := cleanText("comment", in.Comment, false)
	if err != nil {
		return store.LedgerEntry{}, err
	}
	note, err := cleanText("private_note", in.PrivateNote, false)
	if err != nil {
		return store.LedgerEntry{}, err
	}
	h, cur, err := s.Household(ctx)
	if err != nil {
		return store.LedgerEntry{}, err
	}
	kid, err := getKid(ctx, s.Store, in.KidID)
	if err != nil {
		return store.LedgerEntry{}, err
	}

	var out store.LedgerEntry
	err = s.Store.Tx(ctx, kid.ID, func(tx store.Tx) error {
		if in.IdempotencyKey != "" {
			if prev, err := tx.GetLedgerEntryByKey(ctx, in.IdempotencyKey); err == nil {
				if prev.KidID != kid.ID {
					return ErrForbidden
				}
				out = prev
				return nil
			} else if !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		jar, err := jarFor(ctx, tx, kid.ID, in.JarID)
		if err != nil {
			return err
		}
		now := s.now()
		e := store.LedgerEntry{
			ID: store.NewID(), KidID: kid.ID, JarID: jar.ID, Amount: in.Amount, Kind: store.KindDeposit,
			Comment: comment, PrivateNote: note, ActorID: actor.ID, IdempotencyKey: in.IdempotencyKey,
			EffectiveAt: now, CreatedAt: now,
		}
		kind := notify.FundsAdded
		if in.Remove {
			if !h.AllowNegative && jar.Available()-in.Amount < 0 {
				return &ErrInsufficient{Available: jar.Available(), Jar: jar.Name, Currency: cur}
			}
			e.Amount, e.Kind, kind = -in.Amount, store.KindWithdrawal, notify.FundsRemoved
		}
		if _, err := tx.InsertLedgerEntry(ctx, e); err != nil {
			return err
		}
		out = e
		out.JarName, out.ActorName = jar.Name, actor.DisplayName
		return notify.Send(ctx, tx, now, []string{kid.ID}, kind, "", notify.Payload{
			KidID: kid.ID, KidName: kid.DisplayName, ActorName: actor.DisplayName, Amount: in.Amount, Jar: jar.Name, Text: comment,
		})
	})
	return out, err
}

// Reverse posts a reversal that cancels entryID (plan §5.1). Reversing an
// already-reversed entry is a no-op that returns the existing reversal.
func (s *Service) Reverse(ctx context.Context, actor store.User, entryID string) (store.LedgerEntry, error) {
	if err := requireAdmin(actor); err != nil {
		return store.LedgerEntry{}, err
	}
	orig, err := s.Store.GetLedgerEntry(ctx, entryID)
	if err != nil {
		return store.LedgerEntry{}, err
	}
	h, cur, err := s.Household(ctx)
	if err != nil {
		return store.LedgerEntry{}, err
	}
	kid, err := getKid(ctx, s.Store, orig.KidID)
	if err != nil {
		return store.LedgerEntry{}, err
	}
	var out store.LedgerEntry
	err = s.Store.Tx(ctx, orig.KidID, func(tx store.Tx) error {
		orig, err := tx.GetLedgerEntry(ctx, entryID)
		if err != nil {
			return err
		}
		if orig.Kind == store.KindReversal {
			return ErrCannotReverse
		}
		if orig.Reversed() {
			out, err = tx.GetLedgerEntry(ctx, orig.ReversedByID)
			return err
		}
		if orig.Amount > 0 && !h.AllowNegative {
			jar, err := jarFor(ctx, tx, orig.KidID, orig.JarID)
			if err != nil {
				return err
			}
			if jar.Available()-orig.Amount < 0 {
				return &ErrInsufficient{Available: jar.Available(), Jar: jar.Name, Currency: cur}
			}
		}
		now := s.now()
		e := store.LedgerEntry{
			ID: store.NewID(), KidID: orig.KidID, JarID: orig.JarID, Amount: -orig.Amount, Kind: store.KindReversal,
			Comment: orig.Comment, ActorID: actor.ID, ReversesID: orig.ID, IdempotencyKey: "reverse:" + orig.ID,
			EffectiveAt: now, CreatedAt: now,
		}
		if _, err := tx.InsertLedgerEntry(ctx, e); err != nil {
			return err
		}
		out = e
		return notify.Send(ctx, tx, now, []string{orig.KidID}, notify.EntryReversed, "", notify.Payload{
			KidID: kid.ID, KidName: kid.DisplayName, ActorName: actor.DisplayName, Amount: orig.Amount, Jar: orig.JarName, Text: orig.Comment,
		})
	})
	return out, err
}

// RequestInput is a kid asking for money (plan §6).
type RequestInput struct {
	JarID          string
	Amount         int64
	Reason         string
	IdempotencyKey string
}

// CreateRequest places a hold for a withdrawal request. In one transaction it
// checks amount ≤ available(jar) and the pending cap, then inserts the row.
func (s *Service) CreateRequest(ctx context.Context, kid store.User, in RequestInput) (store.WithdrawalRequest, error) {
	if !kid.IsKid() || kid.Disabled() {
		return store.WithdrawalRequest{}, ErrForbidden
	}
	if in.Amount <= 0 {
		return store.WithdrawalRequest{}, invalid("amount", "Enter an amount above zero.")
	}
	reason, err := cleanText("reason", in.Reason, true)
	if err != nil {
		return store.WithdrawalRequest{}, err
	}
	h, cur, err := s.Household(ctx)
	if err != nil {
		return store.WithdrawalRequest{}, err
	}
	var out store.WithdrawalRequest
	err = s.Store.Tx(ctx, kid.ID, func(tx store.Tx) error {
		if in.IdempotencyKey != "" {
			if prev, err := tx.GetRequestByKey(ctx, in.IdempotencyKey); err == nil {
				if prev.KidID != kid.ID {
					return ErrForbidden
				}
				out = prev
				return nil
			} else if !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		if n, err := tx.CountPendingRequests(ctx, kid.ID); err != nil {
			return err
		} else if n >= MaxPendingRequests {
			return ErrTooManyPending
		}
		jar, err := jarFor(ctx, tx, kid.ID, in.JarID)
		if err != nil {
			return err
		}
		if in.Amount > jar.Available() {
			return &ErrInsufficient{Available: jar.Available(), Jar: jar.Name, Currency: cur}
		}
		now := s.now()
		r := store.WithdrawalRequest{
			ID: store.NewID(), KidID: kid.ID, JarID: jar.ID, Amount: in.Amount, Reason: reason,
			Status: store.StatusPending, IdempotencyKey: in.IdempotencyKey, CreatedAt: now,
			KidName: kid.DisplayName, JarName: jar.Name,
		}
		if h.RequestExpiryDays > 0 {
			exp := now.Add(time.Duration(h.RequestExpiryDays) * 24 * time.Hour)
			r.ExpiresAt = &exp
		}
		if _, err := tx.InsertRequest(ctx, r); err != nil {
			return err
		}
		out = r
		admins, err := notify.Admins(ctx, tx)
		if err != nil {
			return err
		}
		return notify.Send(ctx, tx, now, admins, notify.RequestCreated, r.ID, notify.Payload{
			KidID: kid.ID, KidName: kid.DisplayName, Amount: r.Amount, Jar: jar.Name, Text: reason,
		})
	})
	return out, err
}

// CancelRequest lets a kid withdraw their own pending request.
func (s *Service) CancelRequest(ctx context.Context, kid store.User, requestID string) error {
	if !kid.IsKid() {
		return ErrForbidden
	}
	return s.Store.Tx(ctx, kid.ID, func(tx store.Tx) error {
		r, err := tx.GetRequest(ctx, requestID)
		if err != nil {
			return err
		}
		if r.KidID != kid.ID {
			return ErrNotFound
		}
		if r.Status != store.StatusPending {
			return &ErrAlreadyDecided{Request: r}
		}
		now := s.now()
		r.Status, r.DecidedBy, r.DecidedAt = store.StatusCancelled, kid.ID, &now
		if ok, err := tx.DecideRequest(ctx, r); err != nil {
			return err
		} else if !ok {
			return &ErrAlreadyDecided{Request: r}
		}
		admins, err := notify.Admins(ctx, tx)
		if err != nil {
			return err
		}
		return notify.Send(ctx, tx, now, admins, notify.RequestCancelled, r.ID, notify.Payload{
			KidID: kid.ID, KidName: kid.DisplayName, Amount: r.Amount, Jar: r.JarName, Text: r.Reason,
		})
	})
}

// Decision is an admin's answer to a request.
type Decision struct {
	Approve        bool
	ApprovedAmount int64 // 0 = the full amount; may be lower, never higher
	Note           string
}

// DecideRequest approves or denies a pending request. The first decision
// wins; a later one gets ErrAlreadyDecided naming who decided. Approval posts
// exactly one withdrawal entry and can't fail on funds, because the hold
// already kept the money out of available.
func (s *Service) DecideRequest(ctx context.Context, actor store.User, requestID string, d Decision) (store.WithdrawalRequest, error) {
	if err := requireAdmin(actor); err != nil {
		return store.WithdrawalRequest{}, err
	}
	note, err := cleanText("note", d.Note, false)
	if err != nil {
		return store.WithdrawalRequest{}, err
	}
	r, err := s.Store.GetRequest(ctx, requestID)
	if err != nil {
		return r, err
	}
	err = s.Store.Tx(ctx, r.KidID, func(tx store.Tx) error {
		r, err = tx.GetRequest(ctx, requestID)
		if err != nil {
			return err
		}
		if r.Status != store.StatusPending {
			return &ErrAlreadyDecided{Request: r}
		}
		now := s.now()
		r.DecidedBy, r.DecidedByName, r.DecisionNote, r.DecidedAt = actor.ID, actor.DisplayName, note, &now
		kind := notify.RequestDenied
		r.Status = store.StatusDenied
		if d.Approve {
			amt := d.ApprovedAmount
			if amt == 0 {
				amt = r.Amount
			}
			if amt < 0 || amt > r.Amount {
				return invalid("approved_amount", "Approve between a cent and the amount asked for.")
			}
			r.Status, r.ApprovedAmount, kind = store.StatusApproved, amt, notify.RequestApproved
		}
		if ok, err := tx.DecideRequest(ctx, r); err != nil {
			return err
		} else if !ok {
			latest, err := tx.GetRequest(ctx, requestID)
			if err != nil {
				return err
			}
			return &ErrAlreadyDecided{Request: latest}
		}
		if d.Approve {
			if _, err := tx.InsertLedgerEntry(ctx, store.LedgerEntry{
				ID: store.NewID(), KidID: r.KidID, JarID: r.JarID, Amount: -r.ApprovedAmount, Kind: store.KindWithdrawal,
				Comment: r.Reason, ActorID: actor.ID, RequestID: r.ID, IdempotencyKey: "request:" + r.ID,
				EffectiveAt: now, CreatedAt: now,
			}); err != nil {
				return err
			}
		}
		return notify.Send(ctx, tx, now, []string{r.KidID}, kind, r.ID, notify.Payload{
			KidID: r.KidID, KidName: r.KidName, ActorName: actor.DisplayName, Amount: r.Amount,
			ApprovedAmount: r.ApprovedAmount, Jar: r.JarName, Text: r.Reason, Note: note,
		})
	})
	return r, err
}

// ExpireRequests moves stale pending requests to expired, releasing their
// holds, and tells the kid and the admins. The scheduler calls it.
func (s *Service) ExpireRequests(ctx context.Context) (int, error) {
	var n int
	err := s.Store.Tx(ctx, "", func(tx store.Tx) error {
		now := s.now()
		expired, err := tx.ExpireRequests(ctx, now)
		if err != nil {
			return err
		}
		n = len(expired)
		if n == 0 {
			return nil
		}
		admins, err := notify.Admins(ctx, tx)
		if err != nil {
			return err
		}
		for _, r := range expired {
			if err := notify.Send(ctx, tx, now, append([]string{r.KidID}, admins...), notify.RequestExpired, r.ID, notify.Payload{
				KidID: r.KidID, KidName: r.KidName, Amount: r.Amount, Jar: r.JarName, Text: r.Reason,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return n, err
}
