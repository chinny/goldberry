package store

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// ExportVersion is the dump format version written in the header line.
const ExportVersion = 1

const exportFormat = "goldberry-export"

type colKind int

const (
	cText colKind = iota
	cInt
	cBool
	cTime
)

type exportCol struct {
	name string
	kind colKind
}

type exportTable struct {
	name  string
	cols  []exportCol
	order string
}

func cols(spec string) []exportCol {
	var out []exportCol
	for _, f := range strings.Fields(spec) {
		name, kind, _ := strings.Cut(f, ":")
		c := exportCol{name: name}
		switch kind {
		case "i":
			c.kind = cInt
		case "b":
			c.kind = cBool
		case "t":
			c.kind = cTime
		}
		out = append(out, c)
	}
	return out
}

// exportTables lists every table worth keeping, parents before children.
// Sessions and sign-in throttling are left out: they're transient, and a
// restored install should make everyone sign in again.
var exportTables = []exportTable{
	{"household", cols("id name currency timezone pin_length:i allow_negative:b request_expiry_days:i kid_picker_login:b created_at:t"), "id"},
	{"users", cols("id role username display_name avatar password_hash pin_hash email disabled_at:t created_at:t"), "created_at, id"},
	{"jars", cols("id kid_id name kind sort_order:i archived_at:t"), "kid_id, sort_order, id"},
	{"split_rules", cols("kid_id jar_id basis_points:i"), "kid_id, jar_id"},
	{"schedules", cols("id kid_id amount:i cadence weekday:i day_of_month:i anchor_date target_jar_id comment_template active:b last_occurrence created_by created_at:t"), "created_at, id"},
	{"goals", cols("id kid_id jar_id name emoji target_amount:i priority:i created_by reached_at:t archived_at:t created_at:t"), "created_at, id"},
	{"ledger_entries", cols("id kid_id jar_id amount:i kind comment private_note actor_id reverses_id transfer_id batch_id request_id schedule_id idempotency_key effective_at:t created_at:t"),
		"CASE WHEN reverses_id IS NULL THEN 0 ELSE 1 END, created_at, id"}, // originals before their reversals
	{"withdrawal_requests", cols("id kid_id jar_id amount:i approved_amount:i reason goal_id status decided_by decision_note decided_at:t idempotency_key created_at:t expires_at:t"), "created_at, id"},
	{"jar_locks", cols("id jar_id to_jar_id set_by_role set_by reason until_date created_at:t removed_by removed_at:t"), "created_at, id"},
	{"lock_overrides", cols("id lock_id kid_id action ledger_entry_id request_id created_at:t"), "created_at, id"},
	{"interest_rules", cols("kid_id jar_id monthly_bps:i monthly_cap:i active:b"), "kid_id, jar_id"},
	{"notifications", cols("id recipient_id kind payload request_id read_at:t created_at:t"), "created_at, id"},
	{"notification_prefs", cols("user_id kind email:b"), "user_id, kind"},
	{"settings", cols("key value secret:b"), "key"},
	{"audit_log", cols("id actor_id action target_id detail created_at:t"), "created_at, id"},
}

type exportHeader struct {
	Format     string    `json:"format"`
	Version    int       `json:"version"`
	ExportedAt time.Time `json:"exported_at"`
	From       string    `json:"from"`
}

type exportLine struct {
	Table string                     `json:"t"`
	Row   map[string]json.RawMessage `json:"r"`
}

// Export writes every table as JSON lines: a header, then one {"t","r"} line
// per row. Values are typed the same way for both engines (times as RFC 3339
// UTC, booleans as booleans), so a dump loads into either.
func (s *SQL) Export(ctx context.Context, w io.Writer, now time.Time) (int, error) {
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	if err := enc.Encode(exportHeader{exportFormat, ExportVersion, now.UTC(), s.Dialect()}); err != nil {
		return 0, err
	}
	n := 0
	for _, t := range exportTables {
		m, err := s.exportTable(ctx, enc, t)
		n += m
		if err != nil {
			return n, fmt.Errorf("export %s: %w", t.name, err)
		}
	}
	return n, bw.Flush()
}

func (s *SQL) exportTable(ctx context.Context, enc *json.Encoder, t exportTable) (int, error) {
	names := make([]string, len(t.cols))
	for i, c := range t.cols {
		names[i] = c.name
	}
	// Table and column names come from the static exportTables list, never input.
	rows, err := s.read.QueryContext(ctx, `SELECT `+strings.Join(names, ", ")+` FROM `+t.name+` ORDER BY `+t.order) //nolint:gosec // see above
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		dest := make([]any, len(t.cols))
		for i, c := range t.cols {
			switch c.kind {
			case cText:
				dest[i] = new(sql.NullString)
			case cInt:
				dest[i] = new(sql.NullInt64)
			case cBool:
				dest[i] = new(sql.NullBool)
			case cTime:
				var p *time.Time
				dest[i] = &ntime{&p}
			}
		}
		if err := rows.Scan(dest...); err != nil {
			return n, err
		}
		row := map[string]json.RawMessage{}
		for i, c := range t.cols {
			var v any
			switch d := dest[i].(type) {
			case *sql.NullString:
				if d.Valid {
					v = d.String
				}
			case *sql.NullInt64:
				if d.Valid {
					v = d.Int64
				}
			case *sql.NullBool:
				if d.Valid {
					v = d.Bool
				}
			case *ntime:
				if *d.p != nil {
					v = (*d.p).UTC().Format(time.RFC3339Nano)
				}
			}
			b, err := json.Marshal(v)
			if err != nil {
				return n, err
			}
			row[c.name] = b
		}
		if err := enc.Encode(exportLine{Table: t.name, Row: row}); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

// ErrNotEmpty is returned when importing into a database that has users.
var ErrNotEmpty = errors.New("import needs an empty database (no users yet)")

// Import loads a dump written by Export into an empty database, in one
// transaction: either everything loads or nothing does.
func (s *SQL) Import(ctx context.Context, r io.Reader) (int, error) {
	tables := map[string]exportTable{}
	for _, t := range exportTables {
		tables[t.name] = t
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	if !sc.Scan() {
		return 0, errors.New("empty dump")
	}
	var h exportHeader
	if err := json.Unmarshal(sc.Bytes(), &h); err != nil || h.Format != exportFormat {
		return 0, errors.New("not a goldberry export")
	}
	if h.Version > ExportVersion {
		return 0, fmt.Errorf("dump version %d is newer than this goldberry understands (%d)", h.Version, ExportVersion)
	}
	n := 0
	err := s.Tx(ctx, "", func(tx Tx) error {
		q := tx.(queries)
		if users, err := q.CountAdmins(ctx, true); err != nil {
			return err
		} else if users > 0 {
			return ErrNotEmpty
		}
		if _, err := q.GetHousehold(ctx); err == nil {
			return ErrNotEmpty
		}
		// Settings may already hold per-install keys created at startup;
		// the dump's values replace them.
		if _, err := q.exec(ctx, `DELETE FROM settings`); err != nil {
			return err
		}
		for line := 2; sc.Scan(); line++ {
			var l exportLine
			dec := json.NewDecoder(strings.NewReader(sc.Text()))
			dec.UseNumber()
			if err := dec.Decode(&l); err != nil {
				return fmt.Errorf("line %d: %w", line, err)
			}
			t, ok := tables[l.Table]
			if !ok {
				return fmt.Errorf("line %d: unknown table %q", line, l.Table)
			}
			names := make([]string, len(t.cols))
			args := make([]any, len(t.cols))
			for i, c := range t.cols {
				names[i] = c.name
				v, err := importValue(q, c, l.Row[c.name])
				if err != nil {
					return fmt.Errorf("line %d: %s.%s: %w", line, t.name, c.name, err)
				}
				args[i] = v
			}
			// t comes from the static exportTables list: the dump only picks which one.
			stmt := `INSERT INTO ` + t.name + ` (` + strings.Join(names, ", ") + `) VALUES (?` + strings.Repeat(`, ?`, len(names)-1) + `)`
			if _, err := q.exec(ctx, stmt, args...); err != nil {
				return fmt.Errorf("line %d: %s: %w", line, t.name, err)
			}
			n++
		}
		return sc.Err()
	})
	return n, err
}

func importValue(q queries, c exportCol, raw json.RawMessage) (any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	switch c.kind {
	case cText:
		var s string
		err := json.Unmarshal(raw, &s)
		return s, err
	case cInt:
		var n json.Number
		if err := json.Unmarshal(raw, &n); err != nil {
			return nil, err
		}
		return n.Int64()
	case cBool:
		var b bool
		err := json.Unmarshal(raw, &b)
		return b, err
	case cTime:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return nil, err
		}
		return q.t(t), nil
	}
	return nil, fmt.Errorf("unknown column kind")
}
