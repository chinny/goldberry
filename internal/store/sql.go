package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Dialect captures the few places SQLite and Postgres differ. Queries are
// written once, in a portable subset, with ? placeholders (plan §9.1).
type Dialect interface {
	Name() string
	// Rebind rewrites ? placeholders for the engine ($1, $2… for Postgres).
	Rebind(query string) string
	// Time converts a timestamp to a driver argument (always UTC).
	Time(t time.Time) any
	// LockKid serializes writes for one kid inside tx.
	LockKid(ctx context.Context, tx *sql.Tx, kidID string) error
	IsUniqueViolation(err error) bool
}

// SQL implements Store on database/sql. Reads use the read pool; Tx uses the
// write pool. For Postgres both are the same *sql.DB.
type SQL struct {
	queries
	read, write *sql.DB
	dialect     Dialect
	close       func() error
}

// NewSQL wraps already-open, already-migrated pools.
func NewSQL(read, write *sql.DB, d Dialect, closeFn func() error) *SQL {
	return &SQL{queries: queries{q: read, d: d}, read: read, write: write, dialect: d, close: closeFn}
}

func (s *SQL) Dialect() string                { return s.dialect.Name() }
func (s *SQL) Ping(ctx context.Context) error { return s.write.PingContext(ctx) }
func (s *SQL) Close() error                   { return s.close() }

// WriteDB exposes the write pool to engine packages (backups, migrations).
func (s *SQL) WriteDB() *sql.DB { return s.write }

func (s *SQL) Tx(ctx context.Context, lockKid string, fn func(Tx) error) (err error) {
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if lockKid != "" {
		if err = s.dialect.LockKid(ctx, tx, lockKid); err != nil {
			return err
		}
	}
	if err = fn(queries{q: tx, d: s.dialect}); err != nil {
		return err
	}
	return tx.Commit()
}

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type queries struct {
	q querier
	d Dialect
}

func (x queries) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	res, err := x.q.ExecContext(ctx, x.d.Rebind(query), args...)
	if err != nil && x.d.IsUniqueViolation(err) {
		return nil, fmt.Errorf("%w: %w", ErrConflict, err)
	}
	return res, err
}

func (x queries) row(ctx context.Context, query string, args ...any) *sql.Row {
	return x.q.QueryRowContext(ctx, x.d.Rebind(query), args...)
}

func (x queries) rows(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return x.q.QueryContext(ctx, x.d.Rebind(query), args...)
}

func (x queries) t(t time.Time) any { return x.d.Time(t) }

func (x queries) tp(t *time.Time) any {
	if t == nil {
		return nil
	}
	return x.d.Time(*t)
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// --- scanning helpers -------------------------------------------------------

// nstr scans a nullable TEXT column into a string ("" for NULL).
type nstr struct{ p *string }

func (n nstr) Scan(v any) error {
	switch x := v.(type) {
	case nil:
		*n.p = ""
	case string:
		*n.p = x
	case []byte:
		*n.p = string(x)
	default:
		*n.p = fmt.Sprint(x)
	}
	return nil
}

// ntime scans a timestamp stored as TIMESTAMPTZ or RFC 3339 TEXT.
type ntime struct{ p **time.Time }

func (n ntime) Scan(v any) error {
	t, err := scanTime(v)
	if err != nil {
		return err
	}
	*n.p = t
	return nil
}

// rtime scans a non-null timestamp.
type rtime struct{ p *time.Time }

func (r rtime) Scan(v any) error {
	t, err := scanTime(v)
	if err != nil {
		return err
	}
	if t == nil {
		return errors.New("unexpected NULL timestamp")
	}
	*r.p = *t
	return nil
}

// nint scans a nullable integer into an int64 (0 for NULL).
type nint struct{ p *int64 }

func (n nint) Scan(v any) error {
	var ni sql.NullInt64
	if err := ni.Scan(v); err != nil {
		return err
	}
	*n.p = ni.Int64
	return nil
}

// TimeLayout is the fixed-width UTC format SQLite stores, so text order is
// time order.
const TimeLayout = "2006-01-02T15:04:05.000000Z"

func scanTime(v any) (*time.Time, error) {
	var s string
	switch x := v.(type) {
	case nil:
		return nil, nil
	case time.Time:
		t := x.UTC()
		return &t, nil
	case string:
		s = x
	case []byte:
		s = string(x)
	default:
		return nil, fmt.Errorf("cannot scan %T into a time", v)
	}
	t, err := time.Parse(TimeLayout, s)
	if err != nil {
		if t, err = time.Parse(time.RFC3339Nano, s); err != nil {
			return nil, err
		}
	}
	t = t.UTC()
	return &t, nil
}

// nul turns "" into NULL.
func nul(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// --- household & settings ---------------------------------------------------

const householdCols = `id, name, currency, timezone, pin_length, allow_negative, request_expiry_days, kid_picker_login, created_at`

func (x queries) GetHousehold(ctx context.Context) (Household, error) {
	var h Household
	err := x.row(ctx, `SELECT `+householdCols+` FROM household LIMIT 1`).Scan(
		&h.ID, &h.Name, &h.Currency, &h.Timezone, &h.PINLength, &h.AllowNegative,
		&h.RequestExpiryDays, &h.KidPickerLogin, rtime{&h.CreatedAt})
	return h, notFound(err)
}

func (x queries) CreateHousehold(ctx context.Context, h Household) error {
	_, err := x.exec(ctx, `INSERT INTO household (`+householdCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		h.ID, h.Name, h.Currency, h.Timezone, h.PINLength, h.AllowNegative, h.RequestExpiryDays, h.KidPickerLogin, x.t(h.CreatedAt))
	return err
}

func (x queries) UpdateHousehold(ctx context.Context, h Household) error {
	_, err := x.exec(ctx, `UPDATE household SET name = ?, currency = ?, timezone = ?, pin_length = ?,
		allow_negative = ?, request_expiry_days = ?, kid_picker_login = ? WHERE id = ?`,
		h.Name, h.Currency, h.Timezone, h.PINLength, h.AllowNegative, h.RequestExpiryDays, h.KidPickerLogin, h.ID)
	return err
}

func (x queries) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := x.row(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	return v, notFound(err)
}

func (x queries) PutSetting(ctx context.Context, key, value string, secret bool) error {
	_, err := x.exec(ctx, `INSERT INTO settings (key, value, secret) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, secret = excluded.secret`, key, value, secret)
	return err
}

// --- users ------------------------------------------------------------------

const userCols = `id, role, username, display_name, avatar, password_hash, pin_hash, email, disabled_at, created_at`

func scanUser(sc interface{ Scan(...any) error }) (User, error) {
	var u User
	err := sc.Scan(&u.ID, &u.Role, &u.Username, &u.DisplayName, nstr{&u.Avatar}, nstr{&u.PasswordHash},
		nstr{&u.PINHash}, nstr{&u.Email}, ntime{&u.DisabledAt}, rtime{&u.CreatedAt})
	return u, err
}

func (x queries) CountAdmins(ctx context.Context, includeDisabled bool) (int, error) {
	q := `SELECT COUNT(*) FROM users WHERE role = 'admin'`
	if !includeDisabled {
		q += ` AND disabled_at IS NULL`
	}
	var n int
	err := x.row(ctx, q).Scan(&n)
	return n, err
}

func (x queries) GetUser(ctx context.Context, id string) (User, error) {
	u, err := scanUser(x.row(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
	return u, notFound(err)
}

func (x queries) GetUserByUsername(ctx context.Context, username string) (User, error) {
	u, err := scanUser(x.row(ctx, `SELECT `+userCols+` FROM users WHERE username = ?`, strings.ToLower(username)))
	return u, notFound(err)
}

func (x queries) ListUsers(ctx context.Context, role Role) ([]User, error) {
	q, args := `SELECT `+userCols+` FROM users`, []any{}
	if role != "" {
		q += ` WHERE role = ?`
		args = append(args, role)
	}
	rows, err := x.rows(ctx, q+` ORDER BY role, display_name, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (x queries) CreateUser(ctx context.Context, u User) error {
	_, err := x.exec(ctx, `INSERT INTO users (`+userCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Role, strings.ToLower(u.Username), u.DisplayName, nul(u.Avatar), nul(u.PasswordHash), nul(u.PINHash),
		nul(u.Email), x.tp(u.DisabledAt), x.t(u.CreatedAt))
	return err
}

func (x queries) UpdateUser(ctx context.Context, u User) error {
	_, err := x.exec(ctx, `UPDATE users SET display_name = ?, avatar = ?, password_hash = ?, pin_hash = ?,
		email = ?, disabled_at = ? WHERE id = ?`,
		u.DisplayName, nul(u.Avatar), nul(u.PasswordHash), nul(u.PINHash), nul(u.Email), x.tp(u.DisabledAt), u.ID)
	return err
}

// --- sessions & throttling --------------------------------------------------

const sessionCols = `id_hash, user_id, device_label, remember, created_at, last_seen_at, expires_at, revoked_at`

func scanSession(sc interface{ Scan(...any) error }) (Session, error) {
	var s Session
	err := sc.Scan(&s.IDHash, &s.UserID, nstr{&s.DeviceLabel}, &s.Remember, rtime{&s.CreatedAt},
		rtime{&s.LastSeenAt}, rtime{&s.ExpiresAt}, ntime{&s.RevokedAt})
	return s, err
}

func (x queries) GetSession(ctx context.Context, idHash string) (Session, error) {
	s, err := scanSession(x.row(ctx, `SELECT `+sessionCols+` FROM sessions WHERE id_hash = ?`, idHash))
	return s, notFound(err)
}

func (x queries) ListSessions(ctx context.Context, userID string, now time.Time) ([]Session, error) {
	rows, err := x.rows(ctx, `SELECT `+sessionCols+` FROM sessions
		WHERE user_id = ? AND revoked_at IS NULL AND expires_at > ? ORDER BY last_seen_at DESC`, userID, x.t(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (x queries) CreateSession(ctx context.Context, s Session) error {
	_, err := x.exec(ctx, `INSERT INTO sessions (`+sessionCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		s.IDHash, s.UserID, nul(s.DeviceLabel), s.Remember, x.t(s.CreatedAt), x.t(s.LastSeenAt), x.t(s.ExpiresAt), x.tp(s.RevokedAt))
	return err
}

func (x queries) TouchSession(ctx context.Context, idHash string, lastSeen, expires time.Time) error {
	_, err := x.exec(ctx, `UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE id_hash = ? AND revoked_at IS NULL`,
		x.t(lastSeen), x.t(expires), idHash)
	return err
}

func (x queries) RevokeSession(ctx context.Context, idHash string, at time.Time) error {
	_, err := x.exec(ctx, `UPDATE sessions SET revoked_at = ? WHERE id_hash = ? AND revoked_at IS NULL`, x.t(at), idHash)
	return err
}

func (x queries) RevokeUserSessions(ctx context.Context, userID string, at time.Time) error {
	_, err := x.exec(ctx, `UPDATE sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, x.t(at), userID)
	return err
}

func (x queries) GetAuthAttempts(ctx context.Context, userID string) (AuthAttempts, error) {
	a := AuthAttempts{UserID: userID}
	err := x.row(ctx, `SELECT failures, window_start, next_allowed_at, locked_at FROM auth_attempts WHERE user_id = ?`, userID).
		Scan(&a.Failures, ntime{&a.WindowStart}, ntime{&a.NextAllowedAt}, ntime{&a.LockedAt})
	if errors.Is(err, sql.ErrNoRows) {
		return a, nil
	}
	return a, err
}

func (x queries) PutAuthAttempts(ctx context.Context, a AuthAttempts) error {
	_, err := x.exec(ctx, `INSERT INTO auth_attempts (user_id, failures, window_start, next_allowed_at, locked_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET failures = excluded.failures, window_start = excluded.window_start,
			next_allowed_at = excluded.next_allowed_at, locked_at = excluded.locked_at`,
		a.UserID, a.Failures, x.tp(a.WindowStart), x.tp(a.NextAllowedAt), x.tp(a.LockedAt))
	return err
}

func (x queries) ClearAuthAttempts(ctx context.Context, userID string) error {
	_, err := x.exec(ctx, `DELETE FROM auth_attempts WHERE user_id = ?`, userID)
	return err
}

// --- jars -------------------------------------------------------------------

const jarCols = `j.id, j.kid_id, j.name, j.kind, j.sort_order, j.archived_at`

func scanJar(sc interface{ Scan(...any) error }, extra ...any) (Jar, error) {
	var j Jar
	err := sc.Scan(append([]any{&j.ID, &j.KidID, &j.Name, &j.Kind, &j.SortOrder, ntime{&j.ArchivedAt}}, extra...)...)
	return j, err
}

func (x queries) ListJars(ctx context.Context, kidID string) ([]Jar, error) {
	rows, err := x.rows(ctx, `SELECT `+jarCols+` FROM jars j WHERE j.kid_id = ? AND j.archived_at IS NULL
		ORDER BY j.sort_order, j.name`, kidID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Jar
	for rows.Next() {
		j, err := scanJar(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (x queries) GetJar(ctx context.Context, id string) (Jar, error) {
	j, err := scanJar(x.row(ctx, `SELECT `+jarCols+` FROM jars j WHERE j.id = ?`, id))
	return j, notFound(err)
}

func (x queries) JarBalances(ctx context.Context, kidID string) ([]JarBalance, error) {
	rows, err := x.rows(ctx, `SELECT `+jarCols+`,
		CAST(COALESCE((SELECT SUM(l.amount) FROM ledger_entries l WHERE l.jar_id = j.id), 0) AS BIGINT),
		CAST(COALESCE((SELECT SUM(w.amount) FROM withdrawal_requests w WHERE w.jar_id = j.id AND w.status = 'pending'), 0) AS BIGINT)
		FROM jars j WHERE j.kid_id = ? AND j.archived_at IS NULL ORDER BY j.sort_order, j.name`, kidID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JarBalance
	for rows.Next() {
		var b JarBalance
		if b.Jar, err = scanJar(rows, &b.Balance, &b.Held); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (x queries) CreateJar(ctx context.Context, j Jar) error {
	_, err := x.exec(ctx, `INSERT INTO jars (id, kid_id, name, kind, sort_order, archived_at) VALUES (?, ?, ?, ?, ?, ?)`,
		j.ID, j.KidID, j.Name, j.Kind, j.SortOrder, x.tp(j.ArchivedAt))
	return err
}

func (x queries) ListSplitRules(ctx context.Context, kidID string) ([]SplitRule, error) {
	rows, err := x.rows(ctx, `SELECT s.kid_id, s.jar_id, s.basis_points FROM split_rules s
		JOIN jars j ON j.id = s.jar_id WHERE s.kid_id = ? AND j.archived_at IS NULL ORDER BY j.sort_order, j.name`, kidID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SplitRule
	for rows.Next() {
		var r SplitRule
		if err := rows.Scan(&r.KidID, &r.JarID, &r.BasisPoints); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (x queries) PutSplitRule(ctx context.Context, r SplitRule) error {
	_, err := x.exec(ctx, `INSERT INTO split_rules (kid_id, jar_id, basis_points) VALUES (?, ?, ?)
		ON CONFLICT (kid_id, jar_id) DO UPDATE SET basis_points = excluded.basis_points`, r.KidID, r.JarID, r.BasisPoints)
	return err
}

// --- ledger -----------------------------------------------------------------

const ledgerSelect = `SELECT e.id, e.kid_id, e.jar_id, e.amount, e.kind, e.comment, e.private_note, e.actor_id,
	e.reverses_id, e.transfer_id, e.batch_id, e.request_id, e.schedule_id, e.idempotency_key,
	e.effective_at, e.created_at, a.display_name, j.name, r.id
	FROM ledger_entries e
	JOIN jars j ON j.id = e.jar_id
	LEFT JOIN users a ON a.id = e.actor_id
	LEFT JOIN ledger_entries r ON r.reverses_id = e.id`

func scanEntry(sc interface{ Scan(...any) error }) (LedgerEntry, error) {
	var e LedgerEntry
	err := sc.Scan(&e.ID, &e.KidID, &e.JarID, &e.Amount, &e.Kind, nstr{&e.Comment}, nstr{&e.PrivateNote},
		nstr{&e.ActorID}, nstr{&e.ReversesID}, nstr{&e.TransferID}, nstr{&e.BatchID}, nstr{&e.RequestID},
		nstr{&e.ScheduleID}, nstr{&e.IdempotencyKey}, rtime{&e.EffectiveAt}, rtime{&e.CreatedAt},
		nstr{&e.ActorName}, &e.JarName, nstr{&e.ReversedByID})
	return e, err
}

func (x queries) GetLedgerEntry(ctx context.Context, id string) (LedgerEntry, error) {
	e, err := scanEntry(x.row(ctx, ledgerSelect+` WHERE e.id = ?`, id))
	return e, notFound(err)
}

func (x queries) GetLedgerEntryByKey(ctx context.Context, key string) (LedgerEntry, error) {
	e, err := scanEntry(x.row(ctx, ledgerSelect+` WHERE e.idempotency_key = ?`, key))
	return e, notFound(err)
}

func (x queries) ListLedger(ctx context.Context, f LedgerFilter) ([]LedgerEntry, error) {
	q, args := ledgerSelect+` WHERE e.kid_id = ?`, []any{f.KidID}
	if f.JarID != "" {
		q += ` AND e.jar_id = ?`
		args = append(args, f.JarID)
	}
	if !f.From.IsZero() {
		q += ` AND e.effective_at >= ?`
		args = append(args, x.t(f.From))
	}
	if !f.To.IsZero() {
		q += ` AND e.effective_at < ?`
		args = append(args, x.t(f.To))
	}
	q += ` ORDER BY e.effective_at DESC, e.created_at DESC, e.id DESC`
	if f.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}
	rows, err := x.rows(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LedgerEntry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (x queries) InsertLedgerEntry(ctx context.Context, e LedgerEntry) (bool, error) {
	res, err := x.exec(ctx, `INSERT INTO ledger_entries (id, kid_id, jar_id, amount, kind, comment, private_note,
		actor_id, reverses_id, transfer_id, batch_id, request_id, schedule_id, idempotency_key, effective_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (idempotency_key) DO NOTHING`,
		e.ID, e.KidID, e.JarID, e.Amount, e.Kind, nul(e.Comment), nul(e.PrivateNote), nul(e.ActorID), nul(e.ReversesID),
		nul(e.TransferID), nul(e.BatchID), nul(e.RequestID), nul(e.ScheduleID), nul(e.IdempotencyKey),
		x.t(e.EffectiveAt), x.t(e.CreatedAt))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// --- withdrawal requests ----------------------------------------------------

const requestSelect = `SELECT w.id, w.kid_id, w.jar_id, w.amount, w.approved_amount, w.reason, w.goal_id, w.status,
	w.decided_by, w.decision_note, w.decided_at, w.idempotency_key, w.created_at, w.expires_at,
	k.display_name, j.name, d.display_name
	FROM withdrawal_requests w
	JOIN users k ON k.id = w.kid_id
	JOIN jars j ON j.id = w.jar_id
	LEFT JOIN users d ON d.id = w.decided_by`

func scanRequest(sc interface{ Scan(...any) error }) (WithdrawalRequest, error) {
	var r WithdrawalRequest
	err := sc.Scan(&r.ID, &r.KidID, &r.JarID, &r.Amount, nint{&r.ApprovedAmount}, &r.Reason, nstr{&r.GoalID},
		&r.Status, nstr{&r.DecidedBy}, nstr{&r.DecisionNote}, ntime{&r.DecidedAt}, nstr{&r.IdempotencyKey},
		rtime{&r.CreatedAt}, ntime{&r.ExpiresAt}, &r.KidName, &r.JarName, nstr{&r.DecidedByName})
	return r, err
}

func (x queries) GetRequest(ctx context.Context, id string) (WithdrawalRequest, error) {
	r, err := scanRequest(x.row(ctx, requestSelect+` WHERE w.id = ?`, id))
	return r, notFound(err)
}

func (x queries) GetRequestByKey(ctx context.Context, key string) (WithdrawalRequest, error) {
	r, err := scanRequest(x.row(ctx, requestSelect+` WHERE w.idempotency_key = ?`, key))
	return r, notFound(err)
}

func (x queries) ListRequests(ctx context.Context, f RequestFilter) ([]WithdrawalRequest, error) {
	var where []string
	var args []any
	if f.KidID != "" {
		where, args = append(where, `w.kid_id = ?`), append(args, f.KidID)
	}
	if f.Status != "" {
		where, args = append(where, `w.status = ?`), append(args, f.Status)
	}
	q := requestSelect
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	q += ` ORDER BY w.created_at DESC, w.id DESC`
	if f.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}
	rows, err := x.rows(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WithdrawalRequest
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (x queries) CountPendingRequests(ctx context.Context, kidID string) (int, error) {
	var n int
	err := x.row(ctx, `SELECT COUNT(*) FROM withdrawal_requests WHERE kid_id = ? AND status = 'pending'`, kidID).Scan(&n)
	return n, err
}

func (x queries) InsertRequest(ctx context.Context, r WithdrawalRequest) (bool, error) {
	res, err := x.exec(ctx, `INSERT INTO withdrawal_requests (id, kid_id, jar_id, amount, reason, goal_id, status,
		idempotency_key, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (idempotency_key) DO NOTHING`,
		r.ID, r.KidID, r.JarID, r.Amount, r.Reason, nul(r.GoalID), r.Status, nul(r.IdempotencyKey),
		x.t(r.CreatedAt), x.tp(r.ExpiresAt))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (x queries) DecideRequest(ctx context.Context, r WithdrawalRequest) (bool, error) {
	var approved any
	if r.Status == StatusApproved {
		approved = r.ApprovedAmount
	}
	res, err := x.exec(ctx, `UPDATE withdrawal_requests SET status = ?, approved_amount = ?, decided_by = ?,
		decision_note = ?, decided_at = ? WHERE id = ? AND status = 'pending'`,
		r.Status, approved, nul(r.DecidedBy), nul(r.DecisionNote), x.tp(r.DecidedAt), r.ID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (x queries) ExpireRequests(ctx context.Context, now time.Time) ([]WithdrawalRequest, error) {
	ids, err := func() ([]string, error) {
		rows, err := x.rows(ctx, `UPDATE withdrawal_requests SET status = 'expired', decided_at = ?
			WHERE status = 'pending' AND expires_at IS NOT NULL AND expires_at <= ? RETURNING id`, x.t(now), x.t(now))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		return ids, rows.Err()
	}()
	if err != nil {
		return nil, err
	}
	out := make([]WithdrawalRequest, 0, len(ids))
	for _, id := range ids {
		r, err := x.GetRequest(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// --- notifications & audit --------------------------------------------------

func (x queries) ListNotifications(ctx context.Context, recipientID string, limit int) ([]Notification, error) {
	q := `SELECT id, recipient_id, kind, payload, request_id, read_at, created_at FROM notifications
		WHERE recipient_id = ? ORDER BY created_at DESC, id DESC`
	if limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, limit)
	}
	rows, err := x.rows(ctx, q, recipientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var n Notification
		var payload string
		if err := rows.Scan(&n.ID, &n.RecipientID, &n.Kind, &payload, nstr{&n.RequestID}, ntime{&n.ReadAt}, rtime{&n.CreatedAt}); err != nil {
			return nil, err
		}
		n.Payload = []byte(payload)
		out = append(out, n)
	}
	return out, rows.Err()
}

func (x queries) CountUnread(ctx context.Context, recipientID string) (int, error) {
	var n int
	err := x.row(ctx, `SELECT COUNT(*) FROM notifications WHERE recipient_id = ? AND read_at IS NULL`, recipientID).Scan(&n)
	return n, err
}

func (x queries) InsertNotification(ctx context.Context, n Notification) error {
	payload := string(n.Payload)
	if payload == "" {
		payload = "{}"
	}
	_, err := x.exec(ctx, `INSERT INTO notifications (id, recipient_id, kind, payload, request_id, read_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, n.ID, n.RecipientID, n.Kind, payload, nul(n.RequestID), x.tp(n.ReadAt), x.t(n.CreatedAt))
	return err
}

func (x queries) MarkNotificationsRead(ctx context.Context, recipientID string, ids []string, at time.Time) error {
	q, args := `UPDATE notifications SET read_at = ? WHERE recipient_id = ? AND read_at IS NULL`, []any{x.t(at), recipientID}
	if len(ids) > 0 {
		q += ` AND id IN (?` + strings.Repeat(`, ?`, len(ids)-1) + `)`
		for _, id := range ids {
			args = append(args, id)
		}
	}
	_, err := x.exec(ctx, q, args...)
	return err
}

func (x queries) InsertAudit(ctx context.Context, a AuditEntry) error {
	_, err := x.exec(ctx, `INSERT INTO audit_log (id, actor_id, action, target_id, detail, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		a.ID, nul(a.ActorID), a.Action, nul(a.TargetID), nul(string(a.Detail)), x.t(a.CreatedAt))
	return err
}

func (x queries) ListAudit(ctx context.Context, targetID string, limit int) ([]AuditEntry, error) {
	q, args := `SELECT id, actor_id, action, target_id, detail, created_at FROM audit_log`, []any{}
	if targetID != "" {
		q += ` WHERE target_id = ?`
		args = append(args, targetID)
	}
	q += ` ORDER BY created_at DESC, id DESC`
	if limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, limit)
	}
	rows, err := x.rows(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var a AuditEntry
		var detail string
		if err := rows.Scan(&a.ID, nstr{&a.ActorID}, &a.Action, nstr{&a.TargetID}, nstr{&detail}, rtime{&a.CreatedAt}); err != nil {
			return nil, err
		}
		if detail != "" {
			a.Detail = []byte(detail)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
