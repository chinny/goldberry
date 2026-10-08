// Package scheduler runs Goldberry's background jobs on one in-process
// ticker (plan §3, ADR 0005). Jobs must be idempotent: a duplicate or
// catch-up run is a no-op. Today's jobs are recurring allowance and request
// expiry; interest, the email outbox and backups join them in later phases.
package scheduler

import (
	"context"
	"log/slog"
	"time"
)

// Job is one periodic task.
type Job struct {
	Name string
	Run  func(ctx context.Context) error
}

// Scheduler ticks every Interval and runs each job in turn.
type Scheduler struct {
	Interval time.Duration
	Jobs     []Job
	Log      *slog.Logger
}

// Run blocks until ctx is cancelled. It runs every job once at start, so
// work missed while the box was off happens straight away.
func (s *Scheduler) Run(ctx context.Context) {
	if s.Interval <= 0 {
		s.Interval = time.Minute
	}
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		s.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	for _, j := range s.Jobs {
		if ctx.Err() != nil {
			return
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					s.Log.Error("job panicked", "job", j.Name, "panic", r)
				}
			}()
			if err := j.Run(ctx); err != nil && ctx.Err() == nil {
				s.Log.Error("job failed", "job", j.Name, "err", err)
			}
		}()
	}
}
