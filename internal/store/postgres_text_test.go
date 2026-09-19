package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/splendideXmendax/mysmpp/internal/message"
)

func allASCIIText() string {
	b := make([]byte, 128)
	for i := range b {
		b[i] = byte(i)
	}
	return string(b)
}

func TestPostgresTextEnvelopes(t *testing.T) {
	for _, text := range []string{"", "hello", "中文😀", `literal \u0000 \x00 text_bytes`, allASCIIText(), "\x00", "a\x00中\x00😀"} {
		p := OutboxPayload{Text: text, GatewayID: "g1", RawPayload: []byte{0, 255}, RawPayloadSet: true}
		raw, err := marshalPostgresOutbox(p)
		if err != nil {
			t.Fatal(err)
		}
		got, err := unmarshalPostgresOutbox(raw)
		if err != nil || got.Text != text || !bytes.Equal(got.RawPayload, p.RawPayload) || !got.RawPayloadSet {
			t.Fatalf("outbox roundtrip failed: %+v %v", got, err)
		}
		d := ReceiptDelivery{Pending: Pending{Text: text, Provider: "p", ProviderID: "up"}, Event: ReceiptEvent{State: "DELIVRD"}}
		original, _ := json.Marshal(d)
		stored, err := marshalPostgresReceipt(ReceiptJob{Kind: "delivery", Payload: original})
		if err != nil {
			t.Fatal(err)
		}
		restored, err := unmarshalPostgresReceipt(stored)
		if err != nil || !bytes.Equal(restored, original) {
			t.Fatalf("receipt roundtrip failed: %s %v", restored, err)
		}
		if strings.IndexByte(text, 0) >= 0 {
			if bytes.Contains(raw, []byte(`\u0000`)) || bytes.Contains(stored, []byte(`\u0000`)) {
				t.Fatal("NUL escaped into PostgreSQL JSONB instead of binary sidecar")
			}
		} else {
			legacy, _ := json.Marshal(p)
			if !bytes.Equal(legacy, raw) || !bytes.Equal(original, stored) {
				t.Fatal("ordinary payload format changed")
			}
		}
	}
	if _, err := unmarshalPostgresOutbox([]byte(`{"text_bytes":"!invalid!"}`)); err == nil {
		t.Fatal("corrupt binary sidecar silently accepted")
	}
	if _, err := unmarshalPostgresReceipt([]byte(`{"text_bytes":"!invalid!"}`)); err == nil {
		t.Fatal("corrupt receipt sidecar silently accepted")
	}
}

func TestLosslessTextMemoryAndFile(t *testing.T) {
	t.Run("memory", func(t *testing.T) { testLosslessTextLifecycle(t, NewMemory(), nil) })
	t.Run("file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "store.json")
		st, err := NewFile(path)
		if err != nil {
			t.Fatal(err)
		}
		testLosslessTextLifecycle(t, st, func() Store {
			reopened, err := NewFile(path)
			if err != nil {
				t.Fatal(err)
			}
			return reopened
		})
	})
}

func TestPostgresLosslessText(t *testing.T) {
	dsn := os.Getenv("MYSMPP_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MYSMPP_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("mysmpp_text_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { pool.Close() }()
	applyMigrationSet(t, ctx, pool, "*.up.sql")
	st := &PostgresStore{pool: pool}
	if _, err := pool.Exec(ctx, migrationSQL(t, "010_lossless_message_text.down.sql")); err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureSchema(ctx); err == nil {
		t.Fatal("startup allowed a schema without lossless text columns")
	}
	// A pre-migration row must remain readable; no backfill or text rewrite.
	if _, err := pool.Exec(ctx, `INSERT INTO messages(gateway_id,direction,from_addr,to_addr,text,state,received_at) VALUES('legacy','mt','brand','8613800138000','literal \u0000','sent',NOW())`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, migrationSQL(t, "010_lossless_message_text.up.sql")); err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if m, ok, err := st.GetMessage(ctx, "legacy"); err != nil || !ok || m.Text != `literal \u0000` {
		t.Fatalf("historical text changed: %+v %v", m, err)
	}
	testLosslessTextLifecycle(t, st, func() Store {
		pool.Close()
		var err error
		pool, err = pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		return &PostgresStore{pool: pool}
	})
	st = &PostgresStore{pool: pool}
	if _, err := pool.Exec(ctx, migrationSQL(t, "010_lossless_message_text.down.sql")); err == nil {
		t.Fatal("downgrade silently discarded NUL text")
	}
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT text_bytes FROM messages WHERE gateway_id='nul-delivered'`).Scan(&raw); err != nil || string(raw) != allASCIIText() {
		t.Fatalf("binary bytes changed: %x %v", raw, err)
	}
	// Overwriting a previously binary-backed row must clear its sidecar.
	m := message.New("nul-delivered", message.DirectionMT, "brand", "8613800138000", "ordinary")
	if err := st.SaveMessage(ctx, m); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := st.GetMessage(ctx, m.ID); err != nil || !ok || got.Text != "ordinary" {
		t.Fatalf("stale sidecar shadowed text update: %+v %v", got, err)
	}
	j := deliveryJob(Pending{Text: allASCIIText()}, ReceiptEvent{Provider: "direct", ProviderID: "direct", State: "DELIVRD"}, nil)
	if err := st.EnqueueReceipt(ctx, j); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := st.ClaimReceipt(ctx, "delivery", time.Now().Add(time.Second)); err != nil || !ok || !bytes.Equal(got.Payload, j.Payload) {
		t.Fatalf("direct receipt enqueue lost text: %s %v", got.Payload, err)
	}
	if _, err := st.EnqueueOutbox(ctx, OutboxItem{GatewayID: "direct", Provider: "test", Payload: OutboxPayload{Text: allASCIIText()}}); err != nil {
		t.Fatal(err)
	}
	if got, err := st.ClaimOutbox(ctx, "direct", 1); err != nil || len(got) != 1 || got[0].Payload.Text != allASCIIText() {
		t.Fatalf("direct outbox enqueue lost text: %+v %v", got, err)
	}
}

// Used by memory/file and the isolated PostgreSQL suite. Includes restart,
// idempotency, exact quota, pending, successful/synthetic/expired DR snapshots.
func testLosslessTextLifecycle(t *testing.T, st Store, reopen func() Store) {
	t.Helper()
	ctx := context.Background()
	text := allASCIIText()
	now := time.Now().UTC()
	for _, outcome := range []string{"delivered", "rejected", "expired"} {
		id := "nul-" + outcome
		msg := message.New(id, message.DirectionMT, "brand", "8613800138000", text)
		msg.Segments = message.Split(text, message.SplitOptions{})
		item := OutboxItem{GatewayID: id, Provider: "nul-test", Payload: OutboxPayload{GatewayID: id, Provider: "nul-test", Text: text, Encoding: msg.Encoding}}
		opts := SubmitOptions{Idempotency: IdempotencyOptions{ClientID: "nul-test", Key: id}, Quota: &DailyQuotaDebit{TenantID: id, Date: "2026-09-20", Segments: 2, Limit: 2}}
		outboxID, _, dup, err := st.SubmitAtomic(ctx, msg, item, opts)
		if err != nil || dup {
			t.Fatalf("submit: dup=%v err=%v", dup, err)
		}
		if reopen != nil {
			st = reopen()
		}
		if _, gatewayID, duplicate, err := st.SubmitAtomic(ctx, msg, item, opts); err != nil || !duplicate || gatewayID != id {
			t.Fatalf("duplicate charged: %s %v %v", gatewayID, duplicate, err)
		}
		other := msg
		other.ID += "-over-quota"
		opts.Idempotency.Key = other.ID
		if _, _, _, err := st.SubmitAtomic(ctx, other, item, opts); err != ErrQuotaExceeded {
			t.Fatalf("quota not preserved: %v", err)
		}
		got, found, err := st.GetMessage(ctx, id)
		if err != nil || !found || got.Text != text || message.Join(got.Segments) != text {
			t.Fatalf("message roundtrip: %+v %v", got, err)
		}
		listed, err := st.ListMessagesPage(ctx, ListOptions{Limit: 1000})
		if err != nil {
			t.Fatal(err)
		}
		matched := false
		for _, m := range listed {
			if m.ID == id {
				matched = m.Text == text
			}
		}
		if !matched {
			t.Fatal("list lost original text")
		}
		claimed, err := st.ClaimOutbox(ctx, "nul-worker", 1000)
		if err != nil || len(claimed) != 1 || claimed[0].ID != outboxID || claimed[0].Payload.Text != text {
			t.Fatalf("outbox roundtrip: %+v %v", claimed, err)
		}
		if err := st.MarkOutboxSending(ctx, outboxID, "nul-worker"); err != nil {
			t.Fatal(err)
		}
		rows := make([]Pending, 2)
		for i := range rows {
			rows[i] = Pending{GatewayID: id, Provider: "nul-test", ProviderID: fmt.Sprintf("%s-%d", id, i), SourceKind: "http", Text: text, SegmentIndex: i + 1, SegmentCount: 2, ExpiresAt: now.Add(time.Hour)}
			if outcome == "rejected" {
				rows[i].DLRReady, rows[i].DLRState, rows[i].DLRDoneAt = true, "REJECTD", now
			}
		}
		if err := st.CompleteOutboxSend(ctx, outboxID, "nul-worker", rows); err != nil {
			t.Fatal(err)
		}
		for _, p := range rows {
			got, found, err := st.GetPending(ctx, p.Provider, p.ProviderID)
			if err != nil || !found || got.Text != text {
				t.Fatalf("pending roundtrip: %+v %v", got, err)
			}
		}
		if outcome == "delivered" {
			for _, p := range rows {
				if err := st.PrepareReceipt(ctx, ReceiptEvent{Provider: p.Provider, ProviderID: p.ProviderID, State: "DELIVRD", DoneAt: now}); err != nil {
					t.Fatal(err)
				}
			}
		}
		if outcome == "expired" {
			if _, err := st.SweepExpiredPending(ctx, now.Add(2*time.Hour)); err != nil {
				t.Fatal(err)
			}
		}
		if reopen != nil {
			st = reopen()
		}
		for range rows {
			j, ok, err := st.ClaimReceipt(ctx, "delivery", time.Now().Add(time.Second))
			if err != nil || !ok {
				t.Fatalf("missing receipt: %v %v", ok, err)
			}
			var d ReceiptDelivery
			if err := json.Unmarshal(j.Payload, &d); err != nil || d.Pending.Text != text {
				t.Fatalf("receipt text lost: %+v %v", d, err)
			}
			if err := st.FinishReceipt(ctx, j, time.Time{}, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
}
