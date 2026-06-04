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
