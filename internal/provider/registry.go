package provider

import (
	"sort"
	"sync"

	"github.com/splendideXmendax/mysmpp/internal/smppclient"
)

type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
	onDLR     DLRCallback
}

type SMPPStatusReporter interface {
	SMPPStatus() (smppclient.PoolStatus, bool)
}

func NewRegistry() *Registry {
	return &Registry{providers: map[string]Provider{}}
}

func (r *Registry) Get(name string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	return p, ok
}

func (r *Registry) Replace(next map[string]Provider) {
	r.mu.Lock()
	old := r.providers
	r.providers = next
	cb := r.onDLR
	if cb != nil {
		for _, p := range next {
			p.OnDLR(cb)
		}
	}
	r.mu.Unlock()

	for name, p := range old {
		if kept, ok := next[name]; ok && kept == p {
			continue
		}
		if closer, ok := p.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}
}

func (r *Registry) SetDLRHandler(cb DLRCallback) {
	r.mu.Lock()
	r.onDLR = cb
	for _, p := range r.providers {
		p.OnDLR(cb)
	}
	r.mu.Unlock()
}

func (r *Registry) SMPPStatuses() []smppclient.PoolStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()

	statuses := make([]smppclient.PoolStatus, 0, len(r.providers))
	for _, p := range r.providers {
		reporter, ok := p.(SMPPStatusReporter)
		if !ok {
			continue
		}
		status, ok := reporter.SMPPStatus()
		if ok {
			statuses = append(statuses, status)
		}
	}
	sort.Slice(statuses, func(i, j int) bool {
		return statuses[i].Name < statuses[j].Name
	})
	return statuses
}

func (r *Registry) CloseAll() {
	r.mu.Lock()
	old := r.providers
	r.providers = map[string]Provider{}
	r.mu.Unlock()

	for _, p := range old {
		if closer, ok := p.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}
}
