package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/chinny/goldberry/internal/store"
	"github.com/chinny/goldberry/internal/store/postgres"
	"github.com/chinny/goldberry/internal/store/storetest"
)

// GOLDBERRY_TEST_POSTGRES_URL points at a server where the test user may
// create databases. Each test gets its own database, dropped afterwards.
func baseURL(t *testing.T) string {
	u := os.Getenv("GOLDBERRY_TEST_POSTGRES_URL")
	if u == "" {
		t.Skip("GOLDBERRY_TEST_POSTGRES_URL not set")
	}
	return u
}

func open(t *testing.T) store.Store {
	t.Helper()
	base := baseURL(t)
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("gb_test_%s", strings.ToLower(strings.NewReplacer("/", "_", "-", "_").Replace(t.Name())))
	if len(name) > 63 {
		name = name[:63]
	}
	ctx := context.Background()
	if _, err := admin.ExecContext(ctx, `DROP DATABASE IF EXISTS `+name); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	st, err := postgres.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.Close()
		if db, err := sql.Open("pgx", base); err == nil {
			_, _ = db.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
			db.Close()
		}
	})
	return st
}

func TestContract(t *testing.T) { storetest.Run(t, open) }

func TestRebind(t *testing.T) {
	got := postgres.Dialect{}.Rebind(`SELECT * FROM t WHERE a = ? AND b IN (?, ?)`)
	if got != `SELECT * FROM t WHERE a = $1 AND b IN ($2, $3)` {
		t.Fatal(got)
	}
}
