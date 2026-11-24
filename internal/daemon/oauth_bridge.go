package daemon

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

func (s *Server) validateOAuthBridgeConfig(routeName string, appPort, callbackPort int) error {
	if callbackPort < 1 || callbackPort > 65535 {
		return fmt.Errorf("oauth_callback_port must be between 1 and 65535")
	}
	if appPort > 0 && callbackPort == appPort {
		return fmt.Errorf("oauth_callback_port (%d) must differ from route port (%d)", callbackPort, appPort)
	}
	if callbackPort == s.cfg.Daemon.Port || (s.cfg.Daemon.TLS.Enabled && callbackPort == s.cfg.Daemon.TLS.Port) {
		return fmt.Errorf("oauth_callback_port %d conflicts with vibe daemon listener", callbackPort)
	}
	for _, r := range s.table.List() {
		if r.Name != routeName && r.OAuthCallbackPort == callbackPort {
			return fmt.Errorf("oauth_callback_port %d is already used by route %q", callbackPort, r.Name)
		}
	}

	// Bindability is enforced by reconcileOAuthBridgeListeners (which holds
	// the actual listener); a probe-and-close here would race against any
	// concurrent listener acquiring the port between the probe and reconcile.
	return nil
}

// oauthCallbackAppPortCollision checks a new or changed callback port against
// the ports other routes' *apps* bind — their primary port and reserve_ports
// (issue #10). validateOAuthBridgeConfig only compares callback ports to each
// other, so a bridge could be configured on a port another route's server
// uses:
//
//   - that route is running → error. The bridge listener would fail to bind,
//     or worse, bind 127.0.0.1 alongside a wildcard listener and silently
//     intercept the app's localhost traffic.
//   - that route is stopped → warning, allowed. Nothing collides yet; once
//     the bridge holds the port, vibePortClaim refuses to start the other
//     route with an error naming this bridge, shown on its start page.
//
// Deliberately not part of validateOAuthBridgeConfig: that one also runs in
// reconcileOAuthBridgeListeners on every route change, where a running-state
// check would fail every bridge whenever any colliding route happened to come
// up. Callers vet only a port that is being set or changed.
func (s *Server) oauthCallbackAppPortCollision(routeName string, callbackPort int) (warning string, err error) {
	if callbackPort <= 0 {
		return "", nil
	}
	for _, r := range s.table.List() {
		if r.Name == routeName || r.Type == RouteBookmark {
			continue
		}
		what := ""
		if r.Port == callbackPort {
			what = "port"
		}
		for key, p := range r.ReservePorts {
			if p == callbackPort {
				what = fmt.Sprintf("reserve_ports[%q]", key)
				break
			}
		}
		if what == "" {
			continue
		}
		if r.Running.Load() {
			return "", fmt.Errorf("oauth_callback_port %d is route %q's %s, and %q is running — pick another callback port", callbackPort, r.Name, what, r.Name)
		}
		return fmt.Sprintf("oauth_callback_port %d is also route %q's %s; %q won't be able to start while this bridge holds the port", callbackPort, r.Name, what, r.Name), nil
	}
	return "", nil
}

// reconcileOAuthBridgeListeners binds a loopback bridge for every local
// route's oauth_callback_port and — when peers are enabled — for every
// peer route's advertised port that no local route claims. It returns the
// local failures keyed by callback port (nil when all succeeded); a peer bind
// failure is logged once and skipped, since a peer's choice of port must
// never break this machine's own route changes.
//
// It deliberately does not stop at the first local failure either. Every
// register, update and deregister reconciles the whole table, so one route
// whose callback port was grabbed by some unrelated process used to fail all
// of them — rejecting a new route over a port it never asked for — and,
// depending on map order, leave other routes' bridges unbound. Callers check
// only the port they are responsible for (oauthBridgeErr) and log the rest;
// failed ports are retried on the next reconcile.
func (s *Server) reconcileOAuthBridgeListeners() map[int]error {
	var errs map[int]error
	fail := func(port int, err error) {
		if errs == nil {
			errs = make(map[int]error)
		}
		errs[port] = err
	}
	wanted := make(map[int]bool) // port -> owned by a local route
	for _, r := range s.table.List() {
		if r.OAuthCallbackPort > 0 {
			if err := s.validateOAuthBridgeConfig(r.Name, r.Port, r.OAuthCallbackPort); err != nil {
				fail(r.OAuthCallbackPort, fmt.Errorf("route %q: %w", r.Name, err))
				continue
			}
			wanted[r.OAuthCallbackPort] = true
		}
	}
	peerPorts := s.peerOAuthBridges()

	s.oauthBridgeMu.Lock()
	defer s.oauthBridgeMu.Unlock()

	for port := range peerPorts {
		if _, local := wanted[port]; local || s.peerBridgeYieldedLocked(port) {
			continue
		}
		wanted[port] = false
	}

	for port, srv := range s.oauthBridgeServers {
		if _, ok := wanted[port]; ok {
			continue
		}
		s.closeBridgeLocked(port, srv)
	}

	for port, local := range wanted {
		if _, ok := s.oauthBridgeServers[port]; ok {
			continue
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			if local {
				fail(port, fmt.Errorf("oauth_callback_port %d is not available: %w", port, err))
				continue
			}
			if s.oauthPeerBindFailed == nil {
				s.oauthPeerBindFailed = make(map[int]bool)
			}
			if !s.oauthPeerBindFailed[port] {
				s.oauthPeerBindFailed[port] = true
				b := peerPorts[port]
				fmt.Fprintf(os.Stderr, "warning: peer OAuth bridge for %s (on %s) could not bind localhost:%d: %v\n", b.route, b.peer.Name, port, err)
			}
			continue
		}
		delete(s.oauthPeerBindFailed, port)
		if !local {
			// Say so where a confused user will look: a local server that
			// later wants this port gets EADDRINUSE from *this* listener.
			b := peerPorts[port]
			fmt.Fprintf(os.Stderr, "vibe: holding localhost:%d for %s's OAuth callback (route on peer %s); see `vibe peers`\n", port, b.route, b.peer.Name)
		}
		srv := newHTTPServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.handleOAuthCallbackBridge(w, r, port)
		}))
		s.oauthBridgeServers[port] = srv
		s.oauthBridgeListeners[port] = ln
		go func(p int, server *http.Server, listener net.Listener) {
			if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
				fmt.Fprintf(os.Stderr, "warning: oauth callback bridge listener %d stopped: %v\n", p, err)
			}
		}(port, srv, ln)
	}

	return errs
}

// oauthBridgeErr returns the reconcile failure for port, if any, and logs the
// failures belonging to other ports — they are another route's problem and
// must not fail the caller's request. port <= 0 logs everything.
func oauthBridgeErr(errs map[int]error, port int) error {
	for p, err := range errs {
		if p != port || port <= 0 {
			fmt.Fprintf(os.Stderr, "warning: oauth callback bridge: %v\n", err)
		}
	}
	if port <= 0 {
		return nil
	}
	return errs[port]
}

// reconcileOAuthBridgeListenersLogged is reconcileOAuthBridgeListeners for
// callers with no request to fail (peer sync, peer removal, deregister,
// startup).
func (s *Server) reconcileOAuthBridgeListenersLogged() {
	_ = oauthBridgeErr(s.reconcileOAuthBridgeListeners(), 0)
}

// closeBridgeLocked shuts one bridge listener down. Caller holds oauthBridgeMu.
func (s *Server) closeBridgeLocked(port int, srv *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_ = srv.Shutdown(ctx)
	cancel()
	if ln, ok := s.oauthBridgeListeners[port]; ok {
		_ = ln.Close()
	}
	delete(s.oauthBridgeServers, port)
	delete(s.oauthBridgeListeners, port)
}

func (s *Server) stopOAuthBridgeListeners() {
	s.oauthBridgeMu.Lock()
	defer s.oauthBridgeMu.Unlock()
	for port, srv := range s.oauthBridgeServers {
		s.closeBridgeLocked(port, srv)
	}
}

func (s *Server) handleOAuthCallbackBridge(w http.ResponseWriter, r *http.Request, callbackPort int) {
	var route *Route
	for _, rt := range s.table.List() {
		if rt.OAuthCallbackPort == callbackPort {
			route = rt
			break
		}
	}
	if route == nil {
		// Not a local route's port: maybe a peer route's (local wins —
		// checked first). Never auto-forwarded; see oauth_peer_bridge.go.
		if b, ok := s.peerOAuthBridges()[callbackPort]; ok {
			s.servePeerOAuthBridge(w, r, b, callbackPort)
			return
		}
		http.NotFound(w, r)
		return
	}

	// Worktree routing: a state tagged by the reverse proxy names the route
	// that started the flow. Restore the original state and forward there —
	// but only to an actual worktree of this bridge's owner, so a crafted
	// state can't bounce callbacks to an unrelated route.
	q := r.URL.Query()
	if name, orig, ok := parseWrappedOAuthState(q.Get("state")); ok {
		if wt, found := s.table.Get(name); found && wt.Parent == route.Name {
			if orig == "" {
				// Synthetic state injected for a PKCE-only flow — the app
				// never sent one, so it must not receive one.
				q.Del("state")
			} else {
				q.Set("state", orig)
			}
			r.URL.RawQuery = q.Encode()
			route = wt
		}
	}

	target := fmt.Sprintf("%s://%s.%s%s", s.vibeScheme(), route.Name, s.cfg.Daemon.TLD, r.URL.RequestURI())
	http.Redirect(w, r, target, http.StatusTemporaryRedirect)
}
