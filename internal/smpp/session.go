package smpp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
)

type BindMode int32

const (
	BindNone BindMode = iota
	BindRX
	BindTX
	BindTRX
)

const bindTimeout = 30 * time.Second
const writeTimeout = 30 * time.Second

const (
	statusInvalidPassword uint32 = 0x0000000E
	statusInvalidSystemID uint32 = 0x0000000F
)

func (m BindMode) CanSubmit() bool  { return m == BindTX || m == BindTRX }
func (m BindMode) CanReceive() bool { return m == BindRX || m == BindTRX }

type SubmitHandler func(*Session, SubmitSM)
type AuthFunc func(systemID, password string) bool

type SessionConfig struct {
	ID            string
	Logger        *slog.Logger
	OwnSystemID   string
	Auth          AuthFunc
	BindAllowed   func(*Session, string) bool
	OnSubmit      SubmitHandler
	OnClosed      func(*Session)
	EnquirePeriod time.Duration
	WindowSize    int32
}

type Session struct {
	id      string
	conn    net.Conn
	logger  *slog.Logger
	cfg     SessionConfig
	out     chan PDU
	closed  chan struct{}
	closeMu sync.Once

	bindMode atomic.Int32
	inflight atomic.Int32
	nextSeq  atomic.Uint32
	systemID atomic.Value
	lastSeen atomic.Int64
}

func NewSession(conn net.Conn, cfg SessionConfig) *Session {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	id := cfg.ID
	if id == "" {
		id = conn.RemoteAddr().String()
	}
	s := &Session{
		id:     id,
		conn:   conn,
		logger: logger.With("remote", conn.RemoteAddr().String()),
		cfg:    cfg,
		out:    make(chan PDU, 64),
		closed: make(chan struct{}),
	}
	s.systemID.Store("")
	s.nextSeq.Store(0)
	s.lastSeen.Store(time.Now().UnixNano())
	return s
}

func (s *Session) ID() string { return s.id }

func (s *Session) SystemID() string {
	v, _ := s.systemID.Load().(string)
	return v
}

func (s *Session) currentBind() BindMode {
	return BindMode(s.bindMode.Load())
}

func (s *Session) NextSeq() uint32 {
	return s.nextSeq.Add(1)
}

func (s *Session) Send(p PDU) {
	select {
	case s.out <- p:
	case <-s.closed:
	}
}

func (s *Session) Close() {
	s.closeMu.Do(func() {
		close(s.closed)
		_ = s.conn.Close()
	})
}

func (s *Session) Serve(ctx context.Context) {
	defer func() {
		s.Close()
		if s.cfg.OnClosed != nil {
			s.cfg.OnClosed(s)
		}
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = s.conn.SetReadDeadline(time.Now())
			s.Close()
		case <-s.closed:
		}
	}()
	go s.writeLoop()
	if s.cfg.EnquirePeriod > 0 {
		go s.enquireLoop(ctx)
	}
	_ = s.conn.SetReadDeadline(time.Now().Add(bindTimeout))
	s.readLoop(ctx)
}

func (s *Session) writeLoop() {
	for {
		select {
		case p := <-s.out:
			_ = s.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := WritePDU(s.conn, p); err != nil {
				s.logger.Warn("write smpp pdu failed", "err", err)
				s.Close()
				return
			}
			_ = s.conn.SetWriteDeadline(time.Time{})
		case <-s.closed:
			return
		}
	}
}

func (s *Session) readLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		pdu, err := ReadPDU(s.conn)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				s.logger.Warn("read smpp pdu failed", "err", err)
			}
			return
		}
		s.lastSeen.Store(time.Now().UnixNano())
		s.dispatch(pdu)
	}
}

func (s *Session) enquireLoop(ctx context.Context) {
	t := time.NewTicker(s.cfg.EnquirePeriod)
	defer t.Stop()
	timeout := int64(2 * s.cfg.EnquirePeriod)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.closed:
			return
		case <-t.C:
			if s.currentBind() == BindNone {
				continue
			}
			last := s.lastSeen.Load()
			if last > 0 && time.Now().UnixNano()-last > timeout {
				s.logger.Warn("smpp session idle timeout")
				s.Close()
				return
			}
			s.Send(PDU{CommandID: commandEnquireLink, Status: statusOK, SequenceID: s.NextSeq()})
		}
	}
}

func (s *Session) dispatch(pdu PDU) {
	s.logger.Debug("smpp pdu", "command", commandName(pdu.CommandID), "sequence", pdu.SequenceID)
	switch pdu.CommandID {
	case commandBindReceiver, commandBindTransmitter, commandBindTransceiver:
		s.handleBind(pdu)
	case commandSubmitSM:
		if !s.currentBind().CanSubmit() {
			s.Send(PDU{CommandID: commandSubmitSMResp, Status: statusInvalidBind, SequenceID: pdu.SequenceID})
			return
		}
		windowSize := s.cfg.WindowSize
		if windowSize <= 0 {
			windowSize = 16
		}
		if s.inflight.Add(1) > windowSize {
			s.inflight.Add(-1)
			s.Send(PDU{CommandID: commandSubmitSMResp, Status: statusThrottled, SequenceID: pdu.SequenceID})
			return
		}
		msg, err := ParseSubmitSM(pdu)
		if err != nil {
			s.inflight.Add(-1)
			s.logger.Warn("submit_sm parse failed", "err", err)
			s.Send(PDU{CommandID: commandSubmitSMResp, Status: statusInvalidCmd, SequenceID: pdu.SequenceID})
			return
		}
		if s.cfg.OnSubmit != nil {
			s.cfg.OnSubmit(s, msg)
		} else {
			s.inflight.Add(-1)
		}
	case commandDeliverSMResp:
		s.logger.Debug("deliver_sm_resp", "sequence", pdu.SequenceID, "status", pdu.Status)
	case commandEnquireLink:
		s.Send(PDU{CommandID: commandEnquireLinkResp, Status: statusOK, SequenceID: pdu.SequenceID})
	case commandUnbind:
		s.Send(PDU{CommandID: commandUnbindResp, Status: statusOK, SequenceID: pdu.SequenceID})
	case commandEnquireLinkResp, commandUnbindResp:
	default:
		s.Send(PDU{CommandID: commandGenericNack, Status: statusInvalidCmd, SequenceID: pdu.SequenceID})
	}
}

func (s *Session) CompleteSubmit() {
	s.inflight.Add(-1)
}

func (s *Session) handleBind(pdu PDU) {
	offset := 0
	systemID, err := readCStringMax(pdu.Body, &offset, config.SMPPMaxSystemID)
	if err != nil {
		s.Send(PDU{CommandID: bindRespID(pdu.CommandID), Status: statusInvalidSystemID, SequenceID: pdu.SequenceID, Body: CString(s.cfg.OwnSystemID)})
		return
	}
	password, err := readCStringMax(pdu.Body, &offset, config.SMPPMaxPassword)
	if err != nil {
		s.Send(PDU{CommandID: bindRespID(pdu.CommandID), Status: statusInvalidPassword, SequenceID: pdu.SequenceID, Body: CString(s.cfg.OwnSystemID)})
		return
	}
	if _, err := readCStringMax(pdu.Body, &offset, config.SMPPMaxSystemType); err != nil {
		s.Send(PDU{CommandID: bindRespID(pdu.CommandID), Status: statusBindFailed, SequenceID: pdu.SequenceID, Body: CString(s.cfg.OwnSystemID)})
		return
	}

	status := statusOK
	if s.currentBind() != BindNone {
		status = statusAlreadyBound
	} else if s.cfg.Auth != nil && !s.cfg.Auth(systemID, password) {
		status = statusBindFailed
	} else if s.cfg.BindAllowed != nil && !s.cfg.BindAllowed(s, systemID) {
		status = statusBindFailed
	}

	mode := BindTRX
	respID := bindRespID(pdu.CommandID)
	switch pdu.CommandID {
	case commandBindReceiver:
		mode = BindRX
	case commandBindTransmitter:
		mode = BindTX
	}
	if status == statusOK {
		s.bindMode.Store(int32(mode))
		s.systemID.Store(systemID)
		_ = s.conn.SetReadDeadline(time.Time{})
		s.logger.Info("bind ok", "system_id", systemID, "mode", mode)
	} else {
		s.logger.Warn("bind rejected", "system_id", systemID, "status", status)
	}
	s.Send(PDU{CommandID: respID, Status: status, SequenceID: pdu.SequenceID, Body: CString(s.cfg.OwnSystemID)})
}

func bindRespID(commandID uint32) uint32 {
	switch commandID {
	case commandBindReceiver:
		return commandBindReceiverResp
	case commandBindTransmitter:
		return commandBindTransmitterResp
	default:
		return commandBindTransceiverResp
	}
}
