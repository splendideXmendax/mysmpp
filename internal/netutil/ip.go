package netutil

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

func RequestIPAllowed(r *http.Request, allowed, trustedProxies []string) bool {
	host := ClientIP(r, trustedProxies)
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	for _, item := range allowed {
		if prefix, err := netip.ParsePrefix(item); err == nil {
			if prefix.Contains(ip) {
				return true
			}
			continue
		}
		if allowedIP, err := netip.ParseAddr(item); err == nil && allowedIP == ip {
			return true
		}
	}
	return false
}

func ClientIP(r *http.Request, trustedProxies []string) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	direct, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	prefixes := ParseTrustedProxyPrefixes(trustedProxies)
	if len(prefixes) == 0 || !AddrInPrefixes(direct, prefixes) {
		return host
	}
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			candidate := strings.TrimSpace(parts[i])
			addr, err := netip.ParseAddr(candidate)
			if err != nil {
				continue
			}
			if !AddrInPrefixes(addr, prefixes) {
				return candidate
			}
		}
		return strings.TrimSpace(parts[0])
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}
	return host
}

func ParseTrustedProxyPrefixes(values []string) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		if prefix, err := netip.ParsePrefix(value); err == nil {
			prefixes = append(prefixes, prefix)
			continue
		}
		if addr, err := netip.ParseAddr(value); err == nil {
			prefixes = append(prefixes, AddrPrefix(addr))
		}
	}
	return prefixes
}

func AddrInPrefixes(addr netip.Addr, prefixes []netip.Prefix) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func AddrPrefix(addr netip.Addr) netip.Prefix {
	bits := 128
	if addr.Is4() {
		bits = 32
	}
	return netip.PrefixFrom(addr, bits)
}
