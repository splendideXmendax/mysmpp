package router

import (
	"testing"

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
