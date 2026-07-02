package router

import (
	"hash/fnv"
	"sort"
	"strings"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/message"
)

type Router struct {
	routes           []config.RouteConfig
	enabledProviders map[string]bool
}

func New(routes []config.RouteConfig) *Router {
	cp := make([]config.RouteConfig, len(routes))
	copy(cp, routes)
	sortRoutes(cp)
	return &Router{routes: cp, enabledProviders: map[string]bool{}}
}

func NewWithProviders(routes []config.RouteConfig, providers []config.ProviderConfig) *Router {
	enabled := map[string]bool{}
	for _, p := range providers {
		enabled[p.Name] = p.Enabled
	}
	cp := make([]config.RouteConfig, 0, len(routes))
	for _, route := range routes {
		if route.Enabled != nil && !*route.Enabled {
			continue
		}
		if routeSelectable(route, enabled) {
			cp = append(cp, route)
		}
	}
	sortRoutes(cp)
	return &Router{routes: cp, enabledProviders: enabled}
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

type MatchInput struct {
	To       string
	From     string
	SystemID string
	ClientID string
	Tags     map[string]struct{}
	Now      time.Time
}

type MatchResult struct {
	Route         config.RouteConfig
	Provider      string
	FailoverChain []string
}

func (r *Router) MatchSubmit(input MatchInput) (MatchResult, bool) {
	if input.Now.IsZero() {
		input.Now = time.Now()
	}
	bestPrefix := -1
	bestPriority := 0
	var best config.RouteConfig
	found := false
	for _, route := range r.routes {
		if !routeMatches(route, input) {
			continue
		}
		matchLen, ok := matchedPrefixLen(route.Prefix, input.To)
		if !ok {
			continue
		}
		if !found || route.Priority > bestPriority || (route.Priority == bestPriority && matchLen > bestPrefix) {
			found = true
			best = route
			bestPriority = route.Priority
			bestPrefix = matchLen
		}
	}
	if !found {
		return MatchResult{}, false
	}
	provider, chain := r.selectProvider(best, input.To)
	if provider == "" {
		return MatchResult{}, false
	}
	return MatchResult{Route: best, Provider: provider, FailoverChain: chain}, true
}

func routeSelectable(route config.RouteConfig, enabled map[string]bool) bool {
	if len(route.Weighted) > 0 {
		for _, wp := range route.Weighted {
			if enabled[wp.Provider] {
				return true
			}
		}
		return false
	}
	if len(route.Failover) > 0 {
		for _, provider := range route.Failover {
			if enabled[provider] {
				return true
			}
		}
		return false
	}
	return enabled[route.Provider]
}

func routeMatches(route config.RouteConfig, input MatchInput) bool {
	if route.Enabled != nil && !*route.Enabled {
		return false
	}
	if len(route.FromPrefix) > 0 && !hasAnyPrefix(input.From, route.FromPrefix) {
		return false
	}
	if len(route.SystemIDs) > 0 && !contains(route.SystemIDs, input.SystemID) {
		return false
	}
	if len(route.ClientIDs) > 0 && !contains(route.ClientIDs, input.ClientID) {
		return false
	}
	for _, tag := range route.ContentTags {
		if _, ok := input.Tags[tag]; !ok {
			return false
		}
	}
	if len(route.TimeWindows) > 0 && !inAnyWindow(input.Now, route.TimeWindows) {
		return false
	}
	return true
}

func matchedPrefixLen(prefixes []string, to string) (int, bool) {
	if len(prefixes) == 0 {
		return 0, true
	}
	best := -1
	for _, prefix := range prefixes {
		if strings.HasPrefix(to, prefix) && len(prefix) > best {
			best = len(prefix)
		}
	}
	return best, best >= 0
}

func hasAnyPrefix(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (r *Router) selectProvider(route config.RouteConfig, key string) (string, []string) {
	if len(route.Weighted) > 0 {
		total := 0
		for _, wp := range route.Weighted {
			if r.providerEnabled(wp.Provider) {
				total += wp.Weight
			}
		}
		if total <= 0 {
			return "", nil
		}
		n := int(stableHash(key) % uint32(total))
		for _, wp := range route.Weighted {
			if !r.providerEnabled(wp.Provider) {
				continue
			}
			if n < wp.Weight {
				return wp.Provider, nil
			}
			n -= wp.Weight
		}
		return "", nil
	}
	if len(route.Failover) > 0 {
		chain := []string{}
		for _, provider := range route.Failover {
			if r.providerEnabled(provider) {
				chain = append(chain, provider)
			}
		}
		if len(chain) == 0 {
			return "", nil
		}
		return chain[0], chain
	}
	if !r.providerEnabled(route.Provider) {
		return "", nil
	}
	return route.Provider, nil
}

func (r *Router) providerEnabled(name string) bool {
	if len(r.enabledProviders) == 0 {
		return true
	}
	return r.enabledProviders[name]
}

func stableHash(value string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(value))
	return h.Sum32()
}

func inAnyWindow(now time.Time, windows []config.TimeWindow) bool {
	for _, window := range windows {
		if !dayMatches(now.Weekday(), window.Days) {
			continue
		}
		start := clock(window.Start)
		end := clock(window.End)
		cur := time.Duration(now.Hour())*time.Hour + time.Duration(now.Minute())*time.Minute
		if start <= end {
			if cur >= start && cur <= end {
				return true
			}
			continue
		}
		if cur >= start || cur <= end {
			return true
		}
	}
	return false
}

func dayMatches(day time.Weekday, days []string) bool {
	if len(days) == 0 {
		return true
	}
	for _, value := range days {
		switch strings.ToLower(value) {
		case "sun", "sunday":
			if day == time.Sunday {
				return true
			}
		case "mon", "monday":
			if day == time.Monday {
				return true
			}
		case "tue", "tuesday":
			if day == time.Tuesday {
				return true
			}
		case "wed", "wednesday":
			if day == time.Wednesday {
				return true
			}
		case "thu", "thursday":
			if day == time.Thursday {
				return true
			}
		case "fri", "friday":
			if day == time.Friday {
				return true
			}
		case "sat", "saturday":
			if day == time.Saturday {
				return true
			}
		}
	}
	return false
}

func clock(value string) time.Duration {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0
	}
	return time.Duration(atoi(parts[0]))*time.Hour + time.Duration(atoi(parts[1]))*time.Minute
}

func atoi(value string) int {
	n := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
