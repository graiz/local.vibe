package daemon

import (
	"sort"
	"sync"
	"time"
)

// RouteType determines a route's lifecycle behavior.
type RouteType string

const (
	RouteSticky     RouteType = "sticky"
	RoutePIDTracked RouteType = "pid"
	RouteTTL        RouteType = "ttl"
	RouteManaged    RouteType = "managed"
	RouteBookmark   RouteType = "bookmark"
)

// Route represents a registered service or bookmark that maps a .vibe subdomain
// to a local port or external URL.
type Route struct {
	Name         string     `json:"name"`
	Port         int        `json:"port"`
	PID          *int       `json:"pid,omitempty"`
	TTL          *int       `json:"ttl,omitempty"`
	Cmd          string     `json:"cmd,omitempty"`
	Dir          string     `json:"dir,omitempty"`
	ExternalURL  string     `json:"external_url,omitempty"`
	RegisteredAt time.Time  `json:"registered_at"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	Type         RouteType  `json:"type"`
	Healthy      bool       `json:"healthy"`
	IdleTimeout  int        `json:"idle_timeout,omitempty"`  // minutes; 0 = never auto-stop
	LastActivity time.Time  `json:"-"`                       // runtime only, not serialized
}

// RouteTable is a thread-safe in-memory registry of routes, keyed by name.
type RouteTable struct {
	mu     sync.RWMutex
	routes map[string]*Route
}

// NewRouteTable creates an empty route table.
func NewRouteTable() *RouteTable {
	return &RouteTable{routes: make(map[string]*Route)}
}

func (t *RouteTable) Add(r *Route) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.routes[r.Name] = r
}

func (t *RouteTable) Remove(name string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.routes[name]; !ok {
		return false
	}
	delete(t.routes, name)
	return true
}

func (t *RouteTable) Get(name string) (*Route, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	r, ok := t.routes[name]
	return r, ok
}

func (t *RouteTable) List() []*Route {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]*Route, 0, len(t.routes))
	for _, r := range t.routes {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}
