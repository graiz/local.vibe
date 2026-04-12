package daemon

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// validName matches DNS-safe route names: lowercase letters, digits, and hyphens.
var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$|^[a-z0-9]$`)

type registerRequest struct {
	Name        string `json:"name"`
	Port        int    `json:"port"`
	PID         *int   `json:"pid,omitempty"`
	TTL         *int   `json:"ttl,omitempty"`
	Cmd         string `json:"cmd,omitempty"`
	Dir         string `json:"dir,omitempty"`
	URL         string `json:"url,omitempty"`
	IdleTimeout *int   `json:"idle_timeout,omitempty"` // minutes; 0 = never
	Icon        string `json:"icon,omitempty"`
}

type routeResponse struct {
	Name         string     `json:"name"`
	Port         int        `json:"port,omitempty"`
	PID          *int       `json:"pid,omitempty"`
	RegisteredAt time.Time  `json:"registered_at"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	Type         RouteType  `json:"type"`
	Running      bool       `json:"running"`
	Ready        bool       `json:"ready"`
	URL          string     `json:"url"`
	ExternalURL  string     `json:"external_url,omitempty"`
	IdleTimeout  int        `json:"idle_timeout,omitempty"`
	Icon         string     `json:"icon,omitempty"`
}

// apiHandler routes /_api/* requests to the appropriate handler.
func (s *Server) apiHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/_api")
	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.Method == http.MethodGet && path == "/health":
		s.handleHealth(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/routes/") && strings.HasSuffix(path, "/ready"):
		name := strings.TrimPrefix(path, "/routes/")
		name = strings.TrimSuffix(name, "/ready")
		s.handleReady(w, r, name)
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
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/stop"):
		name := strings.TrimPrefix(path, "/routes/")
		name = strings.TrimSuffix(name, "/stop")
		s.handleStop(w, r, name)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/start"):
		name := strings.TrimPrefix(path, "/routes/")
		name = strings.TrimSuffix(name, "/start")
		s.handleStart(w, r, name)
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
		resp[i] = toResponse(r, s.cfg.Daemon.TLD)
	}
	json.NewEncoder(w).Encode(resp)
}

// handleRegister creates a new route. The route type is inferred from the request:
// url → bookmark, cmd → managed, pid → pid-tracked, ttl → ttl, default → sticky.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, `{"error":"name is required"}`, http.StatusBadRequest)
		return
	}
	if req.Port == 0 && req.URL == "" {
		http.Error(w, `{"error":"port or url is required"}`, http.StatusBadRequest)
		return
	}
	req.Name = strings.ToLower(req.Name)
	if !validName.MatchString(req.Name) {
		http.Error(w, `{"error":"name must be lowercase letters, digits, or hyphens"}`, http.StatusBadRequest)
		return
	}
	if req.Name == "local" {
		http.Error(w, `{"error":"'local' is reserved for the dashboard"}`, http.StatusBadRequest)
		return
	}

	route := &Route{
		Name:         req.Name,
		Port:         req.Port,
		PID:          req.PID,
		ExternalURL:  req.URL,
		RegisteredAt: time.Now(),
	}
	route.Running.Store(true)
	route.Ready.Store(true)
	if req.IdleTimeout != nil {
		route.IdleTimeout = *req.IdleTimeout
	}
	route.Icon = req.Icon

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
		// Clear stale process on the port before launching.
		if route.Port > 0 && s.isPortReady(route.Port) {
			s.killPort(route.Port)
			time.Sleep(500 * time.Millisecond)
		}
		pid, err := s.procs.Start(route)
		if err != nil {
			s.table.Remove(route.Name)
			http.Error(w, fmt.Sprintf(`{"error":"failed to start: %s"}`, err), http.StatusInternalServerError)
			return
		}
		route.PID = &pid
		route.Running.Store(true)
		route.Ready.Store(false)
		go s.waitForReady(route)
	}

	if route.Type == RouteSticky || route.Type == RouteManaged || route.Type == RouteBookmark {
		_ = s.saveStickyRoutes()
	}

	json.NewEncoder(w).Encode(map[string]any{
		"ok":  true,
		"url": fmt.Sprintf("http://%s.%s", req.Name, s.cfg.Daemon.TLD),
	})
}

func (s *Server) handleDeregister(w http.ResponseWriter, _ *http.Request, name string) {
	route, ok := s.table.Get(name)
	if !ok {
		http.Error(w, `{"error":"route not found"}`, http.StatusNotFound)
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
		http.Error(w, `{"error":"route not found"}`, http.StatusNotFound)
		return
	}
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.Name != "" {
		req.Name = strings.ToLower(req.Name)
	}
	// If the name changed, remove old and re-add.
	if req.Name != "" && req.Name != name {
		s.table.Remove(name)
		route.Name = req.Name
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
	}
	s.table.Add(route)
	_ = s.saveStickyRoutes()
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request, name string) {
	route, ok := s.table.Get(name)
	if !ok {
		http.Error(w, `{"error":"route not found"}`, http.StatusNotFound)
		return
	}
	if route.Cmd == "" {
		http.Error(w, `{"error":"route has no launch command"}`, http.StatusBadRequest)
		return
	}

	// If the port is already in use, try to clear the stale process before starting.
	if route.Port > 0 && s.isPortReady(route.Port) {
		s.killPort(route.Port)
		// Give the OS a moment to release the port.
		time.Sleep(500 * time.Millisecond)
	}

	pid, err := s.procs.Start(route)
	if err != nil {
		route.Running.Store(false)
		route.Ready.Store(false)
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusInternalServerError)
		return
	}
	route.PID = &pid
	route.Running.Store(true)
	route.Ready.Store(false)
	route.LastActivity = time.Now()
	go s.waitForReady(route)

	// If request came from browser form, redirect back to the app.
	if r.Header.Get("Content-Type") == "application/x-www-form-urlencoded" || r.Header.Get("Accept") != "application/json" {
		http.Redirect(w, r, fmt.Sprintf("http://%s.%s/", name, s.cfg.Daemon.TLD), http.StatusSeeOther)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "pid": pid})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request, name string) {
	route, ok := s.table.Get(name)
	if !ok {
		http.Error(w, `{"error":"route not found"}`, http.StatusNotFound)
		return
	}
	// Try the ProcessManager first, then fall back to killing whatever is on the port.
	if err := s.procs.Stop(name); err != nil {
		s.killPort(route.Port)
	}
	route.Running.Store(false)
	route.Ready.Store(false)

	// If request came from browser form, redirect back to dashboard.
	if r.Header.Get("Content-Type") == "application/x-www-form-urlencoded" || r.Header.Get("Accept") != "application/json" {
		http.Redirect(w, r, fmt.Sprintf("http://local.%s/", s.cfg.Daemon.TLD), http.StatusSeeOther)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (s *Server) handleReady(w http.ResponseWriter, _ *http.Request, name string) {
	route, ok := s.table.Get(name)
	if !ok {
		json.NewEncoder(w).Encode(map[string]any{"ready": false})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"ready": s.isPortReady(route.Port)})
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
			if route.PID != nil && !processAlive(*route.PID) {
				route.Running.Store(false)
				route.Ready.Store(false)
				route.PID = nil
				return
			}
			if route.PID == nil || !route.Running.Load() {
				return
			}
			if s.isPortReady(route.Port) {
				route.Ready.Store(true)
				// Try to auto-detect favicon if we haven't already.
				if route.AutoIcon == "" {
					go s.fetchFavicon(route)
				}
				return
			}
		}
	}
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

func toResponse(r *Route, tld string) routeResponse {
	resp := routeResponse{
		Name:         r.Name,
		Port:         r.Port,
		PID:          r.PID,
		RegisteredAt: r.RegisteredAt,
		ExpiresAt:    r.ExpiresAt,
		Type:         r.Type,
		Running:      r.Running.Load(),
		Ready:        r.Ready.Load(),
		ExternalURL:  r.ExternalURL,
		IdleTimeout:  r.IdleTimeout,
		Icon:         r.Icon,
	}
	if r.Type == RouteBookmark {
		resp.URL = r.ExternalURL
	} else {
		resp.URL = fmt.Sprintf("http://%s.%s", r.Name, tld)
	}
	return resp
}
