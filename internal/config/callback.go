package config

import (
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// CallbackForwarding is an operator-owned exact mapping for a tenant's HTTP
// receiver reachable through a private address. It never matches a URL prefix.
type CallbackForwarding struct {
	TenantID    string `json:"tenant_id"`
	CallbackURL string `json:"callback_url"`
	TargetURL   string `json:"target_url"`
}

// Validate deliberately excludes HTTPS downgrade, dynamic targets, credentials,
// query strings, loopback and metadata/link-local destinations.
func (r CallbackForwarding) Validate() error {
	if r.TenantID == "" || strings.TrimSpace(r.TenantID) != r.TenantID {
		return fmt.Errorf("callback_forwarding.tenant_id is required")
	}
	for _, raw := range []string{r.CallbackURL, r.TargetURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "http" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") || u.Path == "" || u.RawPath != "" {
			return fmt.Errorf("callback_forwarding URLs must be complete HTTP URLs with a path and without credentials, query or fragment")
		}
		if port := u.Port(); port != "" {
			n, err := strconv.Atoi(port)
			if err != nil || n < 1 || n > 65535 {
				return fmt.Errorf("callback_forwarding URL port must be between 1 and 65535")
			}
		}
	}
	target, _ := url.Parse(r.TargetURL)
	ip, err := netip.ParseAddr(target.Hostname())
	if err != nil || ip.Zone() != "" || !ip.Unmap().IsPrivate() || target.Port() == "" {
		return fmt.Errorf("callback_forwarding.target_url requires a literal private IP and explicit port")
	}
	return nil
}
