package provider

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
)

type RateLimitedProvider struct {
	inner   Provider
	tokens  chan struct{}
	timeout time.Duration
	stop    chan struct{}
	once    sync.Once
}

func NewRateLimitedProvider(inner Provider, cfg config.ProviderRateLimit) Provider {
	if inner == nil || cfg.TPS <= 0 {
		return inner
	}
	burst := cfg.Burst
	if burst <= 0 {
		burst = cfg.TPS
	}
	timeout := time.Duration(cfg.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	p := &RateLimitedProvider{
		inner:   inner,
		tokens:  make(chan struct{}, burst),
		timeout: timeout,
		stop:    make(chan struct{}),
	}
	for i := 0; i < burst; i++ {
		p.tokens <- struct{}{}
	}
	go p.refill(cfg.TPS)
	return p
}

func (p *RateLimitedProvider) Send(msg OutboundMessage) (string, error) {
	ctx := msg.Context
	if ctx == nil {
		ctx = context.Background()
	}
	waitCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	select {
	case <-waitCtx.Done():
		return "", fmt.Errorf("rate limited: %w", waitCtx.Err())
	case <-p.tokens:
	}
	return p.inner.Send(msg)
}

func (p *RateLimitedProvider) OnDLR(cb DLRCallback) {
	p.inner.OnDLR(cb)
}

func (p *RateLimitedProvider) Close() error {
	p.once.Do(func() {
		close(p.stop)
	})
	if closer, ok := p.inner.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func (p *RateLimitedProvider) refill(tps int) {
	interval := time.Second / time.Duration(tps)
	if interval <= 0 {
		interval = time.Nanosecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			select {
			case p.tokens <- struct{}{}:
			default:
			}
		case <-p.stop:
			return
		}
	}
}
