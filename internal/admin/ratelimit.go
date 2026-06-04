package admin

import (
	"sync"
	"time"
)

type loginLimiter struct {
	mu       sync.Mutex
	failures map[string]loginFailure
	limit    int
	window   time.Duration
}

type loginFailure struct {
	start time.Time
	count int
}

func newLoginLimiter(limit int, window time.Duration) *loginLimiter {
	if limit <= 0 {
		limit = 5
	}
	if window <= 0 {
		window = 15 * time.Minute
	}
	return &loginLimiter{
		failures: map[string]loginFailure{},
		limit:    limit,
		window:   window,
	}
}

func (l *loginLimiter) Allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	rec := l.failures[key]
	if rec.start.IsZero() || now.Sub(rec.start) >= l.window {
		delete(l.failures, key)
		return true
	}
	return rec.count < l.limit
}

func (l *loginLimiter) Fail(key string) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	rec := l.failures[key]
	if rec.start.IsZero() || now.Sub(rec.start) >= l.window {
		l.failures[key] = loginFailure{start: now, count: 1}
		return
	}
	rec.count++
	l.failures[key] = rec
}

func (l *loginLimiter) Reset(key string) {
	l.mu.Lock()
	delete(l.failures, key)
	l.mu.Unlock()
}
