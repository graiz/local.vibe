package daemon

import (
	"sort"
	"sync"
	"sync/atomic"
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
	TTL          *int       `json:"ttl,omitempty"`
	Cmd          string     `json:"cmd,omitempty"`
	Dir          string     `json:"dir,omitempty"`
	ExternalURL  string     `json:"external_url,omitempty"`
	RegisteredAt time.Time  `json:"registered_at"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	Type         RouteType  `json:"type"`
	IdleTimeout  int        `json:"idle_timeout,omitempty"` // minutes; 0 = never auto-stop
	Icon         string     `json:"icon,omitempty"`         // user-chosen emoji or URL for dashboard
	AutoIcon     string     `json:"auto_icon,omitempty"`    // auto-detected favicon (data URI)

	// Optional OAuth bridge: if set, vibe binds localhost:<OAuthCallbackPort>
	// and redirects /auth/google/callback to {name}.{tld}. This lets apps keep
	// localhost redirect URIs while sessions are established on the .vibe host.
	OAuthCallbackPort int `json:"oauth_callback_port,omitempty"`

	// Bookmark-only: when true, requests to name.vibe are reverse-proxied to
	// ExternalURL instead of 307-redirected, so the browser keeps the .vibe
	// host in the URL bar. InsecureSkipVerify disables upstream TLS
	// verification for self-signed targets (e.g. Tailscale MagicDNS).
	Proxy              bool `json:"proxy,omitempty"`
	InsecureSkipVerify bool `json:"insecure_skip_verify,omitempty"`

	// Runtime-only state; safe for concurrent access.
	Running      atomic.Bool               `json:"-"`
	Ready        atomic.Bool               `json:"-"`
	PID          atomic.Pointer[int]       `json:"-"` // managed/tracked process PID; nil when not running
	LastActivity atomic.Pointer[time.Time] `json:"-"` // last proxy request; nil until first activity
	LastFailure  atomic.Pointer[Failure]   `json:"-"` // diagnostics from the most recent failed start, if any
}

// Failure captures why a managed route's most recent start attempt failed,
// including the log tail and any actionable recovery hint extracted from it.
// Stored on the Route so that asynchronous crashes (process dies after Start
// returned successfully) can still surface a "Kill PID X and retry" button
// to whoever polls the /ready endpoint.
type Failure struct {
	Message  string    `json:"message"`
	Log      string    `json:"log,omitempty"`
	Recovery *Recovery `json:"recovery,omitempty"`
}

// SetPID stores the process PID atomically.
func (r *Route) SetPID(pid int) {
	p := pid
	r.PID.Store(&p)
}

// ClearPID removes any stored PID.
func (r *Route) ClearPID() { r.PID.Store(nil) }

// PIDValue returns the stored PID and whether one is set.
func (r *Route) PIDValue() (int, bool) {
	p := r.PID.Load()
	if p == nil {
		return 0, false
	}
	return *p, true
}

// TouchActivity records "now" as the most recent proxy request time.
func (r *Route) TouchActivity() {
	t := time.Now()
	r.LastActivity.Store(&t)
}

// SetLastActivity stores an explicit activity timestamp (used by tests
// to simulate elapsed idle time).
func (r *Route) SetLastActivity(t time.Time) { r.LastActivity.Store(&t) }

// SetFailure records diagnostics from a failed start attempt. Pass nil to
// clear.
func (r *Route) SetFailure(f *Failure) { r.LastFailure.Store(f) }

// LoadFailure returns the most recent failure, or nil if the route has
// started cleanly since the last reset.
func (r *Route) LoadFailure() *Failure { return r.LastFailure.Load() }

// LastActivityOr returns the last activity timestamp, or fallback if none set.
func (r *Route) LastActivityOr(fallback time.Time) time.Time {
	if la := r.LastActivity.Load(); la != nil && !la.IsZero() {
		return *la
	}
	return fallback
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

// UpdatePort rewrites an existing route's Port field under the table lock.
// Returns false if the route is unknown. Used by the self-healing repair
// flow when a managed app rebinds itself to a different port.
func (t *RouteTable) UpdatePort(name string, port int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.routes[name]
	if !ok {
		return false
	}
	r.Port = port
	return true
}

// Names returns a sorted list of all route names.
func (t *RouteTable) Names() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	names := make([]string, 0, len(t.routes))
	for name := range t.routes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
