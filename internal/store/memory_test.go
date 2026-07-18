package store

import (
	"context"
	"testing"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/message"
)

func TestMemoryStoreSweepsExpiredPending(t *testing.T) {
	st := NewMemory()
	if err := st.SavePending(context.Background(), Pending{
		ProviderID: "p1",
		GatewayID:  "g1",
		ExpiresAt:  time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := st.GetPending(context.Background(), "p1"); err != nil {
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
	if _, ok, err := st.GetPending(context.Background(), "p1"); err != nil {
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

func TestMemoryStoreSubmitAtomicStoresMessageOutboxAndIdempotency(t *testing.T) {
	st := NewMemory()
	msg := message.New("g1", message.DirectionMT, "1069", "13800138000", "hello")
	msg.State = "queued"
	id, gatewayID, duplicate, err := st.SubmitAtomic(context.Background(), msg, OutboxItem{
		GatewayID: "g1",
		Provider:  "mock",
		Payload: OutboxPayload{
			GatewayID: "g1",
			Provider:  "mock",
			From:      "1069",
			To:        "13800138000",
			Text:      "hello",
		},
	}, "client-a", "m1", time.Hour)
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
	}, "client-a", "m1", time.Hour)
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
			GatewayID:  "g1",
			Provider:   "mock",
			From:       "1069",
			To:         "13800138000",
			Text:       "hello",
			ReceivedAt: time.Now().UTC(),
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
	if _, ok, err := reopened.GetPending(context.Background(), "p1"); err != nil {
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
