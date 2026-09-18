package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/message"
)

func testReceiptExpiryWaitsForInbox(t *testing.T, st Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	for _, claimed := range []bool{false, true} {
		id := fmt.Sprintf("expiry-inbox-%v", claimed)
		m := message.New(id, message.DirectionMT, "brand", "8613800138000", "test")
		if err := st.SaveMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
		p := Pending{GatewayID: id, Provider: "expiry-inbox", ProviderID: id, SourceKind: "http", SegmentIndex: 1, SegmentCount: 1, ReliabilityManaged: true, ExpiresAt: now.Add(10 * time.Second)}
		if err := st.SavePending(ctx, p); err != nil {
			t.Fatal(err)
		}
		e := ReceiptEvent{Provider: p.Provider, ProviderID: p.ProviderID, State: "DELIVRD", DoneAt: now}
		raw, _ := json.Marshal(e)
		if err := st.EnqueueReceipt(ctx, ReceiptJob{ID: ReceiptKey("inbox", e), Kind: "inbox", Payload: raw, NextAt: now, ExpiresAt: now.Add(48 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
		var job ReceiptJob
		var ok bool
		var err error
		if claimed {
			job, ok, err = st.ClaimReceipt(ctx, "inbox", now.Add(time.Second))
			if err != nil || !ok {
				t.Fatalf("claim: %v %v", ok, err)
			}
		}
		if _, err = st.SweepExpiredPending(ctx, now.Add(20*time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, ok, err = st.GetPending(ctx, p.Provider, p.ProviderID); err != nil || !ok {
			t.Fatalf("durable inbox lost mapping: %v %v", ok, err)
		}
		if err = st.PrepareReceipt(ctx, e); err != nil {
			t.Fatal(err)
		}
		if !claimed {
			job, ok, err = st.ClaimReceipt(ctx, "inbox", now.Add(time.Second))
			if err != nil || !ok {
				t.Fatalf("claim: %v %v", ok, err)
			}
		}
		if err = st.FinishReceipt(ctx, job, time.Time{}, ""); err != nil {
			t.Fatal(err)
		}
		if _, err = st.SweepExpiredPending(ctx, now.Add(20*time.Second)); err != nil {
			t.Fatal(err)
		}
		got, ok, err := st.GetMessage(ctx, id)
		if err != nil || !ok || got.State != "DELIVRD" {
			t.Fatalf("real receipt superseded: %+v %v", got, err)
		}
		if _, ok, err = st.GetPending(ctx, p.Provider, p.ProviderID); err != nil || ok {
			t.Fatalf("finished expired mapping not cleaned: %v %v", ok, err)
		}
	}
}

func testPostgresMaintenanceBatches(t *testing.T, st *PostgresStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	// More than one former batch, without 24,000 individual setup round trips.
	_, err := st.pool.Exec(ctx, `INSERT INTO pending(provider,provider_id,gateway_id,source_kind,received_at,expires_at,reliability_managed,segment_index,segment_count,dlr_state)
 SELECT 'bulk-test','bulk-'||n,'bulk-'||n,'http',$1::timestamptz,$1::timestamptz-INTERVAL '1 hour',TRUE,1,1,'DELIVRD' FROM generate_series(1,12050) n`, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.pool.Exec(ctx, `INSERT INTO receipt_jobs(id,kind,payload,state,expires_at)
 SELECT 'bulk-job-'||n,'delivery','{}','done',$1::timestamptz-INTERVAL '1 hour' FROM generate_series(1,24100) n`, now)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err = st.SweepExpiredPending(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err = st.SweepReliability(ctx, now); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err = st.pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM pending WHERE provider='bulk-test')+(SELECT COUNT(*) FROM receipt_jobs WHERE id LIKE 'bulk-job-%')`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("maintenance backlog=%d err=%v", remaining, err)
	}
	t.Logf("cleaned 12050 terminal mappings + 24100 expired jobs in %s", time.Since(start))
	// Missing DRs require durable EXPIRED snapshots, not the terminal fast path.
	_, err = st.pool.Exec(ctx, `INSERT INTO messages(gateway_id,direction,from_addr,to_addr,text,state,received_at)
 SELECT 'bulk-wait-'||n,'MT','brand','8613800138000','test','sent',$1::timestamptz FROM generate_series(1,12050) n`, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.pool.Exec(ctx, `INSERT INTO pending(provider,provider_id,gateway_id,source_kind,received_at,expires_at,reliability_managed,segment_index,segment_count)
 SELECT 'bulk-wait','bulk-wait-'||n,'bulk-wait-'||n,'http',$1::timestamptz,$1::timestamptz-INTERVAL '1 hour',TRUE,1,1 FROM generate_series(1,12050) n`, now)
	if err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	if _, err = st.SweepExpiredPending(ctx, now); err != nil {
		t.Fatal(err)
	}
	var expired, snapshots int
	if err = st.pool.QueryRow(ctx, `SELECT
 (SELECT COUNT(*) FROM pending WHERE provider='bulk-wait'),
 (SELECT COUNT(*) FROM messages WHERE gateway_id LIKE 'bulk-wait-%' AND state='EXPIRED'),
 (SELECT COUNT(*) FROM receipt_jobs WHERE kind='delivery' AND payload->'Pending'->>'Provider'='bulk-wait')`).Scan(&remaining, &expired, &snapshots); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 || expired != 12050 || snapshots != 12050 {
		t.Fatalf("missing DR cleanup remaining=%d expired=%d snapshots=%d", remaining, expired, snapshots)
	}
	t.Logf("expired 12050 missing-DR messages with durable snapshots in %s", time.Since(start))
}
