package sqlite

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chinny/goldberry/internal/store"
)

// BackupPrefix names every snapshot file: goldberry-YYYYMMDD[-HHMMSS].db.
const BackupPrefix = "goldberry-"

// Backup writes a consistent snapshot of the live database to path with
// VACUUM INTO (plan §12.5). It runs on the write connection, so it waits for
// any write in flight and never sees half a transaction.
func Backup(ctx context.Context, st *store.SQL, path string) error {
	if st.Dialect() != "sqlite" {
		return fmt.Errorf("built-in backups are for SQLite; use pg_dump or your CNPG backups for Postgres")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	if _, err := st.WriteDB().ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("vacuum into %s: %w", path, err)
	}
	return os.Chmod(path, 0o600)
}

// Prune keeps the newest retain snapshots in dir and deletes the rest.
func Prune(dir string, retain int) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), BackupPrefix) && strings.HasSuffix(e.Name(), ".db") {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names))) // the date is in the name
	var removed []string
	for i, n := range names {
		if i < retain {
			continue
		}
		if err := os.Remove(filepath.Join(dir, n)); err != nil {
			return removed, err
		}
		removed = append(removed, n)
	}
	return removed, nil
}

// NightlyName is the snapshot name for a calendar day.
func NightlyName(day time.Time) string { return BackupPrefix + day.Format("20060102") + ".db" }

// OnDemandName is the snapshot name for `goldberry backup`.
func OnDemandName(at time.Time) string { return BackupPrefix + at.Format("20060102-150405") + ".db" }
