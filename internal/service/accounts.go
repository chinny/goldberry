package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/chinny/goldberry/internal/auth"
	"github.com/chinny/goldberry/internal/money"
	"github.com/chinny/goldberry/internal/notify"
	"github.com/chinny/goldberry/internal/store"
)

// Session lifetimes (plan §4.1).
const (
	AdminSession       = 7 * 24 * time.Hour
	KidSession         = 12 * time.Hour
	KidRememberSession = 30 * 24 * time.Hour
	failureWindow      = 24 * time.Hour
	touchEvery         = time.Minute
)

// SetupInput is the first-run wizard.
type SetupInput struct {
	HouseholdName string
	Currency      string
	Timezone      string
	Username      string
	DisplayName   string
	Password      string
}

// Setup creates the household and the first admin. It refuses once any
// admin exists.
func (s *Service) Setup(ctx context.Context, in SetupInput) (store.User, error) {
	name, err := cleanText("household_name", in.HouseholdName, true)
	if err != nil {
		return store.User{}, err
	}
	cur, err := money.Lookup(in.Currency)
	if err != nil {
		return store.User{}, invalid("currency", "Use a three-letter currency code like USD.")
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil || in.Timezone == "" {
		return store.User{}, invalid("timezone", "Use a time zone name like America/New_York.")
	}
	admin, err := s.newAdmin(in.Username, in.DisplayName, in.Password, "")
	if err != nil {
		return store.User{}, err
	}
	err = s.Store.Tx(ctx, "", func(tx store.Tx) error {
		if n, err := tx.CountAdmins(ctx, true); err != nil {
			return err
		} else if n > 0 {
			return ErrAlreadySetUp
		}
		if _, err := tx.GetHousehold(ctx); errors.Is(err, store.ErrNotFound) {
			if err := tx.CreateHousehold(ctx, store.Household{
				ID: store.NewID(), Name: name, Currency: cur.Code, Timezone: in.Timezone,
				PINLength: 4, RequestExpiryDays: 14, CreatedAt: s.now(),
			}); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if err := tx.CreateUser(ctx, admin); err != nil {
			return usernameTaken(err)
		}
		return s.audit(ctx, tx, admin.ID, "setup", admin.ID, map[string]any{"household": name})
	})
	return admin, err
}

func usernameTaken(err error) error {
	if errors.Is(err, store.ErrConflict) {
		return invalid("username", "That username is taken.")
	}
	return err
}

func (s *Service) newAdmin(username, displayName, password, email string) (store.User, error) {
	u, err := normalizeUsername(username)
	if err != nil {
		return store.User{}, err
	}
	dn, err := cleanName(displayName)
	if err != nil {
		return store.User{}, err
	}
	if err := auth.CheckPassword(password); err != nil {
		return store.User{}, invalid("password", "%s", capitalize(err.Error()))
	}
	email = strings.TrimSpace(email)
	if email != "" && !strings.Contains(email, "@") {
		return store.User{}, invalid("email", "That doesn't look like an email address.")
	}
	hash, err := auth.Hash(password)
	if err != nil {
		return store.User{}, err
	}
	return store.User{ID: store.NewID(), Role: store.RoleAdmin, Username: u, DisplayName: dn,
		PasswordHash: hash, Email: email, CreatedAt: s.now()}, nil
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// AdminInput adds a parent.
type AdminInput struct{ Username, DisplayName, Password, Email string }

// CreateAdmin adds another admin.
func (s *Service) CreateAdmin(ctx context.Context, actor store.User, in AdminInput) (store.User, error) {
	if err := requireAdmin(actor); err != nil {
		return store.User{}, err
	}
	u, err := s.newAdmin(in.Username, in.DisplayName, in.Password, in.Email)
	if err != nil {
		return u, err
	}
	err = s.Store.Tx(ctx, "", func(tx store.Tx) error {
		if err := tx.CreateUser(ctx, u); err != nil {
			return usernameTaken(err)
		}
		return s.audit(ctx, tx, actor.ID, "user.create", u.ID, map[string]any{"role": "admin", "username": u.Username})
	})
	return u, err
}

// KidInput adds a kid.
type KidInput struct{ Username, DisplayName, PIN string }

// CreateKid adds a kid with Spend/Save/Give jars split 70/20/10.
func (s *Service) CreateKid(ctx context.Context, actor store.User, in KidInput) (store.User, error) {
	if err := requireAdmin(actor); err != nil {
		return store.User{}, err
	}
	h, _, err := s.Household(ctx)
	if err != nil {
		return store.User{}, err
	}
	username, err := normalizeUsername(in.Username)
	if err != nil {
		return store.User{}, err
	}
	dn, err := cleanName(in.DisplayName)
	if err != nil {
		return store.User{}, err
	}
	if err := auth.CheckPIN(in.PIN, h.PINLength); err != nil {
		return store.User{}, invalid("pin", "%s", err.Error())
	}
	hash, err := auth.Hash(in.PIN)
	if err != nil {
		return store.User{}, err
	}
	kid := store.User{ID: store.NewID(), Role: store.RoleKid, Username: username, DisplayName: dn, PINHash: hash, CreatedAt: s.now()}
	err = s.Store.Tx(ctx, "", func(tx store.Tx) error {
		if err := tx.CreateUser(ctx, kid); err != nil {
			return usernameTaken(err)
		}
		for i, d := range DefaultJars {
			jar := store.Jar{ID: store.NewID(), KidID: kid.ID, Name: d.Name, Kind: d.Kind, SortOrder: i}
			if err := tx.CreateJar(ctx, jar); err != nil {
				return err
			}
			if err := tx.PutSplitRule(ctx, store.SplitRule{KidID: kid.ID, JarID: jar.ID, BasisPoints: d.BPS}); err != nil {
				return err
			}
		}
		return s.audit(ctx, tx, actor.ID, "user.create", kid.ID, map[string]any{"role": "kid", "username": kid.Username})
	})
	return kid, err
}

// ResetSecret sets a new PIN (kids) or password (admins) and unlocks the
// account. actor is nil when called from the CLI.
func (s *Service) ResetSecret(ctx context.Context, actor *store.User, userID, secret string) error {
	actorID := ""
	if actor != nil {
		if err := requireAdmin(*actor); err != nil {
			return err
		}
		actorID = actor.ID
	}
	u, err := s.Store.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if u.IsKid() {
		h, _, err := s.Household(ctx)
		if err != nil {
			return err
		}
		if err := auth.CheckPIN(secret, h.PINLength); err != nil {
			return invalid("pin", "%s", err.Error())
		}
	} else if err := auth.CheckPassword(secret); err != nil {
		return invalid("password", "%s", capitalize(err.Error()))
	}
	hash, err := auth.Hash(secret)
	if err != nil {
		return err
	}
	return s.Store.Tx(ctx, "", func(tx store.Tx) error {
		u, err := tx.GetUser(ctx, userID)
		if err != nil {
			return err
		}
		action := "user.reset_password"
		if u.IsKid() {
			u.PINHash, action = hash, "user.reset_pin"
		} else {
			u.PasswordHash = hash
		}
		if err := tx.UpdateUser(ctx, u); err != nil {
			return err
		}
		if err := tx.ClearAuthAttempts(ctx, u.ID); err != nil {
			return err
		}
		return s.audit(ctx, tx, actorID, action, u.ID, nil)
	})
}

// Unlock clears a locked or throttled account.
func (s *Service) Unlock(ctx context.Context, actor store.User, userID string) error {
	if err := requireAdmin(actor); err != nil {
		return err
	}
	return s.Store.Tx(ctx, "", func(tx store.Tx) error {
		if _, err := tx.GetUser(ctx, userID); err != nil {
			return err
		}
		if err := tx.ClearAuthAttempts(ctx, userID); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor.ID, "user.unlock", userID, nil)
	})
}

// SetDisabled disables or re-enables an account. Disabling revokes its
// sessions. The last active admin can't be disabled, nor can you disable
// yourself.
func (s *Service) SetDisabled(ctx context.Context, actor store.User, userID string, disabled bool) error {
	if err := requireAdmin(actor); err != nil {
		return err
	}
	if disabled && actor.ID == userID {
		return invalid("", "You can't disable your own account.")
	}
	return s.Store.Tx(ctx, "", func(tx store.Tx) error {
		u, err := tx.GetUser(ctx, userID)
		if err != nil {
			return err
		}
		if disabled == u.Disabled() {
			return nil
		}
		now := s.now()
		if disabled {
			if u.IsAdmin() {
				if n, err := tx.CountAdmins(ctx, false); err != nil {
					return err
				} else if n <= 1 {
					return invalid("", "At least one admin must always exist.")
				}
			}
			u.DisabledAt = &now
			if err := tx.RevokeUserSessions(ctx, u.ID, now); err != nil {
				return err
			}
		} else {
			u.DisabledAt = nil
		}
		if err := tx.UpdateUser(ctx, u); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor.ID, map[bool]string{true: "user.disable", false: "user.enable"}[disabled], u.ID, nil)
	})
}

// RevokeSessions signs a user out everywhere (the shared-tablet fix).
func (s *Service) RevokeSessions(ctx context.Context, actor store.User, userID string) error {
	if actor.ID != userID {
		if err := requireAdmin(actor); err != nil {
			return err
		}
	}
	return s.Store.Tx(ctx, "", func(tx store.Tx) error {
		if err := tx.RevokeUserSessions(ctx, userID, s.now()); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor.ID, "user.revoke_sessions", userID, nil)
	})
}

// HouseholdInput is the editable part of the household.
type HouseholdInput struct {
	Name              string
	Timezone          string
	PINLength         int
	AllowNegative     bool
	RequestExpiryDays int
}

// UpdateHousehold saves household settings. The currency is fixed at setup.
func (s *Service) UpdateHousehold(ctx context.Context, actor store.User, in HouseholdInput) error {
	if err := requireAdmin(actor); err != nil {
		return err
	}
	name, err := cleanText("name", in.Name, true)
	if err != nil {
		return err
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil || in.Timezone == "" {
		return invalid("timezone", "Use a time zone name like America/New_York.")
	}
	if in.PINLength < 4 || in.PINLength > 6 {
		return invalid("pin_length", "PINs are 4, 5 or 6 digits.")
	}
	if in.RequestExpiryDays < 0 || in.RequestExpiryDays > 365 {
		return invalid("request_expiry_days", "Use 0 (never) to 365 days.")
	}
	return s.Store.Tx(ctx, "", func(tx store.Tx) error {
		h, err := tx.GetHousehold(ctx)
		if err != nil {
			return err
		}
		h.Name, h.Timezone, h.PINLength, h.AllowNegative, h.RequestExpiryDays = name, in.Timezone, in.PINLength, in.AllowNegative, in.RequestExpiryDays
		if err := tx.UpdateHousehold(ctx, h); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor.ID, "household.update", h.ID, map[string]any{
			"pin_length": h.PINLength, "allow_negative": h.AllowNegative, "request_expiry_days": h.RequestExpiryDays,
		})
	})
}

// --- sign-in -----------------------------------------------------------------

// LoginResult carries a new session's raw token (for the cookie).
type LoginResult struct {
	Token   string
	User    store.User
	Session store.Session
}

// LookupLoginRole tells the login form whether to show a PIN pad. Unknown
// usernames get the password form.
func (s *Service) LookupLoginRole(ctx context.Context, username string) store.Role {
	u, err := s.Store.GetUserByUsername(ctx, strings.TrimSpace(username))
	if err != nil || u.Disabled() {
		return store.RoleAdmin
	}
	return u.Role
}

// Login checks credentials with per-account throttling (plan §4.1).
func (s *Service) Login(ctx context.Context, username, secret string, remember bool, device string) (LoginResult, error) {
	u, err := s.Store.GetUserByUsername(ctx, strings.TrimSpace(username))
	if errors.Is(err, store.ErrNotFound) || (err == nil && u.Disabled()) {
		auth.BurnTime(secret)
		return LoginResult{}, ErrBadCredentials
	}
	if err != nil {
		return LoginResult{}, err
	}
	now := s.now()
	a, err := s.Store.GetAuthAttempts(ctx, u.ID)
	if err != nil {
		return LoginResult{}, err
	}
	if th := auth.Check(a.Failures, a.NextAllowedAt, a.LockedAt, now); th.Locked {
		return LoginResult{}, ErrLocked
	} else if th.Wait > 0 {
		return LoginResult{}, &ThrottledError{Wait: th.Wait}
	}
	hash := u.PasswordHash
	if u.IsKid() {
		hash = u.PINHash
	}
	if !auth.Verify(secret, hash) {
		return LoginResult{}, s.recordFailure(ctx, u)
	}

	token := auth.NewToken()
	life := sessionLife(u, remember)
	sess := store.Session{IDHash: auth.HashToken(token), UserID: u.ID, DeviceLabel: device, Remember: remember,
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(life)}
	err = s.Store.Tx(ctx, "", func(tx store.Tx) error {
		if a.Failures > 0 {
			if err := tx.ClearAuthAttempts(ctx, u.ID); err != nil {
				return err
			}
		}
		return tx.CreateSession(ctx, sess)
	})
	return LoginResult{Token: token, User: u, Session: sess}, err
}

func sessionLife(u store.User, remember bool) time.Duration {
	switch {
	case u.IsAdmin():
		return AdminSession
	case remember:
		return KidRememberSession
	default:
		return KidSession
	}
}

// recordFailure counts a failed attempt, applies backoff, hard-locks kids at
// 10, and tells admins. It returns the error to show.
func (s *Service) recordFailure(ctx context.Context, u store.User) error {
	result := ErrBadCredentials
	err := s.Store.Tx(ctx, u.ID, func(tx store.Tx) error {
		now := s.now()
		a, err := tx.GetAuthAttempts(ctx, u.ID)
		if err != nil {
			return err
		}
		if a.WindowStart == nil || now.Sub(*a.WindowStart) > failureWindow {
			a = store.AuthAttempts{UserID: u.ID, WindowStart: &now}
		}
		a.Failures++
		if d := auth.Delay(a.Failures); d > 0 {
			next := now.Add(d)
			a.NextAllowedAt = &next
			result = &ThrottledError{Wait: d}
		}
		p := notify.Payload{KidID: u.ID, KidName: u.DisplayName, Failures: a.Failures}
		var kind string
		switch {
		case auth.ShouldLock(u.IsKid(), a.Failures):
			a.LockedAt, result, kind = &now, ErrLocked, notify.KidLocked
			if err := tx.RevokeUserSessions(ctx, u.ID, now); err != nil {
				return err
			}
		case a.Failures == auth.BackoffAfter:
			kind = notify.SignInFailures
		}
		if err := tx.PutAuthAttempts(ctx, a); err != nil {
			return err
		}
		if kind == "" {
			return nil
		}
		admins, err := notify.Admins(ctx, tx)
		if err != nil {
			return err
		}
		if kind == notify.KidLocked {
			if err := s.audit(ctx, tx, "", "user.locked", u.ID, map[string]any{"failures": a.Failures}); err != nil {
				return err
			}
		}
		return notify.Send(ctx, tx, now, without(admins, u.ID), kind, "", p)
	})
	if err != nil {
		return err
	}
	return result
}

func without(ids []string, drop string) []string {
	out := ids[:0:0]
	for _, id := range ids {
		if id != drop {
			out = append(out, id)
		}
	}
	return out
}

// Authenticate resolves a session cookie to its user, sliding the expiry.
func (s *Service) Authenticate(ctx context.Context, token string) (store.User, store.Session, error) {
	if token == "" {
		return store.User{}, store.Session{}, ErrNotFound
	}
	now := s.now()
	sess, err := s.Store.GetSession(ctx, auth.HashToken(token))
	if err != nil {
		return store.User{}, store.Session{}, err
	}
	if sess.RevokedAt != nil || !now.Before(sess.ExpiresAt) {
		return store.User{}, store.Session{}, ErrNotFound
	}
	u, err := s.Store.GetUser(ctx, sess.UserID)
	if err != nil || u.Disabled() {
		return store.User{}, store.Session{}, ErrNotFound
	}
	if now.Sub(sess.LastSeenAt) >= touchEvery {
		sess.LastSeenAt, sess.ExpiresAt = now, now.Add(sessionLife(u, sess.Remember))
		if err := s.Store.Tx(ctx, "", func(tx store.Tx) error {
			return tx.TouchSession(ctx, sess.IDHash, sess.LastSeenAt, sess.ExpiresAt)
		}); err != nil {
			s.Log.Warn("touch session", "err", err)
		}
	}
	return u, sess, nil
}

// Logout revokes one session.
func (s *Service) Logout(ctx context.Context, token string) error {
	return s.Store.Tx(ctx, "", func(tx store.Tx) error {
		return tx.RevokeSession(ctx, auth.HashToken(token), s.now())
	})
}

// LoginState describes a user's throttle state for the People page.
type LoginState struct {
	Failures int
	Locked   bool
}

// LoginStates returns the throttle state for each user ID.
func (s *Service) LoginStates(ctx context.Context, users []store.User) (map[string]LoginState, error) {
	out := map[string]LoginState{}
	for _, u := range users {
		a, err := s.Store.GetAuthAttempts(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		out[u.ID] = LoginState{Failures: a.Failures, Locked: a.LockedAt != nil}
	}
	return out, nil
}
