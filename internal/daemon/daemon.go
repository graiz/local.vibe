package daemon

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/localvibe/vibe/internal/config"
)

// Server is the vibe daemon. It listens on a TCP port and a Unix socket,
// routing HTTP requests to registered local services by subdomain.
type Server struct {
	cfg       *config.Config
	table     *RouteTable
	procs     *ProcessManager
	startedAt time.Time
	quit      chan struct{}
	httpSrv   *http.Server
}

// NewServer creates a daemon server with the given configuration.
func NewServer(cfg *config.Config) *Server {
	return &Server{
		cfg:   cfg,
		table: NewRouteTable(),
		procs: NewProcessManager(),
		quit:  make(chan struct{}),
	}
}

// Start binds the TCP port, loads persisted routes, starts the Unix socket
// listener and route monitor, and serves HTTP until shutdown.
func (s *Server) Start() error {
	if err := os.MkdirAll(config.Dir(), 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	if err := loadStickyRoutes(s.table); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not load persisted routes: %v\n", err)
	}

	s.startedAt = time.Now()

	mux := http.NewServeMux()
	mux.HandleFunc("/_api/", s.apiHandler)
	mux.HandleFunc("/setup.md", s.serveSetupMD)
	mux.HandleFunc("/", s.routeRequest)

	// Bind the TCP port first — if this fails (address in use), we exit
	// without touching the PID file so isDaemonRunning() stays accurate.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.cfg.Daemon.Port))
	if err != nil {
		return err
	}

	s.httpSrv = &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", s.cfg.Daemon.Port),
		Handler: mux,
	}

	pidFile := fmt.Sprintf("%s/daemon.pid", config.Dir())
	_ = os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0644)

	go s.startUnixSocket(mux)
	go s.monitorRoutes(time.Duration(s.cfg.Daemon.PIDCheckInterval) * time.Second)

	fmt.Printf("vibe daemon listening on 127.0.0.1:%d\n", s.cfg.Daemon.Port)

	if err := s.httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Stop gracefully shuts down the HTTP server and cleans up the socket and PID file.
func (s *Server) Stop() {
	close(s.quit)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.httpSrv.Shutdown(ctx)
	_ = os.Remove(s.cfg.Daemon.Socket)
	_ = os.Remove(fmt.Sprintf("%s/daemon.pid", config.Dir()))
}

func (s *Server) startUnixSocket(handler http.Handler) {
	if err := os.Remove(s.cfg.Daemon.Socket); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "warning: could not remove stale socket: %v\n", err)
	}
	ln, err := net.Listen("unix", s.cfg.Daemon.Socket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: unix socket unavailable: %v\n", err)
		return
	}
	_ = os.Chmod(s.cfg.Daemon.Socket, 0666) // allow non-root CLI to connect
	srv := &http.Server{Handler: handler}
	_ = srv.Serve(ln)
}

// routeRequest is the catch-all HTTP handler. It inspects the Host header
// to determine which registered service should handle the request:
//   - local.vibe → dashboard
//   - bookmark routes → 307 redirect to external URL
//   - managed routes with stopped process → start page
//   - all other known routes → reverse proxy to local port
//   - unknown hosts → dashboard with "not found" banner
func (s *Server) routeRequest(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if idx := strings.Index(host, ":"); idx != -1 {
		host = host[:idx]
	}

	if strings.HasSuffix(host, "."+s.cfg.Daemon.TLD) {
		name := strings.TrimSuffix(host, "."+s.cfg.Daemon.TLD)

		// local.vibe is the built-in dashboard — always serve directly.
		if name == "local" {
			s.serveDashboard(w, r)
			return
		}

		if route, ok := s.table.Get(name); ok {
			// Bookmarks redirect to the external URL.
			if route.Type == RouteBookmark && route.ExternalURL != "" {
				http.Redirect(w, r, route.ExternalURL, http.StatusTemporaryRedirect)
				return
			}
			// For managed routes, check if the port is actually accepting connections.
			if route.Type == RouteManaged && !s.isPortReady(route.Port) {
				route.Healthy = false
				s.serveStartPage(w, r, route)
				return
			}
			route.LastActivity = time.Now()
			target, _ := url.Parse(fmt.Sprintf("http://localhost:%d", route.Port))
			proxy := httputil.NewSingleHostReverseProxy(target)
			proxy.ServeHTTP(w, r)
			return
		}
	}

	s.serveDashboard(w, r)
}

func (s *Server) killPort(port int) {
	// Use lsof to find the PID holding the port, then SIGTERM it.
	out, err := exec.Command("lsof", "-ti", fmt.Sprintf("tcp:%d", port)).Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var pid int
		if _, err := fmt.Sscan(line, &pid); err == nil && pid > 0 {
			if p, err := os.FindProcess(pid); err == nil {
				_ = p.Signal(syscall.SIGTERM)
			}
		}
	}
}

func (s *Server) isPortReady(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
