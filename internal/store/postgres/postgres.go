// Package postgres opens the optional Postgres store, selected when
// GOLDBERRY_DATABASE_URL is a postgres:// URL.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/pressly/goose/v3/lock"

	"github.com/chinny/goldberry/internal/store"
	"github.com/chinny/goldberry/migrations"
)

// Open connects to Postgres and applies migrations under an advisory lock, so
// several replicas starting at once migrate exactly once.
func Open(ctx context.Context, url string) (*store.SQL, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	if err := Migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return store.NewSQL(db, db, Dialect{}, db.Close), nil
}

// Migrate applies the embedded Postgres migrations, holding pg_advisory_lock.
func Migrate(ctx context.Context, db *sql.DB) error {
	fsys, err := fs.Sub(migrations.Postgres, "postgres")
	if err != nil {
		return err
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return err
	}
	p, err := goose.NewProvider(database.DialectPostgres, db, fsys, goose.WithSessionLocker(locker))
	if err != nil {
		return err
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Dialect is the Postgres flavour of the portable query subset.
type Dialect struct{}

func (Dialect) Name() string         { return "postgres" }
func (Dialect) Time(t time.Time) any { return t.UTC() }

// Rebind turns ? placeholders into $1, $2, … Queries never contain a literal ?.
func (Dialect) Rebind(q string) string {
	if !strings.Contains(q, "?") {
		return q
	}
	var b strings.Builder
	n := 0
	for _, r := range q {
		if r == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (Dialect) IsUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// LockKid serializes this kid's writes by locking their users row.
func (Dialect) LockKid(ctx context.Context, tx *sql.Tx, kidID string) error {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id = $1 FOR UPDATE`, kidID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	return err
}
