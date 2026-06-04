package provider

import (
	"context"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
)

func BuildProviders(ctx context.Context, cfg config.Config) map[string]Provider {
	ruleByName := map[string]config.HTTPRuleConfig{}
	for _, rule := range cfg.Outbound {
		ruleByName[rule.Name] = rule
	}
	out := map[string]Provider{}
	for _, p := range cfg.Providers {
		if !p.Enabled {
			continue
		}
		var built Provider
		switch p.Protocol {
		case "http", "https":
			rule, ok := ruleByName[p.Rule]
			if !ok {
				continue
			}
			built = NewHTTPProvider(p, rule)
		case "mock":
			mock := NewNamedMock(ctx, p.Name)
			mock.DelayMin = 2 * time.Second
			mock.DelayMax = 4 * time.Second
			built = mock
		}
		if built != nil {
			out[p.Name] = NewRateLimitedProvider(built, p.RateLimit)
		}
	}
	if len(out) == 0 {
		mock := NewNamedMock(ctx, "mock")
		mock.DelayMin = 2 * time.Second
		mock.DelayMax = 4 * time.Second
		out["mock"] = mock
	}
	return out
}
