package dispatch

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
)

type callbackForwardKey struct{ tenant, url string }
type callbackForward struct {
	target string
	client *http.Client
}
type callbackForwardTable struct {
	routes map[callbackForwardKey]callbackForward
}

// ReloadCallbacks atomically replaces operator-approved exact mappings. Queued
// receipts keep their original URL and use the current mapping on each attempt.
func (d *Dispatcher) ReloadCallbacks(cfg config.DispatcherConfig) {
	next := &callbackForwardTable{routes: make(map[callbackForwardKey]callbackForward)}
	for _, rule := range cfg.CallbackForwarding {
		if err := rule.Validate(); err != nil {
			d.logger.Error("invalid callback forwarding rule ignored", "err", err)
			continue // fail closed even when constructed without Config.Validate
		}
		target, _ := url.Parse(rule.TargetURL)
		address := target.Host
		client := newCallbackClient()
		transport := client.Transport.(*http.Transport)
		transport.DialContext = func(ctx context.Context, network, requested string) (net.Conn, error) {
			if requested != address {
				return nil, errUnsafeCallback
			}
			// Target is a validated literal private IP: no DNS lookup, proxy,
			// customer-supplied destination or fallback to another address.
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, address)
		}
		next.routes[callbackForwardKey{rule.TenantID, rule.CallbackURL}] = callbackForward{rule.TargetURL, client}
	}
	if old := d.callbackForwards.Swap(next); old != nil {
		old.closeIdle()
	}
}

func (t *callbackForwardTable) closeIdle() {
	for _, route := range t.routes {
		route.client.CloseIdleConnections()
	}
}
