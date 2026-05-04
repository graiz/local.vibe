package daemon

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/graiz/local.vibe/internal/config"
)

// validName matches DNS-safe route names: lowercase letters, digits, and hyphens.
var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$|^[a-z0-9]$`)

// writeJSONError sends a well-formed JSON error response. Using fmt.Sprintf
// into a literal `{"error":"..."}` string is unsafe when the message can
// contain quotes, backslashes, or newlines (e.g. tailed process output).
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// failureFromError turns a Start() error into a Failure record, scanning the
// log tail (if present) for an actionable recovery hint. cmd is the route's
// current command line — used for cmd-rewrite suggestions like python→python3.
func failureFromError(err error, cmd string) *Failure {
	if err == nil {
		return nil
	}
	var se *StartError
	f := &Failure{Message: err.Error()}
	if errors.As(err, &se) {
		f.Message = se.Err.Error()
		f.Log = se.Tail
		f.Recovery = scanLogForRecovery(se.Tail, cmd)
	}
	return f
}

// reservePortNamePattern restricts reserve_port keys to env-var-safe identifiers.
// The name is uppercased and prefixed with "PORT_" when injected as an env
// var (e.g. {"server": 3001} → PORT_SERVER=3001), so the key has to be a
// legal POSIX env var suffix to begin with.
var reservePortNamePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)

// validateReservePorts checks a reserve_ports map for: legal name shape, no
// collision with the primary or oauth_callback_port, no port out of range,
// no two names mapping to the same port. Returns a normalized copy with
// names lower-cased so config files can use either case interchangeably
// (env var injection always uppercases on the way out).
//
// The forbidden name "PORT" is rejected explicitly — using it would make
// PORT_<NAME> = PORT, which would either shadow or duplicate the primary
// port's env var depending on map iteration order. Confusing either way.
func validateReservePorts(reserve map[string]int, primary, oauthCallback int) (map[string]int, error) {
	if len(reserve) == 0 {
		return nil, nil
	}
	out := make(map[string]int, len(reserve))
	seenPort := make(map[int]string, len(reserve))
	for rawName, p := range reserve {
		name := strings.ToLower(strings.TrimSpace(rawName))
		if name == "" {
			return nil, fmt.Errorf("reserve_ports has empty name")
		}
		if !reservePortNamePattern.MatchString(name) {
			return nil, fmt.Errorf("reserve_ports name %q must match [a-zA-Z][a-zA-Z0-9_]*", rawName)
		}
		if strings.EqualFold(name, "port") {
			return nil, fmt.Errorf("reserve_ports name %q is reserved (it would collide with the primary $PORT env var)", rawName)
		}
		if p < 1 || p > 65535 {
			return nil, fmt.Errorf("reserve_ports[%q] = %d is out of range (1-65535)", rawName, p)
		}
		if p == primary {
			return nil, fmt.Errorf("reserve_ports[%q] = %d collides with the route's primary port", rawName, p)
		}
		if oauthCallback > 0 && p == oauthCallback {
			return nil, fmt.Errorf("reserve_ports[%q] = %d collides with oauth_callback_port", rawName, p)
		}
		if other, dup := seenPort[p]; dup {
			return nil, fmt.Errorf("reserve_ports[%q] and reserve_ports[%q] both map to port %d", other, rawName, p)
		}
		if _, exists := out[name]; exists {
			return nil, fmt.Errorf("reserve_ports has duplicate name %q (case-insensitive)", rawName)
		}
		seenPort[p] = rawName
		out[name] = p
	}
	return out, nil
}

// reservePortValuesSorted returns the port numbers from a reserve_ports map
// sorted by name, so iteration order is deterministic for preflight checks
// and env var injection.
func reservePortValuesSorted(reserve map[string]int) []struct {
	Name string
	Port int
} {
	if len(reserve) == 0 {
		return nil
	}
	names := make([]string, 0, len(reserve))
	for n := range reserve {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]struct {
		Name string
		Port int
	}, len(names))
	for i, n := range names {
		out[i].Name = n
		out[i].Port = reserve[n]
	}
	return out
}

// reservePortConflictsWith returns the offending port and the route that owns
// it if any of this route's reserve_ports values is already claimed by another
// route's primary, OAuth callback, or reserve port. Used to surface config-time
// collisions before the spawn.
func reservePortConflictsWith(table *RouteTable, ownName string, reserve map[string]int) (int, string) {
	if len(reserve) == 0 {
		return 0, ""
	}
	want := make(map[int]bool, len(reserve))
	for _, p := range reserve {
		want[p] = true
	}
	for _, r := range table.List() {
		if r.Name == ownName {
			continue
		}
		if r.Port > 0 && want[r.Port] {
			return r.Port, r.Name
		}
		if r.OAuthCallbackPort > 0 && want[r.OAuthCallbackPort] {
			return r.OAuthCallbackPort, r.Name
		}
		for _, p := range r.ReservePorts {
			if want[p] {
				return p, r.Name
			}
		}
	}
	return 0, ""
}

// preflightPort verifies that a port is currently free, attempting to clear
// any stale process via killPort before giving up. Returns a Recovery hint
// when the port is still held by an external process after the kill attempt;
// returns nil when the port is free (or only held by daemon-managed PIDs).
//
// This is the shared entry point used for the route's primary port AND each
// of its reserve_ports — both go through the same kill-and-recheck flow so
// the user gets a single, consistent recovery UX regardless of which port
// is the offender.
func (s *Server) preflightPort(port int) *Recovery {
	if port <= 0 {
		return nil
	}
	if !s.isPortReady(port) {
		return nil
	}
	s.killPort(port)
	time.Sleep(500 * time.Millisecond)
	if !s.isPortReady(port) {
		return nil
	}
	return s.buildPortConflictRecovery(port)
}

// writePortConflict sends a 409 Conflict with a recovery hint identifying the
// process holding the port (when one can be discovered). The startpage reads
// the recovery field and renders a one-click "Kill PID X and retry" button
// instead of just a useless "Retry" that would hit the same conflict.
func writePortConflict(w http.ResponseWriter, port int, recovery *Recovery) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	resp := map[string]any{
		"error": fmt.Sprintf("port %d is already in use by another process", port),
	}
	if recovery != nil {
		resp["recovery"] = recovery
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// writeStartFailure sends a managed-process start error with the log tail and
// a recovery hint attached when one of the known patterns matches. The
// dashboard surfaces the hint as a one-click "kill and retry" button.
func writeStartFailure(w http.ResponseWriter, status int, err error, cmd string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	f := failureFromError(err, cmd)
	resp := map[string]any{"error": f.Message}
	if f.Log != "" {
		resp["log"] = f.Log
	}
	if f.Recovery != nil {
		resp["recovery"] = f.Recovery
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// isBrowserFormRequest returns true for requests that came from a dashboard
// HTML form submission (so handlers should 303-redirect back to the dashboard
// instead of returning JSON). Identified by the form-encoded Content-Type —
// the previous "Accept != application/json" heuristic mis-classified CLI
// clients that omit Accept entirely, which then chased a 303 to https://local.vibe/
// over the Unix socket and surfaced as a spurious "daemon not running" error.
func isBrowserFormRequest(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	if i := strings.Index(ct, ";"); i != -1 {
		ct = ct[:i]
	}
	return strings.TrimSpace(ct) == "application/x-www-form-urlencoded"
}

// validateExternalURL rejects any bookmark target that isn't a plain http://
// or https:// URL. Prevents javascript:, file:, data:, etc. from being used
// as a 307 redirect target, which would be an open-redirect vector.
func validateExternalURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url must use http:// or https://")
	}
	if u.Host == "" {
		return fmt.Errorf("url must include a host")
	}
	return nil
}

type registerRequest struct {
	Name               string         `json:"name"`
	Port               int            `json:"port"`
	PID                *int           `json:"pid,omitempty"`
	TTL                *int           `json:"ttl,omitempty"`
	Cmd                string         `json:"cmd,omitempty"`
	Dir                string         `json:"dir,omitempty"`
	URL                string         `json:"url,omitempty"`
	IdleTimeout        *int           `json:"idle_timeout,omitempty"` // minutes; 0 = never
	Icon               *string        `json:"icon,omitempty"`
	Proxy              *bool          `json:"proxy,omitempty"`                // bookmark: reverse-proxy instead of 307-redirect
	InsecureSkipVerify *bool          `json:"insecure_skip_verify,omitempty"` // bookmark+proxy: skip upstream TLS verify
	OAuthCallbackPort  *int           `json:"oauth_callback_port,omitempty"`
	ReservePorts         map[string]int `json:"reserve_ports,omitempty"` // managed: named auxiliary ports, exposed as PORT_<UPPER_NAME>
}

type routeResponse struct {
	Name               string     `json:"name"`
	Port               int        `json:"port,omitempty"`
	PID                *int       `json:"pid,omitempty"`
	RegisteredAt       time.Time  `json:"registered_at"`
	ExpiresAt          *time.Time `json:"expires_at,omitempty"`
	Type               RouteType  `json:"type"`
	Running            bool       `json:"running"`
	Ready              bool       `json:"ready"`
	URL                string     `json:"url"`
	ExternalURL        string     `json:"external_url,omitempty"`
	IdleTimeout        int        `json:"idle_timeout,omitempty"`
	Icon               string     `json:"icon,omitempty"`
	Proxy              bool       `json:"proxy,omitempty"`
	InsecureSkipVerify bool       `json:"insecure_skip_verify,omitempty"`
	OAuthCallbackPort  int        `json:"oauth_callback_port,omitempty"`
}

// apiHandler routes /_api/* requests to the appropriate handler.
func (s *Server) apiHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/_api")

	// On non-local .vibe hosts, reserve only the daemon's known API routes.
	// Unknown /_api/* paths should continue through normal proxying so apps
	// that expose their own /_api namespace (e.g. Jekyll Admin) still work.
	host := r.Host
	if idx := strings.Index(host, ":"); idx != -1 {
		host = host[:idx]
	}
	nonLocalVibeHost := false
	if strings.HasSuffix(host, "."+s.cfg.Daemon.TLD) {
		name := strings.TrimSuffix(host, "."+s.cfg.Daemon.TLD)
		nonLocalVibeHost = name != "local"
	}
	if nonLocalVibeHost && !isDaemonAPIPath(r.Method, path) {
		s.routeRequest(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.Method == http.MethodGet && path == "/health":
		s.handleHealth(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/routes/") && strings.HasSuffix(path, "/ready"):
		name := strings.TrimPrefix(path, "/routes/")
		name = strings.TrimSuffix(name, "/ready")
		s.handleReady(w, r, name)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/routes/") && strings.HasSuffix(path, "/repair"):
		name := strings.TrimSuffix(strings.TrimPrefix(path, "/routes/"), "/repair")
		s.handleRepair(w, r, name)
	case r.Method == http.MethodGet && path == "/routes":
		s.handleListRoutes(w, r)
	case r.Method == http.MethodPost && path == "/routes":
		s.handleRegister(w, r)
	case r.Method == http.MethodPut && strings.HasPrefix(path, "/routes/"):
		name := strings.TrimPrefix(path, "/routes/")
		s.handleUpdate(w, r, name)
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/routes/"):
		name := strings.TrimPrefix(path, "/routes/")
		if strings.HasSuffix(name, "/stop") {
			s.handleStop(w, r, strings.TrimSuffix(name, "/stop"))
		} else {
			s.handleDeregister(w, r, name)
		}
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/routes/") && strings.HasSuffix(path, "/stop"):
		name := strings.TrimSuffix(strings.TrimPrefix(path, "/routes/"), "/stop")
		s.handleStop(w, r, name)
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/routes/") && strings.HasSuffix(path, "/start"):
		name := strings.TrimSuffix(strings.TrimPrefix(path, "/routes/"), "/start")
		s.handleStart(w, r, name)
	case r.Method == http.MethodPut && path == "/preferences":
		s.handleSetPreferences(w, r)
	default:
		if nonLocalVibeHost {
			s.routeRequest(w, r)
			return
		}
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
	}
}

func isDaemonAPIPath(method, path string) bool {
	switch {
	case method == http.MethodGet && path == "/health":
		return true
	case method == http.MethodGet && strings.HasPrefix(path, "/routes/") && strings.HasSuffix(path, "/ready"):
		return true
	case method == http.MethodGet && strings.HasPrefix(path, "/routes/") && strings.HasSuffix(path, "/repair"):
		return true
	case method == http.MethodGet && path == "/routes":
		return true
	case method == http.MethodPost && path == "/routes":
		return true
	case method == http.MethodPut && strings.HasPrefix(path, "/routes/"):
		return true
	case method == http.MethodDelete && strings.HasPrefix(path, "/routes/"):
		return true
	case method == http.MethodPost && strings.HasPrefix(path, "/routes/") && strings.HasSuffix(path, "/stop"):
		return true
	case method == http.MethodPost && strings.HasPrefix(path, "/routes/") && strings.HasSuffix(path, "/start"):
		return true
	case method == http.MethodPut && path == "/preferences":
		return true
	default:
		return false
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"routes": len(s.table.List()),
		"uptime": int(time.Since(s.startedAt).Seconds()),
	})
}

func (s *Server) handleListRoutes(w http.ResponseWriter, _ *http.Request) {
	routes := s.table.List()
	resp := make([]routeResponse, len(routes))
	for i, r := range routes {
		resp[i] = toResponse(r, s.cfg.Daemon.TLD, s.vibeScheme())
	}
	json.NewEncoder(w).Encode(resp)
}

// handleRegister creates a new route. The route type is inferred from the request:
// url → bookmark, cmd → managed, pid → pid-tracked, ttl → ttl, default → sticky.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeJSONError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Port == 0 && req.URL == "" && req.Cmd == "" {
		writeJSONError(w, http.StatusBadRequest, "port, url, or cmd is required")
		return
	}
	req.Name = strings.ToLower(req.Name)
	if !validName.MatchString(req.Name) {
		writeJSONError(w, http.StatusBadRequest, "name must be lowercase letters, digits, or hyphens")
		return
	}
	if req.Name == "local" {
		writeJSONError(w, http.StatusBadRequest, "'local' is reserved for the dashboard")
		return
	}
	if req.URL != "" {
		if err := validateExternalURL(req.URL); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	// Proxy mode is only meaningful for bookmarks (routes with a URL).
	if req.Proxy != nil && *req.Proxy && req.URL == "" {
		writeJSONError(w, http.StatusBadRequest, "proxy requires a url")
		return
	}
	if req.OAuthCallbackPort != nil {
		if err := s.validateOAuthBridgeConfig(req.Name, req.Port, *req.OAuthCallbackPort); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	oauthCB := 0
	if req.OAuthCallbackPort != nil {
		oauthCB = *req.OAuthCallbackPort
	}
	reservePorts, err := validateReservePorts(req.ReservePorts, req.Port, oauthCB)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if conflictPort, conflictRoute := reservePortConflictsWith(s.table, req.Name, reservePorts); conflictPort != 0 {
		writeJSONError(w, http.StatusConflict, fmt.Sprintf("reserve_ports value %d conflicts with route %q", conflictPort, conflictRoute))
		return
	}

	// Reject if a managed route with this name is already running.
	if existing, ok := s.table.Get(req.Name); ok && existing.Type == RouteManaged && existing.Running.Load() {
		writeJSONError(w, http.StatusConflict, fmt.Sprintf("route %q is already running on port %d", req.Name, existing.Port))
		return
	}

	route := &Route{
		Name:         req.Name,
		Port:         req.Port,
		ExternalURL:  req.URL,
		RegisteredAt: time.Now(),
	}
	if req.PID != nil {
		route.SetPID(*req.PID)
	}
	route.Running.Store(true)
	route.Ready.Store(true)
	if req.IdleTimeout != nil {
		route.IdleTimeout = *req.IdleTimeout
	}
	if req.Icon != nil {
		route.Icon = *req.Icon
	}
	if req.Proxy != nil {
		route.Proxy = *req.Proxy
	}
	if req.InsecureSkipVerify != nil {
		route.InsecureSkipVerify = *req.InsecureSkipVerify
	}
	if req.OAuthCallbackPort != nil {
		route.OAuthCallbackPort = *req.OAuthCallbackPort
	}
	route.ReservePorts = reservePorts

	switch {
	case req.URL != "":
		route.Type = RouteBookmark
	case req.Cmd != "":
		route.Type = RouteManaged
		route.Cmd = req.Cmd
		route.Dir = req.Dir
	case req.PID != nil:
		route.Type = RoutePIDTracked
	case req.TTL != nil:
		route.Type = RouteTTL
		exp := time.Now().Add(time.Duration(*req.TTL) * time.Second)
		route.ExpiresAt = &exp
		route.TTL = req.TTL
	default:
		route.Type = RouteSticky
	}

	s.table.Add(route)

	// Launch managed process immediately.
	if route.Type == RouteManaged {
		// Auto-assign a free port if none was specified.
		if route.Port == 0 {
			port, err := findFreePort(s.table)
			if err != nil {
				s.table.Remove(route.Name)
				writeJSONError(w, http.StatusInternalServerError, "could not find a free port")
				return
			}
			route.Port = port
		}
		// Clear stale process on the primary port and each reserve_port before
		// launching. Reserve ports check first so a multi-port app surfaces
		// the right collision instead of silently bleeding into a stale
		// holder of a non-routed port (the screener.vibe / task-tracker bug).
		// Iterate in sorted-name order so behavior is deterministic.
		for _, kv := range reservePortValuesSorted(route.ReservePorts) {
			if rec := s.preflightPort(kv.Port); rec != nil {
				s.table.Remove(route.Name)
				writePortConflict(w, kv.Port, rec)
				return
			}
		}
		if rec := s.preflightPort(route.Port); rec != nil {
			s.table.Remove(route.Name)
			writePortConflict(w, route.Port, rec)
			return
		}
		pid, err := s.procs.Start(route)
		if err != nil {
			s.table.Remove(route.Name)
			writeStartFailure(w, http.StatusInternalServerError, err, route.Cmd)
			return
		}
		route.SetPID(pid)
		route.Running.Store(true)
		route.Ready.Store(false)
		route.SetFailure(nil)
		// Seed LastActivity so the idle-timeout sweep has a baseline even if
		// the process is started but never receives a proxy request.
		route.TouchActivity()
		go s.waitForReady(route)
	}

	if err := s.reconcileOAuthBridgeListeners(); err != nil {
		if route.Type == RouteManaged {
			_ = s.procs.Stop(route.Name)
		}
		s.table.Remove(route.Name)
		writeJSONError(w, http.StatusConflict, err.Error())
		return
	}

	if route.Type == RouteSticky || route.Type == RouteManaged || route.Type == RouteBookmark {
		_ = s.saveStickyRoutes()
	}

	json.NewEncoder(w).Encode(map[string]any{
		"ok":   true,
		"url":  fmt.Sprintf("%s://%s.%s", s.vibeScheme(), req.Name, s.cfg.Daemon.TLD),
		"port": route.Port,
	})
}

func (s *Server) handleDeregister(w http.ResponseWriter, _ *http.Request, name string) {
	route, ok := s.table.Get(name)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "route not found")
		return
	}
	if route.Type == RouteManaged {
		_ = s.procs.Stop(name)
	}
	s.table.Remove(name)
	if err := s.reconcileOAuthBridgeListeners(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: oauth bridge listener reconcile failed: %v\n", err)
	}
	_ = s.saveStickyRoutes()
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// handleUpdate modifies an existing route's name, port, or URL.
func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request, name string) {
	route, ok := s.table.Get(name)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "route not found")
		return
	}
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name != "" {
		req.Name = strings.ToLower(req.Name)
	}
	// If the name changed, validate and remove old entry.
	if req.Name != "" && req.Name != name {
		if !validName.MatchString(req.Name) {
			writeJSONError(w, http.StatusBadRequest, "invalid name — use lowercase letters, digits, and hyphens")
			return
		}
		if req.Name == "local" {
			writeJSONError(w, http.StatusConflict, "'local' is reserved for the dashboard")
			return
		}
		if _, exists := s.table.Get(req.Name); exists {
			writeJSONError(w, http.StatusConflict, "a route with that name already exists")
			return
		}
		s.table.Remove(name)
		route.Name = req.Name
	}
	if req.URL != "" {
		if err := validateExternalURL(req.URL); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	// Proxy=true requires a URL: either one provided on this update, or the
	// route is already a bookmark.
	if req.Proxy != nil && *req.Proxy && req.URL == "" && route.Type != RouteBookmark {
		writeJSONError(w, http.StatusBadRequest, "proxy requires a url")
		return
	}
	originalName := route.Name
	originalPort := route.Port
	originalExternalURL := route.ExternalURL
	originalType := route.Type
	originalIdleTimeout := route.IdleTimeout
	originalIcon := route.Icon
	originalProxy := route.Proxy
	originalInsecureSkipVerify := route.InsecureSkipVerify
	originalOAuthCallbackPort := route.OAuthCallbackPort
	if req.Port != 0 {
		route.Port = req.Port
	}
	if req.IdleTimeout != nil {
		route.IdleTimeout = *req.IdleTimeout
	}
	if req.Icon != nil {
		route.Icon = *req.Icon
	}
	// Cmd is only meaningful on managed routes — silently ignore on others so
	// generic clients can PUT a full route shape without surprises.
	if req.Cmd != "" && route.Type == RouteManaged {
		route.Cmd = req.Cmd
	}
	if req.URL != "" {
		route.ExternalURL = req.URL
		route.Type = RouteBookmark
		route.Port = 0
	} else if route.Type == RouteBookmark && req.Port != 0 {
		// Switching from bookmark to sticky
		route.ExternalURL = ""
		route.Type = RouteSticky
		route.Proxy = false
		route.InsecureSkipVerify = false
	}
	if req.Proxy != nil {
		route.Proxy = *req.Proxy
	}
	if req.InsecureSkipVerify != nil {
		route.InsecureSkipVerify = *req.InsecureSkipVerify
	}
	if req.OAuthCallbackPort != nil {
		route.OAuthCallbackPort = *req.OAuthCallbackPort
	}
	if route.OAuthCallbackPort > 0 {
		if err := s.validateOAuthBridgeConfig(route.Name, route.Port, route.OAuthCallbackPort); err != nil {
			route.Name = originalName
			route.Port = originalPort
			route.ExternalURL = originalExternalURL
			route.Type = originalType
			route.IdleTimeout = originalIdleTimeout
			route.Icon = originalIcon
			route.Proxy = originalProxy
			route.InsecureSkipVerify = originalInsecureSkipVerify
			route.OAuthCallbackPort = originalOAuthCallbackPort
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	s.table.Add(route)
	if err := s.reconcileOAuthBridgeListeners(); err != nil {
		route.Name = originalName
		route.Port = originalPort
		route.ExternalURL = originalExternalURL
		route.Type = originalType
		route.IdleTimeout = originalIdleTimeout
		route.Icon = originalIcon
		route.Proxy = originalProxy
		route.InsecureSkipVerify = originalInsecureSkipVerify
		route.OAuthCallbackPort = originalOAuthCallbackPort
		s.table.Add(route)
		writeJSONError(w, http.StatusConflict, err.Error())
		return
	}
	_ = s.saveStickyRoutes()
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request, name string) {
	route, ok := s.table.Get(name)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "route not found")
		return
	}
	// Re-read vibe.json from the route's Dir before launching so edits to
	// oauth_callback_port, reserve_ports, or cmd take effect without forcing
	// the user to deregister + re-register. Errors here are config conflicts
	// that should block the start; missing/malformed files are silent no-ops.
	if err := s.syncRouteFromVibeJSON(route); err != nil {
		writeJSONError(w, http.StatusConflict, err.Error())
		return
	}
	if route.Cmd == "" {
		writeJSONError(w, http.StatusBadRequest, "route has no launch command")
		return
	}

	// Optional: the dashboard may send a recovery action with the retry, e.g.
	// {"kill_pid": 23674} after a log-scan hint told the user a previous Next
	// dev server is holding things up. Kill it before starting the new process.
	var rec struct {
		KillPID  int `json:"kill_pid,omitempty"`
		KillPort int `json:"kill_port,omitempty"`
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") && r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&rec)
	}
	if rec.KillPID > 0 {
		if err := s.safeKillPID(rec.KillPID); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("could not kill PID %d: %s", rec.KillPID, err))
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	if rec.KillPort > 0 {
		s.killPort(rec.KillPort)
		time.Sleep(300 * time.Millisecond)
	}

	// Auto-assign a free port if the route has none (e.g. auto-assigned on first start).
	if route.Port == 0 {
		port, err := findFreePort(s.table)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "could not find a free port")
			return
		}
		route.Port = port
	}
	// Pre-flight every port the cmd will bind: each reserve_port first, then
	// the primary. Persist the recovery on the route's Failure so a startpage
	// reload rehydrates the "Kill PID X and retry" button via
	// startPageRecoveryInitJS instead of showing a bare error.
	for _, kv := range reservePortValuesSorted(route.ReservePorts) {
		if rec := s.preflightPort(kv.Port); rec != nil {
			route.SetFailure(&Failure{
				Message:  fmt.Sprintf("reserve_ports[%q] = %d is already in use by another process", kv.Name, kv.Port),
				Recovery: rec,
			})
			writePortConflict(w, kv.Port, rec)
			return
		}
	}
	if rec := s.preflightPort(route.Port); rec != nil {
		route.SetFailure(&Failure{
			Message:  fmt.Sprintf("port %d is already in use by another process", route.Port),
			Recovery: rec,
		})
		writePortConflict(w, route.Port, rec)
		return
	}

	pid, err := s.procs.Start(route)
	if err != nil {
		route.Running.Store(false)
		route.Ready.Store(false)
		route.SetFailure(failureFromError(err, route.Cmd))
		writeStartFailure(w, http.StatusInternalServerError, err, route.Cmd)
		return
	}
	route.SetPID(pid)
	route.Running.Store(true)
	route.Ready.Store(false)
	route.SetFailure(nil)
	route.TouchActivity()
	go s.waitForReady(route)

	// If request came from browser form, redirect back to the app.
	if isBrowserFormRequest(r) {
		http.Redirect(w, r, fmt.Sprintf("%s://%s.%s/", s.vibeScheme(), name, s.cfg.Daemon.TLD), http.StatusSeeOther)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "pid": pid})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request, name string) {
	route, ok := s.table.Get(name)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "route not found")
		return
	}
	// Try the ProcessManager first, then fall back to killing whatever is on the port.
	if err := s.procs.Stop(name); err != nil {
		s.killPort(route.Port)
	}
	route.Running.Store(false)
	route.Ready.Store(false)
	route.ClearPID()

	// If request came from browser form, redirect back to dashboard.
	if isBrowserFormRequest(r) {
		http.Redirect(w, r, fmt.Sprintf("%s://local.%s/", s.vibeScheme(), s.cfg.Daemon.TLD), http.StatusSeeOther)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// handleRepair attempts to locate the real listening port for a route
// whose registered port is no longer responding. When found it silently
// updates the route, persists to routes.json, and logs a single line so
// the user can tell the daemon self-healed.
//
// Response shape:
//
//	{ ok: true,  port: 3001, from: 3000 }    // auto-resolved
//	{ ok: true,  port: 3000, note: "..." }   // nothing to fix, already reachable
//	{ ok: false, port: 3000, reason: "...", restartable: true } // no candidate found
func (s *Server) handleRepair(w http.ResponseWriter, _ *http.Request, name string) {
	route, ok := s.table.Get(name)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "route not found")
		return
	}
	if route.Type == RouteBookmark {
		writeJSONError(w, http.StatusBadRequest, "bookmarks don't need repair")
		return
	}
	if s.isPortReady(route.Port) {
		json.NewEncoder(w).Encode(map[string]any{
			"ok":   true,
			"port": route.Port,
			"note": "already reachable",
		})
		return
	}

	newPort, found := s.discoverRoutePort(route)
	if !found {
		resp := map[string]any{
			"ok":     false,
			"port":   route.Port,
			"reason": "could not locate a listening port for this route",
		}
		// Offer a restart affordance when the managed child is gone.
		if route.Type == RouteManaged {
			pid, hasPID := route.PIDValue()
			if !hasPID || !processAlive(pid) {
				resp["restartable"] = true
			}
		}
		json.NewEncoder(w).Encode(resp)
		return
	}

	// Safety: refuse to adopt a port that another route has already registered.
	// The candidate is plausibly an orphan instance belonging to THAT route,
	// not this one — silently rewriting would make both routes proxy to the
	// same listener. Surface it as a non-fix so the user can clean up.
	for _, other := range s.table.List() {
		if other.Name != route.Name && other.Port == newPort {
			resp := map[string]any{
				"ok":   false,
				"port": route.Port,
				"reason": fmt.Sprintf(
					"port %d is registered to route %q; refusing to adopt",
					newPort, other.Name,
				),
			}
			if route.Type == RouteManaged {
				pid, hasPID := route.PIDValue()
				if !hasPID || !processAlive(pid) {
					resp["restartable"] = true
				}
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
	}

	old := route.Port
	s.table.UpdatePort(name, newPort)
	route.Ready.Store(true)
	if err := s.saveStickyRoutes(); err != nil {
		fmt.Fprintf(os.Stderr, "vibe: failed to persist repaired port for %s: %v\n", name, err)
	}
	fmt.Fprintf(os.Stderr, "vibe: %s port auto-updated from %d -> %d\n", name, old, newPort)

	json.NewEncoder(w).Encode(map[string]any{
		"ok":   true,
		"port": newPort,
		"from": old,
	})
}

func (s *Server) handleReady(w http.ResponseWriter, _ *http.Request, name string) {
	route, ok := s.table.Get(name)
	if !ok {
		json.NewEncoder(w).Encode(map[string]any{"ready": false, "running": false})
		return
	}
	resp := map[string]any{
		"ready":   s.isPortReady(route.Port),
		"running": route.Running.Load(),
	}
	// Include diagnostics from the most recent failed start so the browser
	// can render a "Kill PID X and retry" button when the process crashed
	// asynchronously (after Start() returned success but before the port
	// came up).
	if f := route.LoadFailure(); f != nil {
		resp["failure"] = f
	}
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleSetPreferences(w http.ResponseWriter, r *http.Request) {
	var req struct {
		View string `json:"view"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.View == "list" || req.View == "grid" {
		s.cfg.Dashboard.View = req.View
		s.saveConfig()
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// waitForReady polls the route's port until it accepts connections, then sets
// Ready = true. If the process dies or the timeout expires, it marks the route
// as not running so the dashboard reflects the failure immediately.
func (s *Server) waitForReady(route *Route) {
	timeout := s.ReadyTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	deadline := time.After(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			// Timed out — port never came up. Process may still be alive,
			// so only mark Ready=false. Running reflects actual process state.
			if !s.isPortReady(route.Port) {
				route.Ready.Store(false)
				// Surface something actionable on /ready so the start page
				// can render the log tail + a recovery button instead of a
				// bare "check logs" timeout message. Prefer a log-scan hint
				// (EADDRINUSE, orphan PID, etc.); otherwise fall back to
				// "restart" — stop the stuck child and try again.
				logPath := filepath.Join(s.configDir(), route.Name+".log")
				tail := tailLogFile(logPath, 24)
				f := &Failure{
					Message: fmt.Sprintf("Server started but never bound port %d within %s.", route.Port, timeout),
					Log:     tail,
				}
				if rec := scanLogForRecovery(tail, route.Cmd); rec != nil {
					f.Recovery = rec
				} else if pid, ok := route.PIDValue(); ok && processAlive(pid) {
					f.Recovery = &Recovery{
						Action:  "restart",
						Message: fmt.Sprintf("Process (PID %d) is running but never bound port %d. Restart it?", pid, route.Port),
						PID:     pid,
						Port:    route.Port,
					}
				}
				route.SetFailure(f)
			}
			return
		case <-ticker.C:
			// Check if the process died (don't wait for the monitor sweep).
			pid, ok := route.PIDValue()
			if ok && !processAlive(pid) {
				route.Running.Store(false)
				route.Ready.Store(false)
				route.ClearPID()
				// Scan the log tail for an actionable recovery hint (e.g.
				// Next.js's "Another dev server running — PID: 23674") so
				// whoever polls /ready can offer a one-click "kill and retry".
				route.SetFailure(failureFromLog(route.Name, "process exited before becoming ready", route.Cmd))
				return
			}
			if !ok || !route.Running.Load() {
				return
			}
			if s.isPortReady(route.Port) {
				route.Ready.Store(true)
				route.SetFailure(nil)
				// Try to auto-detect favicon if we haven't already.
				if route.AutoIcon == "" {
					go s.fetchFavicon(route)
				}
				return
			}
		}
	}
}

// failureFromLog builds a Failure by tailing the route's log file and
// running the recovery-hint scanner over it. Used when a managed process
// dies asynchronously (after Start() returned) — we don't have an error
// value from Start, only what the process wrote to its log.
func failureFromLog(routeName, message, cmd string) *Failure {
	logPath := filepath.Join(config.Dir(), routeName+".log")
	tail := tailLogFile(logPath, 12)
	f := &Failure{Message: message, Log: tail}
	if tail != "" {
		f.Recovery = scanLogForRecovery(tail, cmd)
	}
	return f
}

// fetchFavicon tries to grab a favicon from the running app and store it as a
// data URI so it works even when the app is stopped. It checks /favicon.ico
// first, then parses the HTML for a <link rel="icon"> tag.
func (s *Server) fetchFavicon(route *Route) {
	client := &http.Client{Timeout: 3 * time.Second}

	// Try /favicon.ico first — most frameworks serve one automatically.
	if dataURI := downloadAsDataURI(client, fmt.Sprintf("http://localhost:%d/favicon.ico", route.Port)); dataURI != "" {
		route.AutoIcon = dataURI
		_ = s.saveStickyRoutes()
		return
	}

	// Try parsing the homepage for a <link rel="icon"> tag.
	resp, err := client.Get(fmt.Sprintf("http://localhost:%d/", route.Port))
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}

	buf := make([]byte, 16*1024)
	n, _ := resp.Body.Read(buf)
	page := string(buf[:n])

	href := findFaviconHref(page)
	if href == "" {
		return
	}
	// Resolve relative URLs.
	if strings.HasPrefix(href, "//") {
		href = "http:" + href
	} else if strings.HasPrefix(href, "/") {
		href = fmt.Sprintf("http://localhost:%d%s", route.Port, href)
	} else if !strings.HasPrefix(href, "http://") && !strings.HasPrefix(href, "https://") && !strings.HasPrefix(href, "data:") {
		href = fmt.Sprintf("http://localhost:%d/%s", route.Port, href)
	}
	if strings.HasPrefix(href, "data:") {
		route.AutoIcon = href
		_ = s.saveStickyRoutes()
		return
	}
	if dataURI := downloadAsDataURI(client, href); dataURI != "" {
		route.AutoIcon = dataURI
		_ = s.saveStickyRoutes()
	}
}

// downloadAsDataURI fetches a URL and returns its content as a data URI.
// Returns "" if the fetch fails or the response isn't an image.
// Limits to 256KB to avoid storing huge icons.
func downloadAsDataURI(client *http.Client, url string) string {
	resp, err := client.Get(url)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		return ""
	}
	// Strip parameters from content type (e.g. "image/png; charset=utf-8" → "image/png")
	if idx := strings.Index(ct, ";"); idx != -1 {
		ct = strings.TrimSpace(ct[:idx])
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil || len(data) == 0 {
		return ""
	}
	return fmt.Sprintf("data:%s;base64,%s", ct, base64.StdEncoding.EncodeToString(data))
}

// findFaviconHref extracts the href from a <link rel="icon"> tag in HTML.
func findFaviconHref(page string) string {
	lower := strings.ToLower(page)
	for _, marker := range []string{`rel="icon"`, `rel="shortcut icon"`, `rel='icon'`, `rel='shortcut icon'`} {
		idx := strings.Index(lower, marker)
		if idx == -1 {
			continue
		}
		snippet := page[max(0, idx-200):min(len(page), idx+200)]
		hrefIdx := strings.Index(strings.ToLower(snippet), `href="`)
		if hrefIdx == -1 {
			hrefIdx = strings.Index(strings.ToLower(snippet), `href='`)
		}
		if hrefIdx == -1 {
			continue
		}
		quote := snippet[hrefIdx+5]
		rest := snippet[hrefIdx+6:]
		end := strings.IndexByte(rest, quote)
		if end == -1 {
			continue
		}
		return rest[:end]
	}
	return ""
}

func toResponse(r *Route, tld, scheme string) routeResponse {
	resp := routeResponse{
		Name:               r.Name,
		Port:               r.Port,
		PID:                r.PID.Load(),
		RegisteredAt:       r.RegisteredAt,
		ExpiresAt:          r.ExpiresAt,
		Type:               r.Type,
		Running:            r.Running.Load(),
		Ready:              r.Ready.Load(),
		ExternalURL:        r.ExternalURL,
		IdleTimeout:        r.IdleTimeout,
		Icon:               r.Icon,
		Proxy:              r.Proxy,
		InsecureSkipVerify: r.InsecureSkipVerify,
		OAuthCallbackPort:  r.OAuthCallbackPort,
	}
	if r.Type == RouteBookmark {
		resp.URL = r.ExternalURL
	} else {
		resp.URL = fmt.Sprintf("%s://%s.%s", scheme, r.Name, tld)
	}
	return resp
}
