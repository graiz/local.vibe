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
	for _, rt := range routes {
		if rt.Type == RouteBookmark {
			continue
		}
		summaries = append(summaries, peer.RouteSummary{
			Name:    rt.Name,
			Type:    string(rt.Type),
			Running: rt.Running.Load(),
			Ready:   rt.Ready.Load(),
			Icon:    rt.Icon,
		})
	}
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
		s.servePeerStartingPage(w, route)
		return
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
// reconnecting pages: a themed 503 that refreshes itself every 2s until the
// route answers. No buttons — remote viewers cannot operate this daemon.
func (s *Server) servePeerStartingPage(w http.ResponseWriter, route *Route) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	name := template.HTMLEscapeString(route.Name)
	fmt.Fprintf(w, `<!doctype html><html><head>%s<meta http-equiv="refresh" content="2"><style>%s</style></head>
<body><div class="wrap"><h1>%s is starting</h1>
<p>This route lives on another machine and its server isn't answering yet. Retrying automatically…</p>
</div></body></html>`, themeHead(name+"."+s.cfg.Daemon.TLD), themeCSS, name)
}
