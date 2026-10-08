package service

import (
	"context"
	"errors"
	"fmt"
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
	ErrCannotReverse     = errors.New("a reversal can't itself be reversed; post a new entry instead")
	ErrCannotReverseMove = errors.New("a move between jars can't be undone; move the money back instead")
	ErrTooManyPending    = errors.New("you already have 5 requests waiting; cancel one or wait for a grown-up")
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
	JarID          string // one jar; "" = the kid's first jar (Spend)
	UseSplit       bool   // deposits only: split across jars by the kid's split rule
	Amount         int64  // positive; Remove makes it a debit
	Remove         bool
	Comment        string // the kid sees this
	PrivateNote    string // admins only
	IdempotencyKey string
}

// part is one jar's share of a deposit.
type part struct {
	Jar    store.JarBalance
	Amount int64
}

// splitParts divides amount across a kid's jars by their split rule. Shares
// are floored and the remainder goes to the first jar (Spend), so the parts
// always sum to amount (plan §5.2). Jars with a 0% share are left out.
func splitParts(ctx context.Context, r store.Reader, kidID string, amount int64) ([]part, error) {
	jars, err := r.JarBalances(ctx, kidID)
	if err != nil {
		return nil, err
	}
	rules, err := r.ListSplitRules(ctx, kidID)
	if err != nil {
		return nil, err
	}
	bps := map[string]int{}
	for _, rule := range rules {
		bps[rule.JarID] = rule.BasisPoints
	}
	weights := make([]int, len(jars))
	total := 0
	for i, j := range jars {
		weights[i] = bps[j.ID]
		total += weights[i]
	}
	if len(jars) == 0 {
		return nil, errors.New("kid has no jars")
	}
	if total != 10000 { // no usable rule: everything to the first jar
		weights = make([]int, len(jars))
		weights[0] = 10000
	}
	shares, err := money.Split(amount, weights)
	if err != nil {
		return nil, err
	}
	var out []part
	for i, j := range jars {
		if shares[i] != 0 {
			out = append(out, part{Jar: j, Amount: shares[i]})
		}
	}
	return out, nil
}

// PostEntry adds or removes funds and returns the posted entries (several for
// a split deposit, sharing a batch_id). A removal that would take available
// below zero is refused unless the household allows negative balances.
// Reusing an idempotency key returns the original entries and posts nothing.
func (s *Service) PostEntry(ctx context.Context, actor store.User, in EntryInput) ([]store.LedgerEntry, error) {
	if err := requireAdmin(actor); err != nil {
		return nil, err
	}
	if in.Amount <= 0 {
		return nil, invalid("amount", "Enter an amount above zero.")
	}
	if in.Amount > money.MaxAmount {
		return nil, invalid("amount", "%s", money.ErrTooLarge.Error())
	}
	if in.UseSplit && in.Remove {
		return nil, invalid("jar", "Pick one jar to remove money from.")
	}
	comment, err := cleanText("comment", in.Comment, false)
	if err != nil {
		return nil, err
	}
	note, err := cleanText("private_note", in.PrivateNote, false)
	if err != nil {
		return nil, err
	}
	h, cur, err := s.Household(ctx)
	if err != nil {
		return nil, err
	}
	kid, err := getKid(ctx, s.Store, in.KidID)
	if err != nil {
		return nil, err
	}

	var out []store.LedgerEntry
	err = s.Store.Tx(ctx, kid.ID, func(tx store.Tx) error {
		if in.IdempotencyKey != "" {
			prev, err := priorEntries(ctx, tx, in.IdempotencyKey)
			if err != nil {
				return err
			}
			if len(prev) > 0 {
				if prev[0].KidID != kid.ID {
					return ErrForbidden
				}
				out = prev
				return nil
			}
		}
		var parts []part
		if in.UseSplit {
			if parts, err = splitParts(ctx, tx, kid.ID, in.Amount); err != nil {
				return err
			}
		} else {
			jar, err := jarFor(ctx, tx, kid.ID, in.JarID)
			if err != nil {
				return err
			}
			parts = []part{{Jar: jar, Amount: in.Amount}}
		}
		now := s.now()
		kind := notify.FundsAdded
		if in.Remove {
			jar := parts[0].Jar
			if !h.AllowNegative && jar.Available()-in.Amount < 0 {
				return &ErrInsufficient{Available: jar.Available(), Jar: jar.Name, Currency: cur}
			}
			kind = notify.FundsRemoved
		}
		batch := ""
		if len(parts) > 1 {
			batch = store.NewID()
		}
		for _, p := range parts {
			e := store.LedgerEntry{
				ID: store.NewID(), KidID: kid.ID, JarID: p.Jar.ID, Amount: p.Amount, Kind: store.KindDeposit,
				Comment: comment, PrivateNote: note, ActorID: actor.ID, BatchID: batch,
				IdempotencyKey: partKey(in.IdempotencyKey, p.Jar.ID, len(parts) > 1),
				EffectiveAt:    now, CreatedAt: now, JarName: p.Jar.Name, ActorName: actor.DisplayName,
			}
			if in.Remove {
				e.Amount, e.Kind = -p.Amount, store.KindWithdrawal
			}
			if _, err := tx.InsertLedgerEntry(ctx, e); err != nil {
				return err
			}
			out = append(out, e)
		}
		return notify.Send(ctx, tx, now, []string{kid.ID}, kind, "", notify.Payload{
			KidID: kid.ID, KidName: kid.DisplayName, ActorName: actor.DisplayName, Amount: in.Amount,
			Jar: jarLabel(parts), Text: comment,
		})
	})
	return out, err
}

// partKey derives each split part's idempotency key from the form's key.
func partKey(key, jarID string, split bool) string {
	if key == "" || !split {
		return key
	}
	return key + ":" + jarID
}

// priorEntries finds entries already posted under a form key: the entry
// itself, or the batch a split deposit made with keys "<key>:<jar>".
func priorEntries(ctx context.Context, r store.Reader, key string) ([]store.LedgerEntry, error) {
	if e, err := r.GetLedgerEntryByKey(ctx, key); err == nil {
		return []store.LedgerEntry{e}, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	e, err := r.GetLedgerEntryByKeyPrefix(ctx, key+":")
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if e.BatchID == "" {
		return []store.LedgerEntry{e}, nil
	}
	return r.ListBatch(ctx, e.BatchID)
}

// jarLabel names where a posting went: "Spend", or "3 jars".
func jarLabel(parts []part) string {
	if len(parts) == 1 {
		return parts[0].Jar.Name
	}
	return fmt.Sprintf("%d jars", len(parts))
}

// Reverse posts reversals that cancel entryID (plan §5.1). An entry that is
// part of a batch (a split deposit or an allowance) is reversed together with
// the rest of its batch. Reversing again is a no-op that returns the existing
// reversals.
func (s *Service) Reverse(ctx context.Context, actor store.User, entryID string) ([]store.LedgerEntry, error) {
	if err := requireAdmin(actor); err != nil {
		return nil, err
	}
	first, err := s.Store.GetLedgerEntry(ctx, entryID)
	if err != nil {
		return nil, err
	}
	h, cur, err := s.Household(ctx)
	if err != nil {
		return nil, err
	}
	kid, err := getKid(ctx, s.Store, first.KidID)
	if err != nil {
		return nil, err
	}
	var out []store.LedgerEntry
	err = s.Store.Tx(ctx, first.KidID, func(tx store.Tx) error {
		orig, err := tx.GetLedgerEntry(ctx, entryID)
		if err != nil {
			return err
		}
		if orig.Kind == store.KindReversal {
			return ErrCannotReverse
		}
		if orig.TransferID != "" {
			return ErrCannotReverseMove
		}
		group := []store.LedgerEntry{orig}
		if orig.BatchID != "" {
			if group, err = tx.ListBatch(ctx, orig.BatchID); err != nil {
				return err
			}
		}
		now := s.now()
		var total int64
		for _, e := range group {
			if e.Reversed() {
				prev, err := tx.GetLedgerEntry(ctx, e.ReversedByID)
				if err != nil {
					return err
				}
				out = append(out, prev)
				continue
			}
			if e.Amount > 0 && !h.AllowNegative {
				jar, err := jarFor(ctx, tx, e.KidID, e.JarID)
				if err != nil {
					return err
				}
				if jar.Available()-e.Amount < 0 {
					return &ErrInsufficient{Available: jar.Available(), Jar: jar.Name, Currency: cur}
				}
			}
			r := store.LedgerEntry{
				ID: store.NewID(), KidID: e.KidID, JarID: e.JarID, Amount: -e.Amount, Kind: store.KindReversal,
				Comment: e.Comment, ActorID: actor.ID, ReversesID: e.ID, IdempotencyKey: "reverse:" + e.ID,
				EffectiveAt: now, CreatedAt: now, JarName: e.JarName,
			}
			if _, err := tx.InsertLedgerEntry(ctx, r); err != nil {
				return err
			}
			out = append(out, r)
			total += e.Amount
		}
		if total == 0 {
			return nil // everything was already reversed
		}
		jar := orig.JarName
		if len(group) > 1 {
			jar = fmt.Sprintf("%d jars", len(group))
		}
		return notify.Send(ctx, tx, now, []string{orig.KidID}, notify.EntryReversed, "", notify.Payload{
			KidID: kid.ID, KidName: kid.DisplayName, ActorName: actor.DisplayName, Amount: total, Jar: jar, Text: orig.Comment,
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
	Override       string // gauntlet token, for a self-locked jar
	RemoveLock     bool   // with Override: remove the lock rather than break it once
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
	key, err := s.overrideKey(ctx)
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
		overridden, err := s.checkLocks(ctx, tx, kid, kid.ID, jar.ID, "", in.Override, key)
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
		if len(overridden) > 0 {
			action := "once"
			if in.RemoveLock {
				action = "removed"
			}
			if err := s.recordOverride(ctx, tx, kid, overridden, action, r.Amount, "", r.ID); err != nil {
				return err
			}
		}
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
