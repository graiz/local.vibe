package daemon

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/graiz/local.vibe/internal/cert"
	"github.com/graiz/local.vibe/internal/config"
	vdns "github.com/graiz/local.vibe/internal/dns"
)

// Server is the vibe daemon. It listens on a TCP port and a Unix socket,
// routing HTTP requests to registered local services by subdomain.
type Server struct {
	cfg          *config.Config
	table        *RouteTable
	procs        *ProcessManager
	startedAt    time.Time
	quit         chan struct{}
	httpSrv      *http.Server
	httpsSrv     *http.Server
	ReadyTimeout time.Duration // max time to wait for port to accept connections; 0 = 30s default
	ConfigDir    string        // override config dir for persistence; empty = default (~/.vibe)

	// TLS hot-reload state
	tlsMu   sync.RWMutex
	tlsCert *tls.Certificate
	caCert  *x509.Certificate
	caKey   *ecdsa.PrivateKey

	oauthBridgeMu        sync.Mutex
	oauthBridgeServers   map[int]*http.Server
	oauthBridgeListeners map[int]net.Listener

	dnsServer *vdns.Server // nil unless cfg.Daemon.DNS.Enabled

	// autoStarting tracks managed routes with an on-demand start in flight,
	// so concurrent requests to a just-stopped route don't each spawn a
	// duplicate process. Keys are route names; presence means "starting".
	autoStarting sync.Map
}

// NewServer creates a daemon server with the given configuration.
func NewServer(cfg *config.Config) *Server {
	return &Server{
		cfg:                  cfg,
		table:                NewRouteTable(),
		procs:                NewProcessManager(),
		quit:                 make(chan struct{}),
		oauthBridgeServers:   make(map[int]*http.Server),
		oauthBridgeListeners: make(map[int]net.Listener),
	}
}

// Start binds the TCP port, loads persisted routes, starts the Unix socket
// listener and route monitor, and serves HTTP until shutdown.
func (s *Server) Start() error {
	if err := os.MkdirAll(config.Dir(), 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	if err := loadStickyRoutes(s.table, s.configDir()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not load persisted routes: %v\n", err)
	}
	// Re-adopt managed children that outlived a previous daemon (e.g. across
	// `vibe daemon restart`), so they report as running immediately rather than
	// stopped. This only re-attaches to live, listening processes — it never
	// spawns anything.
	s.adoptManagedOrphansOnStartup()
	if err := s.reconcileOAuthBridgeListeners(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not start oauth callback bridge listeners: %v\n", err)
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

	if s.cfg.Daemon.TLS.Enabled {
		go s.startTLS(mux)
	}

	if s.cfg.Daemon.DNS.Enabled {
		s.dnsServer = vdns.New(vdns.Config{
			TLD:      s.cfg.Daemon.TLD,
			Listen:   s.cfg.Daemon.DNS.Listen,
			Upstream: s.cfg.Daemon.DNS.Upstream,
		})
		if err := s.dnsServer.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: DNS resolver failed to start (%v) — *.vibe lookups won't work until this is fixed\n", err)
			s.dnsServer = nil
		} else {
			fmt.Printf("vibe daemon DNS on %s\n", s.dnsServer.Listen())
		}
	}

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
	if s.httpsSrv != nil {
		_ = s.httpsSrv.Shutdown(ctx)
	}
	s.stopOAuthBridgeListeners()
	if s.dnsServer != nil {
		s.dnsServer.Stop()
	}
	_ = os.Remove(s.cfg.Daemon.Socket)
	_ = os.Remove(fmt.Sprintf("%s/daemon.pid", config.Dir()))
}

// startUnixSocket runs the same HTTP handler over a Unix domain socket so
// the CLI can talk to the daemon without going through TCP. On Windows
// net.Listen("unix", ...) fails (UDS support exists on recent Windows but
// Go's stdlib doesn't expose it on this code path), and we fall through to
// TCP-only — the warning is logged but the daemon still starts.
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

func (s *Server) startTLS(handler http.Handler) {
	certsDir := s.tlsCertsDir()

	caCert, caKey, err := cert.EnsureCA(certsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: TLS CA setup failed: %v\n", err)
		return
	}
	s.caCert = caCert
	s.caKey = caKey

	// Generate initial cert with current route names
	if err := s.reloadTLSCert(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: TLS cert setup failed: %v\n", err)
		return
	}

	tlsConfig := &tls.Config{
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			s.tlsMu.RLock()
			defer s.tlsMu.RUnlock()
			return s.tlsCert, nil
		},
	}

	ln, err := tls.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.cfg.Daemon.TLS.Port), tlsConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: HTTPS listener failed: %v\n", err)
		return
	}

	s.httpsSrv = &http.Server{Handler: handler}
	fmt.Printf("vibe daemon HTTPS on 127.0.0.1:%d\n", s.cfg.Daemon.TLS.Port)
	if err := s.httpsSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "HTTPS server error: %v\n", err)
	}
}

// tlsHostnames builds the SAN list from the current route table.
// Includes local.{tld} (dashboard) plus {name}.{tld} for every registered route.
func (s *Server) tlsHostnames() []string {
	tld := s.cfg.Daemon.TLD
	names := s.table.Names()
	hostnames := make([]string, 0, len(names)+2)
	hostnames = append(hostnames, "local."+tld) // dashboard always
	for _, name := range names {
		hostnames = append(hostnames, name+"."+tld)
	}
	return hostnames
}

// reloadTLSCert regenerates the leaf certificate with SANs for all current routes
// and atomically swaps it into the running TLS listener.
func (s *Server) reloadTLSCert() error {
	if s.caCert == nil || s.caKey == nil {
		return nil // TLS not initialized
	}
	certsDir := s.tlsCertsDir()
	hostnames := s.tlsHostnames()

	certFile, keyFile, err := cert.GenerateLeaf(certsDir, s.caCert, s.caKey, hostnames)
	if err != nil {
		return err
	}

	tlsCert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return fmt.Errorf("load new TLS cert: %w", err)
	}

	s.tlsMu.Lock()
	s.tlsCert = &tlsCert
	s.tlsMu.Unlock()
	return nil
}

func (s *Server) tlsCertsDir() string {
	certsDir := s.cfg.Daemon.TLS.CertsDir
	if certsDir == "" {
		certsDir = filepath.Join(config.Dir(), "certs")
	}
	return certsDir
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

	// Redirect HTTP → HTTPS when TLS is enabled (browser requests only;
	// /_api/ has its own handler and is unaffected).
	if s.cfg.Daemon.TLS.Enabled && r.TLS == nil {
		target := "https://" + host + r.URL.RequestURI()
		http.Redirect(w, r, target, http.StatusMovedPermanently)
		return
	}

	if strings.HasSuffix(host, "."+s.cfg.Daemon.TLD) {
		name := strings.TrimSuffix(host, "."+s.cfg.Daemon.TLD)

		// local.vibe is the built-in dashboard — always serve directly.
		if name == "local" {
			s.serveDashboard(w, r)
			return
		}

		if route, ok := s.table.Get(name); ok {
			// Bookmarks: reverse-proxy when Proxy=true so the browser keeps
			// the .vibe host in the URL bar; otherwise 307-redirect to the
			// external URL.
			if route.Type == RouteBookmark && route.ExternalURL != "" {
				if route.Proxy {
					s.proxyBookmark(w, r, route, host)
					return
				}
				http.Redirect(w, r, route.ExternalURL, http.StatusTemporaryRedirect)
				return
			}
			// Managed routes must never proxy when the route is marked stopped,
			// even if another process is now listening on the old port.
			if route.Type == RouteManaged {
				pid, hasPID := route.PIDValue()
				if !route.Running.Load() || !hasPID || !processAlive(pid) {
					// Stopped, or the tracked process died: attempt on-demand
					// recovery — re-adopt a surviving child, auto-spawn it, or
					// fall back to the manual start page. recoverManagedRoute
					// writes the response itself unless it adopted a live
					// process, in which case it returns false and we fall
					// through to the readiness check + proxy below.
					if s.recoverManagedRoute(w, r, route) {
						return
					}
				}
			}
			// If the registered port isn't answering, use the repair page for
			// running managed routes (and other non-bookmark routes).
			if !s.isPortReady(route.Port) {
				route.Ready.Store(false)
				s.serveRepairPage(w, r, route)
				return
			}
			route.TouchActivity()
			target, _ := url.Parse(fmt.Sprintf("http://localhost:%d", route.Port))
			proxy := httputil.NewSingleHostReverseProxy(target)
			proxy.ServeHTTP(w, r)
			return
		}
	}

	s.serveDashboard(w, r)
}

// safeKillPID sends a termination signal to an arbitrary PID on behalf of
// a user-confirmed recovery action. Refuses to signal pid ≤ 1, the daemon
// itself, or any PID the daemon already manages — those should be stopped
// via the route's Stop handler instead so ProcessManager state stays
// consistent. The actual signalling lives in daemon_<goos>.go: SIGTERM on
// unix, TerminateProcess on Windows (no graceful equivalent exists).
func (s *Server) safeKillPID(pid int) error {
	if pid <= 1 {
		return fmt.Errorf("invalid pid")
	}
	if pid == os.Getpid() {
		return fmt.Errorf("refusing to kill the daemon")
	}
	if s.procs.OwnsPID(pid) {
		return fmt.Errorf("pid is a managed vibe route; stop the route instead")
	}
	if !processAlive(pid) {
		return fmt.Errorf("process not found")
	}
	return terminateProcess(pid)
}

// killPort terminates every process listening on the given TCP port. Used
// by the recovery flow to clear an EADDRINUSE before retrying a managed
// route's start. The PID-discovery and signal calls live in per-OS files.
func (s *Server) killPort(port int) {
	for _, pid := range findPortHoldersFn(port) {
		_ = terminateProcess(pid)
	}
}

// findPortHoldersFn returns the listening PIDs on a TCP port. The default
// implementation is per-OS (lsof on unix, netstat on Windows). Tests swap
// this var to inject deterministic results.
var findPortHoldersFn = findPortHoldersDefault

// pidCommandFn returns a short command name for a pid. Per-OS default
// implementations live in daemon_<goos>.go.
var pidCommandFn = pidCommandDefault

// buildPortConflictRecovery builds a Recovery hint for a "port already in use"
// situation by inspecting which process is holding the port. Filters out the
// daemon itself and any PID currently owned by another managed route (those
// should be stopped via the route's Stop handler, not signaled directly).
//
// Returns nil if the port is no longer held by any external process — the
// caller should treat that as a transient condition and not surface a hint.
func (s *Server) buildPortConflictRecovery(port int) *Recovery {
	pids := findPortHoldersFn(port)
	myPID := os.Getpid()
	external := make([]int, 0, len(pids))
	for _, pid := range pids {
		if pid == myPID {
			continue
		}
		if s.procs.OwnsPID(pid) {
			continue
		}
		external = append(external, pid)
	}
	if len(external) == 0 {
		return nil
	}
	if len(external) == 1 {
		pid := external[0]
		cmd := pidCommandFn(pid)
		msg := fmt.Sprintf("Port %d is held by PID %d", port, pid)
		if cmd != "" {
			msg += fmt.Sprintf(" (%s)", cmd)
		}
		msg += ". Kill it and retry?"
		return &Recovery{Action: "kill_pid", PID: pid, Message: msg}
	}
	return &Recovery{
		Action:  "kill_port",
		Port:    port,
		Message: fmt.Sprintf("Port %d is held by %d processes. Kill them all and retry?", port, len(external)),
	}
}

func (s *Server) configDir() string {
	if s.ConfigDir != "" {
		return s.ConfigDir
	}
	return config.Dir()
}

func (s *Server) saveStickyRoutes() error {
	if err := saveStickyRoutes(s.table, s.configDir()); err != nil {
		return err
	}
	// Regenerate TLS cert with updated route names
	if s.cfg.Daemon.TLS.Enabled {
		if err := s.reloadTLSCert(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: TLS cert reload failed: %v\n", err)
		}
	}
	return nil
}

func (s *Server) saveConfig() {
	path := filepath.Join(s.configDir(), "config.json")
	data, _ := json.MarshalIndent(s.cfg, "", "  ")
	_ = os.WriteFile(path, data, 0644)
}

// findFreePort finds an available port starting from 3000, which is the
// conventional range for local dev servers. It skips ports already claimed
// by other vibe routes (even if not currently listening). Falls back to OS
// assignment if the entire 3000-3999 range is occupied.
func findFreePort(table *RouteTable) (int, error) {
	claimed := make(map[int]bool)
	for _, r := range table.List() {
		if r.Port > 0 {
			claimed[r.Port] = true
		}
		if r.OAuthCallbackPort > 0 {
			claimed[r.OAuthCallbackPort] = true
		}
		for _, p := range r.ReservePorts {
			if p > 0 {
				claimed[p] = true
			}
		}
	}
	for port := 3000; port < 4000; port++ {
		if claimed[port] {
			continue
		}
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		l.Close()
		return port, nil
	}
	// Fallback: let the OS pick.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port, nil
}

func (s *Server) isPortReady(port int) bool {
	// Try IPv4 first, then IPv6 — some tools (e.g. vite) only bind to [::1].
	for _, addr := range []string{
		fmt.Sprintf("127.0.0.1:%d", port),
		fmt.Sprintf("[::1]:%d", port),
	} {
		conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
	}
	return false
}
