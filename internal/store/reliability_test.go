package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/message"
)

func TestReliabilityMemoryAndFile(t *testing.T) {
	t.Run("memory", func(t *testing.T) { testReliabilityStore(t, NewMemory()) })
	t.Run("file", func(t *testing.T) {
		st, err := NewFile(filepath.Join(t.TempDir(), "state.json"))
		if err != nil {
			t.Fatal(err)
		}
		testReliabilityStore(t, st)
	})
}

func testReliabilityStore(t *testing.T, st Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	t.Run("provider-id-collision", func(t *testing.T) {
		p := Pending{Provider: "collision-provider", ProviderID: "same-id", GatewayID: "original", ExpiresAt: now.Add(time.Hour), UpstreamStatus: 0xffffffff}
		if err := st.SavePending(ctx, p); err != nil {
			t.Fatal(err)
		}
		p.GatewayID = "other"
		if err := st.SavePending(ctx, p); err == nil {
			t.Fatal("provider ID collision replaced original mapping")
		}
		got, ok, err := st.GetPending(ctx, p.Provider, p.ProviderID)
		if err != nil || !ok || got.GatewayID != "original" || got.UpstreamStatus != 0xffffffff {
			t.Fatalf("original mapping lost: %+v %v", got, err)
		}
	})
	t.Run("binding-concurrency", func(t *testing.T) {
		var wg sync.WaitGroup
		winners := make(chan string, 32)
		for i := 1; i <= 32; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				b, err := st.BindMultipart(ctx, MultipartBinding{Key: "group", Total: 32, Route: json.RawMessage(fmt.Sprintf(`{"name":"r%d"}`, i)), Provider: fmt.Sprintf("p%d", i), ExpiresAt: now.Add(time.Minute)}, i, fmt.Sprint(i))
				if err != nil {
					t.Error(err)
					return
				}
				winners <- b.Provider
			}(i)
		}
		wg.Wait()
		close(winners)
		winner := ""
		for v := range winners {
			if winner == "" {
				winner = v
			}
			if winner != v {
				t.Fatalf("split route %s / %s", winner, v)
			}
		}
		if _, err := st.BindMultipart(ctx, MultipartBinding{Key: "group", Total: 32}, 2, "changed"); !errors.Is(err, ErrMultipartConflict) {
			t.Fatalf("conflict err=%v", err)
		}
		if _, err := st.BindMultipart(ctx, MultipartBinding{Key: "group", Total: 31}, 3, "3"); !errors.Is(err, ErrMultipartConflict) {
			t.Fatalf("changed total err=%v", err)
		}
	})
	t.Run("lease-and-dedup", func(t *testing.T) {
		j := ReceiptJob{ID: "lease-test", Kind: "test", Payload: json.RawMessage(`{"v":1}`), NextAt: now, ExpiresAt: now.Add(time.Hour)}
		if err := st.EnqueueReceipt(ctx, j); err != nil {
			t.Fatal(err)
		}
		j.Payload = json.RawMessage(`{"v":2}`)
		if err := st.EnqueueReceipt(ctx, j); err != nil {
			t.Fatal(err)
		}
		first, ok, err := st.ClaimReceipt(ctx, "test", now.Add(time.Second))
		if err != nil || !ok || string(first.Payload) != `{"v": 1}` && string(first.Payload) != `{"v":1}` {
			t.Fatalf("claim=%+v %v %v", first, ok, err)
		}
		if _, ok, err := st.ClaimReceipt(ctx, "test", now.Add(2*time.Second)); err != nil || ok {
			t.Fatalf("double claim %v %v", ok, err)
		}
		second, ok, err := st.ClaimReceipt(ctx, "test", now.Add(2*time.Minute))
		if err != nil || !ok {
			t.Fatalf("lease recovery %v %v", ok, err)
		}
		if err := st.FinishReceipt(ctx, first, time.Time{}, ""); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("old owner committed: %v", err)
		}
		if err := st.FinishReceipt(ctx, second, time.Time{}, ""); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := st.ClaimReceipt(ctx, "test", now.Add(3*time.Minute)); err != nil || ok {
			t.Fatalf("done job reclaimed: %v %v", ok, err)
		}
	})
	t.Run("atomic-aggregate-and-monotonic-final", func(t *testing.T) {
		msg := message.New("receipt-concurrent", message.DirectionMT, "brand", "8613800138000", "hello")
		if err := st.SaveMessage(ctx, msg); err != nil {
			t.Fatal(err)
		}
		const n = 20
		for i := 1; i <= n; i++ {
			if err := st.SavePending(ctx, Pending{Provider: "p", ProviderID: fmt.Sprintf("r%d", i), GatewayID: msg.ID, SegmentIndex: i, SegmentCount: n, SourceKind: "http", ExpiresAt: now.Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
		}
		var wg sync.WaitGroup
		for i := 1; i <= n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				state := "DELIVRD"
				if i == 7 {
					state = "REJECTD"
				}
				if err := st.PrepareReceipt(ctx, ReceiptEvent{Provider: "p", ProviderID: fmt.Sprintf("r%d", i), State: state, DoneAt: now}); err != nil {
					t.Error(err)
				}
			}(i)
		}
		wg.Wait()
		got, ok, err := st.GetMessage(ctx, msg.ID)
		if err != nil || !ok || got.State != "REJECTD" {
			t.Fatalf("final=%+v %v", got, err)
		}
		if err := st.PrepareReceipt(ctx, ReceiptEvent{Provider: "p", ProviderID: "r7", State: "ENROUTE", DoneAt: now.Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
		p, ok, err := st.GetPending(ctx, "p", "r7")
		if err != nil || !ok || p.DLRState != "REJECTD" {
			t.Fatalf("final regressed=%+v %v", p, err)
		}
		if err := st.PrepareReceipt(ctx, ReceiptEvent{Provider: "other", ProviderID: "r7", State: "DELIVRD", DoneAt: now}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("provider isolation: %v", err)
		}
	})
	t.Run("expiry-with-missing-fragment", func(t *testing.T) {
		msg := message.New("expiry-message", message.DirectionMT, "brand", "8613800138000", "long")
		if err := st.SaveMessage(ctx, msg); err != nil {
			t.Fatal(err)
		}
		if err := st.SavePending(ctx, Pending{Provider: "p", ProviderID: "expiry-1", GatewayID: msg.ID, SegmentIndex: 1, SegmentCount: 2, ReliabilityManaged: true, SourceKind: "http", ExpiresAt: now.Add(-time.Second)}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.SweepExpiredPending(ctx, now); err != nil {
			t.Fatal(err)
		}
		got, ok, err := st.GetMessage(ctx, msg.ID)
		if err != nil || !ok || got.State != "EXPIRED" {
			t.Fatalf("expiry=%+v %v", got, err)
		}
		rows, err := st.ListPendingByGatewayID(ctx, msg.ID)
		if err != nil || len(rows) != 0 {
			t.Fatalf("expired rows=%+v %v", rows, err)
		}
		// Callback snapshots survive the removal of original mappings.
		found := 0
		for {
			j, ok, err := st.ClaimReceipt(ctx, "delivery", time.Now().Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				break
			}
			var d ReceiptDelivery
			if err = json.Unmarshal(j.Payload, &d); err != nil {
				t.Fatal(err)
			}
			if d.Pending.GatewayID == msg.ID {
				found++
				if !d.Aggregate.Final || d.Aggregate.State != "EXPIRED" {
					t.Fatalf("bad expiry callback %+v", d)
				}
			}
			if err = st.FinishReceipt(ctx, j, time.Time{}, ""); err != nil {
				t.Fatal(err)
			}
		}
		if found != 2 {
			t.Fatalf("expired snapshots=%d want 2", found)
		}
	})
}
