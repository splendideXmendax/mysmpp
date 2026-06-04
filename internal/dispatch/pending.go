package dispatch

import (
	"sync"
	"time"
)

type pendingRecord struct {
	GatewayID          string
	ProviderID         string
	Source             SubmitSource
	ReceivedAt         time.Time
	From               string
	To                 string
	Text               string
	DataCoding         uint8
	RegisteredDelivery uint8
	Provider           string
	Route              string
	expiresAt          time.Time
}

type pendingMap struct {
	mu      sync.Mutex
	entries map[string]pendingRecord
	ttl     time.Duration
	stop    chan struct{}
	once    sync.Once
}

func newPendingMap(ttl time.Duration) *pendingMap {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	m := &pendingMap{
		entries: map[string]pendingRecord{},
		ttl:     ttl,
		stop:    make(chan struct{}),
	}
	go m.sweepLoop()
	return m
}

func (m *pendingMap) Put(providerID string, rec pendingRecord) {
	rec.ProviderID = providerID
	rec.expiresAt = time.Now().Add(m.ttl)
	m.mu.Lock()
	m.entries[providerID] = rec
	m.mu.Unlock()
}

func (m *pendingMap) Get(providerID string) (pendingRecord, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.entries[providerID]
	return rec, ok
}

func (m *pendingMap) Complete(providerID string) {
	m.mu.Lock()
	delete(m.entries, providerID)
	m.mu.Unlock()
}

func (m *pendingMap) Size() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

func (m *pendingMap) Close() {
	m.once.Do(func() { close(m.stop) })
}

func (m *pendingMap) sweepLoop() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case now := <-t.C:
			m.mu.Lock()
			for id, rec := range m.entries {
				if rec.expiresAt.Before(now) {
					delete(m.entries, id)
				}
			}
			m.mu.Unlock()
		}
	}
}
