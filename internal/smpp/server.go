package smpp

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"

	"github.com/splendideXmendax/mysmpp/internal/config"
)

type Server struct {
	mu       sync.RWMutex
	cfg      config.SMPPConfig
	logger   *slog.Logger
	auth     AuthFunc
	onSubmit SubmitHandler
	listener net.Listener
	sessions map[string]*Session
	wg       sync.WaitGroup
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
		if s.maxSessionsReached() {
			s.logger.Warn("max sessions reached", "remote", conn.RemoteAddr(), "limit", s.cfg.MaxSessions)
			_ = conn.Close()
			continue
		}
		session := NewSession(conn, SessionConfig{
			Logger:      s.logger,
			OwnSystemID: s.cfg.SystemID,
			Auth:        s.auth,
			OnSubmit:    s.onSubmit,
			OnClosed:    s.unregister,
		})
		s.register(session)
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

func (s *Server) register(session *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[session.ID()] = session
}

func (s *Server) unregister(session *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, session.ID())
}

func (s *Server) maxSessionsReached() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.MaxSessions > 0 && len(s.sessions) >= s.cfg.MaxSessions
}
