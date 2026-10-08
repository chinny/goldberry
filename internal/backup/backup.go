// Package backup runs Goldberry's nightly SQLite snapshots (plan §12.5):
// once a day at GOLDBERRY_BACKUP_SCHEDULE (household-agnostic, in TZ), a
// VACUUM INTO snapshot lands in /data/backups and only the newest
// GOLDBERRY_BACKUP_RETAIN are kept. Copy that folder off the box: a backup on
// the same disk is not a backup.
package backup

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/chinny/goldberry/internal/store"
	"github.com/chinny/goldberry/internal/store/sqlite"
)

// Nightly is the scheduler job.
type Nightly struct {
	Store  *store.SQL
	Dir    string
	At     time.Duration // time of day, e.g. 3h15m
	Retain int
	Loc    *time.Location
	Now    func() time.Time
	Log    *slog.Logger
}

// Dir returns the backup folder next to a SQLite database file.
func Dir(dbPath string) string { return filepath.Join(filepath.Dir(dbPath), "backups") }

// Run takes today's snapshot once the scheduled time has passed, if it
// doesn't exist yet. Missing the time (the box was off) just means it runs on
// the next tick.
func (n *Nightly) Run(ctx context.Context) error {
	now := n.Now().In(n.Loc)
	y, m, d := now.Date()
	midnight := time.Date(y, m, d, 0, 0, 0, 0, n.Loc)
	if now.Before(midnight.Add(n.At)) {
		return nil
	}
	path := filepath.Join(n.Dir, sqlite.NightlyName(midnight))
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	start := time.Now()
	if err := sqlite.Backup(ctx, n.Store, path); err != nil {
		return err
	}
	removed, err := sqlite.Prune(n.Dir, n.Retain)
	n.Log.Info("backup written", "path", path, "dur_ms", time.Since(start).Milliseconds(), "pruned", len(removed))
	return err
}
