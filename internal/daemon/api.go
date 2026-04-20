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
// log tail (if present) for an actionable recovery hint.
func failureFromError(err error) *Failure {
	if err == nil {
		return nil
	}
	var se *StartError
	f := &Failure{Message: err.Error()}
	if errors.As(err, &se) {
		f.Message = se.Err.Error()
		f.Log = se.Tail
		f.Recovery = scanLogForRecovery(se.Tail)
	}
	return f
}

// writeStartFailure sends a managed-process start error with the log tail and
// a recovery hint attached when one of the known patterns matches. The
// dashboard surfaces the hint as a one-click "kill and retry" button.
func writeStartFailure(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	f := failureFromError(err)
	resp := map[string]any{"error": f.Message}
	if f.Log != "" {
		resp["log"] = f.Log
	}
	if f.Recovery != nil {
		resp["recovery"] = f.Recovery
	}
	_ = json.NewEncoder(w).Encode(resp)
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
	Name               string `json:"name"`
	Port               int    `json:"port"`
	PID                *int   `json:"pid,omitempty"`
	TTL                *int   `json:"ttl,omitempty"`
	Cmd                string `json:"cmd,omitempty"`
	Dir                string `json:"dir,omitempty"`
	URL                string `json:"url,omitempty"`
	IdleTimeout        *int   `json:"idle_timeout,omitempty"` // minutes; 0 = never
	Icon               string `json:"icon,omitempty"`
	Proxy              *bool  `json:"proxy,omitempty"`                // bookmark: reverse-proxy instead of 307-redirect
	InsecureSkipVerify *bool  `json:"insecure_skip_verify,omitempty"` // bookmark+proxy: skip upstream TLS verify
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
}

// apiHandler routes /_api/* requests to the appropriate handler.
func (s *Server) apiHandler(w http.ResponseWriter, r *http.Request) {
	// The daemon API is only meant for the dashboard host (local.vibe) and
	// localhost/unix-socket CLI callers. On any other .vibe host, /_api/*
	// belongs to the proxied upstream — e.g. Jekyll Admin fetches
	// /_api/configuration — so fall through to the normal request router.
	host := r.Host
	if idx := strings.Index(host, ":"); idx != -1 {
		host = host[:idx]
	}
	if strings.HasSuffix(host, "."+s.cfg.Daemon.TLD) {
		name := strings.TrimSuffix(host, "."+s.cfg.Daemon.TLD)
		if name != "local" {
			s.routeRequest(w, r)
			return
		}
	}

	path := strings.TrimPrefix(r.URL.Path, "/_api")
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
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
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
	route.Icon = req.Icon
	if req.Proxy != nil {
		route.Proxy = *req.Proxy
	}
	if req.InsecureSkipVerify != nil {
		route.InsecureSkipVerify = *req.InsecureSkipVerify
	}

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
		// Clear stale process on the port before launching.
		if route.Port > 0 && s.isPortReady(route.Port) {
			s.killPort(route.Port)
			time.Sleep(500 * time.Millisecond)
			// Verify the port was actually freed.
			if s.isPortReady(route.Port) {
				s.table.Remove(route.Name)
				writeJSONError(w, http.StatusConflict, fmt.Sprintf("port %d is already in use by another process", route.Port))
				return
			}
		}
		pid, err := s.procs.Start(route)
		if err != nil {
			s.table.Remove(route.Name)
			writeStartFailure(w, http.StatusInternalServerError, err)
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
	if req.Port != 0 {
		route.Port = req.Port
	}
	if req.IdleTimeout != nil {
		route.IdleTimeout = *req.IdleTimeout
	}
	if req.Icon != "" {
		route.Icon = req.Icon
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
	s.table.Add(route)
	_ = s.saveStickyRoutes()
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request, name string) {
	route, ok := s.table.Get(name)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "route not found")
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
	// If the port is already in use, try to clear the stale process before starting.
	if route.Port > 0 && s.isPortReady(route.Port) {
		s.killPort(route.Port)
		// Give the OS a moment to release the port.
		time.Sleep(500 * time.Millisecond)
		// Verify the port was actually freed.
		if s.isPortReady(route.Port) {
			writeJSONError(w, http.StatusConflict, fmt.Sprintf("port %d is already in use by another process", route.Port))
			return
		}
	}

	pid, err := s.procs.Start(route)
	if err != nil {
		route.Running.Store(false)
		route.Ready.Store(false)
		route.SetFailure(failureFromError(err))
		writeStartFailure(w, http.StatusInternalServerError, err)
		return
	}
	route.SetPID(pid)
	route.Running.Store(true)
	route.Ready.Store(false)
	route.SetFailure(nil)
	route.TouchActivity()
	go s.waitForReady(route)

	// If request came from browser form, redirect back to the app.
	if r.Header.Get("Content-Type") == "application/x-www-form-urlencoded" || r.Header.Get("Accept") != "application/json" {
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
	if r.Header.Get("Content-Type") == "application/x-www-form-urlencoded" || r.Header.Get("Accept") != "application/json" {
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
//   { ok: true,  port: 3001, from: 3000 }    // auto-resolved
//   { ok: true,  port: 3000, note: "..." }   // nothing to fix, already reachable
//   { ok: false, port: 3000, reason: "...", restartable: true } // no candidate found
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
				route.SetFailure(failureFromLog(route.Name, "process exited before becoming ready"))
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
func failureFromLog(routeName, message string) *Failure {
	logPath := filepath.Join(config.Dir(), routeName+".log")
	tail := tailLogFile(logPath, 12)
	f := &Failure{Message: message, Log: tail}
	if tail != "" {
		f.Recovery = scanLogForRecovery(tail)
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
	}
	if r.Type == RouteBookmark {
		resp.URL = r.ExternalURL
	} else {
		resp.URL = fmt.Sprintf("%s://%s.%s", scheme, r.Name, tld)
	}
	return resp
}
