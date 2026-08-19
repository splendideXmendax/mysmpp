package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/message"
)

func TestMemoryStorePendingUsesProviderCompositeKey(t *testing.T) {
	st := NewMemory()
	for _, provider := range []string{"provider-a", "provider-b"} {
		if err := st.SavePending(context.Background(), Pending{
			Provider: provider, ProviderID: "same-id", GatewayID: "g-" + provider,
			ExpiresAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	first, ok, err := st.GetPending(context.Background(), "provider-a", "same-id")
	if err != nil || !ok || first.GatewayID != "g-provider-a" {
		t.Fatalf("provider-a lookup failed: ok=%v pending=%+v err=%v", ok, first, err)
	}
	second, ok, err := st.GetPending(context.Background(), "provider-b", "same-id")
	if err != nil || !ok || second.GatewayID != "g-provider-b" {
		t.Fatalf("provider-b lookup failed: ok=%v pending=%+v err=%v", ok, second, err)
	}
}

func TestMemoryStoreDailyQuotaIsExactUnderConcurrency(t *testing.T) {
	st := NewMemory()
	const attempts = 100
	const limit = 25
	var accepted int
	var rejected int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("g-%03d", i)
			msg := message.New(id, message.DirectionMT, "1069", "13800138000", "hello")
			_, _, _, err := st.SubmitAtomic(context.Background(), msg, OutboxItem{
				GatewayID: id, Provider: "mock", Payload: OutboxPayload{GatewayID: id, Provider: "mock"},
			}, SubmitOptions{Quota: &DailyQuotaDebit{TenantID: "tenant-a", Date: "2026-08-19", Segments: 1, Limit: limit}})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				accepted++
			case errors.Is(err, ErrQuotaExceeded):
				rejected++
			default:
				t.Errorf("unexpected submit error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if accepted != limit || rejected != attempts-limit {
		t.Fatalf("quota boundary violated: accepted=%d rejected=%d", accepted, rejected)
	}
	if got := st.quotaUsage[quotaKey{tenantID: "tenant-a", date: "2026-08-19"}]; got != limit {
		t.Fatalf("used segments = %d, want %d", got, limit)
	}
}

func TestMemoryStoreIdempotentDuplicateDoesNotDebitQuota(t *testing.T) {
	st := NewMemory()
	opts := SubmitOptions{
		Idempotency: IdempotencyOptions{ClientID: "client-a", Key: "order-1", TTL: time.Hour},
		Quota:       &DailyQuotaDebit{TenantID: "tenant-a", Date: "2026-08-19", Segments: 1, Limit: 1},
	}
	msg := message.New("g1", message.DirectionMT, "1069", "13800138000", "hello")
	item := OutboxItem{GatewayID: "g1", Provider: "mock", Payload: OutboxPayload{GatewayID: "g1", Provider: "mock"}}
	if _, _, duplicate, err := st.SubmitAtomic(context.Background(), msg, item, opts); err != nil || duplicate {
		t.Fatalf("first submit failed: duplicate=%v err=%v", duplicate, err)
	}
	msg.ID = "g2"
	item.GatewayID = "g2"
	item.Payload.GatewayID = "g2"
	if _, gatewayID, duplicate, err := st.SubmitAtomic(context.Background(), msg, item, opts); err != nil || !duplicate || gatewayID != "g1" {
		t.Fatalf("duplicate did not return original: gateway=%q duplicate=%v err=%v", gatewayID, duplicate, err)
	}
	opts.Idempotency.Key = "order-2"
	if _, _, _, err := st.SubmitAtomic(context.Background(), msg, item, opts); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("new work should exceed quota, got %v", err)
	}
}

func TestFileStorePersistsQuotaUsage(t *testing.T) {
	path := t.TempDir() + "/store.json"
	st, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	msg := message.New("g1", message.DirectionMT, "1069", "13800138000", "hello")
	opts := SubmitOptions{Quota: &DailyQuotaDebit{TenantID: "tenant-a", Date: "2026-08-19", Segments: 1, Limit: 1}}
	if _, _, _, err := st.SubmitAtomic(context.Background(), msg, OutboxItem{
		GatewayID: "g1", Provider: "mock", Payload: OutboxPayload{GatewayID: "g1", Provider: "mock"},
	}, opts); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	msg.ID = "g2"
	if _, _, _, err := reopened.SubmitAtomic(context.Background(), msg, OutboxItem{
		GatewayID: "g2", Provider: "mock", Payload: OutboxPayload{GatewayID: "g2", Provider: "mock"},
	}, opts); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("persisted quota was not enforced after reopen: %v", err)
	}
}

func TestFileStoreRebuildsLegacyPendingKeys(t *testing.T) {
	path := t.TempDir() + "/store.json"
	snapshot := fmt.Sprintf(`{"pending":{"legacy-provider-id":{"ProviderID":"same-id","GatewayID":"g1","Provider":"mock-a","ExpiresAt":%q}}}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(path, []byte(snapshot), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pending, ok, err := st.GetPending(context.Background(), "mock-a", "same-id")
	if err != nil || !ok || pending.GatewayID != "g1" {
		t.Fatalf("legacy pending key was not rebuilt: ok=%v pending=%+v err=%v", ok, pending, err)
	}
}

func TestMemoryStoreSweepsExpiredPending(t *testing.T) {
	st := NewMemory()
	if err := st.SavePending(context.Background(), Pending{
		ProviderID: "p1",
		GatewayID:  "g1",
		ExpiresAt:  time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := st.GetPending(context.Background(), "", "p1"); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("expected expired pending record to be removed")
	}
	if got, err := st.PendingSize(context.Background()); err != nil {
		t.Fatal(err)
	} else if got != 0 {
		t.Fatalf("expected no pending records, got %d", got)
	}
}

func TestMemoryStoreKeepsReadyDLRWhenExpired(t *testing.T) {
	st := NewMemory()
	if err := st.SavePending(context.Background(), Pending{
		ProviderID: "p1",
		GatewayID:  "g1",
		ExpiresAt:  time.Now().Add(-time.Minute),
		DLRReady:   true,
		DLRState:   "DELIVRD",
	}); err != nil {
		t.Fatal(err)
	}

	n, err := st.SweepExpiredPending(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected ready DLR to be kept, swept %d", n)
	}
	if _, ok, err := st.GetPending(context.Background(), "", "p1"); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("expected ready DLR to remain pending")
	}
}

func TestMemoryStoreSweepsExpiredIdempotency(t *testing.T) {
	st := NewMemory()
	if err := st.SaveIdempotency(context.Background(), "client-a", "key-a", "g1", time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)

	if _, ok, err := st.CheckIdempotency(context.Background(), "client-a", "key-a"); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("expected expired idempotency record to be removed")
	}
}

func TestMemoryStoreTrimsOldMessages(t *testing.T) {
	st := NewMemory()
	st.maxMessages = 2
	for _, id := range []string{"g1", "g2", "g3"} {
		if err := st.SaveMessage(context.Background(), message.New(id, message.DirectionMT, "1069", "13800138000", "hello")); err != nil {
			t.Fatal(err)
		}
	}

	messages, err := st.ListMessages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].ID != "g2" || messages[1].ID != "g3" {
		t.Fatalf("expected oldest message to be trimmed, got %+v", messages)
	}
	if _, ok, err := st.GetMessage(context.Background(), "g1"); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("trimmed message should not be addressable by id")
	}
}

func TestMemoryStoreListMessagesPageFiltersBeforePagination(t *testing.T) {
	st := NewMemory()
	for _, tc := range []struct {
		id       string
		clientID string
	}{
		{id: "a-1", clientID: "client-a"},
		{id: "b-1", clientID: "client-b"},
		{id: "a-2", clientID: "client-a"},
	} {
		msg := message.New(tc.id, message.DirectionMT, "1069", "13800138000", "hello")
		msg.Metadata["client_id"] = tc.clientID
		if err := st.SaveMessage(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
	}

	messages, err := st.ListMessagesPage(context.Background(), ListOptions{
		Limit:    1,
		Offset:   1,
		ClientID: "client-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].ID != "a-2" {
		t.Fatalf("expected second client-a message, got %+v", messages)
	}
	all, err := st.ListMessagesPage(context.Background(), ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("expected unfiltered admin-style query to return all messages, got %+v", all)
	}
}

func TestMemoryStoreSubmitAtomicStoresMessageOutboxAndIdempotency(t *testing.T) {
	st := NewMemory()
	msg := message.New("g1", message.DirectionMT, "1069", "13800138000", "hello")
	msg.State = "queued"
	id, gatewayID, duplicate, err := st.SubmitAtomic(context.Background(), msg, OutboxItem{
		GatewayID: "g1",
		Provider:  "mock",
		Payload: OutboxPayload{
			GatewayID:     "g1",
			Provider:      "mock",
			From:          "1069",
			To:            "13800138000",
			Text:          "hello",
			RawPayload:    []byte{0x1b, 0x65},
			RawPayloadSet: true,
			SARRefNum:     []byte{0x12, 0x34}, SARTotalSegments: []byte{0x02}, SARSegmentSeqnum: []byte{0x01}, SARSet: true,
		},
	}, SubmitOptions{Idempotency: IdempotencyOptions{ClientID: "client-a", Key: "m1", TTL: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 || gatewayID != "g1" || duplicate {
		t.Fatalf("unexpected submit result: id=%d gateway=%q duplicate=%v", id, gatewayID, duplicate)
	}
	if _, ok, err := st.GetMessage(context.Background(), "g1"); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("message was not saved")
	}
	if depth, err := st.OutboxDepth(context.Background(), "pending"); err != nil {
		t.Fatal(err)
	} else if depth != 1 {
		t.Fatalf("expected one pending outbox item, got %d", depth)
	}
	items, err := st.ClaimOutbox(context.Background(), "raw-check", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !items[0].Payload.RawPayloadSet || string(items[0].Payload.RawPayload) != string([]byte{0x1b, 0x65}) {
		t.Fatalf("raw payload not preserved: %+v", items)
	}
	items[0].Payload.RawPayload[0] = 0xff
	stored := st.outbox[id]
	if stored.Payload.RawPayload[0] != 0x1b {
		t.Fatal("claimed raw payload aliases stored outbox payload")
	}
	if err := st.AckOutbox(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	stored = st.outbox[id]
	if stored.Payload.UDH != nil || stored.Payload.RawPayloadSet || stored.Payload.RawPayload != nil || stored.Payload.SARSet || stored.Payload.SARRefNum != nil {
		t.Fatalf("acked outbox retained raw transport payload: %+v", stored.Payload)
	}
	if got, ok, err := st.CheckIdempotency(context.Background(), "client-a", "m1"); err != nil {
		t.Fatal(err)
	} else if !ok || got != "g1" {
		t.Fatalf("idempotency not saved: ok=%v gateway=%q", ok, got)
	}

	second := message.New("g2", message.DirectionMT, "1069", "13800138000", "hello")
	_, gatewayID, duplicate, err = st.SubmitAtomic(context.Background(), second, OutboxItem{
		GatewayID: "g2",
		Provider:  "mock",
		Payload:   OutboxPayload{GatewayID: "g2", Provider: "mock", To: "13800138000", Text: "hello"},
	}, SubmitOptions{Idempotency: IdempotencyOptions{ClientID: "client-a", Key: "m1", TTL: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate || gatewayID != "g1" {
		t.Fatalf("expected duplicate to return original gateway id, got duplicate=%v gateway=%q", duplicate, gatewayID)
	}
	if depth, err := st.OutboxDepth(context.Background(), ""); err != nil {
		t.Fatal(err)
	} else if depth != 1 {
		t.Fatalf("duplicate submit should not create another outbox row, got %d", depth)
	}
}

func TestMemoryStoreRequeuesStaleOutbox(t *testing.T) {
	st := NewMemory()
	id, err := st.EnqueueOutbox(context.Background(), OutboxItem{
		GatewayID: "g1",
		Provider:  "mock",
		Payload: OutboxPayload{
			GatewayID: "g1",
			Provider:  "mock",
			To:        "8613800138000",
			Text:      "hello",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err := st.ClaimOutbox(context.Background(), "worker-a", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != id {
		t.Fatalf("unexpected claimed items: %+v", items)
	}
	count, err := st.RequeueStaleOutbox(context.Background(), time.Now().Add(time.Second), 10)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one requeued item, got %d", count)
	}
	items, err = st.ClaimOutbox(context.Background(), "worker-b", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ClaimedBy != "worker-b" {
		t.Fatalf("expected requeued item to be claimable, got %+v", items)
	}
}

func TestMemoryStoreDoesNotRequeueSendingOrUncertainOutbox(t *testing.T) {
	st := NewMemory()
	msg := message.New("g1", message.DirectionMT, "1069", "13800138000", "hello")
	if err := st.SaveMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	id, err := st.EnqueueOutbox(context.Background(), OutboxItem{
		GatewayID: "g1", Provider: "mock", Payload: OutboxPayload{GatewayID: "g1", Provider: "mock"},
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err := st.ClaimOutbox(context.Background(), "worker-a", 1)
	if err != nil || len(items) != 1 {
		t.Fatalf("claim failed: items=%+v err=%v", items, err)
	}
	if err := st.MarkOutboxSending(context.Background(), id, "worker-a"); err != nil {
		t.Fatal(err)
	}
	if n, err := st.RequeueStaleOutbox(context.Background(), time.Now().Add(time.Hour), 10); err != nil || n != 0 {
		t.Fatalf("sending outbox was requeued: count=%d err=%v", n, err)
	}
	if err := st.FailOutbox(context.Background(), id, "ambiguous", time.Now().Add(time.Second)); err == nil {
		t.Fatal("sending outbox accepted a retry transition")
	}
	if err := st.MarkOutboxUncertain(context.Background(), id, "worker-a", "ambiguous"); err != nil {
		t.Fatal(err)
	}
	if n, err := st.RequeueStaleOutbox(context.Background(), time.Now().Add(time.Hour), 10); err != nil || n != 0 {
		t.Fatalf("uncertain outbox was requeued: count=%d err=%v", n, err)
	}
	if depth, _ := st.OutboxDepth(context.Background(), "uncertain"); depth != 1 {
		t.Fatalf("uncertain depth=%d want=1", depth)
	}
	stored, ok, err := st.GetMessage(context.Background(), "g1")
	if err != nil || !ok || stored.State != "UNKNOWN" {
		t.Fatalf("unexpected message after uncertain transition: ok=%v msg=%+v err=%v", ok, stored, err)
	}
}

func TestMemoryStoreCompletesOutboxAndPendingAtomically(t *testing.T) {
	st := NewMemory()
	msg := message.New("g1", message.DirectionMT, "1069", "13800138000", "hello")
	if err := st.SaveMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	id, err := st.EnqueueOutbox(context.Background(), OutboxItem{
		GatewayID: "g1", Provider: "mock", Payload: OutboxPayload{GatewayID: "g1", Provider: "mock", RawPayload: []byte("secret"), RawPayloadSet: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if items, err := st.ClaimOutbox(context.Background(), "worker-a", 1); err != nil || len(items) != 1 {
		t.Fatalf("claim failed: items=%+v err=%v", items, err)
	}
	if err := st.MarkOutboxSending(context.Background(), id, "worker-a"); err != nil {
		t.Fatal(err)
	}
	pending := []Pending{
		{Provider: "mock", ProviderID: "p1", GatewayID: "g1", SegmentIndex: 1, SegmentCount: 2, ExpiresAt: time.Now().Add(time.Hour)},
		{Provider: "mock", ProviderID: "p2", GatewayID: "g1", SegmentIndex: 2, SegmentCount: 2, ExpiresAt: time.Now().Add(time.Hour)},
	}
	if err := st.CompleteOutboxSend(context.Background(), id, "worker-a", pending); err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteOutboxSend(context.Background(), id, "worker-a", pending); err != nil {
		t.Fatalf("completion retry must be idempotent: %v", err)
	}
	stored, ok, err := st.GetMessage(context.Background(), "g1")
	if err != nil || !ok || stored.State != "sent" || stored.ProviderID != "p1" {
		t.Fatalf("unexpected completed message: ok=%v msg=%+v err=%v", ok, stored, err)
	}
	if rows, err := st.ListPendingByGatewayID(context.Background(), "g1"); err != nil || len(rows) != 2 {
		t.Fatalf("pending rows=%+v err=%v", rows, err)
	}
	if depth, _ := st.OutboxDepth(context.Background(), "done"); depth != 1 {
		t.Fatalf("done depth=%d want=1", depth)
	}
	if st.outbox[id].Payload.RawPayload != nil || st.outbox[id].Payload.RawPayloadSet {
		t.Fatal("completed outbox retained raw payload")
	}
}

func TestFileStoreKeepsSendingOutboxFailClosedAfterRestart(t *testing.T) {
	path := t.TempDir() + "/store.json"
	st, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveMessage(context.Background(), message.New("g1", message.DirectionMT, "1069", "13800138000", "hello")); err != nil {
		t.Fatal(err)
	}
	id, err := st.EnqueueOutbox(context.Background(), OutboxItem{GatewayID: "g1", Provider: "mock", Payload: OutboxPayload{GatewayID: "g1", Provider: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	if items, err := st.ClaimOutbox(context.Background(), "worker-a", 1); err != nil || len(items) != 1 {
		t.Fatalf("claim failed: items=%+v err=%v", items, err)
	}
	if err := st.MarkOutboxSending(context.Background(), id, "worker-a"); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if depth, _ := reopened.OutboxDepth(context.Background(), "sending"); depth != 1 {
		t.Fatalf("sending depth after restart=%d want=1", depth)
	}
	if n, err := reopened.RequeueStaleOutbox(context.Background(), time.Now().Add(time.Hour), 10); err != nil || n != 0 {
		t.Fatalf("sending outbox was recovered after restart: count=%d err=%v", n, err)
	}
	if items, err := reopened.ClaimOutbox(context.Background(), "worker-b", 1); err != nil || len(items) != 0 {
		t.Fatalf("sending outbox became claimable after restart: items=%+v err=%v", items, err)
	}
}

func TestFileStorePersistsMessagesOutboxPendingAndIdempotency(t *testing.T) {
	path := t.TempDir() + "/store.json"
	st, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	msg := message.New("g1", message.DirectionMT, "1069", "13800138000", "hello")
	if err := st.SaveMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueOutbox(context.Background(), OutboxItem{
		GatewayID: "g1",
		Provider:  "mock",
		Payload: OutboxPayload{
			GatewayID:        "g1",
			Provider:         "mock",
			From:             "1069",
			To:               "13800138000",
			Text:             "hello",
			ReceivedAt:       time.Now().UTC(),
			RawPayload:       []byte{0x1b, 0x65},
			RawPayloadSet:    true,
			SARRefNum:        []byte{0x12, 0x34},
			SARTotalSegments: []byte{0x02},
			SARSegmentSeqnum: []byte{0x01},
			SARSet:           true,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SavePending(context.Background(), Pending{
		ProviderID: "p1",
		GatewayID:  "g1",
		Provider:   "mock",
		ExpiresAt:  time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveIdempotency(context.Background(), "client", "key", "g1", time.Hour); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok, err := reopened.GetMessage(context.Background(), "g1"); err != nil {
		t.Fatal(err)
	} else if !ok || got.Text != "hello" {
		t.Fatalf("message was not persisted: ok=%v msg=%+v", ok, got)
	}
	if _, ok, err := reopened.GetPending(context.Background(), "mock", "p1"); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("pending record was not persisted")
	}
	if id, ok, err := reopened.CheckIdempotency(context.Background(), "client", "key"); err != nil {
		t.Fatal(err)
	} else if !ok || id != "g1" {
		t.Fatalf("idempotency was not persisted: ok=%v id=%q", ok, id)
	}
	if depth, err := reopened.OutboxDepth(context.Background(), "pending"); err != nil {
		t.Fatal(err)
	} else if depth != 1 {
		t.Fatalf("outbox was not persisted, depth=%d", depth)
	}
	items, err := reopened.ClaimOutbox(context.Background(), "raw-check", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !items[0].Payload.RawPayloadSet || string(items[0].Payload.RawPayload) != string([]byte{0x1b, 0x65}) {
		t.Fatalf("raw payload was not persisted: %+v", items)
	}
	if !items[0].Payload.SARSet || string(items[0].Payload.SARRefNum) != string([]byte{0x12, 0x34}) || string(items[0].Payload.SARTotalSegments) != "\x02" || string(items[0].Payload.SARSegmentSeqnum) != "\x01" {
		t.Fatalf("SAR metadata was not persisted: %+v", items[0].Payload)
	}
	if err := reopened.AckOutbox(context.Background(), items[0].ID); err != nil {
		t.Fatal(err)
	}
	reopenedAgain, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	acked := reopenedAgain.outbox[items[0].ID]
	if acked.Payload.UDH != nil || acked.Payload.RawPayloadSet || acked.Payload.RawPayload != nil || acked.Payload.SARSet || acked.Payload.SARRefNum != nil {
		t.Fatalf("file store retained raw transport payload after ack: %+v", acked.Payload)
	}
}

func TestFileStoreRecoversSequenceFromLegacyAndCurrentIDs(t *testing.T) {
	path := t.TempDir() + "/store.json"
	st, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"g000000019328", "m0000ewx"} {
		if err := st.SaveMessage(context.Background(), message.New(id, message.DirectionMT, "1069", "8613800138000", "hello")); err != nil {
			t.Fatal(err)
		}
	}

	reopened, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	start, _, err := reopened.ReserveGatewayIDRange(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if start <= 19329 {
		t.Fatalf("recovered sequence did not advance beyond current id: %d", start)
	}
}

// TestFileStoreConcurrentPersistNoErrorOrCorruption drives many concurrent
// mutating ops (each triggers persist) to exercise the snapshot+write+rename
// path. Before persistMu serialized it, concurrent renames could make an op
// return a spurious error or leave a lost/older snapshot on disk. This asserts
// every op succeeds and the final file is valid, complete JSON.
func TestFileStoreConcurrentPersistNoErrorOrCorruption(t *testing.T) {
	path := t.TempDir() + "/store.json"
	st, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const writers, perWriter = 8, 40
	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				id := fmt.Sprintf("g-%d-%d", w, i)
				msg := message.New(id, message.DirectionMT, "1069", "13800138000", "hi")
				if err := st.SaveMessage(context.Background(), msg); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent SaveMessage failed: %v", err)
	}
	// Final on-disk snapshot must be valid, complete JSON with all records.
	reopened, err := NewFile(path)
	if err != nil {
		t.Fatalf("reopen persisted store failed (possible corruption): %v", err)
	}
	msgs, err := reopened.ListMessages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != writers*perWriter {
		t.Fatalf("persisted message count = %d, want %d", len(msgs), writers*perWriter)
	}
}
