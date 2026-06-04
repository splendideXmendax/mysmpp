package provider

import (
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

type MockProvider struct {
	mu sync.Mutex
	cb DLRCallback

	DelayMin time.Duration
	DelayMax time.Duration
	FailRate float64
	seq      atomic.Uint64
}

func NewMock() *MockProvider {
	return &MockProvider{
		DelayMin: time.Second,
		DelayMax: 5 * time.Second,
	}
}

func (m *MockProvider) OnDLR(cb DLRCallback) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cb = cb
}

func (m *MockProvider) Send(msg OutboundMessage) (string, error) {
	n := m.seq.Add(1)
	providerID := fmt.Sprintf("p%016x", n)
	if msg.RegisteredDelivery&0x03 != 0 {
		go m.scheduleDLR(providerID)
	}
	return providerID, nil
}

func (m *MockProvider) scheduleDLR(providerID string) {
	delay := m.DelayMin
	if m.DelayMax > m.DelayMin {
		delay += time.Duration(rand.Int63n(int64(m.DelayMax - m.DelayMin)))
	}
	time.Sleep(delay)

	state := "DELIVRD"
	errCode := 0
	if m.FailRate > 0 && rand.Float64() < m.FailRate {
		state = "UNDELIV"
		errCode = 1
	}

	m.mu.Lock()
	cb := m.cb
	m.mu.Unlock()
	if cb == nil {
		return
	}
	cb(DLR{
		ProviderID: providerID,
		State:      state,
		ErrorCode:  errCode,
		DoneAt:     time.Now().UTC(),
	})
}
