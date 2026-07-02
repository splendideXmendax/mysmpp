package smppclient

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

type Pool struct {
	cfg     Config
	ctx     context.Context
	cancel  context.CancelFunc
	conns   []*connection
	next    atomic.Uint64
	onDLRMu sync.RWMutex
	onDLR   DLRCallback
}

func NewPool(parent context.Context, cfg Config) *Pool {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	count := cfg.SMPP.Binds
	if count <= 0 {
		count = 1
	}
	p := &Pool{cfg: cfg, ctx: ctx, cancel: cancel}
	for i := 0; i < count; i++ {
		conn := newConnection(i+1, cfg, p.handleDLR)
		p.conns = append(p.conns, conn)
	}
	for _, conn := range p.conns {
		conn.start(ctx)
	}
	return p
}

func (p *Pool) Send(ctx context.Context, msg Message) (string, error) {
	ids, err := p.SendAll(ctx, msg)
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", nil
	}
	return ids[0], nil
}

func (p *Pool) SendAll(ctx context.Context, msg Message) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	parts := BuildSubmitSM(msg, p.cfg.SMPP)
	if len(parts) == 0 {
		return nil, errors.New("empty smpp submit")
	}
	ids := make([]string, 0, len(parts))
	for _, part := range parts {
		conn, ok := p.pick()
		if !ok {
			return ids, errors.New("no bound smpp upstream connection")
		}
		id, err := conn.submit(ctx, part.Body)
		if err != nil {
			return ids, err
		}
		id = NormalizeID(id, p.cfg.SMPP.MessageIDRespFormat)
		ids = append(ids, id)
	}
	return ids, nil
}

func (p *Pool) OnDLR(cb DLRCallback) {
	p.onDLRMu.Lock()
	p.onDLR = cb
	p.onDLRMu.Unlock()
}

func (p *Pool) Close() error {
	p.cancel()
	for _, conn := range p.conns {
		conn.close()
	}
	return nil
}

func (p *Pool) Status() PoolStatus {
	status := PoolStatus{Name: p.cfg.Name, Endpoint: p.cfg.Endpoint}
	for _, conn := range p.conns {
		status.Connections = append(status.Connections, conn.status())
	}
	return status
}

func (p *Pool) pick() (*connection, bool) {
	if len(p.conns) == 0 {
		return nil, false
	}
	start := int(p.next.Add(1) % uint64(len(p.conns)))
	for i := 0; i < len(p.conns); i++ {
		conn := p.conns[(start+i)%len(p.conns)]
		if conn.bound.Load() {
			return conn, true
		}
	}
	return nil, false
}

func (p *Pool) handleDLR(dlr DLR) {
	p.onDLRMu.RLock()
	cb := p.onDLR
	p.onDLRMu.RUnlock()
	if cb != nil {
		cb(dlr)
	}
}
