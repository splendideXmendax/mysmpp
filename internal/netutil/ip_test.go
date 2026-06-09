package netutil

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientIPUsesForwardedForOnlyFromTrustedProxy(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.10:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.5, 10.0.0.10")

	if got := ClientIP(req, []string{"10.0.0.0/8"}); got != "203.0.113.5" {
		t.Fatalf("expected forwarded client ip, got %q", got)
	}
	if got := ClientIP(req, nil); got != "10.0.0.10" {
		t.Fatalf("untrusted proxy must not be able to spoof client ip, got %q", got)
	}
}

func TestRequestIPAllowedSupportsCIDR(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.10:12345"
	req.Header.Set("X-Real-IP", "203.0.113.9")

	if !RequestIPAllowed(req, []string{"203.0.113.0/24"}, []string{"10.0.0.10/32"}) {
		t.Fatal("expected real ip to match allowed cidr through trusted proxy")
	}
	if RequestIPAllowed(req, []string{"203.0.113.0/24"}, nil) {
		t.Fatal("untrusted proxy must not be allowed")
	}
}
