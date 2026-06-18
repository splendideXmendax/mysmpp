package smpp

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
)

type Server struct {
	mu              sync.RWMutex
	cfg             config.SMPPConfig
	logger          *slog.Logger
	auth            AuthFunc
	onSubmit        SubmitHandler
	onReceiverBound func(string)
	listener        net.Listener
	sessions        map[string]*Session
	wg              sync.WaitGroup
	nextID          atomic.Uint64
}

func NewServer(cfg config.SMPPConfig, logger *slog.Logger, auth AuthFunc, onSubmit SubmitHandler) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		cfg:      cfg,
		logger:   logger,
		auth:     auth,
		onSubmit: onSubmit,
		sessions: map[string]*Session{},
	}
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()
	s.logger.Info("smpp listening", "addr", s.cfg.Addr)

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				s.wg.Wait()
				return nil
			}
			return err
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetKeepAlive(true)
			_ = tcp.SetKeepAlivePeriod(defaultEnquirePeriod)
		}
		enquirePeriod := effectiveEnquirePeriod(s.cfg.EnquirePeriod)
		session := NewSession(conn, SessionConfig{
			ID:              s.nextSessionID(),
			Logger:          s.logger,
			OwnSystemID:     s.cfg.SystemID,
			Auth:            s.auth,
			BindAllowed:     s.bindAllowed,
			OnSubmit:        s.onSubmit,
			OnClosed:        s.unregister,
			OnReceiverBound: s.notifyReceiverBound,
			EnquirePeriod:   enquirePeriod,
			WindowSize:      int32(s.cfg.WindowSize),
		})
		if !s.register(session) {
			s.logger.Warn("max sessions reached", "remote", conn.RemoteAddr(), "limit", s.cfg.MaxSessions)
			_ = conn.Close()
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			session.Serve(ctx)
		}()
	}
}

func (s *Server) Addr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

func (s *Server) Session(id string) (*Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[id]
	return session, ok
}

func (s *Server) SetReceiverBoundHandler(cb func(string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onReceiverBound = cb
}

func (s *Server) receiverBoundHandler() func(string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.onReceiverBound
}

func (s *Server) notifyReceiverBound(systemID string) {
	if cb := s.receiverBoundHandler(); cb != nil {
		cb(systemID)
	}
}

func (s *Server) ReceiversBySystemID(systemID string) []*Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Session, 0)
	for _, session := range s.sessions {
		if session.SystemID() == systemID && session.currentBind().CanReceive() {
			out = append(out, session)
		}
	}
	return out
}

func (s *Server) bindAllowed(candidate *Session, systemID string) bool {
	limit := s.cfg.MaxSessionsPerSystemID
	if limit <= 0 {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, session := range s.sessions {
		if session == candidate {
			continue
		}
		if session.SystemID() == systemID {
			count++
		}
	}
	return count < limit
}

func (s *Server) register(session *Session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.MaxSessions > 0 && len(s.sessions) >= s.cfg.MaxSessions {
		return false
	}
	s.sessions[session.ID()] = session
	return true
}

func (s *Server) unregister(session *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, session.ID())
}

func (s *Server) nextSessionID() string {
	return "s" + strconv.FormatUint(s.nextID.Add(1), 10)
}

const defaultEnquirePeriod = 30 * time.Second

func effectiveEnquirePeriod(raw string) time.Duration {
	period, err := time.ParseDuration(raw)
	if err != nil || period <= 0 {
		return defaultEnquirePeriod
	}
	return period
}
