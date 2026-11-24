package daemon

// Browsing-side state for the peer subsystem: the per-peer route cache that
// routeRequest consults for names this machine doesn't serve. Populated by a
// 30s ETag poll plus throttled on-demand refreshes; a peer outage keeps the
// stale cache (misses self-correct on the next successful refresh).

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/graiz/local.vibe/internal/peer"
)

// peerSyncInterval is the background poll cadence. Deliberately a poll, not
// push: a 30s ETag exchange per LAN peer is negligible, and the on-demand
// refresh paths make cache misses self-correcting between ticks.
const peerSyncInterval = 30 * time.Second

// peerRefreshThrottle bounds on-demand refreshes the same way
// managedOwnerCheckInterval bounds ownership probes: a burst of misses for
// an unknown host becomes at most one network round-trip per window.
const peerRefreshThrottle = 3 * time.Second

// peerState is one peer's cached route list plus sync bookkeeping. Guarded
// by Server.peerMu except refreshing, which single-flights HTTP refreshes.
type peerState struct {
	routes      []peer.RouteSummary
	etag        string
	lastOK      time.Time
	lastErr     string
	lastRefresh time.Time
	refreshing  atomic.Bool
}

// reachable reports whether this peer's cached routes are fresh: its last
// successful sync falls within two poll intervals. Caller holds peerMu.
func (st *peerState) reachable() bool {
	return !st.lastOK.IsZero() && time.Since(st.lastOK) < 2*peerSyncInterval
}

// peerSyncLoop polls every paired peer's route list until daemon shutdown.
// The first refresh runs immediately: the cache is in-memory, so after a
// daemon restart a ticker-only loop would leave the dashboard's peer section
// empty for a full interval.
func (s *Server) peerSyncLoop() {
	s.refreshAllPeers(false)
	ticker := time.NewTicker(peerSyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.quit:
			return
		case <-ticker.C:
			s.refreshAllPeers(false)
		}
	}
}

func (s *Server) refreshAllPeers(force bool) {
	s.peerMu.Lock()
	peers := append([]peer.Peer(nil), s.peerList...)
	s.peerMu.Unlock()
	for _, p := range peers {
		s.refreshPeerRoutes(p, force)
	}
}

// peerStateFor returns (creating if needed) the sync state for a peer name.
func (s *Server) peerStateFor(name string) *peerState {
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	st, ok := s.peerStates[name]
	if !ok {
		st = &peerState{}
		s.peerStates[name] = st
	}
	return st
}

// peerTransport builds an HTTP transport pinned to one peer's fingerprint.
// Every connection this daemon makes to a peer goes through here — there is
// no other client-side tls.Config on the peer channel.
func (s *Server) peerTransport(fingerprint string) (*http.Transport, error) {
	s.peerMu.Lock()
	id := s.peerIdentity
	s.peerMu.Unlock()
	if id == nil {
		return nil, errors.New("peer identity not initialized")
	}
	verify := func(got string) error {
		if got != fingerprint {
			return fmt.Errorf("peer fingerprint changed — if the machine was reinstalled, `vibe peer remove` and re-pair")
		}
		return nil
	}
	return &http.Transport{TLSClientConfig: peer.ClientTLSConfig(*id, verify)}, nil
}

// refreshPeerRoutes fetches one peer's route list. Throttled unless force,
// single-flighted, ETag-aware. Errors keep the stale cache and record
// lastErr; the route names only change on a successful 200.
func (s *Server) refreshPeerRoutes(p peer.Peer, force bool) {
	st := s.peerStateFor(p.Name)
	s.peerMu.Lock()
	recent := time.Since(st.lastRefresh) < peerRefreshThrottle
	etag := st.etag
	s.peerMu.Unlock()
	if !force && recent {
		return
	}
	if !st.refreshing.CompareAndSwap(false, true) {
		return
	}
	defer st.refreshing.Store(false)

	s.peerMu.Lock()
	st.lastRefresh = time.Now()
	s.peerMu.Unlock()

	transport, err := s.peerTransport(p.Fingerprint)
	if err != nil {
		s.setPeerErr(st, err)
		return
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: transport}
	addr := net.JoinHostPort(p.Host, fmt.Sprint(p.Port))
	req, err := http.NewRequest(http.MethodGet, "https://"+addr+"/peer/routes", nil)
	if err != nil {
		s.setPeerErr(st, err)
		return
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := client.Do(req)
	if err != nil {
		s.setPeerErr(st, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotModified {
		s.adoptPeerName(p, resp.Header.Get(peerNameHeader))
	}

	switch resp.StatusCode {
	case http.StatusNotModified:
		s.peerMu.Lock()
		st.lastOK = time.Now()
		st.lastErr = ""
		s.peerMu.Unlock()
	case http.StatusOK:
		var routes []peer.RouteSummary
		if err := json.NewDecoder(io.LimitReader(resp.Body, apiMaxBodyBytes)).Decode(&routes); err != nil {
			s.setPeerErr(st, err)
			return
		}
		routes = sanitizePeerSummaries(routes)
		s.peerMu.Lock()
		changed := !sameRouteNames(st.routes, routes)
		bridgesChanged := !sameOAuthPorts(st.routes, routes)
		st.routes = routes
		st.etag = resp.Header.Get("ETag")
		st.lastOK = time.Now()
		st.lastErr = ""
		s.peerMu.Unlock()
		if changed {
			// Peer names must land in the leaf cert's SANs (Chrome rejects
			// *.vibe wildcards), same hot-reload as local route changes.
			if err := s.reloadTLSCert(); err != nil {
				fmt.Printf("vibe: peer SAN reload failed: %v\n", err)
			}
		}
		if bridgesChanged {
			s.reconcileOAuthBridgeListenersLogged()
		}
	default:
		s.setPeerErr(st, fmt.Errorf("peer returned %d", resp.StatusCode))
	}
}

// peerNameHeader carries a peer's current name on GET /peer/routes.
const peerNameHeader = "X-Vibe-Peer-Name"

// adoptPeerName picks up a paired peer's new name after the machine was
// renamed. A peer's name was fixed at pairing (its hostname then), so a
// rename left it listed under the old name forever. Safe because the claim
// arrives over a connection already pinned to this peer's fingerprint — the
// same machine holding the same key — and only the display name changes, never
// the host or the pin. Skipped when another paired peer already uses the
// name: names key `vibe peer remove` and the peers.json collision order.
func (s *Server) adoptPeerName(p peer.Peer, raw string) {
	// No header = a peer on an older build that doesn't advertise a name.
	// Checked before sanitizing: SanitizeName maps "" to the placeholder
	// "peer", which renamed every not-yet-updated peer to "peer".
	if strings.TrimSpace(raw) == "" {
		return
	}
	name := peer.SanitizeName(raw)
	if name == "" || name == p.Name {
		return
	}
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	idx := -1
	for i := range s.peerList {
		switch {
		case s.peerList[i].Fingerprint == p.Fingerprint:
			if s.peerList[i].Name != p.Name {
				return // renamed concurrently; the next sync re-checks
			}
			idx = i
		case s.peerList[i].Name == name:
			return // taken by another peer
		}
	}
	if idx < 0 {
		return // removed while this sync was in flight
	}
	s.peerList[idx].Name = name
	if st, ok := s.peerStates[p.Name]; ok {
		delete(s.peerStates, p.Name)
		s.peerStates[name] = st
	}
	if err := peer.SavePeers(s.configDir(), s.peerList); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not persist peer rename %s → %s: %v\n", p.Name, name, err)
	}
	fmt.Fprintf(os.Stderr, "vibe: peer %s is now called %s\n", p.Name, name)
}

func (s *Server) setPeerErr(st *peerState, err error) {
	s.peerMu.Lock()
	st.lastErr = err.Error()
	s.peerMu.Unlock()
}

// sanitizePeerSummaries filters what a peer sent down to what this daemon can
// safely use. Names must be valid route names: they go into the leaf cert's
// SANs, and one non-IA5 name would make every later reloadTLSCert fail. Icons
// are kept only when they are inline (emoji or data:image) — an http(s) icon
// would be rendered as <img src> on this machine's dashboard, a tracking
// beacon the peer controls.
func sanitizePeerSummaries(in []peer.RouteSummary) []peer.RouteSummary {
	out := make([]peer.RouteSummary, 0, len(in))
	for _, r := range in {
		if _, err := parseRouteName(r.Name); err != nil || r.Name == "local" {
			continue
		}
		if strings.HasPrefix(r.Icon, "http://") || strings.HasPrefix(r.Icon, "https://") ||
			(strings.HasPrefix(r.Icon, "data:") && !strings.HasPrefix(strings.ToLower(r.Icon), "data:image/")) {
			r.Icon = ""
		}
		// The viewing machine binds a loopback listener on this port, so
		// only accept an unprivileged TCP port; daemon/local-route conflicts
		// are resolved at bind time (peerOAuthBridges), where they're known.
		if r.OAuthCallbackPort < 1024 || r.OAuthCallbackPort > 65535 {
			r.OAuthCallbackPort = 0
		}
		out = append(out, r)
	}
	return out
}

// sameOAuthPorts reports whether two route lists advertise the same
// (name, oauth_callback_port) pairs — the input to the peer OAuth bridges.
func sameOAuthPorts(a, b []peer.RouteSummary) bool {
	ports := func(rs []peer.RouteSummary) map[string]int {
		m := make(map[string]int)
		for _, r := range rs {
			if r.OAuthCallbackPort > 0 {
				m[r.Name] = r.OAuthCallbackPort
			}
		}
		return m
	}
	pa, pb := ports(a), ports(b)
	if len(pa) != len(pb) {
		return false
	}
	for k, v := range pa {
		if pb[k] != v {
			return false
		}
	}
	return true
}

func sameRouteNames(a, b []peer.RouteSummary) bool {
	if len(a) != len(b) {
		return false
	}
	names := make(map[string]bool, len(a))
	for _, r := range a {
		names[r.Name] = true
	}
	for _, r := range b {
		if !names[r.Name] {
			return false
		}
	}
	return true
}

// findPeerRoute resolves a route name against the peer caches, peers.json
// order first-match (first-paired wins ties). Only consulted after the local
// table missed, so local routes always shadow peers. A total miss triggers
// one throttled synchronous sweep — the first visit to a just-registered
// peer route resolves without waiting for the next poll tick.
func (s *Server) findPeerRoute(name string) (peer.Peer, peer.RouteSummary, bool) {
	if p, sum, ok := s.peerRouteFromCache(name); ok {
		return p, sum, ok
	}
	s.refreshAllPeers(false)
	return s.peerRouteFromCache(name)
}

// peerCacheCold reports whether any paired peer has never been fetched (no
// success and no recorded failure). True only in the window right after
// startup, before the first sync completes — the dashboard uses it to decide
// whether a render is worth briefly waiting on the in-flight refresh.
func (s *Server) peerCacheCold() bool {
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	for _, p := range s.peerList {
		st, ok := s.peerStates[p.Name]
		if !ok || (st.lastOK.IsZero() && st.lastErr == "") {
			return true
		}
	}
	return false
}

func (s *Server) peerRouteFromCache(name string) (peer.Peer, peer.RouteSummary, bool) {
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	for _, p := range s.peerList {
		st, ok := s.peerStates[p.Name]
		if !ok {
			continue
		}
		for _, sum := range st.routes {
			if sum.Name == name {
				return p, sum, true
			}
		}
	}
	return peer.Peer{}, peer.RouteSummary{}, false
}

// peerRouteNames returns the deduped set of route names across all peer
// caches, for the TLS SAN list.
func (s *Server) peerRouteNames() []string {
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	seen := make(map[string]bool)
	var names []string
	for _, p := range s.peerList {
		st, ok := s.peerStates[p.Name]
		if !ok {
			continue
		}
		for _, sum := range st.routes {
			if !seen[sum.Name] {
				seen[sum.Name] = true
				names = append(names, sum.Name)
			}
		}
	}
	return names
}
