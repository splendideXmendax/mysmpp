package store

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/message"
)

type Store interface {
	Ping(context.Context) error
	SaveMessage(context.Context, message.Message) error
	GetMessage(context.Context, string) (message.Message, bool, error)
	UpdateMessageSent(context.Context, string, string) error
	UpdateMessageState(context.Context, string, string, int) error
	ListMessages(context.Context) ([]message.Message, error)
	ListMessagesPage(context.Context, ListOptions) ([]message.Message, error)
	SavePending(context.Context, Pending) error
	GetPending(context.Context, string) (Pending, bool, error)
	MarkDLRReady(context.Context, string, string, int, time.Time) error
	ListReadyDLR(context.Context, string, int) ([]Pending, error)
	DeletePending(context.Context, string) error
	SweepExpiredPending(context.Context, time.Time) (int, error)
	PendingSize(context.Context) (int, error)
	ReserveGatewayIDRange(context.Context, uint64) (uint64, uint64, error)
	EnqueueOutbox(context.Context, OutboxItem) (int64, error)
	ClaimOutbox(context.Context, string, int) ([]OutboxItem, error)
	RequeueStaleOutbox(context.Context, time.Time, int) (int, error)
	AckOutbox(context.Context, int64) error
	FailOutbox(context.Context, int64, string, time.Time) error
	OutboxDepth(context.Context, string) (int, error)
	CheckIdempotency(context.Context, string, string) (string, bool, error)
	SaveIdempotency(context.Context, string, string, string, time.Duration) error
	SubmitAtomic(context.Context, message.Message, OutboxItem, string, string, time.Duration) (int64, error)
}

type ListOptions struct {
	Limit  int
	Offset int
}

type Pending struct {
	ProviderID         string
	GatewayID          string
	SourceKind         string
	SourceSession      string
	SourceSystem       string
	From               string
	To                 string
	Text               string
	DataCoding         uint8
	RegisteredDelivery uint8
	Provider           string
	Route              string
	ReceivedAt         time.Time
	ExpiresAt          time.Time
	DLRReady           bool
	DLRState           string
	DLRErrorCode       int
	DLRDoneAt          time.Time
}

type OutboxPayload struct {
	GatewayID          string            `json:"gateway_id"`
	Provider           string            `json:"provider"`
	Route              string            `json:"route"`
	From               string            `json:"from"`
	To                 string            `json:"to"`
	Text               string            `json:"text"`
	DataCoding         uint8             `json:"data_coding"`
	RegisteredDelivery uint8             `json:"registered_delivery"`
	Encoding           string            `json:"encoding"`
	Meta               map[string]string `json:"meta"`
	SourceKind         string            `json:"source_kind"`
	SourceSession      string            `json:"source_session"`
	SourceSystem       string            `json:"source_system"`
	ReceivedAt         time.Time         `json:"received_at"`
	UDH                []byte            `json:"udh,omitempty"`
}

type OutboxItem struct {
	ID          int64         `json:"id"`
	GatewayID   string        `json:"gateway_id"`
	Provider    string        `json:"provider"`
	Payload     OutboxPayload `json:"payload"`
	State       string        `json:"state"`
	ClaimedBy   string        `json:"claimed_by,omitempty"`
	ClaimedAt   time.Time     `json:"claimed_at,omitempty"`
	NextRetryAt time.Time     `json:"next_retry_at"`
	Attempt     int           `json:"attempt"`
	MaxAttempts int           `json:"max_attempts"`
	LastError   string        `json:"last_error,omitempty"`
	CreatedAt   time.Time     `json:"created_at"`
}

var ErrNotFound = errors.New("not found")

type MemoryStore struct {
	mu          sync.RWMutex
	messages    []message.Message
	messageByID map[string]int
	pending     map[string]Pending
	outbox      map[int64]OutboxItem
	nextOutbox  int64
	idempotency map[idempotencyKey]idempotencyRecord
	gatewaySeq  uint64
	lastSweep   time.Time
	maxMessages int
}

type idempotencyKey struct {
	clientID string
	key      string
}

type idempotencyRecord struct {
	gatewayID string
	expiresAt time.Time
}

func NewMemory() *MemoryStore {
	return &MemoryStore{
		messageByID: map[string]int{},
		pending:     map[string]Pending{},
		outbox:      map[int64]OutboxItem{},
		idempotency: map[idempotencyKey]idempotencyRecord{},
		maxMessages: 10000,
	}
}

func (s *MemoryStore) Ping(context.Context) error {
	return nil
}

func (s *MemoryStore) SaveMessage(_ context.Context, msg message.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg = cloneMessage(msg)
	if idx, ok := s.messageByID[msg.ID]; ok {
		s.messages[idx] = msg
		return nil
	}
	s.messageByID[msg.ID] = len(s.messages)
	s.messages = append(s.messages, msg)
	s.trimMessagesLocked()
	return nil
}

func (s *MemoryStore) GetMessage(_ context.Context, gatewayID string) (message.Message, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, ok := s.messageByID[gatewayID]
	if !ok {
		return message.Message{}, false, nil
	}
	return cloneMessage(s.messages[idx]), true, nil
}

func (s *MemoryStore) UpdateMessageSent(_ context.Context, gatewayID, providerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.messageByID[gatewayID]
	if !ok {
		return ErrNotFound
	}
	msg := s.messages[idx]
	msg.ProviderID = providerID
	msg.State = "sent"
	msg.SentAt = time.Now().UTC()
	s.messages[idx] = msg
	return nil
}

func (s *MemoryStore) UpdateMessageState(_ context.Context, gatewayID, state string, errCode int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.messageByID[gatewayID]
	if !ok {
		return ErrNotFound
	}
	msg := s.messages[idx]
	msg.State = state
	msg.ErrorCode = errCode
	msg.DoneAt = time.Now().UTC()
	s.messages[idx] = msg
	return nil
}

func (s *MemoryStore) ListMessages(_ context.Context) ([]message.Message, error) {
	return s.ListMessagesPage(context.Background(), ListOptions{Limit: len(s.messages)})
}

func (s *MemoryStore) ListMessagesPage(_ context.Context, opts ListOptions) ([]message.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > len(s.messages) {
		offset = len(s.messages)
	}
	limit := opts.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	end := offset + limit
	if end > len(s.messages) {
		end = len(s.messages)
	}
	out := make([]message.Message, end-offset)
	for i, msg := range s.messages[offset:end] {
		out[i] = cloneMessage(msg)
	}
	return out, nil
}

func (s *MemoryStore) SavePending(_ context.Context, p Pending) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(time.Now().UTC())
	s.pending[p.ProviderID] = p
	return nil
}

func (s *MemoryStore) MarkDLRReady(_ context.Context, providerID, state string, errCode int, doneAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[providerID]
	if !ok {
		return ErrNotFound
	}
	p.DLRReady = true
	p.DLRState = state
	p.DLRErrorCode = errCode
	p.DLRDoneAt = doneAt
	s.pending[providerID] = p
	return nil
}

func (s *MemoryStore) ListReadyDLR(_ context.Context, systemID string, limit int) ([]Pending, error) {
	if limit <= 0 {
		limit = 100
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	out := make([]Pending, 0, limit)
	for _, p := range s.pending {
		if !p.DLRReady {
			continue
		}
		if systemID != "" && p.SourceSystem != systemID {
			continue
		}
		out = append(out, p)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *MemoryStore) GetPending(_ context.Context, providerID string) (Pending, bool, error) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	p, ok := s.pending[providerID]
	if ok && !p.ExpiresAt.IsZero() && p.ExpiresAt.Before(now) {
		delete(s.pending, providerID)
		return Pending{}, false, nil
	}
	return p, ok, nil
}

func (s *MemoryStore) DeletePending(_ context.Context, providerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pending, providerID)
	return nil
}

func (s *MemoryStore) SweepExpiredPending(_ context.Context, before time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for id, p := range s.pending {
		if p.ExpiresAt.Before(before) {
			delete(s.pending, id)
			count++
		}
	}
	return count, nil
}

func (s *MemoryStore) PendingSize(_ context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(time.Now().UTC())
	return len(s.pending), nil
}

func (s *MemoryStore) ReserveGatewayIDRange(_ context.Context, span uint64) (uint64, uint64, error) {
	if span == 0 {
		span = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	start := s.gatewaySeq + 1
	s.gatewaySeq += span
	return start, s.gatewaySeq, nil
}

func (s *MemoryStore) EnqueueOutbox(_ context.Context, item OutboxItem) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextOutbox++
	item.ID = s.nextOutbox
	if item.State == "" {
		item.State = "pending"
	}
	if item.NextRetryAt.IsZero() {
		item.NextRetryAt = time.Now().UTC()
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	if item.MaxAttempts <= 0 {
		item.MaxAttempts = 5
	}
	item.Payload.Meta = cloneMap(item.Payload.Meta)
	s.outbox[item.ID] = item
	return item.ID, nil
}

func (s *MemoryStore) ClaimOutbox(_ context.Context, workerID string, limit int) ([]OutboxItem, error) {
	if limit <= 0 {
		limit = 1
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	candidates := make([]OutboxItem, 0, len(s.outbox))
	for _, item := range s.outbox {
		if item.State == "pending" && !item.NextRetryAt.After(now) {
			candidates = append(candidates, item)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].NextRetryAt.Equal(candidates[j].NextRetryAt) {
			return candidates[i].ID < candidates[j].ID
		}
		return candidates[i].NextRetryAt.Before(candidates[j].NextRetryAt)
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	out := make([]OutboxItem, 0, len(candidates))
	for _, item := range candidates {
		item.State = "claimed"
		item.ClaimedBy = workerID
		item.ClaimedAt = now
		item.Attempt++
		s.outbox[item.ID] = item
		out = append(out, cloneOutbox(item))
	}
	return out, nil
}

func (s *MemoryStore) RequeueStaleOutbox(_ context.Context, before time.Time, limit int) (int, error) {
	if limit <= 0 {
		limit = 1000
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for id, item := range s.outbox {
		if item.State != "claimed" || item.ClaimedAt.IsZero() || !item.ClaimedAt.Before(before) {
			continue
		}
		item.State = "pending"
		item.ClaimedBy = ""
		item.ClaimedAt = time.Time{}
		if item.NextRetryAt.IsZero() || item.NextRetryAt.After(before) {
			item.NextRetryAt = before
		}
		s.outbox[id] = item
		count++
		if count >= limit {
			break
		}
	}
	return count, nil
}

func (s *MemoryStore) AckOutbox(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.outbox[id]
	if !ok {
		return ErrNotFound
	}
	item.State = "done"
	s.outbox[id] = item
	return nil
}

func (s *MemoryStore) FailOutbox(_ context.Context, id int64, errMsg string, nextRetryAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.outbox[id]
	if !ok {
		return ErrNotFound
	}
	item.LastError = errMsg
	if nextRetryAt.IsZero() || item.Attempt >= item.MaxAttempts {
		item.State = "failed"
	} else {
		item.State = "pending"
		item.NextRetryAt = nextRetryAt
		item.ClaimedBy = ""
		item.ClaimedAt = time.Time{}
	}
	s.outbox[id] = item
	return nil
}

func (s *MemoryStore) OutboxDepth(_ context.Context, state string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, item := range s.outbox {
		if state == "" || item.State == state {
			count++
		}
	}
	return count, nil
}

func (s *MemoryStore) CheckIdempotency(_ context.Context, clientID, key string) (string, bool, error) {
	if clientID == "" || key == "" {
		return "", false, nil
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	k := idempotencyKey{clientID: clientID, key: key}
	rec, ok := s.idempotency[k]
	if !ok {
		return "", false, nil
	}
	if rec.expiresAt.Before(now) {
		delete(s.idempotency, k)
		return "", false, nil
	}
	return rec.gatewayID, true, nil
}

func (s *MemoryStore) SaveIdempotency(_ context.Context, clientID, key, gatewayID string, ttl time.Duration) error {
	if clientID == "" || key == "" {
		return nil
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(time.Now().UTC())
	s.idempotency[idempotencyKey{clientID: clientID, key: key}] = idempotencyRecord{
		gatewayID: gatewayID,
		expiresAt: time.Now().UTC().Add(ttl),
	}
	return nil
}

func (s *MemoryStore) SubmitAtomic(_ context.Context, msg message.Message, item OutboxItem, clientID, key string, ttl time.Duration) (int64, error) {
	now := time.Now().UTC()
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)

	msg = cloneMessage(msg)
	if idx, ok := s.messageByID[msg.ID]; ok {
		s.messages[idx] = msg
	} else {
		s.messageByID[msg.ID] = len(s.messages)
		s.messages = append(s.messages, msg)
		s.trimMessagesLocked()
	}

	s.nextOutbox++
	item.ID = s.nextOutbox
	if item.State == "" {
		item.State = "pending"
	}
	if item.NextRetryAt.IsZero() {
		item.NextRetryAt = now
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	if item.MaxAttempts <= 0 {
		item.MaxAttempts = 5
	}
	item.Payload.Meta = cloneMap(item.Payload.Meta)
	s.outbox[item.ID] = item

	if clientID != "" && key != "" {
		s.idempotency[idempotencyKey{clientID: clientID, key: key}] = idempotencyRecord{
			gatewayID: msg.ID,
			expiresAt: now.Add(ttl),
		}
	}
	return item.ID, nil
}

func (s *MemoryStore) sweepLocked(now time.Time) {
	for id, p := range s.pending {
		if !p.ExpiresAt.IsZero() && p.ExpiresAt.Before(now) {
			delete(s.pending, id)
		}
	}
	for key, rec := range s.idempotency {
		if !rec.expiresAt.IsZero() && rec.expiresAt.Before(now) {
			delete(s.idempotency, key)
		}
	}
	s.lastSweep = now
}

func (s *MemoryStore) trimMessagesLocked() {
	if s.maxMessages <= 0 || len(s.messages) <= s.maxMessages {
		return
	}
	drop := len(s.messages) - s.maxMessages
	s.messages = append([]message.Message(nil), s.messages[drop:]...)
	s.messageByID = make(map[string]int, len(s.messages))
	for i, msg := range s.messages {
		s.messageByID[msg.ID] = i
	}
}

func cloneMessage(msg message.Message) message.Message {
	msg.Metadata = cloneMap(msg.Metadata)
	if msg.Segments != nil {
		msg.Segments = append([]message.Segment(nil), msg.Segments...)
	}
	return msg
}

func cloneOutbox(item OutboxItem) OutboxItem {
	item.Payload.Meta = cloneMap(item.Payload.Meta)
	return item
}

func cloneMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
