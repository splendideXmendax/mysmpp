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
	sort.SliceStable(cp, func(i, j int) bool {
		return cp[i].Priority > cp[j].Priority
	})
	return &Router{routes: cp}
}

func (r *Router) Match(msg message.Message) (config.RouteConfig, bool) {
	for _, route := range r.routes {
		if len(route.Prefix) == 0 {
			return route, true
		}
		for _, prefix := range route.Prefix {
			if strings.HasPrefix(msg.To, prefix) {
				return route, true
			}
		}
	}
	return config.RouteConfig{}, false
}
