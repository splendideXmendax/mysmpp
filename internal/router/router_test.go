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
