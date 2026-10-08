package store

import (
	"context"
	"time"
)

const goalCols = `id, kid_id, jar_id, name, emoji, target_amount, priority, created_by, reached_at, archived_at, created_at`

func scanGoal(sc interface{ Scan(...any) error }) (Goal, error) {
	var g Goal
	err := sc.Scan(&g.ID, &g.KidID, &g.JarID, &g.Name, nstr{&g.Emoji}, &g.TargetAmount, &g.Priority, nstr{&g.CreatedBy},
		ntime{&g.ReachedAt}, ntime{&g.ArchivedAt}, rtime{&g.CreatedAt})
	return g, err
}

func (x queries) ListGoals(ctx context.Context, kidID string, includeArchived bool) ([]Goal, error) {
	q := `SELECT ` + goalCols + ` FROM goals WHERE kid_id = ?`
	if !includeArchived {
		q += ` AND archived_at IS NULL`
	}
	rows, err := x.rows(ctx, q+` ORDER BY priority, created_at, id`, kidID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Goal
	for rows.Next() {
		g, err := scanGoal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (x queries) GetGoal(ctx context.Context, id string) (Goal, error) {
	g, err := scanGoal(x.row(ctx, `SELECT `+goalCols+` FROM goals WHERE id = ?`, id))
	return g, notFound(err)
}

func (x queries) CreateGoal(ctx context.Context, g Goal) error {
	_, err := x.exec(ctx, `INSERT INTO goals (`+goalCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		g.ID, g.KidID, g.JarID, g.Name, nul(g.Emoji), g.TargetAmount, g.Priority, nul(g.CreatedBy),
		x.tp(g.ReachedAt), x.tp(g.ArchivedAt), x.t(g.CreatedAt))
	return err
}

func (x queries) UpdateGoal(ctx context.Context, g Goal) error {
	_, err := x.exec(ctx, `UPDATE goals SET jar_id = ?, name = ?, emoji = ?, target_amount = ?, priority = ?,
		reached_at = ?, archived_at = ? WHERE id = ?`,
		g.JarID, g.Name, nul(g.Emoji), g.TargetAmount, g.Priority, x.tp(g.ReachedAt), x.tp(g.ArchivedAt), g.ID)
	return err
}

func (x queries) ListInterestRules(ctx context.Context, kidID string) ([]InterestRule, error) {
	q, args := `SELECT kid_id, jar_id, monthly_bps, monthly_cap, active FROM interest_rules`, []any{}
	if kidID != "" {
		q += ` WHERE kid_id = ?`
		args = append(args, kidID)
	}
	rows, err := x.rows(ctx, q+` ORDER BY kid_id, jar_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InterestRule
	for rows.Next() {
		var r InterestRule
		if err := rows.Scan(&r.KidID, &r.JarID, &r.MonthlyBPS, nint{&r.MonthlyCap}, &r.Active); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (x queries) PutInterestRule(ctx context.Context, r InterestRule) error {
	var capArg any
	if r.MonthlyCap > 0 {
		capArg = r.MonthlyCap
	}
	_, err := x.exec(ctx, `INSERT INTO interest_rules (kid_id, jar_id, monthly_bps, monthly_cap, active) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (kid_id, jar_id) DO UPDATE SET monthly_bps = excluded.monthly_bps, monthly_cap = excluded.monthly_cap,
			active = excluded.active`, r.KidID, r.JarID, r.MonthlyBPS, capArg, r.Active)
	return err
}

func (x queries) JarBalanceBefore(ctx context.Context, jarID string, t time.Time) (int64, error) {
	var v int64
	err := x.row(ctx, `SELECT CAST(COALESCE(SUM(amount), 0) AS BIGINT) FROM ledger_entries WHERE jar_id = ? AND effective_at < ?`,
		jarID, x.t(t)).Scan(&v)
	return v, err
}

func (x queries) JarEntriesBetween(ctx context.Context, jarID string, from, to time.Time) ([]DatedAmount, error) {
	rows, err := x.rows(ctx, `SELECT amount, effective_at FROM ledger_entries
		WHERE jar_id = ? AND effective_at >= ? AND effective_at < ? ORDER BY effective_at, id`, jarID, x.t(from), x.t(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DatedAmount
	for rows.Next() {
		var d DatedAmount
		if err := rows.Scan(&d.Amount, rtime{&d.EffectiveAt}); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
