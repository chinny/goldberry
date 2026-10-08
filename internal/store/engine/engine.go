// Package engine picks the store implementation from GOLDBERRY_DATABASE_URL.
package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/chinny/goldberry/internal/store"
	"github.com/chinny/goldberry/internal/store/postgres"
	"github.com/chinny/goldberry/internal/store/sqlite"
)

// Open opens and migrates the database named by url.
func Open(ctx context.Context, url string, log *slog.Logger) (*store.SQL, error) {
	switch {
	case strings.HasPrefix(url, "sqlite:"):
		path, err := sqlite.PathFromURL(url)
		if err != nil {
			return nil, err
		}
		return sqlite.Open(ctx, path, log)
	case strings.HasPrefix(url, "postgres://"), strings.HasPrefix(url, "postgresql://"):
		return postgres.Open(ctx, url)
	default:
		return nil, fmt.Errorf("unsupported database URL %q: want sqlite:///path or postgres://", Redact(url))
	}
}

// Redact hides any password in a database URL for logging.
func Redact(url string) string {
	scheme, rest, ok := strings.Cut(url, "://")
	if !ok {
		return url
	}
	userinfo, host, ok := strings.Cut(rest, "@")
	if !ok {
		return url
	}
	if user, _, hasPw := strings.Cut(userinfo, ":"); hasPw {
		userinfo = user + ":***"
	}
	return scheme + "://" + userinfo + "@" + host
}
