// Package service owns every rule about money and accounts (plan §3): balance
// checks, holds, reversals, request decisions, throttling. Web handlers call
// it and never the store, so a JSON API can be added later without a refactor.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chinny/goldberry/internal/money"
	"github.com/chinny/goldberry/internal/store"
)

// Service is the domain layer.
type Service struct {
	Store store.Store
	Now   func() time.Time
	Log   *slog.Logger
}

// New returns a Service using the wall clock.
func New(st store.Store, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{Store: st, Now: time.Now, Log: log}
}

func (s *Service) now() time.Time { return s.Now().UTC() }

// Limits.
const (
	MaxPendingRequests = 5
	MaxTextLen         = 140
)

var (
	ErrNotFound       = store.ErrNotFound
	ErrForbidden      = errors.New("you can't do that")
	ErrAlreadySetUp   = errors.New("this Goldberry is already set up")
	ErrBadCredentials = errors.New("that username and PIN or password don't match")
	ErrLocked         = errors.New("this account is locked after too many wrong tries; a parent can unlock it")
)

// UserError is a validation failure whose message is safe to show.
type UserError struct {
	Field string
	Msg   string
}

func (e *UserError) Error() string { return e.Msg }

func invalid(field, format string, args ...any) error {
	return &UserError{Field: field, Msg: fmt.Sprintf(format, args...)}
}

// ThrottledError says to wait before trying to sign in again.
type ThrottledError struct{ Wait time.Duration }

func (e *ThrottledError) Error() string {
	secs := int(e.Wait.Round(time.Second) / time.Second)
	if secs < 1 {
		secs = 1
	}
	if secs < 120 {
		return fmt.Sprintf("Too many wrong tries. Wait %d seconds and try again.", secs)
	}
	return fmt.Sprintf("Too many wrong tries. Wait %d minutes and try again.", (secs+59)/60)
}

// Household returns the household and its currency.
func (s *Service) Household(ctx context.Context) (store.Household, money.Currency, error) {
	h, err := s.Store.GetHousehold(ctx)
	if err != nil {
		return h, money.Currency{}, err
	}
	return h, money.MustLookup(h.Currency), nil
}

// NeedsSetup reports whether no admin exists yet (plan §4.4).
func (s *Service) NeedsSetup(ctx context.Context) (bool, error) {
	n, err := s.Store.CountAdmins(ctx, true)
	return n == 0, err
}

var usernameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,31}$`)

func normalizeUsername(u string) (string, error) {
	u = strings.ToLower(strings.TrimSpace(u))
	if !usernameRE.MatchString(u) {
		return "", invalid("username", "Usernames are 2–32 letters, numbers, dots, dashes or underscores.")
	}
	return u, nil
}

func cleanText(field, s string, required bool) (string, error) {
	s = strings.TrimSpace(s)
	if required && s == "" {
		return "", invalid(field, "This can't be empty.")
	}
	if utf8.RuneCountInString(s) > MaxTextLen {
		return "", invalid(field, "Keep it under %d characters.", MaxTextLen)
	}
	return s, nil
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", invalid("display_name", "Enter a name.")
	}
	if utf8.RuneCountInString(name) > 40 {
		return "", invalid("display_name", "Keep names under 40 characters.")
	}
	return name, nil
}

func (s *Service) audit(ctx context.Context, tx store.Tx, actorID, action, targetID string, detail map[string]any) error {
	var raw json.RawMessage
	if len(detail) > 0 {
		b, err := json.Marshal(detail)
		if err != nil {
			return err
		}
		raw = b
	}
	return tx.InsertAudit(ctx, store.AuditEntry{
		ID: store.NewID(), ActorID: actorID, Action: action, TargetID: targetID, Detail: raw, CreatedAt: s.now(),
	})
}

func requireAdmin(u store.User) error {
	if !u.IsAdmin() || u.Disabled() {
		return ErrForbidden
	}
	return nil
}

// getKid loads a user and checks it is a kid.
func getKid(ctx context.Context, r store.Reader, id string) (store.User, error) {
	u, err := r.GetUser(ctx, id)
	if err != nil {
		return u, err
	}
	if !u.IsKid() {
		return u, ErrNotFound
	}
	return u, nil
}

// jarFor resolves the jar a kid's money moves in: the given one (which must
// be the kid's), or the kid's first jar (Spend).
func jarFor(ctx context.Context, r store.Reader, kidID, jarID string) (store.JarBalance, error) {
	jars, err := r.JarBalances(ctx, kidID)
	if err != nil {
		return store.JarBalance{}, err
	}
	for _, j := range jars {
		if jarID == "" || j.ID == jarID {
			return j, nil
		}
	}
	if jarID == "" {
		return store.JarBalance{}, errors.New("kid has no jars")
	}
	return store.JarBalance{}, invalid("jar", "Pick one of the kid's jars.")
}
