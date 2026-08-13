package router

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/message"
)

func TestRouterPrefersLongestPrefixWhenPriorityTies(t *testing.T) {
	r := New([]config.RouteConfig{
		{Name: "short", Prefix: []string{"138"}, Provider: "a", Priority: 10},
		{Name: "long", Prefix: []string{"13800"}, Provider: "b", Priority: 10},
	})
	route, ok := r.Match(message.Message{To: "13800138000"})
	if !ok {
		t.Fatal("expected route")
	}
	if route.Name != "long" {
		t.Fatalf("expected long prefix route, got %s", route.Name)
	}
}

func TestMatchSubmitUsesContentTagsAndFromPrefix(t *testing.T) {
	r := NewWithProviders([]config.RouteConfig{
		{Name: "mkt", Prefix: []string{"138"}, FromPrefix: []string{"1069"}, ContentTags: []string{"marketing"}, Provider: "a", Priority: 10},
		{Name: "default", Prefix: []string{"138"}, Provider: "b", Priority: 1},
	}, []config.ProviderConfig{{Name: "a", Enabled: true}, {Name: "b", Enabled: true}})
	res, ok := r.MatchSubmit(MatchInput{
		GatewayID: "m0000001",
		To:        "13800138000",
		From:      "10690001",
		Tags:      map[string]struct{}{"marketing": {}},
	})
	if !ok || res.Route.Name != "mkt" || res.Provider != "a" {
		t.Fatalf("expected marketing route, got ok=%v res=%+v", ok, res)
	}
	res, ok = r.MatchSubmit(MatchInput{To: "13800138000", From: "10086"})
	if !ok || res.Route.Name != "default" {
		t.Fatalf("expected default route, got ok=%v res=%+v", ok, res)
	}
}

func TestMatchSubmitWeightedUsesGatewayID(t *testing.T) {
	r := NewWithProviders([]config.RouteConfig{{
		Name: "weighted",
		Weighted: []config.WeightedProvider{
			{Provider: "a", Weight: 7},
			{Provider: "b", Weight: 3},
		},
		Priority: 1,
	}}, []config.ProviderConfig{{Name: "a", Enabled: true}, {Name: "b", Enabled: true}})
	first, ok := r.MatchSubmit(MatchInput{GatewayID: "m0000001", To: "13800138000"})
	if !ok {
		t.Fatal("expected route")
	}
	sameID, ok := r.MatchSubmit(MatchInput{GatewayID: "m0000001", To: "13900139000"})
	if !ok {
		t.Fatal("expected route for same gateway id")
	}
	if sameID.Provider != first.Provider {
		t.Fatalf("provider changed for gateway id %q: %q then %q", "m0000001", first.Provider, sameID.Provider)
	}

	counts := map[string]int{}
	for i := 0; i < 1000; i++ {
		gatewayID := fmt.Sprintf("m%07s", strconv.FormatUint(uint64(i+1), 36))
		res, ok := r.MatchSubmit(MatchInput{GatewayID: gatewayID, To: "13800138000"})
		if !ok {
			t.Fatal("expected route")
		}
		counts[res.Provider]++
	}
	if counts["a"] < 650 || counts["a"] > 750 {
		t.Fatalf("expected 7:3 weighted routing to stay near configured ratio, got %v", counts)
	}
}

func TestMatchSubmitWeightedRequiresGatewayID(t *testing.T) {
	r := NewWithProviders([]config.RouteConfig{{
		Name:     "weighted",
		Weighted: []config.WeightedProvider{{Provider: "a", Weight: 1}},
		Priority: 1,
	}}, []config.ProviderConfig{{Name: "a", Enabled: true}})
	if _, ok := r.MatchSubmit(MatchInput{To: "13800138000"}); ok {
		t.Fatal("weighted routing should require gateway id")
	}
}

func TestMatchSubmitWeightedIgnoresDisabledProviders(t *testing.T) {
	r := NewWithProviders([]config.RouteConfig{{
		Name: "weighted",
		Weighted: []config.WeightedProvider{
			{Provider: "disabled", Weight: 99},
			{Provider: "enabled", Weight: 1},
		},
		Priority: 1,
	}}, []config.ProviderConfig{{Name: "disabled", Enabled: false}, {Name: "enabled", Enabled: true}})
	for i := 0; i < 100; i++ {
		gatewayID := fmt.Sprintf("m%07s", strconv.FormatUint(uint64(i+1), 36))
		res, ok := r.MatchSubmit(MatchInput{GatewayID: gatewayID, To: "13800138000"})
		if !ok || res.Provider != "enabled" {
			t.Fatalf("disabled provider selected for %q: ok=%v result=%+v", gatewayID, ok, res)
		}
	}
}

func TestMatchSubmitWeightedRejectsInvalidRuntimeWeight(t *testing.T) {
	r := NewWithProviders([]config.RouteConfig{{
		Name:     "weighted",
		Weighted: []config.WeightedProvider{{Provider: "a", Weight: -1}},
		Priority: 1,
	}}, []config.ProviderConfig{{Name: "a", Enabled: true}})
	if _, ok := r.MatchSubmit(MatchInput{GatewayID: "m0000001", To: "13800138000"}); ok {
		t.Fatal("weighted routing should reject a non-positive runtime weight")
	}
}

func TestMatchSubmitFailoverDoesNotRequireGatewayID(t *testing.T) {
	r := NewWithProviders([]config.RouteConfig{{
		Name:     "failover",
		Failover: []string{"disabled", "enabled"},
		Priority: 1,
	}}, []config.ProviderConfig{{Name: "disabled", Enabled: false}, {Name: "enabled", Enabled: true}})
	res, ok := r.MatchSubmit(MatchInput{To: "13800138000"})
	if !ok || res.Provider != "enabled" || len(res.FailoverChain) != 1 || res.FailoverChain[0] != "enabled" {
		t.Fatalf("unexpected failover result without gateway id: ok=%v result=%+v", ok, res)
	}
}

func TestMatchSubmitTimeWindow(t *testing.T) {
	r := NewWithProviders([]config.RouteConfig{{
		Name:        "work-hours",
		Prefix:      []string{"138"},
		Provider:    "a",
		Priority:    10,
		TimeWindows: []config.TimeWindow{{Days: []string{"mon"}, Start: "09:00", End: "21:00"}},
	}}, []config.ProviderConfig{{Name: "a", Enabled: true}})
	inside := time.Date(2026, 7, 6, 10, 0, 0, 0, time.Local)
	if _, ok := r.MatchSubmit(MatchInput{To: "13800138000", Now: inside}); !ok {
		t.Fatal("expected route inside window")
	}
	outside := time.Date(2026, 7, 6, 22, 0, 0, 0, time.Local)
	if _, ok := r.MatchSubmit(MatchInput{To: "13800138000", Now: outside}); ok {
		t.Fatal("did not expect route outside window")
	}
}

func TestRouterUsesActualMatchedPrefixLength(t *testing.T) {
	r := New([]config.RouteConfig{
		{Name: "route-a", Prefix: []string{"44"}, Provider: "a", Priority: 10},
		{Name: "route-b", Prefix: []string{"4", "1234"}, Provider: "b", Priority: 10},
	})
	route, ok := r.MatchPhone("441234")
	if !ok {
		t.Fatal("expected route")
	}
	if route.Name != "route-a" {
		t.Fatalf("expected route-a, got %s", route.Name)
	}
}

func TestRouterFallsThroughToLowerPriorityMatch(t *testing.T) {
	r := New([]config.RouteConfig{
		{Name: "high", Prefix: []string{"99"}, Provider: "a", Priority: 10},
		{Name: "low", Prefix: []string{"44"}, Provider: "b", Priority: 1},
	})
	route, ok := r.MatchPhone("441234")
	if !ok {
		t.Fatal("expected route")
	}
	if route.Name != "low" {
		t.Fatalf("expected low, got %s", route.Name)
	}
}
