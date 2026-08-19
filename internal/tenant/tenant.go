package tenant

import (
	"sync"
	"time"
	_ "time/tzdata"

	"github.com/splendideXmendax/mysmpp/internal/config"
)

const (
	ProtocolHTTP = "http"
	ProtocolSMPP = "smpp"
)

type Identity struct {
	TenantID      string
	AccountID     string
	Enabled       bool
	TPS           int
	Burst         int
	DailySegments int
	Location      *time.Location
}

type Resolver interface {
	Resolve(protocol, accountID string) (Identity, bool)
}

type StaticResolver struct {
	http map[string]Identity
	smpp map[string]Identity
}

func NewResolver(cfg config.Config) *StaticResolver {
	tenants := make(map[string]config.TenantConfig, len(cfg.Tenants))
	for _, item := range cfg.Tenants {
		tenants[item.TenantID] = item
	}
	r := &StaticResolver{
		http: make(map[string]Identity, len(cfg.Clients)),
		smpp: make(map[string]Identity, len(cfg.ESMEs)),
	}
	for _, client := range cfg.Clients {
		r.http[client.ClientID] = identityFor(ProtocolHTTP, client.ClientID, client.TenantID, tenants)
	}
	for _, esme := range cfg.ESMEs {
		r.smpp[esme.SystemID] = identityFor(ProtocolSMPP, esme.SystemID, esme.TenantID, tenants)
	}
	return r
}

func (r *StaticResolver) Resolve(protocol, accountID string) (Identity, bool) {
	if r == nil || accountID == "" {
		return Identity{}, false
	}
	var accounts map[string]Identity
	switch protocol {
	case ProtocolHTTP:
		accounts = r.http
	case ProtocolSMPP:
		accounts = r.smpp
	default:
		return Identity{}, false
	}
	if identity, ok := accounts[accountID]; ok {
		return identity, true
	}
	return fallbackIdentity(protocol, accountID), true
}

func identityFor(protocol, accountID, tenantID string, tenants map[string]config.TenantConfig) Identity {
	if tenantID == "" {
		return fallbackIdentity(protocol, accountID)
	}
	cfg, ok := tenants[tenantID]
	if !ok {
		return Identity{TenantID: tenantID, AccountID: accountID, Enabled: false, Location: time.UTC}
	}
	location := time.UTC
	if cfg.Limits.Timezone != "" {
		if parsed, err := time.LoadLocation(cfg.Limits.Timezone); err == nil {
			location = parsed
		}
	}
	burst := cfg.Limits.Burst
	if cfg.Limits.TPS > 0 && burst == 0 {
		burst = cfg.Limits.TPS
	}
	return Identity{
		TenantID:      tenantID,
		AccountID:     accountID,
		Enabled:       cfg.EnabledValue(),
		TPS:           cfg.Limits.TPS,
		Burst:         burst,
		DailySegments: cfg.Limits.DailySegments,
		Location:      location,
	}
}

func fallbackIdentity(protocol, accountID string) Identity {
	return Identity{
		TenantID:  protocol + ":" + accountID,
		AccountID: accountID,
		Enabled:   true,
		Location:  time.UTC,
	}
}

type RateLimiter interface {
	Allow(tenantID string, tps, burst int) bool
}

type bucket struct {
	tokens float64
	last   time.Time
	tps    int
	burst  int
}

type TokenBucket struct {
	mu      sync.Mutex
	buckets map[string]bucket
	now     func() time.Time
}

func NewTokenBucket() *TokenBucket {
	return newTokenBucket(time.Now)
}

func newTokenBucket(now func() time.Time) *TokenBucket {
	return &TokenBucket{buckets: map[string]bucket{}, now: now}
}

func (l *TokenBucket) Allow(tenantID string, tps, burst int) bool {
	if tenantID == "" || tps <= 0 {
		return true
	}
	if burst <= 0 {
		burst = tps
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[tenantID]
	if !ok || b.tps != tps || b.burst != burst {
		b = bucket{tokens: float64(burst), last: now, tps: tps, burst: burst}
	} else {
		elapsed := now.Sub(b.last).Seconds()
		if elapsed > 0 {
			b.tokens += elapsed * float64(tps)
			if b.tokens > float64(burst) {
				b.tokens = float64(burst)
			}
			b.last = now
		}
	}
	allowed := b.tokens >= 1
	if allowed {
		b.tokens--
	}
	l.buckets[tenantID] = b
	return allowed
}
