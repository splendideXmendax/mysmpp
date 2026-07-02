package router

import (
	"sort"
	"strings"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/message"
)

type Router struct {
	routes []config.RouteConfig
}

func New(routes []config.RouteConfig) *Router {
	cp := make([]config.RouteConfig, len(routes))
	copy(cp, routes)
	sortRoutes(cp)
	return &Router{routes: cp}
}

func NewWithProviders(routes []config.RouteConfig, providers []config.ProviderConfig) *Router {
	enabled := map[string]bool{}
	for _, p := range providers {
		enabled[p.Name] = p.Enabled
	}
	cp := make([]config.RouteConfig, 0, len(routes))
	for _, route := range routes {
		if enabled[route.Provider] {
			cp = append(cp, route)
		}
	}
	sortRoutes(cp)
	return &Router{routes: cp}
}

func sortRoutes(routes []config.RouteConfig) {
	sort.SliceStable(routes, func(i, j int) bool {
		if routes[i].Priority != routes[j].Priority {
			return routes[i].Priority > routes[j].Priority
		}
		return longestPrefix(routes[i].Prefix) > longestPrefix(routes[j].Prefix)
	})
}

func longestPrefix(prefixes []string) int {
	longest := 0
	for _, prefix := range prefixes {
		if len(prefix) > longest {
			longest = len(prefix)
		}
	}
	return longest
}

func (r *Router) Match(msg message.Message) (config.RouteConfig, bool) {
	return r.MatchPhone(msg.To)
}

func (r *Router) MatchPhone(to string) (config.RouteConfig, bool) {
	bestPrefix := -1
	bestPriority := 0
	var bestRoute config.RouteConfig
	found := false
	for _, route := range r.routes {
		if len(route.Prefix) == 0 {
			if !found || route.Priority > bestPriority {
				bestPriority = route.Priority
				bestPrefix = 0
				bestRoute = route
				found = true
			}
			continue
		}
		for _, prefix := range route.Prefix {
			if !strings.HasPrefix(to, prefix) {
				continue
			}
			if !found || route.Priority > bestPriority || (route.Priority == bestPriority && len(prefix) > bestPrefix) {
				bestPriority = route.Priority
				bestPrefix = len(prefix)
				bestRoute = route
				found = true
			}
		}
	}
	if found {
		return bestRoute, true
	}
	return config.RouteConfig{}, false
}
