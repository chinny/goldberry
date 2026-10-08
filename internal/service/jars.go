package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/chinny/goldberry/internal/allowance"
	"github.com/chinny/goldberry/internal/notify"
	"github.com/chinny/goldberry/internal/store"
)

// DefaultJars is every new kid's jars and split (plan §5.2).
var DefaultJars = []struct {
	Name string
	Kind store.JarKind
	BPS  int
}{
	{"Spend", store.JarSpend, 7000},
	{"Save", store.JarSave, 2000},
	{"Give", store.JarGive, 1000},
}

// --- jar management (admin) ------------------------------------------------------

// AddJar adds a custom jar with a 0% share of the split.
func (s *Service) AddJar(ctx context.Context, actor store.User, kidID, name string) (store.Jar, error) {
	if err := requireAdmin(actor); err != nil {
		return store.Jar{}, err
	}
	name, err := cleanJarName(name)
	if err != nil {
		return store.Jar{}, err
	}
	var j store.Jar
	err = s.Store.Tx(ctx, kidID, func(tx store.Tx) error {
		if _, err := getKid(ctx, tx, kidID); err != nil {
			return err
		}
		jars, err := tx.ListJars(ctx, kidID)
		if err != nil {
			return err
		}
		order := 0
		for _, x := range jars {
			if strings.EqualFold(x.Name, name) {
				return invalid("name", "There's already a jar called %s.", x.Name)
			}
			order = max(order, x.SortOrder+1)
		}
		j = store.Jar{ID: store.NewID(), KidID: kidID, Name: name, Kind: store.JarCustom, SortOrder: order}
		if err := tx.CreateJar(ctx, j); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor.ID, "jar.create", kidID, map[string]any{"jar": name})
	})
	return j, err
}

func cleanJarName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 24 {
		return "", invalid("name", "Jar names are 1–24 characters.")
	}
	return name, nil
}

// RenameJar renames one of a kid's jars.
func (s *Service) RenameJar(ctx context.Context, actor store.User, jarID, name string) error {
	if err := requireAdmin(actor); err != nil {
		return err
	}
	name, err := cleanJarName(name)
	if err != nil {
		return err
	}
	j, err := s.Store.GetJar(ctx, jarID)
	if err != nil {
		return err
	}
	return s.Store.Tx(ctx, j.KidID, func(tx store.Tx) error {
		j, err := tx.GetJar(ctx, jarID)
		if err != nil {
			return err
		}
		j.Name = name
		if err := tx.UpdateJar(ctx, j); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor.ID, "jar.rename", j.KidID, map[string]any{"jar": name})
	})
}

// ArchiveJar hides a jar. It must be empty, hold nothing, have a 0% share,
// and not be the kid's only jar or the target of an allowance.
func (s *Service) ArchiveJar(ctx context.Context, actor store.User, jarID string) error {
	if err := requireAdmin(actor); err != nil {
		return err
	}
	j, err := s.Store.GetJar(ctx, jarID)
	if err != nil {
		return err
	}
	return s.Store.Tx(ctx, j.KidID, func(tx store.Tx) error {
		jars, err := tx.JarBalances(ctx, j.KidID)
		if err != nil {
			return err
		}
		if len(jars) <= 1 {
			return invalid("", "A kid needs at least one jar.")
		}
		var jar *store.JarBalance
		for i := range jars {
			if jars[i].ID == jarID {
				jar = &jars[i]
			}
		}
		if jar == nil {
			return ErrNotFound
		}
		if jar.Balance != 0 || jar.Held != 0 {
			return invalid("", "Move the money out of %s before archiving it.", jar.Name)
		}
		rules, err := tx.ListSplitRules(ctx, j.KidID)
		if err != nil {
			return err
		}
		for _, r := range rules {
			if r.JarID == jarID && r.BasisPoints > 0 {
				return invalid("", "Set %s's share of the split to 0%% first.", jar.Name)
			}
		}
		scheds, err := tx.ListSchedules(ctx, j.KidID)
		if err != nil {
			return err
		}
		for _, sc := range scheds {
			if sc.Active && sc.TargetJarID == jarID {
				return invalid("", "An allowance pays into %s. Change it first.", jar.Name)
			}
		}
		now := s.now()
		j.ArchivedAt = &now
		if err := tx.UpdateJar(ctx, j); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor.ID, "jar.archive", j.KidID, map[string]any{"jar": jar.Name})
	})
}

// SetSplit replaces a kid's split rule. Shares are basis points per jar and
// must add up to 100% (10000).
func (s *Service) SetSplit(ctx context.Context, actor store.User, kidID string, bps map[string]int) error {
	if err := requireAdmin(actor); err != nil {
		return err
	}
	return s.Store.Tx(ctx, kidID, func(tx store.Tx) error {
		jars, err := tx.ListJars(ctx, kidID)
		if err != nil {
			return err
		}
		total := 0
		var rules []store.SplitRule
		for _, j := range jars {
			v := bps[j.ID]
			if v < 0 || v > 10000 {
				return invalid("split", "Each share is between 0%% and 100%%.")
			}
			total += v
			rules = append(rules, store.SplitRule{KidID: kidID, JarID: j.ID, BasisPoints: v})
		}
		for id := range bps {
			if !hasJar(jars, id) {
				return invalid("split", "Pick only this kid's jars.")
			}
		}
		if total != 10000 {
			return invalid("split", "The shares add up to %s%%; they need to make 100%%.", strconv.FormatFloat(float64(total)/100, 'f', -1, 64))
		}
		if err := tx.ReplaceSplitRules(ctx, kidID, rules); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor.ID, "split.update", kidID, map[string]any{"bps": bps})
	})
}

func hasJar(jars []store.Jar, id string) bool {
	for _, j := range jars {
		if j.ID == id {
			return true
		}
	}
	return false
}

// EnsureDefaultJars gives every kid created before jars existed a Save and a
// Give jar, and moves a plain 100%-Spend split to 70/20/10. It runs once at
// startup and is a no-op after that.
func (s *Service) EnsureDefaultJars(ctx context.Context) (int, error) {
	const done = "backfill:default_jars_v1"
	if _, err := s.Store.GetSetting(ctx, done); err == nil {
		return 0, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return 0, err
	}
	n := 0
	err := s.Store.Tx(ctx, "", func(tx store.Tx) error {
		kids, err := tx.ListUsers(ctx, store.RoleKid)
		if err != nil {
			return err
		}
		for _, k := range kids {
			jars, err := tx.ListJars(ctx, k.ID)
			if err != nil {
				return err
			}
			rules, err := tx.ListSplitRules(ctx, k.ID)
			if err != nil {
				return err
			}
			has := map[store.JarKind]store.Jar{}
			order := 0
			for _, j := range jars {
				has[j.Kind] = j
				order = max(order, j.SortOrder+1)
			}
			added := false
			for _, d := range DefaultJars[1:] {
				if _, ok := has[d.Kind]; ok {
					continue
				}
				j := store.Jar{ID: store.NewID(), KidID: k.ID, Name: d.Name, Kind: d.Kind, SortOrder: order}
				order++
				if err := tx.CreateJar(ctx, j); err != nil {
					return err
				}
				has[d.Kind], added = j, true
			}
			spend, ok := has[store.JarSpend]
			if added && ok && len(rules) == 1 && rules[0].JarID == spend.ID && rules[0].BasisPoints == 10000 {
				var next []store.SplitRule
				for _, d := range DefaultJars {
					next = append(next, store.SplitRule{KidID: k.ID, JarID: has[d.Kind].ID, BasisPoints: d.BPS})
				}
				if err := tx.ReplaceSplitRules(ctx, k.ID, next); err != nil {
					return err
				}
			}
			if added {
				n++
			}
		}
		return tx.PutSetting(ctx, done, s.now().Format(time.RFC3339), false)
	})
	return n, err
}

// --- locks -------------------------------------------------------------------------

// Override timing (plan §5.4): a 10 s countdown plus a 3 s press-and-hold, so
// a token is only good 13 s after the gauntlet opens, and for 10 minutes.
const (
	OverrideMinWait = 13 * time.Second
	OverrideMaxAge  = 10 * time.Minute
)

// ErrJarLocked is a hard (parent) lock in the way.
type ErrJarLocked struct{ Lock store.JarLock }

func (e *ErrJarLocked) Error() string {
	msg := e.Lock.JarName + " is locked by a parent"
	if e.Lock.UntilDate != "" {
		msg += " until " + e.Lock.UntilDate
	}
	if e.Lock.Reason != "" {
		msg += ": “" + e.Lock.Reason + "”"
	}
	return msg + "."
}

// ErrNeedsOverride is the kid's own lock in the way: they can go ahead after
// the gauntlet.
type ErrNeedsOverride struct {
	JarID string
	Locks []store.JarLock
}

func (e *ErrNeedsOverride) Error() string {
	return "You locked " + e.Locks[0].JarName + ". Break the lock first if you really mean it."
}

var (
	ErrOverrideTooSoon = errors.New("not so fast: wait for the countdown and hold the button")
	ErrOverrideInvalid = errors.New("that lock-breaking screen expired; try again")
)

// activeLocks keeps locks that haven't lifted: a lock lifts at the start of
// its until date in the household time zone.
func activeLocks(locks []store.JarLock, today allowance.Date) []store.JarLock {
	var out []store.JarLock
	for _, l := range locks {
		if l.UntilDate != "" {
			until, err := allowance.ParseDate(l.UntilDate)
			if err == nil && !today.Before(until) {
				continue
			}
		}
		out = append(out, l)
	}
	return out
}

// blocking returns the locks on money leaving fromJar: toward toJar, or as a
// withdrawal request when toJar is "". A route lock (to_jar_id set) only
// blocks that one route.
func blocking(locks []store.JarLock, fromJar, toJar string) (hard *store.JarLock, soft []store.JarLock) {
	for _, l := range locks {
		if l.JarID != fromJar || (l.ToJarID != "" && l.ToJarID != toJar) {
			continue
		}
		if l.Hard() {
			if hard == nil {
				hard = &l
			}
			continue
		}
		soft = append(soft, l)
	}
	return hard, soft
}

func (s *Service) today(h store.Household) allowance.Date {
	return allowance.Today(s.now(), h.Location())
}

// KidLocks returns a kid's locks that are in force today.
func (s *Service) KidLocks(ctx context.Context, r store.Reader, kidID string) ([]store.JarLock, error) {
	h, err := r.GetHousehold(ctx)
	if err != nil {
		return nil, err
	}
	locks, err := r.ListLocks(ctx, kidID)
	if err != nil {
		return nil, err
	}
	return activeLocks(locks, s.today(h)), nil
}

// LockInput sets a lock on a jar.
type LockInput struct {
	JarID   string
	ToJarID string // "" = every route out, plus withdrawal requests
	Reason  string
	Until   string // "YYYY-MM-DD" or ""
}

// SetLock locks a jar. An admin's lock is hard; a kid can lock only their own
// jars, and their lock is soft.
func (s *Service) SetLock(ctx context.Context, actor store.User, in LockInput) (store.JarLock, error) {
	if actor.Disabled() {
		return store.JarLock{}, ErrForbidden
	}
	reason, err := cleanText("reason", in.Reason, actor.IsKid())
	if err != nil {
		return store.JarLock{}, err
	}
	h, _, err := s.Household(ctx)
	if err != nil {
		return store.JarLock{}, err
	}
	until, err := allowance.ParseDate(in.Until)
	if err != nil {
		return store.JarLock{}, invalid("until", "%s", capitalize(err.Error()))
	}
	if !until.IsZero() && !s.today(h).Before(until) {
		return store.JarLock{}, invalid("until", "Pick a date after today.")
	}
	jar, err := s.Store.GetJar(ctx, in.JarID)
	if err != nil {
		return store.JarLock{}, err
	}
	if actor.IsKid() && jar.KidID != actor.ID {
		return store.JarLock{}, ErrNotFound
	}
	var l store.JarLock
	err = s.Store.Tx(ctx, jar.KidID, func(tx store.Tx) error {
		if in.ToJarID != "" {
			to, err := tx.GetJar(ctx, in.ToJarID)
			if err != nil || to.KidID != jar.KidID || to.ID == jar.ID {
				return invalid("to_jar", "Pick another of the kid's jars.")
			}
		}
		l = store.JarLock{ID: store.NewID(), JarID: jar.ID, ToJarID: in.ToJarID, SetByRole: actor.Role, SetBy: actor.ID,
			Reason: reason, UntilDate: until.String(), CreatedAt: s.now(), KidID: jar.KidID, JarName: jar.Name}
		if err := tx.CreateLock(ctx, l); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor.ID, "lock.set", jar.KidID, map[string]any{"jar": jar.Name, "role": actor.Role})
	})
	return l, err
}

// RemoveLock removes a lock. Admins remove any lock without ceremony. A kid
// can remove only their own self-lock, and only with a gauntlet token.
func (s *Service) RemoveLock(ctx context.Context, actor store.User, lockID, override string) error {
	l, err := s.Store.GetLock(ctx, lockID)
	if err != nil {
		return err
	}
	if l.RemovedAt != nil {
		return nil
	}
	if actor.IsKid() {
		if l.KidID != actor.ID {
			return ErrNotFound
		}
		if l.Hard() {
			return &ErrJarLocked{Lock: l}
		}
		key, err := s.overrideKey(ctx)
		if err != nil {
			return err
		}
		if err := verifyOverride(key, override, actor.ID, l.JarID, s.now()); err != nil {
			return err
		}
	} else if err := requireAdmin(actor); err != nil {
		return err
	}
	return s.Store.Tx(ctx, l.KidID, func(tx store.Tx) error {
		ok, err := tx.RemoveLock(ctx, l.ID, actor.ID, s.now())
		if err != nil || !ok {
			return err
		}
		if actor.IsKid() {
			return s.recordOverride(ctx, tx, actor, []store.JarLock{l}, "removed", 0, "", "")
		}
		return s.audit(ctx, tx, actor.ID, "lock.remove", l.KidID, map[string]any{"jar": l.JarName})
	})
}

// StartOverride opens the gauntlet for a kid's self-locked jar and returns a
// token that becomes valid OverrideMinWait later.
func (s *Service) StartOverride(ctx context.Context, kid store.User, jarID string) (string, []store.JarLock, error) {
	if !kid.IsKid() {
		return "", nil, ErrForbidden
	}
	locks, err := s.KidLocks(ctx, s.Store, kid.ID)
	if err != nil {
		return "", nil, err
	}
	var mine []store.JarLock
	for _, l := range locks {
		if l.JarID != jarID {
			continue
		}
		if l.Hard() && l.ToJarID == "" {
			return "", nil, &ErrJarLocked{Lock: l}
		}
		if !l.Hard() {
			mine = append(mine, l)
		}
	}
	if len(mine) == 0 {
		return "", nil, ErrNotFound
	}
	key, err := s.overrideKey(ctx)
	if err != nil {
		return "", nil, err
	}
	return signOverride(key, kid.ID, jarID, s.now()), mine, nil
}

// checkLocks applies the locks on a kid moving money out of fromJar (to toJar,
// or as a request when toJar is ""). Admins aren't subject to locks. A valid
// override clears the kid's own locks; a parent's lock always wins.
func (s *Service) checkLocks(ctx context.Context, tx store.Tx, actor store.User, kidID, fromJar, toJar, override string, key []byte) ([]store.JarLock, error) {
	if actor.IsAdmin() {
		return nil, nil
	}
	h, err := tx.GetHousehold(ctx)
	if err != nil {
		return nil, err
	}
	all, err := tx.ListLocks(ctx, kidID)
	if err != nil {
		return nil, err
	}
	hard, soft := blocking(activeLocks(all, s.today(h)), fromJar, toJar)
	if hard != nil {
		return nil, &ErrJarLocked{Lock: *hard}
	}
	if len(soft) == 0 {
		return nil, nil
	}
	if override == "" {
		return nil, &ErrNeedsOverride{JarID: fromJar, Locks: soft}
	}
	if err := verifyOverride(key, override, kidID, fromJar, s.now()); err != nil {
		return nil, err
	}
	return soft, nil
}

// recordOverride writes lock_overrides rows (removing the locks when the kid
// chose to) and tells the admins.
func (s *Service) recordOverride(ctx context.Context, tx store.Tx, kid store.User, locks []store.JarLock, action string, amount int64, entryID, requestID string) error {
	now := s.now()
	for _, l := range locks {
		if action == "removed" {
			if _, err := tx.RemoveLock(ctx, l.ID, kid.ID, now); err != nil {
				return err
			}
		}
		if err := tx.InsertLockOverride(ctx, store.LockOverride{ID: store.NewID(), LockID: l.ID, KidID: kid.ID,
			Action: action, LedgerEntryID: entryID, RequestID: requestID, CreatedAt: now}); err != nil {
			return err
		}
	}
	admins, err := notify.Admins(ctx, tx)
	if err != nil {
		return err
	}
	return notify.Send(ctx, tx, now, admins, notify.LockOverridden, requestID, notify.Payload{
		KidID: kid.ID, KidName: kid.DisplayName, Amount: amount, Jar: locks[0].JarName, Text: locks[0].Reason, Note: action,
	})
}

// overrideKey is the per-install HMAC key for gauntlet tokens.
func (s *Service) overrideKey(ctx context.Context) ([]byte, error) {
	const name = "override_key"
	v, err := s.Store.GetSetting(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		v = base64.StdEncoding.EncodeToString(b)
		err = s.Store.Tx(ctx, "", func(tx store.Tx) error {
			if existing, err := tx.GetSetting(ctx, name); err == nil {
				v = existing
				return nil
			}
			return tx.PutSetting(ctx, name, v, true)
		})
	}
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(v)
}

func signOverride(key []byte, kidID, jarID string, at time.Time) string {
	payload := kidID + "|" + jarID + "|" + strconv.FormatInt(at.UnixMilli(), 10)
	m := hmac.New(sha256.New, key)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func verifyOverride(key []byte, token, kidID, jarID string, now time.Time) error {
	enc, sig, ok := strings.Cut(token, ".")
	if !ok {
		return ErrOverrideInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return ErrOverrideInvalid
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return ErrOverrideInvalid
	}
	m := hmac.New(sha256.New, key)
	m.Write(payload)
	if !hmac.Equal(got, m.Sum(nil)) {
		return ErrOverrideInvalid
	}
	parts := strings.Split(string(payload), "|")
	if len(parts) != 3 || parts[0] != kidID || parts[1] != jarID {
		return ErrOverrideInvalid
	}
	ms, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return ErrOverrideInvalid
	}
	age := now.Sub(time.UnixMilli(ms))
	switch {
	case age < OverrideMinWait:
		return ErrOverrideTooSoon
	case age > OverrideMaxAge:
		return ErrOverrideInvalid
	}
	return nil
}

// --- transfers ---------------------------------------------------------------------

// TransferInput moves money between a kid's own jars (plan §5.2).
type TransferInput struct {
	KidID          string
	FromJarID      string
	ToJarID        string
	Amount         int64
	IdempotencyKey string
	Override       string // gauntlet token, for a kid's self-locked jar
	RemoveLock     bool   // with Override: remove the lock rather than break it once
}

// Transfer posts a transfer_out / transfer_in pair sharing a transfer_id. A
// kid may move only their own money and is subject to jar locks; admins
// aren't.
func (s *Service) Transfer(ctx context.Context, actor store.User, in TransferInput) ([]store.LedgerEntry, error) {
	switch {
	case actor.Disabled():
		return nil, ErrForbidden
	case actor.IsKid():
		in.KidID = actor.ID
	case !actor.IsAdmin():
		return nil, ErrForbidden
	}
	if in.Amount <= 0 {
		return nil, invalid("amount", "Enter an amount above zero.")
	}
	if in.FromJarID == "" || in.ToJarID == "" || in.FromJarID == in.ToJarID {
		return nil, invalid("to_jar", "Pick two different jars.")
	}
	kid, err := getKid(ctx, s.Store, in.KidID)
	if err != nil {
		return nil, err
	}
	_, cur, err := s.Household(ctx)
	if err != nil {
		return nil, err
	}
	key, err := s.overrideKey(ctx)
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
		from, err := jarFor(ctx, tx, kid.ID, in.FromJarID)
		if err != nil {
			return err
		}
		to, err := jarFor(ctx, tx, kid.ID, in.ToJarID)
		if err != nil {
			return err
		}
		overridden, err := s.checkLocks(ctx, tx, actor, kid.ID, from.ID, to.ID, in.Override, key)
		if err != nil {
			return err
		}
		if in.Amount > from.Available() {
			return &ErrInsufficient{Available: from.Available(), Jar: from.Name, Currency: cur}
		}
		now, tid := s.now(), store.NewID()
		comment := "Moved to " + to.Name
		pair := []store.LedgerEntry{
			{ID: store.NewID(), KidID: kid.ID, JarID: from.ID, Amount: -in.Amount, Kind: store.KindTransferOut,
				Comment: comment, ActorID: actor.ID, TransferID: tid, IdempotencyKey: partKey(in.IdempotencyKey, "out", true),
				EffectiveAt: now, CreatedAt: now, JarName: from.Name, ActorName: actor.DisplayName},
			{ID: store.NewID(), KidID: kid.ID, JarID: to.ID, Amount: in.Amount, Kind: store.KindTransferIn,
				Comment: "Moved from " + from.Name, ActorID: actor.ID, TransferID: tid, IdempotencyKey: partKey(in.IdempotencyKey, "in", true),
				EffectiveAt: now, CreatedAt: now, JarName: to.Name, ActorName: actor.DisplayName},
		}
		for _, e := range pair {
			if _, err := tx.InsertLedgerEntry(ctx, e); err != nil {
				return err
			}
		}
		out = pair
		if len(overridden) > 0 {
			action := "once"
			if in.RemoveLock {
				action = "removed"
			}
			return s.recordOverride(ctx, tx, kid, overridden, action, in.Amount, pair[0].ID, "")
		}
		return nil
	})
	return out, err
}

// SplitSummary renders a split like "70 / 20 / 10" in jar order.
func SplitSummary(jars []store.JarBalance, rules []store.SplitRule) string {
	bps := map[string]int{}
	for _, r := range rules {
		bps[r.JarID] = r.BasisPoints
	}
	var parts []string
	for _, j := range jars {
		parts = append(parts, strconv.FormatFloat(float64(bps[j.ID])/100, 'f', -1, 64))
	}
	return strings.Join(parts, " / ")
}
