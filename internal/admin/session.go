package admin

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"sync"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/netutil"
)

const sessionCookieName = "mysmpp_admin_session"

type Session struct {
	Token     string
	CSRFToken string
	Username  string
	CreatedAt time.Time
	ExpiresAt time.Time
}

type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]Session
	ttl      time.Duration
	stop     chan struct{}
}

func NewSessionStore(ttl time.Duration) *SessionStore {
	if ttl <= 0 {
		ttl = 8 * time.Hour
	}
	s := &SessionStore{
		sessions: map[string]Session{},
		ttl:      ttl,
		stop:     make(chan struct{}),
	}
	go s.sweepLoop()
	return s
}

func (s *SessionStore) New(username string) (Session, error) {
	token, err := randomToken()
	if err != nil {
		return Session{}, err
	}
	csrf, err := randomToken()
	if err != nil {
		return Session{}, err
	}
	now := time.Now().UTC()
	session := Session{
		Token:     token,
		CSRFToken: csrf,
		Username:  username,
		CreatedAt: now,
		ExpiresAt: now.Add(s.ttl),
	}
	s.mu.Lock()
	s.sessions[token] = session
	s.mu.Unlock()
	return session, nil
}

func (s *SessionStore) Get(token string) (Session, bool) {
	if token == "" {
		return Session{}, false
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[token]
	if !ok {
		return Session{}, false
	}
	if session.ExpiresAt.Before(now) {
		delete(s.sessions, token)
		return Session{}, false
	}
	return session, true
}

func (s *SessionStore) Delete(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

func (s *SessionStore) Close() {
	close(s.stop)
}

func (s *SessionStore) sweepLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.sweep(time.Now().UTC())
		case <-s.stop:
			return
		}
	}
}

func (s *SessionStore) sweep(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, session := range s.sessions {
		if session.ExpiresAt.Before(now) {
			delete(s.sessions, token)
		}
	}
}

func randomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func setSessionCookie(w http.ResponseWriter, session Session, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    session.Token,
		Path:     "/admin",
		Expires:  session.ExpiresAt,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   secure,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/admin",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func remoteIP(r *http.Request, trustedProxies []string) string {
	return netutil.ClientIP(r, trustedProxies)
}
