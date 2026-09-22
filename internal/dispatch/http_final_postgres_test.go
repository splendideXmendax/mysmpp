package dispatch

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/splendideXmendax/mysmpp/internal/store"
)

func TestHTTPFinalReceiptPostgres(t *testing.T) {
	dsn := os.Getenv("MYSMPP_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MYSMPP_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("mysmpp_http_final_test_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	scopedDSN := dsn + " search_path=" + schema
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		scopedDSN = u.String()
	}
	pool, err := pgxpool.New(ctx, scopedDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	paths, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.up.sql"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no migrations: %v", err)
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(raw)); err != nil {
			t.Fatalf("migration %s: %v", filepath.Base(path), err)
		}
	}
	st, err := store.NewPostgres(ctx, scopedDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	t.Run("concurrent", func(t *testing.T) { testHTTPFinalConcurrent(t, st) })
	t.Run("batch-failure-expiry-nul", func(t *testing.T) { testHTTPFinalBatches(t, st) })
	t.Run("persistence-failure-boundaries", func(t *testing.T) { testHTTPFinalFailureBoundaries(t, st) })
}
