package daemon

// Peer OAuth bridging.
//
// A route with oauth_callback_port N has its provider registered with
// redirect URI http://localhost:N/... — and "localhost" is whatever machine
// the *browser* runs on. When the route lives on a paired peer, the bridge on
// the peer's loopback is unreachable from here, so the callback dead-ends.
// Peers therefore advertise oauth_callback_port in their route summaries and
// this daemon binds its own 127.0.0.1:N bridge for each, forwarding to
// https://<name>.vibe — which the peer relay already carries.
//
// Two risks shape it:
//
//   - A peer picks a port this machine binds. Local always wins: ports any
//     local route claims (primary, oauth, reserve) and the daemon's own
//     listeners are never bridged for a peer, preflightPort hands a held port
//     back the moment a local route needs it, and a bind failure (some
//     non-vibe process holds it) is logged once and skipped.
//   - The port may also be some *other* local app's OAuth callback, so a
//     blind 307 would ship that app's sign-in code to another machine. The
//     bridge never auto-forwards: it serves a confirmation page naming the
//     route and the peer, and the user clicks through (frame-busted, so a
//     third-party page can't clickjack the button).

import (
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/graiz/local.vibe/internal/peer"
)

// peerBridgeYieldWindow is how long a port released to a local route stays
// unbound for peers — long enough for the local route to bind it and land in
// the table, after which the local-claim check keeps it excluded.
const peerBridgeYieldWindow = time.Minute

// maxPeerOAuthFormBytes caps a form_post callback body replayed through the
// confirmation page. Real callbacks (code, state, id_token) are a few KiB.
const maxPeerOAuthFormBytes = 64 << 10

// peerOAuthBridge is one peer route whose OAuth callback port this daemon
// bridges.
type peerOAuthBridge struct {
	peer  peer.Peer
	route string
}

// peerOAuthBridges maps each bridgeable port to the peer route that owns it.
// peers.json order breaks ties between peers (first wins, as for names);
// ports claimed by a local route or a daemon listener, and peer routes
// shadowed by a local route of the same name, are excluded.
func (s *Server) peerOAuthBridges() map[int]peerOAuthBridge {
	out := make(map[int]peerOAuthBridge)
	if !s.peersEnabled() {
		return out
	}
	s.peerMu.Lock()
	for _, p := range s.peerList {
		st, ok := s.peerStates[p.Name]
		if !ok {
			continue
		}
		for _, sum := range st.routes {
			if sum.OAuthCallbackPort <= 0 {
				continue
			}
			if _, taken := out[sum.OAuthCallbackPort]; !taken {
				out[sum.OAuthCallbackPort] = peerOAuthBridge{peer: p, route: sum.Name}
			}
		}
	}
	s.peerMu.Unlock()

	if len(out) == 0 {
		return out
	}
	claimed := map[int]bool{
		s.cfg.Daemon.Port:       true,
		s.cfg.Daemon.TLS.Port:   true,
		s.cfg.Daemon.Peers.Port: true,
	}
	for _, r := range s.table.List() {
		claimed[r.Port] = true
		claimed[r.OAuthCallbackPort] = true
		for _, p := range r.ReservePorts {
			claimed[p] = true
		}
	}
	for port, b := range out {
		if _, shadowed := s.table.Get(b.route); claimed[port] || shadowed {
			delete(out, port)
		}
	}
	return out
}

// peerBridgeYieldedLocked reports whether port was recently handed back to a
// local route. Caller holds oauthBridgeMu.
func (s *Server) peerBridgeYieldedLocked(port int) bool {
	t, ok := s.oauthPeerYielded[port]
	if !ok {
		return false
	}
	if time.Since(t) >= peerBridgeYieldWindow {
		delete(s.oauthPeerYielded, port)
		return false
	}
	return true
}

// releasePeerOAuthBridge closes a peer bridge holding port so a local route
// can bind it, and keeps peers off it for peerBridgeYieldWindow. A local
// route's own bridge is never released (that port is its oauth_callback_port,
// which validateOAuthBridgeConfig already keeps distinct from route ports).
func (s *Server) releasePeerOAuthBridge(port int) {
	for _, r := range s.table.List() {
		if r.OAuthCallbackPort == port {
			return
		}
	}
	s.oauthBridgeMu.Lock()
	defer s.oauthBridgeMu.Unlock()
	srv, ok := s.oauthBridgeServers[port]
	if !ok {
		return
	}
	s.closeBridgeLocked(port, srv)
	if s.oauthPeerYielded == nil {
		s.oauthPeerYielded = make(map[int]time.Time)
	}
	s.oauthPeerYielded[port] = time.Now()
}

// peerOAuthPageData feeds templates/oauth_peer.html.tmpl.
type peerOAuthPageData struct {
	pageShell
	Route        string
	Icon         string
	Peer         string
	DashboardURL string
	Port         int
	Host         string
	Target       string
	IsPost       bool
	PostFields   []peerOAuthField
}

type peerOAuthField struct{ Name, Value string }

// servePeerOAuthBridge renders the confirmation page for a callback that
// landed on a peer route's bridge port. The destination is the bridge's
// route, or — when the state carries a worktree tag naming a worktree of
// that route on the same peer — that worktree, with the original state
// restored, mirroring the local bridge's unwrap.
func (s *Server) servePeerOAuthBridge(w http.ResponseWriter, r *http.Request, b peerOAuthBridge, port int) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// The URL carries a single-use sign-in code: never cache it, never leak
	// it via Referer, never let another page frame the Continue button.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")

	var form url.Values
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, maxPeerOAuthFormBytes)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad callback body", http.StatusBadRequest)
			return
		}
		form = r.PostForm
	}

	routeName := b.route
	q := r.URL.Query()
	// form_post callbacks carry state in the body; query-mode in the URL.
	stateVals, stateIn := q, "query"
	if form != nil && form.Has("state") {
		stateVals, stateIn = form, "form"
	}
	if name, orig, ok := parseWrappedOAuthState(stateVals.Get("state")); ok {
		if parent, isWT := worktreeParent(name); isWT && parent == b.route {
			if p, _, found := s.peerRouteFromCache(name); found && p.Fingerprint == b.peer.Fingerprint {
				if orig == "" {
					stateVals.Del("state") // synthetic PKCE-only tag
				} else {
					stateVals.Set("state", orig)
				}
				if stateIn == "query" {
					r.URL.RawQuery = q.Encode()
				}
				routeName = name
			}
		}
	}

	// Same icon the dashboard shows for this peer route: the peer's
	// (sanitized) icon, else the deterministic pool pick.
	icon := iconPool[nameHash(routeName)%len(iconPool)]
	if _, sum, ok := s.peerRouteFromCache(routeName); ok && sum.Icon != "" {
		icon = sum.Icon
	}
	host := s.routeHost(routeName)
	data := peerOAuthPageData{
		Route:        routeName,
		Icon:         icon,
		DashboardURL: s.routeURL("local"),
		Peer:         b.peer.Name,
		Port:         port,
		Host:         host,
		Target:       fmt.Sprintf("%s://%s%s", s.vibeScheme(), host, r.URL.RequestURI()),
		IsPost:       r.Method == http.MethodPost,
	}
	for k, vs := range form {
		for _, v := range vs {
			data.PostFields = append(data.PostFields, peerOAuthField{k, v})
		}
	}
	renderPage(w, http.StatusOK, tmplOAuthPeer, "Continue sign-in — "+routeName, &data)
}
