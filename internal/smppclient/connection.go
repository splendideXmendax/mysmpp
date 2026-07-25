package smppclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/smpp"
)

type connection struct {
	id       int
	cfg      Config
	win      *window
	closed   chan struct{}
	closeMu  sync.Once
	connMu   sync.Mutex
	conn     net.Conn
	outMu    sync.RWMutex
	out      chan smpp.PDU
	onDLR    func(DLR)
	seq      atomic.Uint32
	bound    atomic.Bool
	state    atomic.Value
	lastErr  atomic.Value
	lastIn   atomic.Int64
	okCount  atomic.Uint64
	errCount atomic.Uint64
	dlrCount atomic.Uint64
}

func newConnection(id int, cfg Config, onDLR func(DLR)) *connection {
	c := &connection{
		id:     id,
		cfg:    cfg,
		win:    newWindow(cfg.SMPP.WindowSize),
		closed: make(chan struct{}),
		onDLR:  onDLR,
	}
	c.state.Store("idle")
	c.lastErr.Store("")
	return c
}

func (c *connection) start(ctx context.Context) {
	go c.loop(ctx)
}

func (c *connection) close() {
	c.closeMu.Do(func() {
		close(c.closed)
		c.closeConn()
	})
}

func (c *connection) loop(ctx context.Context) {
	minBackoff, _ := time.ParseDuration(c.cfg.SMPP.ReconnectMin)
	maxBackoff, _ := time.ParseDuration(c.cfg.SMPP.ReconnectMax)
	if minBackoff <= 0 {
		minBackoff = time.Second
	}
	if maxBackoff < minBackoff {
		maxBackoff = minBackoff
	}
	backoff := minBackoff
	for {
		select {
		case <-ctx.Done():
			c.close()
			return
		case <-c.closed:
			return
		default:
		}
		if err := c.connectAndServe(ctx); err != nil {
			c.setError(err)
		}
		c.bound.Store(false)
		c.closeConn()
		c.win.failAll(errors.New("smpp upstream connection closed"))
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			c.close()
			return
		case <-c.closed:
			return
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (c *connection) connectAndServe(ctx context.Context) error {
	c.state.Store("connecting")
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", c.cfg.Endpoint)
	if err != nil {
		return err
	}
	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()

	c.state.Store("open")
	if err := c.bind(conn); err != nil {
		return err
	}
	c.bound.Store(true)
	c.state.Store("bound")
	c.lastIn.Store(time.Now().UnixNano())

	attemptDone := make(chan struct{})
	out := make(chan smpp.PDU, 128)
	c.setOut(out)
	defer func() {
		close(attemptDone)
		c.clearOut(out)
	}()

	errCh := make(chan error, 2)
	go c.writeLoop(conn, out, attemptDone, errCh)
	go c.readLoop(conn, attemptDone, errCh)
	go c.enquireLoop(ctx, attemptDone)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return nil
	}
}

func (c *connection) bind(conn net.Conn) error {
	seq := c.nextSeq()
	if err := smpp.WritePDU(conn, smpp.PDU{
		CommandID:  smpp.CommandBindTransceiver,
		SequenceID: seq,
		Body:       bindBody(c.cfg.SystemID, c.cfg.Password, c.cfg.SMPP.SystemType),
	}); err != nil {
		return err
	}
	_ = conn.SetReadDeadline(time.Now().Add(responseTimeout(c.cfg)))
	resp, err := smpp.ReadPDU(conn)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		return err
	}
	if resp.CommandID != smpp.CommandBindTransceiverResp {
		return fmt.Errorf("unexpected bind response command 0x%08x", resp.CommandID)
	}
	if resp.Status != smpp.StatusOK {
		return fmt.Errorf("bind failed status=0x%08x", resp.Status)
	}
	return nil
}

func bindBody(systemID, password, systemType string) []byte {
	body := []byte{}
	body = append(body, smpp.CString(systemID)...)
	body = append(body, smpp.CString(password)...)
	body = append(body, smpp.CString(systemType)...)
	body = append(body, 0x34, 0x00, 0x00)
	body = append(body, smpp.CString("")...)
	return body
}

func (c *connection) writeLoop(conn net.Conn, out <-chan smpp.PDU, done <-chan struct{}, errCh chan<- error) {
	for {
		select {
		case p := <-out:
			if err := smpp.WritePDU(conn, p); err != nil {
				errCh <- err
				return
			}
		case <-done:
			return
		case <-c.closed:
			return
		}
	}
}

func (c *connection) readLoop(conn net.Conn, done <-chan struct{}, errCh chan<- error) {
	for {
		pdu, err := smpp.ReadPDU(conn)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				errCh <- err
			} else {
				errCh <- errors.New("smpp upstream closed")
			}
			return
		}
		c.lastIn.Store(time.Now().UnixNano())
		switch pdu.CommandID {
		case smpp.CommandSubmitSMResp:
			c.win.complete(pdu.SequenceID, pdu)
		case smpp.CommandDeliverSM:
			c.dlrCount.Add(1)
			c.send(smpp.PDU{CommandID: smpp.CommandDeliverSMResp, Status: smpp.StatusOK, SequenceID: pdu.SequenceID, Body: smpp.CString("")})
			dlr, ok, isReceipt := ParseDeliverSM(pdu.Body, c.cfg.SMPP.DLRIDSource, c.cfg.SMPP.MessageIDDLRFormat)
			if !isReceipt {
				slog.Warn("smpp upstream deliver_sm MO ignored", "provider", c.cfg.Name, "connection", c.id)
				c.setError(errors.New("smpp upstream deliver_sm MO ignored"))
				continue
			}
			if ok && c.onDLR != nil {
				c.onDLR(dlr)
			}
		case smpp.CommandEnquireLink:
			c.send(smpp.PDU{CommandID: smpp.CommandEnquireLinkResp, Status: smpp.StatusOK, SequenceID: pdu.SequenceID})
		case smpp.CommandEnquireLinkResp:
		case smpp.CommandUnbind:
			c.send(smpp.PDU{CommandID: smpp.CommandUnbindResp, Status: smpp.StatusOK, SequenceID: pdu.SequenceID})
			errCh <- errors.New("smpp upstream requested unbind")
			return
		default:
			c.send(smpp.PDU{CommandID: smpp.CommandGenericNack, Status: smpp.StatusInvalidCmd, SequenceID: pdu.SequenceID})
		}
		select {
		case <-done:
			return
		default:
		}
	}
}

func (c *connection) enquireLoop(ctx context.Context, done <-chan struct{}) {
	period, _ := time.ParseDuration(c.cfg.SMPP.EnquirePeriod)
	if period <= 0 {
		return
	}
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	timeout := int64(2 * period)
	for {
		select {
		case <-ticker.C:
			if !c.bound.Load() {
				continue
			}
			last := c.lastIn.Load()
			if last > 0 && time.Now().UnixNano()-last > timeout {
				c.setError(errors.New("smpp upstream idle timeout"))
				c.closeConn()
				return
			}
			c.send(smpp.PDU{CommandID: smpp.CommandEnquireLink, Status: smpp.StatusOK, SequenceID: c.nextSeq()})
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-c.closed:
			return
		}
	}
}

func (c *connection) submit(ctx context.Context, body []byte) (string, error) {
	if !c.bound.Load() {
		return "", errors.New("smpp upstream not bound")
	}
	if !c.win.acquire() {
		return "", errWindowFull
	}
	seq := c.nextSeq()
	waiter := c.win.register(seq)
	if !c.send(smpp.PDU{CommandID: smpp.CommandSubmitSM, Status: smpp.StatusOK, SequenceID: seq, Body: body}) {
		err := errors.New("smpp upstream connection not ready")
		c.win.fail(seq, err)
		c.errCount.Add(1)
		return "", err
	}
	timeout := responseTimeout(c.cfg)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-waiter:
		if res.err != nil {
			c.errCount.Add(1)
			return "", res.err
		}
		if res.pdu.Status != smpp.StatusOK {
			c.errCount.Add(1)
			err := SubmitStatusError{Status: res.pdu.Status}
			if permanentStatus(res.pdu.Status) {
				return "", PermanentError{Err: err}
			}
			return "", err
		}
		c.okCount.Add(1)
		return readCString(res.pdu.Body), nil
	case <-timer.C:
		c.win.fail(seq, TimeoutError{Duration: timeout})
		c.errCount.Add(1)
		if c.cfg.SMPP.RetryOnTimeout {
			return "", TimeoutError{Duration: timeout}
		}
		return "", PermanentError{Err: TimeoutError{Duration: timeout}}
	case <-ctx.Done():
		c.win.fail(seq, ctx.Err())
		c.errCount.Add(1)
		return "", ctx.Err()
	}
}

func permanentStatus(status uint32) bool {
	switch status {
	case smpp.StatusInvalidSrcAddr, smpp.StatusInvalidDestAddr, smpp.StatusInvalidCmd, smpp.StatusInvalidBind:
		return true
	default:
		return false
	}
}

func (c *connection) send(p smpp.PDU) bool {
	c.outMu.RLock()
	out := c.out
	c.outMu.RUnlock()
	if out == nil {
		return false
	}
	select {
	case out <- p:
		return true
	case <-c.closed:
		return false
	}
}

func (c *connection) setOut(out chan smpp.PDU) {
	c.outMu.Lock()
	c.out = out
	c.outMu.Unlock()
}

func (c *connection) clearOut(out chan smpp.PDU) {
	c.outMu.Lock()
	if c.out == out {
		c.out = nil
	}
	c.outMu.Unlock()
}

func (c *connection) nextSeq() uint32 {
	next := c.seq.Add(1)
	if next == 0 {
		next = c.seq.Add(1)
	}
	return next
}

func (c *connection) closeConn() {
	c.connMu.Lock()
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	c.connMu.Unlock()
}

func (c *connection) setError(err error) {
	if err != nil {
		c.lastErr.Store(err.Error())
	}
}

func (c *connection) status() ConnectionStatus {
	last := time.Unix(0, c.lastIn.Load()).UTC()
	if c.lastIn.Load() == 0 {
		last = time.Time{}
	}
	state, _ := c.state.Load().(string)
	lastErr, _ := c.lastErr.Load().(string)
	return ConnectionStatus{
		ID:             c.id,
		State:          state,
		Bound:          c.bound.Load(),
		InFlight:       c.win.inFlight(),
		WindowSize:     cap(c.win.slots),
		LastInbound:    last,
		LastError:      lastErr,
		SubmitOK:       c.okCount.Load(),
		SubmitFailed:   c.errCount.Load(),
		DeliverSMCount: c.dlrCount.Load(),
	}
}

func responseTimeout(cfg Config) time.Duration {
	timeout := time.Duration(cfg.SMPP.ResponseTimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return timeout
}

func readCString(body []byte) string {
	for i, b := range body {
		if b == 0x00 {
			return string(body[:i])
		}
	}
	return string(body)
}
