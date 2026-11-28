package daemon

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/graiz/local.vibe/internal/cert"
	"github.com/graiz/local.vibe/internal/config"
	vdns "github.com/graiz/local.vibe/internal/dns"
	"github.com/graiz/local.vibe/internal/netprobe"
	"github.com/graiz/local.vibe/internal/peer"
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

	// TLS hot-reload state. tlsMu guards all four: the CA pair is set by
	// startTLS (a goroutine) and read by every later reloadTLSCert.
	tlsMu   sync.RWMutex
	tlsCert *tls.Certificate
	caCert  *x509.Certificate
	caKey   *ecdsa.PrivateKey

	// saveMu serializes saveStickyRoutes + the TLS leaf reload it triggers.
	// Two concurrent saves could otherwise rename an older snapshot over a
	// newer one (dropping, say, a just-recorded pid) and interleave their
	// cert/key writes.
	saveMu sync.Mutex

	oauthBridgeMu        sync.Mutex
	oauthBridgeServers   map[int]*http.Server
	oauthBridgeListeners map[int]net.Listener
	// Peer OAuth bridge bookkeeping (oauth_peer_bridge.go), guarded by
	// oauthBridgeMu: ports a local route took back (not rebound for
	// peerBridgeYieldWindow) and ports whose bind failure was already logged.
	oauthPeerYielded    map[int]time.Time
	oauthPeerBindFailed map[int]bool

	dnsServer *vdns.Server // nil unless cfg.Daemon.DNS.Enabled

	// autoStarting tracks managed routes with an on-demand start in flight,
	// so concurrent requests to a just-stopped route don't each spawn a
	// duplicate process. Keys are route names; presence means "starting".
	autoStarting sync.Map

	// pidWatchers dedupes exit watchers for PID-tracked routes: name → pid
	// currently watched. Each watcher holds a kqueue/pidfd until the process
	// dies, so re-registering the same pid must not add another.
	pidWatchers sync.Map

	// bookmarkTransports caches one *http.Transport per bookmark upstream
	// (see bookmarkTransport). Keys are "host|insecure".
	bookmarkTransports sync.Map

	// lostAccessRestarts records when each route was last restarted for
	// losing access to its working directory (lost_access.go), so a child
	// that hits the same wall again passes its 5xx through instead of
	// restart-looping. Keys are route names; values are time.Time.
	lostAccessRestarts sync.Map

	// Per-route lifecycle timers (event-based replacements for the monitor
	// sweep): a one-shot TTL-expiry timer and a self-rescheduling idle timer.
	timersMu   sync.Mutex
	ttlTimers  map[string]*time.Timer
	idleTimers map[string]*time.Timer

	// Experimental peer subsystem (cfg.Daemon.Peers): identity, pinned peer
	// list, invite state, the LAN mTLS listener, and per-peer route caches.
	// All guarded by peerMu except where a field doc says otherwise.
	// See peer_listener.go / peer_sync.go.
	peerMu         sync.Mutex
	peerIdentity   *tls.Certificate
	peerFP         string
	peerList       []peer.Peer
	peerInviteCode string
	// peerPairsInFlight counts pairings between consuming the code and
	// saving the pin; the listener stays up while it is non-zero.
	peerPairsInFlight int
	peerInviteExpires time.Time
	peerPairAttempts  int // proof checks reserved against the current invite (peerPairReserve)
	peerSrv           *http.Server
	peerLn            net.Listener
	peerStates        map[string]*peerState
	peerSyncStop      chan struct{} // closed to stop the current sync loop; nil when none runs
	// peerTransports holds one connection pool per pinned peer fingerprint,
	// shared by sync and relay. A transport per request leaked a TLS
	// connection per request (100+ to one peer within minutes).
	peerTransports map[string]*http.Transport
	peerConnSets   map[string]*connSet // live connections per peer transport

	// peersOn is the runtime peers switch (daemon.peers.enabled), flipped by
	// setPeersEnabled without a restart. Atomic: read on the request path.
	peersOn atomic.Bool
	// peerToggleMu serializes setPeersEnabled: a dashboard "on" racing a CLI
	// "off" must not interleave bring-up with teardown.
	peerToggleMu sync.Mutex
	// cfgMu serializes every mutate-and-save of s.cfg (the peers switch,
	// the dashboard view preference): saveConfig marshals the whole struct.
	cfgMu sync.Mutex
	// peerSyncLoops counts running sync loops (0 or 1); tests assert it.
	peerSyncLoops atomic.Int32

	// Discovery sources, overridable in tests (nil = real interfaces / CLI).
	discoverHostsFn  func() ([]netip.Addr, []string)
	tailscaleNodesFn func(context.Context) ([]tailscaleNode, string)
}

// NewServer creates a daemon server with the given configuration. The
// lifecycle wiring — the managed-exit handler, the env hook, and the route
// table's add/remove hooks that arm timers and clear per-route state — is
// part of construction, not Start, so a Server behaves the same whether it
// serves real listeners or is driven in-process by tests.
func NewServer(cfg *config.Config) *Server {
	s := &Server{
		cfg:                  cfg,
		table:                NewRouteTable(),
		procs:                NewProcessManager(),
		quit:                 make(chan struct{}),
		oauthBridgeServers:   make(map[int]*http.Server),
		oauthBridgeListeners: make(map[int]net.Listener),
		ttlTimers:            make(map[string]*time.Timer),
		idleTimers:           make(map[string]*time.Timer),
		peerStates:           make(map[string]*peerState),
	}
	s.procs.SetExitHandler(s.handleManagedExit)
	s.procs.SetEnvHook(s.oauthEnvForRoute)
	s.peersOn.Store(cfg.Daemon.Peers.Enabled)
	if cfg.Logging.MaxSizeMB > 0 {
		s.procs.maxLogBytes = int64(cfg.Logging.MaxSizeMB) << 20
	}
	s.table.SetHooks(s.onRouteAdded, s.onRouteRemoved)
	return s
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
	s.reconcileOAuthBridgeListenersLogged()

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

	s.httpSrv = newHTTPServer(mux)
	s.httpSrv.Addr = fmt.Sprintf("127.0.0.1:%d", s.cfg.Daemon.Port)

	pidFile := fmt.Sprintf("%s/daemon.pid", config.Dir())
	_ = os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0644)

	// Re-adopt managed children that outlived a previous daemon (e.g. across
	// `vibe daemon restart`), so they report as running rather than stopped.
	// This only re-attaches to live, listening processes — it never spawns
	// anything. It runs after the listener is bound: each survivor costs a
	// `ps` and an `lsof`, and several of them used to push startup past the
	// readiness wait `vibe dev` allows. A request that arrives before the
	// pass reaches its route adopts on demand (recoverManagedRoute).
	go s.adoptManagedOrphansOnStartup()

	go s.startUnixSocket(mux)
	// Route lifecycle is event-based (no sweep): managed-process death via
	// cmd.Wait / PID-exit watchers, TTL + idle via per-route timers, PID-tracked
	// removal via PID-exit watchers — all wired through the ProcessManager exit
	// handler and the RouteTable add/remove hooks set above.

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

	if s.peersEnabled() {
		s.loadPeerSubsystem()
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
	if s.httpSrv != nil {
		_ = s.httpSrv.Shutdown(ctx)
	}
	if s.httpsSrv != nil {
		_ = s.httpsSrv.Shutdown(ctx)
	}
	s.stopOAuthBridgeListeners()
	if s.dnsServer != nil {
		s.dnsServer.Stop()
	}
	s.peerMu.Lock()
	s.stopPeerSyncLocked()
	s.closePeerListenerLocked()
	s.peerMu.Unlock()
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
	// Owner-only: the CLI runs as the same user as the daemon, and the socket
	// grants the full API (including shell execution) to whoever can connect.
	_ = os.Chmod(s.cfg.Daemon.Socket, 0600)
	_ = newHTTPServer(handler).Serve(ln)
}

// newHTTPServer builds an http.Server with the timeouts every daemon listener
// needs. A connection that never finishes its headers, or idles forever,
// shouldn't pin a goroutine — the peer listener is reachable from the LAN,
// and even loopback listeners are better off bounded. Body sizes are capped
// per handler (the proxy paths must stream uploads untouched).
func newHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
}

func (s *Server) startTLS(handler http.Handler) {
	certsDir := s.tlsCertsDir()

	caCert, caKey, err := cert.EnsureCA(certsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: TLS CA setup failed: %v\n", err)
		return
	}
	s.tlsMu.Lock()
	s.caCert = caCert
	s.caKey = caKey
	s.tlsMu.Unlock()

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

	s.httpsSrv = newHTTPServer(handler)
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
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		seen[name] = true
		hostnames = append(hostnames, name+"."+tld)
	}
	// Peer routes are browsed through this daemon's TLS listener too, so
	// their names need SANs exactly like local routes (Chrome rejects
	// *.vibe wildcards). Deduped: a local name shadowing a peer's appears
	// once.
	for _, name := range s.peerRouteNames() {
		if !seen[name] {
			hostnames = append(hostnames, name+"."+tld)
		}
	}
	return hostnames
}

// reloadTLSCert regenerates the leaf certificate with SANs for all current routes
// and atomically swaps it into the running TLS listener.
func (s *Server) reloadTLSCert() error {
	s.tlsMu.RLock()
	caCert, caKey := s.caCert, s.caKey
	s.tlsMu.RUnlock()
	if caCert == nil || caKey == nil {
		return nil // TLS not initialized
	}
	certsDir := s.tlsCertsDir()
	hostnames := s.tlsHostnames()

	certFile, keyFile, err := cert.GenerateLeaf(certsDir, caCert, caKey, hostnames)
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
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	// Redirect HTTP → HTTPS when TLS is enabled (browser requests only;
	// /_api/ has its own handler and is unaffected).
	if s.cfg.Daemon.TLS.Enabled && r.TLS == nil {
		target := "https://" + host + r.URL.RequestURI()
		http.Redirect(w, r, target, http.StatusMovedPermanently)
		return
	}

	if name, ok := s.routeNameFromHost(host); ok {

		// local.vibe is the built-in dashboard — always serve directly.
		if name == "local" {
			if r.URL.Path == "/sw.js" {
				s.serveServiceWorker(w, r)
				return
			}
			s.serveDashboard(w, r)
			return
		}

		if route, ok := s.table.Get(name); ok {
			// Worktree routes: if the checkout was removed, prune the route
			// and redirect to the parent — on EVERY request, not just the
			// recovery path, because a running child outlives its worktree
			// and would otherwise keep serving stale content indefinitely.
			// One stat syscall; worktree routes only.
			if route.Parent != "" && s.pruneGoneWorktree(w, r, route) {
				return
			}
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
				} else if s.managedStrangerOnPort(route) {
					// The child is alive, but a foreign process now holds the
					// route's registered port — a recycled ephemeral port grabbed
					// by a stranger that even speaks HTTP, so the proxy round-trip
					// would succeed and slip its response (e.g. a 401) straight
					// through. isPortReady below is fooled by it; only the
					// ownership probe catches it. Don't proxy to the stranger —
					// serve the repair page, which polls /repair and rediscovers
					// the child's real port.
					route.Ready.Store(false)
					s.serveRepairPage(w, r, route)
					return
				}
			}
			// If the registered port isn't answering, use the repair page for
			// running managed routes (and other non-bookmark routes).
			if !s.isPortReady(route.Port) {
				route.Ready.Store(false)
				s.serveRepairPage(w, r, route)
				return
			}
			// Answering again. Only managed routes have a readiness poll to
			// flip this back; without it a sticky route stays "starting" in
			// `vibe ls` and the peer listing after one failed round-trip. A
			// failure recorded earlier (the readiness probe giving up before a
			// slow first compile bound late) is stale now — left in place it
			// would label the next brief flap "isn't running" with no retry.
			route.Ready.Store(true)
			if route.Type == RouteManaged && route.LoadFailure() != nil {
				route.SetFailure(nil)
			}
			route.TouchActivity()
			target, transport := localUpstream(route)
			proxy := httputil.NewSingleHostReverseProxy(target)
			if transport != nil {
				proxy.Transport = transport
			}
			// Dev servers increasingly gate their hot-reload WebSocket on the
			// request's Origin: Next.js 15.2+ (allowedDevOrigins) answers an
			// upgrade carrying a foreign Origin with 200 HTML instead of 101.
			// Behind vibe the Origin is always foreign — the browser sends
			// https://<name>.vibe while the server believes it is
			// localhost:<port> — so the socket never establishes, the dev
			// client retries, and after three failures calls location.reload().
			// That is a full page reload every ~20s that silently wipes client
			// state, and it reads as an app bug rather than a proxy one.
			//
			// Present the upstream with its own origin so the handshake is
			// same-origin from its point of view. Scoped to upgrades on
			// purpose: rewriting Origin on ordinary requests would break
			// apps that check it for CSRF (Auth.js compares Origin against
			// AUTH_URL on POST), and those requests work today. The browser's
			// real origin is preserved in X-Forwarded-Origin for anything
			// that genuinely needs it.
			baseDirector := proxy.Director
			proxy.Director = func(req *http.Request) {
				baseDirector(req)
				if isWebSocketUpgrade(req) {
					if orig := req.Header.Get("Origin"); orig != "" {
						req.Header.Set("X-Forwarded-Origin", orig)
					}
					rewriteOriginHeader(req, target)
				}
			}
			// The default reverse-proxy error path returns a bare 502 when the
			// upstream fails. That happens when the registered port is dead, or
			// when a squatter on a recycled port answers TCP but not HTTP (the
			// pre-proxy isPortReady dial and processAlive check are both fooled
			// by that). Hook the error path to run recovery instead — this fires
			// ONLY on a failed upstream, never on a healthy request, so the
			// ownership probing it triggers costs nothing on the hot path.
			proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
				// A client-aborted request (tab closed, Esc, canceled prefetch or
				// HMR reconnect) surfaces here as context.Canceled — the upstream
				// is healthy, the browser just went away. Treating it as an
				// upstream failure would run full recovery against a live route:
				// leaking a watcher per cancel on macOS, and (since adoptOrphan
				// can't re-adopt on Windows) wedging the route onto the start page
				// while the app is still up. Ignore it.
				if errors.Is(err, context.Canceled) || req.Context().Err() != nil {
					return
				}
				if errors.Is(err, errLostDirAccess) {
					s.restartLostAccess(rw, req, route)
					return
				}
				s.handleProxyError(rw, req, route)
			}
			bridgeModify := s.oauthBridgeModifier(route, r)
			proxy.ModifyResponse = bridgeModify
			if route.Type == RouteManaged {
				proxy.ModifyResponse = func(resp *http.Response) error {
					if s.lostDirAccess(route, resp) {
						return errLostDirAccess
					}
					if bridgeModify != nil {
						return bridgeModify(resp)
					}
					return nil
				}
			}
			proxy.ServeHTTP(w, r)
			return
		}

		// Peer routes: a name we don't serve locally may live on a paired
		// machine. Local routes always win (a table hit returned above);
		// among peers, peers.json order breaks ties inside findPeerRoute.
		// Placed before the worktree-parent redirect because an exact peer
		// match beats a heuristic parent fallback.
		if s.peersEnabled() {
			if p, _, ok := s.findPeerRoute(name); ok {
				s.proxyToPeer(w, r, p, name)
				return
			}
		}

		// A worktree host whose route is missing — never registered, or just
		// pruned because its dir vanished — goes to its parent app instead of
		// dead-ending on the "unknown route" dashboard.
		if parent, ok := worktreeParent(name); ok && s.parentKnown(parent) {
			s.redirectToParent(w, r, parent)
			return
		}
	}

	s.serveDashboard(w, r)
}

// parentKnown reports whether an app name is known to the daemon either as a
// registered route or as the Parent of at least one worktree route — parents
// are plain strings, not necessarily routes themselves.
func (s *Server) parentKnown(app string) bool {
	if _, ok := s.table.Get(app); ok {
		return true
	}
	for _, r := range s.table.List() {
		if r.Parent == app {
			return true
		}
	}
	return false
}

// redirectToParent sends a request for a dead or unknown worktree host to its
// parent app. 307, never 301 — a permanent redirect would be cached by the
// browser and poison a future worktree that reuses the same branch name.
func (s *Server) redirectToParent(w http.ResponseWriter, r *http.Request, parent string) {
	http.Redirect(w, r, s.routeURL(parent)+"/", http.StatusTemporaryRedirect)
}

// managedOwnerCheckInterval bounds how often the managed request hot path
// re-verifies that its registered port is still held by the route's own
// process group. The probe shells out to lsof, so a recent "healthy" result is
// cached this long to keep the hot path cheap; the window also bounds how long
// a freshly-arrived stranger can be proxied before it's caught.
const managedOwnerCheckInterval = 3 * time.Second

// managedStrangerOnPort reports whether a foreign process has taken over the
// route's registered port, throttling the (lsof-backed) ownership probe so the
// request hot path stays cheap. A recent healthy result short-circuits; a
// foreign result or a cold/stale cache triggers a fresh probe. It only ever
// returns true on a positive foreign identification (see portForeignToRoute),
// so a healthy route is never sent into recovery.
func (s *Server) managedStrangerOnPort(route *Route) bool {
	now := time.Now().UnixNano()
	last := route.ownerCheckedUnixNano.Load()
	if last != 0 && now-last < int64(managedOwnerCheckInterval) && !route.ownerForeign.Load() {
		return false
	}
	// Single-flight the lsof probe. A page load fires many parallel asset
	// requests; without this, every one that sees a cold or stale cache forks
	// its own lsof pipeline synchronously on the request path — a process storm
	// that stalls all of them. Only the request that wins the CAS probes; the
	// rest serve the last cached verdict.
	if !route.ownerChecking.CompareAndSwap(false, true) {
		return route.ownerForeign.Load()
	}
	defer route.ownerChecking.Store(false)
	// Re-check under the guard: a prober that just finished may have refreshed
	// the cache while we contended for the CAS.
	last = route.ownerCheckedUnixNano.Load()
	if last != 0 && now-last < int64(managedOwnerCheckInterval) && !route.ownerForeign.Load() {
		return false
	}
	foreign := s.portForeignToRoute(route)
	route.ownerForeign.Store(foreign)
	route.ownerCheckedUnixNano.Store(time.Now().UnixNano())
	return foreign
}

// handleProxyError runs when a proxied upstream fails — connection refused, or
// an empty/garbage reply from a process squatting a recycled port. It is the
// reverse proxy's ErrorHandler, so it executes only on that failure path and
// never on a healthy request; the recovery probing below (process-group
// ownership via lsof, port dial) therefore adds no per-request cost.
//
// Instead of surfacing an opaque 502, it re-checks the route's ground truth.
// For managed routes recoverManagedRoute re-verifies port ownership and either
// re-adopts a surviving child, shows the start page when a stranger holds the
// port, auto-spawns when it's free, or shows the start page — never proxies
// blindly into whatever now occupies the port. Other route types get the
// repair page so the client retries once the port answers again.
func (s *Server) handleProxyError(w http.ResponseWriter, r *http.Request, route *Route) {
	route.Ready.Store(false)
	forgetUpstreamScheme(route)
	if route.Type == RouteManaged {
		if s.recoverManagedRoute(w, r, route) {
			return
		}
		// recoverManagedRoute re-adopted a live child (the registered port is
		// served by our own group again); fall through to the repair page so the
		// client retries into the now-healthy proxy.
	}
	s.serveRepairPage(w, r, route)
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

// killPort terminates external processes listening on the given TCP port. Used
// by the recovery flow to clear an EADDRINUSE before retrying a managed route's
// start. The PID-discovery and signal calls live in per-OS files.
//
// It refuses to signal the daemon itself or another managed route — mirroring
// safeKillPID. This matters when a managed route's primary port collides with a
// port the daemon already binds for another route (an oauth_callback_port or
// reserve_port): without the guard, preflightPort → killPort would SIGTERM the
// daemon and take down every route (observed as the daemon "restarting" on every
// start attempt). A genuine port collision then surfaces as a clean start error
// instead of a daemon suicide.
func (s *Server) killPort(port int) {
	myPID := os.Getpid()
	for _, pid := range findPortHoldersFn(port) {
		switch {
		case pid == myPID:
			fmt.Fprintf(os.Stderr, "vibe: killPort(%d) refusing to signal the daemon itself\n", port)
		case s.procs.OwnsPID(pid):
			fmt.Fprintf(os.Stderr, "vibe: killPort(%d) refusing to signal managed route process %d — stop the route instead\n", port, pid)
		default:
			_ = terminateProcessFn(pid)
		}
	}
}

// terminateProcessFn signals a PID; indirected through a var so tests can
// observe killPort's targeting without actually killing processes.
var terminateProcessFn = terminateProcess

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
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
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

// saveConfig writes s.cfg to config.json. Caller holds cfgMu.
func (s *Server) saveConfig() error {
	path := filepath.Join(s.configDir(), "config.json")
	data, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
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
		// The bind probe alone is not enough: Go sets SO_REUSEADDR, and on
		// macOS a 127.0.0.1 bind then succeeds beside a server on :N, [::]:N,
		// [::1]:N (Vite) or 0.0.0.0:N. Handing such a port out was worse than
		// a crash — the start preflight then SIGTERMed the server holding it.
		// A port that answers on either loopback address is taken.
		if conn, err := netprobe.DialLoopback(port, 150*time.Millisecond); err == nil {
			conn.Close()
			continue
		}
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

// isPortReady reports whether something accepts TCP connections on the
// loopback port (IPv4 or IPv6 — see netprobe.DialLoopback).
func (s *Server) isPortReady(port int) bool {
	conn, err := netprobe.DialLoopback(port, 300*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
