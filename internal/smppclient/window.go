package smppclient

import (
	"errors"
	"sync"

	"github.com/splendideXmendax/mysmpp/internal/smpp"
)

type window struct {
	mu      sync.Mutex
	pending map[uint32]chan result
	slots   chan struct{}
}

type result struct {
	pdu smpp.PDU
	err error
}

func newWindow(size int) *window {
	if size <= 0 {
		size = 1
	}
	return &window{pending: map[uint32]chan result{}, slots: make(chan struct{}, size)}
}

func (w *window) acquire() bool {
	select {
	case w.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (w *window) register(seq uint32) chan result {
	ch := make(chan result, 1)
	w.mu.Lock()
	w.pending[seq] = ch
	w.mu.Unlock()
	return ch
}

func (w *window) complete(seq uint32, pdu smpp.PDU) bool {
	w.mu.Lock()
	ch, ok := w.pending[seq]
	if ok {
		delete(w.pending, seq)
	}
	w.mu.Unlock()
	if !ok {
		return false
	}
	ch <- result{pdu: pdu}
	<-w.slots
	return true
}

func (w *window) fail(seq uint32, err error) {
	w.mu.Lock()
	ch, ok := w.pending[seq]
	if ok {
		delete(w.pending, seq)
	}
	w.mu.Unlock()
	if !ok {
		return
	}
	ch <- result{err: err}
	<-w.slots
}

func (w *window) failAll(err error) {
	w.mu.Lock()
	pending := w.pending
	w.pending = map[uint32]chan result{}
	w.mu.Unlock()
	for _, ch := range pending {
		ch <- result{err: err}
		<-w.slots
	}
}

func (w *window) inFlight() int {
	return len(w.slots)
}

var errWindowFull = errors.New("smpp upstream window full")
