// Package sqlite opens the default Goldberry store: a SQLite file on the
// /data volume, via the pure-Go modernc.org/sqlite driver (no CGO).
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/chinny/goldberry/internal/store"
	"github.com/chinny/goldberry/migrations"
)

// Pragmas applied to every connection (plan §9).
var pragmas = []string{"journal_mode(WAL)", "busy_timeout(5000)", "foreign_keys(ON)", "synchronous(NORMAL)"}

// PathFromURL extracts the file path from sqlite:///abs/path.db, sqlite://rel.db
// or sqlite:rel.db.
func PathFromURL(u string) (string, error) {
	rest, ok := strings.CutPrefix(u, "sqlite:")
	if !ok {
		return "", fmt.Errorf("not a sqlite URL: %q", u)
	}
	rest = strings.TrimPrefix(rest, "//")
	if rest == "" || strings.Contains(rest, ":memory:") {
		return "", errors.New("sqlite URL needs a file path (in-memory databases are not supported)")
	}
	return rest, nil
}

// Open opens (creating if needed) and migrates the database at path. It uses
// two pools: a read pool with several connections and a write pool with
// exactly one, whose transactions start with BEGIN IMMEDIATE.
func Open(ctx context.Context, path string, log *slog.Logger) (*store.SQL, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	if fsType, network := networkFS(dir); network {
		log.Warn("the SQLite database is on a network filesystem; WAL mode can silently corrupt data on NFS/SMB. Use a block-backed volume.",
			"path", dir, "fs", fsType)
	}

	write, err := sql.Open("sqlite", dsn(path, "_txlock=immediate"))
	if err != nil {
		return nil, err
	}
	write.SetMaxOpenConns(1)
	write.SetConnMaxIdleTime(0)
	if err := write.PingContext(ctx); err != nil {
		write.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := Migrate(ctx, write); err != nil {
		write.Close()
		return nil, err
	}

	read, err := sql.Open("sqlite", dsn(path, "_pragma=query_only(1)"))
	if err != nil {
		write.Close()
		return nil, err
	}
	read.SetMaxOpenConns(max(4, runtime.NumCPU()))

	closeFn := func() error { return errors.Join(read.Close(), write.Close()) }
	return store.NewSQL(read, write, Dialect{}, closeFn), nil
}

func dsn(path string, extra ...string) string {
	q := make([]string, 0, len(pragmas)+len(extra))
	for _, p := range pragmas {
		q = append(q, "_pragma="+url.QueryEscape(p))
	}
	q = append(q, extra...)
	return "file:" + path + "?" + strings.Join(q, "&")
}

// Migrate applies the embedded SQLite migrations.
func Migrate(ctx context.Context, db *sql.DB) error {
	fsys, err := fs.Sub(migrations.SQLite, "sqlite")
	if err != nil {
		return err
	}
	p, err := goose.NewProvider(database.DialectSQLite3, db, fsys)
	if err != nil {
		return err
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Dialect is the SQLite flavour of the portable query subset.
type Dialect struct{}

func (Dialect) Name() string           { return "sqlite" }
func (Dialect) Rebind(q string) string { return q }
func (Dialect) Time(t time.Time) any   { return t.UTC().Format(store.TimeLayout) }
func (Dialect) IsUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// LockKid is a no-op: the single-connection write pool plus BEGIN IMMEDIATE
// already serializes every writer.
func (Dialect) LockKid(context.Context, *sql.Tx, string) error { return nil }
