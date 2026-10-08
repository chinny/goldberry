package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/chinny/goldberry/internal/store"
	"github.com/chinny/goldberry/internal/store/sqlite"
	"github.com/chinny/goldberry/internal/store/storetest"
)

func open(t *testing.T) store.Store {
	t.Helper()
	st, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "goldberry.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestContract(t *testing.T) { storetest.Run(t, open) }

func TestPathFromURL(t *testing.T) {
	for in, want := range map[string]string{
		"sqlite:///data/goldberry.db": "/data/goldberry.db",
		"sqlite://rel.db":             "rel.db",
		"sqlite:rel.db":               "rel.db",
	} {
		if got, err := sqlite.PathFromURL(in); err != nil || got != want {
			t.Errorf("PathFromURL(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"sqlite://", "sqlite://:memory:", "postgres://x"} {
		if _, err := sqlite.PathFromURL(bad); err == nil {
			t.Errorf("PathFromURL(%q) accepted", bad)
		}
	}
}

func TestReopenKeepsData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "goldberry.db")
	st, err := sqlite.Open(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Tx(ctx, "", func(tx store.Tx) error { return tx.PutSetting(ctx, "k", "v", false) }); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = sqlite.Open(ctx, path, nil) // migrations are idempotent
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if v, err := st.GetSetting(ctx, "k"); err != nil || v != "v" {
		t.Fatalf("got %q, %v", v, err)
	}
}
