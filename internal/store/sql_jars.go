package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// --- jars & splits ------------------------------------------------------------

func (x queries) UpdateJar(ctx context.Context, j Jar) error {
	_, err := x.exec(ctx, `UPDATE jars SET name = ?, sort_order = ?, archived_at = ? WHERE id = ?`,
		j.Name, j.SortOrder, x.tp(j.ArchivedAt), j.ID)
	return err
}

func (x queries) ReplaceSplitRules(ctx context.Context, kidID string, rules []SplitRule) error {
	if _, err := x.exec(ctx, `DELETE FROM split_rules WHERE kid_id = ?`, kidID); err != nil {
		return err
	}
	for _, r := range rules {
		if err := x.PutSplitRule(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

func (x queries) ListBatch(ctx context.Context, batchID string) ([]LedgerEntry, error) {
	rows, err := x.rows(ctx, ledgerSelect+` WHERE e.batch_id = ? ORDER BY j.sort_order, e.id`, batchID)
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

// --- schedules ------------------------------------------------------------------

const scheduleCols = `id, kid_id, amount, cadence, weekday, day_of_month, anchor_date, target_jar_id,
	comment_template, active, last_occurrence, created_by, created_at`

func scanSchedule(sc interface{ Scan(...any) error }) (Schedule, error) {
	var s Schedule
	var weekday, dom sql.NullInt64
	err := sc.Scan(&s.ID, &s.KidID, &s.Amount, &s.Cadence, &weekday, &dom, nstr{&s.AnchorDate}, nstr{&s.TargetJarID},
		nstr{&s.CommentTemplate}, &s.Active, nstr{&s.LastOccurrence}, nstr{&s.CreatedBy}, rtime{&s.CreatedAt})
	s.Weekday, s.DayOfMonth = int(weekday.Int64), int(dom.Int64)
	return s, err
}

func (x queries) scheduleArgs(s Schedule) []any {
	var weekday, dom any
	switch s.Cadence {
	case "weekly":
		weekday = s.Weekday
	case "monthly":
		dom = s.DayOfMonth
	}
	return []any{s.Amount, s.Cadence, weekday, dom, nul(s.AnchorDate), nul(s.TargetJarID),
		nul(s.CommentTemplate), s.Active, nul(s.LastOccurrence)}
}

func (x queries) ListSchedules(ctx context.Context, kidID string) ([]Schedule, error) {
	q, args := `SELECT `+scheduleCols+` FROM schedules`, []any{}
	if kidID != "" {
		q += ` WHERE kid_id = ?`
		args = append(args, kidID)
	}
	rows, err := x.rows(ctx, q+` ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		s, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (x queries) GetSchedule(ctx context.Context, id string) (Schedule, error) {
	s, err := scanSchedule(x.row(ctx, `SELECT `+scheduleCols+` FROM schedules WHERE id = ?`, id))
	return s, notFound(err)
}

func (x queries) CreateSchedule(ctx context.Context, s Schedule) error {
	args := append([]any{s.ID, s.KidID}, x.scheduleArgs(s)...)
	args = append(args, nul(s.CreatedBy), x.t(s.CreatedAt))
	_, err := x.exec(ctx, `INSERT INTO schedules (`+scheduleCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, args...)
	return err
}

func (x queries) UpdateSchedule(ctx context.Context, s Schedule) error {
	args := append(x.scheduleArgs(s), s.ID)
	_, err := x.exec(ctx, `UPDATE schedules SET amount = ?, cadence = ?, weekday = ?, day_of_month = ?, anchor_date = ?,
		target_jar_id = ?, comment_template = ?, active = ?, last_occurrence = ? WHERE id = ?`, args...)
	return err
}

// --- locks ------------------------------------------------------------------------

const lockSelect = `SELECT l.id, l.jar_id, l.to_jar_id, l.set_by_role, l.set_by, l.reason, l.until_date, l.created_at,
	l.removed_by, l.removed_at, j.kid_id, j.name, t.name, u.display_name
	FROM jar_locks l
	JOIN jars j ON j.id = l.jar_id
	LEFT JOIN jars t ON t.id = l.to_jar_id
	LEFT JOIN users u ON u.id = l.set_by`

func scanLock(sc interface{ Scan(...any) error }) (JarLock, error) {
	var l JarLock
	err := sc.Scan(&l.ID, &l.JarID, nstr{&l.ToJarID}, &l.SetByRole, &l.SetBy, nstr{&l.Reason}, nstr{&l.UntilDate},
		rtime{&l.CreatedAt}, nstr{&l.RemovedBy}, ntime{&l.RemovedAt}, &l.KidID, &l.JarName, nstr{&l.ToJarName}, nstr{&l.SetByName})
	return l, err
}

func (x queries) ListLocks(ctx context.Context, kidID string) ([]JarLock, error) {
	rows, err := x.rows(ctx, lockSelect+` WHERE j.kid_id = ? AND l.removed_at IS NULL ORDER BY j.sort_order, l.created_at`, kidID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JarLock
	for rows.Next() {
		l, err := scanLock(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (x queries) GetLock(ctx context.Context, id string) (JarLock, error) {
	l, err := scanLock(x.row(ctx, lockSelect+` WHERE l.id = ?`, id))
	return l, notFound(err)
}

func (x queries) CreateLock(ctx context.Context, l JarLock) error {
	_, err := x.exec(ctx, `INSERT INTO jar_locks (id, jar_id, to_jar_id, set_by_role, set_by, reason, until_date, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		l.ID, l.JarID, nul(l.ToJarID), l.SetByRole, l.SetBy, nul(l.Reason), nul(l.UntilDate), x.t(l.CreatedAt))
	return err
}

func (x queries) RemoveLock(ctx context.Context, id, removedBy string, at time.Time) (bool, error) {
	res, err := x.exec(ctx, `UPDATE jar_locks SET removed_by = ?, removed_at = ? WHERE id = ? AND removed_at IS NULL`,
		removedBy, x.t(at), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (x queries) InsertLockOverride(ctx context.Context, o LockOverride) error {
	_, err := x.exec(ctx, `INSERT INTO lock_overrides (id, lock_id, kid_id, action, ledger_entry_id, request_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		o.ID, o.LockID, o.KidID, o.Action, nul(o.LedgerEntryID), nul(o.RequestID), x.t(o.CreatedAt))
	return err
}

func (x queries) ListLockOverrides(ctx context.Context, kidID string, limit int) ([]LockOverride, error) {
	q := `SELECT o.id, o.lock_id, o.kid_id, o.action, o.ledger_entry_id, o.request_id, o.created_at, l.reason, j.name
		FROM lock_overrides o JOIN jar_locks l ON l.id = o.lock_id JOIN jars j ON j.id = l.jar_id
		WHERE o.kid_id = ? ORDER BY o.created_at DESC, o.id DESC`
	if limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, limit)
	}
	rows, err := x.rows(ctx, q, kidID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LockOverride
	for rows.Next() {
		var o LockOverride
		if err := rows.Scan(&o.ID, &o.LockID, &o.KidID, &o.Action, nstr{&o.LedgerEntryID}, nstr{&o.RequestID},
			rtime{&o.CreatedAt}, nstr{&o.Reason}, &o.JarName); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (x queries) GetLedgerEntryByKeyPrefix(ctx context.Context, prefix string) (LedgerEntry, error) {
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(prefix)
	e, err := scanEntry(x.row(ctx, ledgerSelect+` WHERE e.idempotency_key LIKE ? ESCAPE '\' ORDER BY e.id LIMIT 1`, esc+"%"))
	return e, notFound(err)
}
