package dispatch

import (
	"context"
	"sync"
)

type gatewayIDStore interface {
	ReserveGatewayIDRange(context.Context, uint64) (uint64, uint64, error)
}

type idAllocator struct {
	mu      sync.Mutex
	next    uint64
	end     uint64
	span    uint64
	reserve func(context.Context, uint64) (uint64, uint64, error)
}

func newIDAllocator(span uint64, reserve func(context.Context, uint64) (uint64, uint64, error)) *idAllocator {
	if span == 0 {
		span = 1000
	}
	return &idAllocator{span: span, reserve: reserve}
}

func (a *idAllocator) Next(ctx context.Context) (uint64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.next == 0 || a.next > a.end {
		start, end, err := a.reserve(ctx, a.span)
		if err != nil {
			return 0, err
		}
		a.next = start
		a.end = end
	}
	id := a.next
	a.next++
	return id, nil
}
