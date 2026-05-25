package store

import (
	"context"
	"sync"

	"github.com/splendideXmendax/mysmpp/internal/message"
)

type Store interface {
	SaveMessage(context.Context, message.Message) error
	ListMessages(context.Context) ([]message.Message, error)
}

type MemoryStore struct {
	mu       sync.RWMutex
	messages []message.Message
}

func NewMemory() *MemoryStore {
	return &MemoryStore{}
}

func (s *MemoryStore) SaveMessage(_ context.Context, msg message.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, msg)
	return nil
}

func (s *MemoryStore) ListMessages(_ context.Context) ([]message.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]message.Message, len(s.messages))
	copy(out, s.messages)
	return out, nil
}
