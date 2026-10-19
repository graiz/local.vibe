package daemon

// The experimental peer subsystem's LAN-facing surface. Everything a paired
// machine can reach lives behind the mTLS listener built here; the loopback
// API, dashboard, and TLS listeners are untouched by this feature. The
// listener's whole contract is: an authenticated peer may read this
// machine's route names and send HTTP to its routes — never operate the
// daemon. See docs/superpowers/specs/2026-08-22-peer-discovery-design.md.

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/graiz/local.vibe/internal/peer"
)

func (s *Server) peersEnabled() bool { return s.cfg.Daemon.Peers.Enabled }

// loadPeerSubsystem loads peers.json and, when at least one peer is already
// paired, brings up the identity, the LAN listener, and the sync loop. An
// enabled-but-unpaired daemon exposes nothing to the network — the listener
// first starts when an invite is opened or a peer exists.
func (s *Server) loadPeerSubsystem() {
	if !s.peersEnabled() {
		return
	}
	peers, err := peer.LoadPeers(s.configDir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not load peers.json: %v\n", err)
		return
	}
	s.peerMu.Lock()
	s.peerList = peers
	s.peerMu.Unlock()
	if len(peers) == 0 {
		return
	}
	if err := s.ensurePeerIdentity(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		return
	}
	if err := s.ensurePeerListener(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		return
	}
	go s.peerSyncLoop()
}

// ensurePeerIdentity lazily loads or creates the peer identity cert. Kept in
// the TLS certs dir alongside the browser CA, but never signed by or
// trusted through it.
func (s *Server) ensurePeerIdentity() error {
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	if s.peerIdentity != nil {
		return nil
	}
	id, err := peer.EnsureIdentity(s.tlsCertsDir())
	if err != nil {
		return fmt.Errorf("peer identity: %w", err)
	}
	s.peerIdentity = &id
	s.peerFP = peer.IdentityFingerprint(id)
	return nil
}

// ensurePeerListener starts the LAN mTLS listener if it isn't running.
// Idempotent. Port 0 (tests) binds an OS-assigned port, readable back from
// s.peerLn.Addr().
func (s *Server) ensurePeerListener() error {
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	if s.peerLn != nil {
		return nil
	}
	if s.peerIdentity == nil {
		return fmt.Errorf("peer listener: identity not initialized")
	}
	sc := peer.ServerTLSConfig(*s.peerIdentity, s.peerCertAuthorized)
	ln, err := tls.Listen("tcp", fmt.Sprintf(":%d", s.cfg.Daemon.Peers.Port), sc)
	if err != nil {
		return fmt.Errorf("peer listener: %w", err)
	}
	s.peerLn = ln
	s.peerSrv = &http.Server{Handler: http.HandlerFunc(s.peerHandler)}
	go s.peerSrv.Serve(ln)
	fmt.Printf("vibe peer listener on %s (mTLS, paired peers only)\n", ln.Addr())
	return nil
}

// peerCertAuthorized is the TLS-handshake gate: a client cert is admitted
// when its fingerprint is pinned, or — solely so /peer/pair is reachable —
// while an invite is open. The HTTP layer still confines an unpinned cert
// to the pairing endpoint.
func (s *Server) peerCertAuthorized(fp string) bool {
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	for _, p := range s.peerList {
		if p.Fingerprint == fp {
			return true
		}
	}
	return s.peerInviteCode != "" && time.Now().Before(s.peerInviteExpires)
}

// peerByFingerprint returns the pinned peer presenting fp, or nil.
func (s *Server) peerByFingerprint(fp string) *peer.Peer {
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	for i := range s.peerList {
		if s.peerList[i].Fingerprint == fp {
			p := s.peerList[i]
			return &p
		}
	}
	return nil
}

// peerHandler dispatches requests on the LAN peer listener. Order is
// security-load-bearing: the /_api blackhole comes first so the daemon API
// can never be reached through this listener, pairing is the only endpoint
// open to a not-yet-pinned cert (and only while an invite is open — the TLS
// authorize callback already enforced that), and everything else requires a
// pinned peer.
func (s *Server) peerHandler(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/_api/") || r.URL.Path == "/_api" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	fp := peer.Fingerprint(r.TLS.PeerCertificates[0].Raw)
	if r.Method == http.MethodPost && r.URL.Path == "/peer/pair" {
		s.handlePeerPair(w, r, fp)
		return
	}
	if s.peerByFingerprint(fp) == nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/peer/routes" {
		s.handlePeerRoutes(w, r)
		return
	}
	s.peerServeRoute(w, r)
}

// handlePeerRoutes returns the read-only route list a paired machine may
// see: every non-bookmark route's summary, sorted by name, with a strong
// ETag so the 30s poll on the other side is a header exchange when nothing
// changed. Bookmarks are excluded — they proxy third-party content and are
// not this machine's to share.
func (s *Server) handlePeerRoutes(w http.ResponseWriter, r *http.Request) {
	routes := s.table.List()
	summaries := make([]peer.RouteSummary, 0, len(routes))
	var live []int // indices into summaries, paired with livePorts
	var livePorts []int
	for _, rt := range routes {
		if rt.Type == RouteBookmark {
			continue
		}
		if rt.Type != RouteManaged {
			// Only managed routes have a lifecycle that keeps these flags
			// honest (exit watchers, the readiness probe). A sticky route's
			// flags are set once at register and never cleared, so a dark
			// port would read "ready" on the browsing machine while the relay
			// — which dials live — fails. Ask the port instead, below.
			live = append(live, len(summaries))
			livePorts = append(livePorts, rt.Port)
		}
		summaries = append(summaries, peer.RouteSummary{
			Name:    rt.Name,
			Type:    string(rt.Type),
			Running: rt.Running.Load(),
			Ready:   rt.Ready.Load(),
			Icon:    rt.Icon,
		})
	}
	// Concurrently: a dark port can cost the full dial timeout (always on
	// Windows, which retries refused loopback connects), and peers poll this
	// with a 3s client timeout. Each goroutine writes only its own element.
	var wg sync.WaitGroup
	for k, i := range live {
		wg.Add(1)
		go func(sum *peer.RouteSummary, port int) {
			defer wg.Done()
			sum.Ready = s.isPortReady(port)
			sum.Running = sum.Ready
		}(&summaries[i], livePorts[k])
	}
	wg.Wait()
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].Name < summaries[j].Name })
	body, err := json.Marshal(summaries)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sum := sha256.Sum256(body)
	etag := fmt.Sprintf("%q", hex.EncodeToString(sum[:]))
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

// discardWriter swallows a handler's response. peerServeRoute uses it to run
// recoverManagedRoute purely for its adopt/spawn side effects — the
// interactive pages it renders POST to /_api/, which this listener
// blackholes, so the remote viewer gets the static starting page instead.
type discardWriter struct{ h http.Header }

func (d *discardWriter) Header() http.Header {
	if d.h == nil {
		d.h = make(http.Header)
	}
	return d.h
}
func (d *discardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (d *discardWriter) WriteHeader(int)             {}

// peerServeRoute proxies a paired peer's request to a local route by Host.
// It mirrors routeRequest's managed-route handling but with the interactive
// recovery pages replaced by a static self-refreshing page: those pages
// drive /_api/ endpoints that this listener deliberately cannot serve.
func (s *Server) peerServeRoute(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if idx := strings.Index(host, ":"); idx != -1 {
		host = host[:idx]
	}
	tld := s.cfg.Daemon.TLD
	if !strings.HasSuffix(host, "."+tld) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	name := strings.TrimSuffix(host, "."+tld)
	route, ok := s.table.Get(name)
	if !ok || route.Type == RouteBookmark {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if route.Type == RouteManaged {
		pid, hasPID := route.PIDValue()
		if !route.Running.Load() || !hasPID || !processAlive(pid) {
			// Run the normal on-demand recovery (adopt a survivor, else
			// auto-spawn) but discard whatever page it renders. served=false
			// means it adopted a live process — fall through and proxy.
			if s.recoverManagedRoute(&discardWriter{}, r, route) {
				s.servePeerStartingPage(w, route)
				return
			}
		}
	}
	if !s.isPortReady(route.Port) {
		// A live managed child that rebound to another port: locally the
		// reconnecting page drives /repair to rediscover it, but remote
		// viewers can't reach /_api/, so run the same repair here (its
		// response discarded, like recovery above) and proxy if it found one.
		if route.Type == RouteManaged && route.Running.Load() {
			s.handleRepair(&discardWriter{}, r, route.Name)
		}
		if !s.isPortReady(route.Port) {
			route.Ready.Store(false) // as routeRequest does locally
			s.servePeerStartingPage(w, route)
			return
		}
	}
	// Answering now. A failure recorded earlier — the readiness probe giving
	// up before a slow first compile bound late — is stale, and left in place
	// it would label the next brief restart "isn't running" with no retry.
	if route.Type == RouteManaged && route.LoadFailure() != nil {
		route.SetFailure(nil)
	}

	route.TouchActivity()
	target, _ := url.Parse(fmt.Sprintf("http://localhost:%d", route.Port))
	proxy := httputil.NewSingleHostReverseProxy(target)
	// Same upgrade-only Origin rewrite as the local proxy path — this is the
	// hop that talks to the dev server, so the HMR-socket Origin gate
	// (Next.js allowedDevOrigins etc.) is defused here. See routeRequest.
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
	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		if errors.Is(err, context.Canceled) || req.Context().Err() != nil {
			return
		}
		// The route's port went dark mid-flight. The static page self-refreshes
		// while this machine's own recovery machinery repairs the route.
		s.servePeerStartingPage(rw, route)
	}
	proxy.ServeHTTP(w, r)
}

// servePeerStartingPage is the remote viewer's stand-in for the start /
// reconnecting pages: a themed 503. No buttons — remote viewers cannot
// operate this daemon. It self-refreshes only while this machine is actually
// bringing the route up; otherwise "Retrying automatically…" would be a
// promise nothing here keeps (a sticky route has no process vibe can start,
// and a managed route whose recovery gave up waits for someone at this
// machine).
func (s *Server) servePeerStartingPage(w http.ResponseWriter, route *Route) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	name := template.HTMLEscapeString(route.Name)
	head := themeHead(name + "." + s.cfg.Daemon.TLD)
	if s.peerRouteRecovering(route) {
		fmt.Fprintf(w, `<!doctype html><html><head>%s<meta http-equiv="refresh" content="2"><style>%s</style></head>
<body><div class="wrap"><h1>%s is starting</h1>
<p>This route lives on another machine and its server isn't answering yet. Retrying automatically…</p>
</div></body></html>`, head, themeCSS, name)
		return
	}
	fmt.Fprintf(w, `<!doctype html><html><head>%s<style>%s</style></head>
<body><div class="wrap"><h1>%s isn't running</h1>
<p>This route lives on another machine and its server isn't running there. Start it on that machine, then reload this page.</p>
</div></body></html>`, head, themeCSS, name)
}

// peerRouteRecovering reports whether this machine is bringing the route up
// on its own: a managed route mid auto-start, or one whose process is alive
// and has yet to bind its port (or is having it rediscovered).
func (s *Server) peerRouteRecovering(route *Route) bool {
	if route.Type != RouteManaged {
		return false
	}
	if s.isAutoStarting(route.Name) {
		return true
	}
	// A recorded failure means recovery stopped trying: e.g. the readiness
	// probe gave up ("never bound port N") while the process lives on.
	return route.Running.Load() && route.LoadFailure() == nil
}
