package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/splendideXmendax/mysmpp/internal/message"
)

func TestPostgresMigrationsAndConcurrentQuota(t *testing.T) {
	dsn := os.Getenv("MYSMPP_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MYSMPP_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("mysmpp_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	applyMigrationSet(t, ctx, pool, "*.up.sql")
	st := &PostgresStore{pool: pool}
	if err := st.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}

	const attempts = 60
	const limit = 17
	var accepted atomic.Int64
	var rejected atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("quota-%03d", i)
			msg := message.New(id, message.DirectionMT, "1069", "13800138000", "hello")
			_, _, _, err := st.SubmitAtomic(ctx, msg, OutboxItem{
				GatewayID: id, Provider: "mock", Payload: OutboxPayload{GatewayID: id, Provider: "mock"},
			}, SubmitOptions{Quota: &DailyQuotaDebit{TenantID: "tenant-a", Date: "2026-08-19", Segments: 1, Limit: limit}})
			switch {
			case err == nil:
				accepted.Add(1)
			case errors.Is(err, ErrQuotaExceeded):
				rejected.Add(1)
			default:
				t.Errorf("unexpected submit error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if got := accepted.Load(); got != limit {
		t.Fatalf("accepted=%d want=%d (rejected=%d)", got, limit, rejected.Load())
	}
	var used int
	if err := pool.QueryRow(ctx, `SELECT used_segments FROM tenant_quota_usage WHERE tenant_id='tenant-a' AND quota_date='2026-08-19'`).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used != limit {
		t.Fatalf("used_segments=%d want=%d", used, limit)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM outbox`); err != nil {
		t.Fatal(err)
	}

	msg := message.New("outbox-complete", message.DirectionMT, "1069", "13800138000", "hello")
	outboxID, _, _, err := st.SubmitAtomic(ctx, msg, OutboxItem{
		GatewayID: msg.ID, Provider: "provider-a", Payload: OutboxPayload{GatewayID: msg.ID, Provider: "provider-a"},
	}, SubmitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := st.ClaimOutbox(ctx, "worker-a", 1)
	if err != nil || len(claimed) != 1 || claimed[0].ID != outboxID {
		t.Fatalf("claim failed: items=%+v err=%v", claimed, err)
	}
	if err := st.MarkOutboxSending(ctx, outboxID, "worker-a"); err != nil {
		t.Fatal(err)
	}
	if n, err := st.RequeueStaleOutbox(ctx, time.Now().Add(time.Hour), 10); err != nil || n != 0 {
		t.Fatalf("sending outbox was requeued: count=%d err=%v", n, err)
	}
	pending := []Pending{
		{Provider: "provider-a", ProviderID: "complete-p1", GatewayID: msg.ID, SegmentIndex: 1, SegmentCount: 2, ExpiresAt: time.Now().Add(time.Hour)},
		{Provider: "provider-a", ProviderID: "complete-p2", GatewayID: msg.ID, SegmentIndex: 2, SegmentCount: 2, ExpiresAt: time.Now().Add(time.Hour)},
	}
	if err := st.CompleteOutboxSend(ctx, outboxID, "worker-a", pending); err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteOutboxSend(ctx, outboxID, "worker-a", pending); err != nil {
		t.Fatalf("completion retry must be idempotent: %v", err)
	}
	if got, ok, err := st.GetMessage(ctx, msg.ID); err != nil || !ok || got.State != "sent" || got.ProviderID != "complete-p1" {
		t.Fatalf("unexpected completed message: ok=%v msg=%+v err=%v", ok, got, err)
	}
	if rows, err := st.ListPendingByGatewayID(ctx, msg.ID); err != nil || len(rows) != 2 {
		t.Fatalf("pending rows=%+v err=%v", rows, err)
	}
	if depth, err := st.OutboxDepth(ctx, "done"); err != nil || depth != 1 {
		t.Fatalf("done depth=%d err=%v", depth, err)
	}

	unknownMsg := message.New("outbox-uncertain", message.DirectionMT, "1069", "13800138001", "hello")
	unknownID, _, _, err := st.SubmitAtomic(ctx, unknownMsg, OutboxItem{
		GatewayID: unknownMsg.ID, Provider: "provider-a", Payload: OutboxPayload{GatewayID: unknownMsg.ID, Provider: "provider-a"},
	}, SubmitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err = st.ClaimOutbox(ctx, "worker-b", 1)
	if err != nil || len(claimed) != 1 || claimed[0].ID != unknownID {
		t.Fatalf("claim uncertain item failed: items=%+v err=%v", claimed, err)
	}
	if err := st.MarkOutboxSending(ctx, unknownID, "worker-b"); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkOutboxUncertain(ctx, unknownID, "worker-b", "submit response lost"); err != nil {
		t.Fatal(err)
	}
	if n, err := st.RequeueStaleOutbox(ctx, time.Now().Add(time.Hour), 10); err != nil || n != 0 {
		t.Fatalf("uncertain outbox was requeued: count=%d err=%v", n, err)
	}
	if got, ok, err := st.GetMessage(ctx, unknownMsg.ID); err != nil || !ok || got.State != "UNKNOWN" {
		t.Fatalf("unexpected uncertain message: ok=%v msg=%+v err=%v", ok, got, err)
	}

	for _, provider := range []string{"provider-a", "provider-b"} {
		if err := st.SavePending(ctx, Pending{Provider: provider, ProviderID: "same-id", GatewayID: "g-" + provider, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, migrationSQL(t, "006_pending_segments_and_provider_key.down.sql")); err == nil {
		t.Fatal("006 down migration should reject colliding provider IDs")
	}
	if err := st.DeletePending(ctx, "provider-b", "same-id"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, migrationSQL(t, "007_tenant_quota.down.sql")); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, migrationSQL(t, "006_pending_segments_and_provider_key.down.sql")); err != nil {
		t.Fatal(err)
	}
}

func applyMigrationSet(t *testing.T, ctx context.Context, pool *pgxpool.Pool, pattern string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", "migrations", pattern))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			t.Fatalf("apply %s: %v", filepath.Base(path), err)
		}
	}
}

func migrationSQL(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
