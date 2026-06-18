package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/message"
)

type FileStore struct {
	*MemoryStore
	path string
}

type fileSnapshot struct {
	Messages    []message.Message       `json:"messages"`
	Pending     map[string]Pending      `json:"pending"`
	Outbox      map[int64]OutboxItem    `json:"outbox"`
	NextOutbox  int64                   `json:"next_outbox"`
	GatewaySeq  uint64                  `json:"gateway_seq"`
	Idempotency []fileIdempotencyRecord `json:"idempotency"`
	SavedAt     time.Time               `json:"saved_at"`
}

type fileIdempotencyRecord struct {
	ClientID  string    `json:"client_id"`
	Key       string    `json:"key"`
	GatewayID string    `json:"gateway_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

func NewFile(path string) (*FileStore, error) {
	if path == "" {
		path = "data/mysmpp-store.json"
	}
	st := &FileStore{MemoryStore: NewMemory(), path: path}
	if err := st.load(); err != nil {
		return nil, err
	}
	return st, nil
}

func (s *FileStore) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var snap fileSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = make([]message.Message, len(snap.Messages))
	s.messageByID = make(map[string]int, len(snap.Messages))
	for i, msg := range snap.Messages {
		s.messages[i] = cloneMessage(msg)
		s.messageByID[msg.ID] = i
	}
	s.pending = map[string]Pending{}
	for k, v := range snap.Pending {
		s.pending[k] = v
	}
	s.outbox = map[int64]OutboxItem{}
	for id, item := range snap.Outbox {
		if item.State == "claimed" {
			item.State = "pending"
			item.ClaimedBy = ""
			item.ClaimedAt = time.Time{}
		}
		s.outbox[id] = cloneOutbox(item)
		if id > s.nextOutbox {
			s.nextOutbox = id
		}
	}
	if snap.NextOutbox > s.nextOutbox {
		s.nextOutbox = snap.NextOutbox
	}
	s.gatewaySeq = snap.GatewaySeq
	for _, msg := range s.messages {
		if n, ok := parseGatewaySeq(msg.ID); ok && n > s.gatewaySeq {
			s.gatewaySeq = n
		}
	}
	s.idempotency = map[idempotencyKey]idempotencyRecord{}
	for _, rec := range snap.Idempotency {
		s.idempotency[idempotencyKey{clientID: rec.ClientID, key: rec.Key}] = idempotencyRecord{
			gatewayID: rec.GatewayID,
			expiresAt: rec.ExpiresAt,
		}
	}
	return nil
}

func (s *FileStore) persist() error {
	s.mu.RLock()
	snap := fileSnapshot{
		Messages:    make([]message.Message, len(s.messages)),
		Pending:     make(map[string]Pending, len(s.pending)),
		Outbox:      make(map[int64]OutboxItem, len(s.outbox)),
		NextOutbox:  s.nextOutbox,
		GatewaySeq:  s.gatewaySeq,
		Idempotency: make([]fileIdempotencyRecord, 0, len(s.idempotency)),
		SavedAt:     time.Now().UTC(),
	}
	for i, msg := range s.messages {
		snap.Messages[i] = cloneMessage(msg)
	}
	for k, v := range s.pending {
		snap.Pending[k] = v
	}
	for k, v := range s.outbox {
		snap.Outbox[k] = cloneOutbox(v)
	}
	for key, rec := range s.idempotency {
		snap.Idempotency = append(snap.Idempotency, fileIdempotencyRecord{
			ClientID:  key.clientID,
			Key:       key.key,
			GatewayID: rec.gatewayID,
			ExpiresAt: rec.expiresAt,
		})
	}
	s.mu.RUnlock()

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".store-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if _, err := os.Stat(s.path); err == nil {
		_ = os.Remove(s.path + ".bak")
		if err := os.Rename(s.path, s.path+".bak"); err != nil {
			return err
		}
	}
	return os.Rename(tmp, s.path)
}

func (s *FileStore) SaveMessage(ctx context.Context, msg message.Message) error {
	if err := s.MemoryStore.SaveMessage(ctx, msg); err != nil {
		return err
	}
	return s.persist()
}

func (s *FileStore) UpdateMessageSent(ctx context.Context, gatewayID, providerID string) error {
	if err := s.MemoryStore.UpdateMessageSent(ctx, gatewayID, providerID); err != nil {
		return err
	}
	return s.persist()
}

func (s *FileStore) UpdateMessageState(ctx context.Context, gatewayID, state string, errCode int) error {
	if err := s.MemoryStore.UpdateMessageState(ctx, gatewayID, state, errCode); err != nil {
		return err
	}
	return s.persist()
}

func (s *FileStore) SavePending(ctx context.Context, p Pending) error {
	if err := s.MemoryStore.SavePending(ctx, p); err != nil {
		return err
	}
	return s.persist()
}

func (s *FileStore) MarkDLRReady(ctx context.Context, providerID, state string, errCode int, doneAt time.Time) error {
	if err := s.MemoryStore.MarkDLRReady(ctx, providerID, state, errCode, doneAt); err != nil {
		return err
	}
	return s.persist()
}

func (s *FileStore) DeletePending(ctx context.Context, providerID string) error {
	if err := s.MemoryStore.DeletePending(ctx, providerID); err != nil {
		return err
	}
	return s.persist()
}

func (s *FileStore) SweepExpiredPending(ctx context.Context, before time.Time) (int, error) {
	n, err := s.MemoryStore.SweepExpiredPending(ctx, before)
	if err != nil || n == 0 {
		return n, err
	}
	return n, s.persist()
}

func (s *FileStore) ReserveGatewayIDRange(ctx context.Context, span uint64) (uint64, uint64, error) {
	start, end, err := s.MemoryStore.ReserveGatewayIDRange(ctx, span)
	if err != nil {
		return 0, 0, err
	}
	return start, end, s.persist()
}

func (s *FileStore) EnqueueOutbox(ctx context.Context, item OutboxItem) (int64, error) {
	id, err := s.MemoryStore.EnqueueOutbox(ctx, item)
	if err != nil {
		return 0, err
	}
	return id, s.persist()
}

func (s *FileStore) ClaimOutbox(ctx context.Context, workerID string, limit int) ([]OutboxItem, error) {
	items, err := s.MemoryStore.ClaimOutbox(ctx, workerID, limit)
	if err != nil || len(items) == 0 {
		return items, err
	}
	return items, s.persist()
}

func (s *FileStore) RequeueStaleOutbox(ctx context.Context, before time.Time, limit int) (int, error) {
	n, err := s.MemoryStore.RequeueStaleOutbox(ctx, before, limit)
	if err != nil || n == 0 {
		return n, err
	}
	return n, s.persist()
}

func (s *FileStore) AckOutbox(ctx context.Context, id int64) error {
	if err := s.MemoryStore.AckOutbox(ctx, id); err != nil {
		return err
	}
	return s.persist()
}

func (s *FileStore) FailOutbox(ctx context.Context, id int64, errMsg string, nextRetryAt time.Time) error {
	if err := s.MemoryStore.FailOutbox(ctx, id, errMsg, nextRetryAt); err != nil {
		return err
	}
	return s.persist()
}

func (s *FileStore) SaveIdempotency(ctx context.Context, clientID, key, gatewayID string, ttl time.Duration) error {
	if err := s.MemoryStore.SaveIdempotency(ctx, clientID, key, gatewayID, ttl); err != nil {
		return err
	}
	return s.persist()
}

func (s *FileStore) SubmitAtomic(ctx context.Context, msg message.Message, item OutboxItem, clientID, key string, ttl time.Duration) (int64, string, bool, error) {
	id, gatewayID, duplicate, err := s.MemoryStore.SubmitAtomic(ctx, msg, item, clientID, key, ttl)
	if err != nil || duplicate {
		return id, gatewayID, duplicate, err
	}
	return id, gatewayID, duplicate, s.persist()
}
