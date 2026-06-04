package provider

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

type MockProvider struct {
	name   string
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu sync.Mutex
	cb DLRCallback

	DelayMin time.Duration
	DelayMax time.Duration
	FailRate float64
	seq      atomic.Uint64
}

func NewMock() *MockProvider {
	return NewNamedMock(context.Background(), "mock")
}

func NewNamedMock(parent context.Context, name string) *MockProvider {
	if parent == nil {
		parent = context.Background()
	}
	if name == "" {
		name = "mock"
	}
	ctx, cancel := context.WithCancel(parent)
	return &MockProvider{
		name:     name,
		ctx:      ctx,
		cancel:   cancel,
		DelayMin: time.Second,
		DelayMax: 5 * time.Second,
	}
}

func (m *MockProvider) Name() string { return m.name }

func (m *MockProvider) OnDLR(cb DLRCallback) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cb = cb
}

func (m *MockProvider) Send(msg OutboundMessage) (string, error) {
	n := m.seq.Add(1)
	providerID := fmt.Sprintf("p%016x", n)
	if msg.RegisteredDelivery&0x03 != 0 {
		m.wg.Add(1)
		go m.scheduleDLR(providerID)
	}
	return providerID, nil
}

func (m *MockProvider) scheduleDLR(providerID string) {
	defer m.wg.Done()
	delay := m.DelayMin
	if m.DelayMax > m.DelayMin {
		delay += time.Duration(rand.Int63n(int64(m.DelayMax - m.DelayMin)))
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-m.ctx.Done():
		return
	}

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
		Provider:   m.name,
		ProviderID: providerID,
		State:      state,
		ErrorCode:  errCode,
		DoneAt:     time.Now().UTC(),
	})
}

func (m *MockProvider) Close() error {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
	return nil
}
