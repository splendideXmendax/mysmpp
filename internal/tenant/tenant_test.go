package tenant

import (
	"testing"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
)

func TestResolverLinksHTTPAndSMPPAccounts(t *testing.T) {
	cfg := config.Config{
		Tenants: []config.TenantConfig{{
			TenantID: "customer-a",
			Limits:   config.TenantLimits{TPS: 10, Burst: 3, DailySegments: 100, Timezone: "Asia/Shanghai"},
		}},
		Clients: []config.ClientAuth{{ClientID: "http-a", TenantID: "customer-a"}},
		ESMEs:   []config.ESMECred{{SystemID: "smpp-a", TenantID: "customer-a"}},
	}
	r := NewResolver(cfg)
	httpIdentity, ok := r.Resolve(ProtocolHTTP, "http-a")
	if !ok {
		t.Fatal("http account was not resolved")
	}
	smppIdentity, ok := r.Resolve(ProtocolSMPP, "smpp-a")
	if !ok {
		t.Fatal("smpp account was not resolved")
	}
	if httpIdentity.TenantID != "customer-a" || smppIdentity.TenantID != httpIdentity.TenantID {
		t.Fatalf("accounts did not resolve to one tenant: http=%+v smpp=%+v", httpIdentity, smppIdentity)
	}
	if httpIdentity.Location.String() != "Asia/Shanghai" || httpIdentity.DailySegments != 100 {
		t.Fatalf("tenant limits were not resolved: %+v", httpIdentity)
	}
}

func TestResolverKeepsLegacyAccountsIndependent(t *testing.T) {
	r := NewResolver(config.Config{})
	httpIdentity, _ := r.Resolve(ProtocolHTTP, "same")
	smppIdentity, _ := r.Resolve(ProtocolSMPP, "same")
	if httpIdentity.TenantID != "http:same" || smppIdentity.TenantID != "smpp:same" {
		t.Fatalf("unexpected fallback identities: http=%+v smpp=%+v", httpIdentity, smppIdentity)
	}
}

func TestTokenBucketRefills(t *testing.T) {
	now := time.Unix(100, 0)
	limiter := newTokenBucket(func() time.Time { return now })
	if !limiter.Allow("a", 2, 2) || !limiter.Allow("a", 2, 2) || limiter.Allow("a", 2, 2) {
		t.Fatal("initial burst was not enforced")
	}
	now = now.Add(500 * time.Millisecond)
	if !limiter.Allow("a", 2, 2) || limiter.Allow("a", 2, 2) {
		t.Fatal("token refill was not enforced")
	}
}
