package daemon

// Loopback /_api/peers endpoints — the CLI's window into the peer
// subsystem. These run on the ordinary daemon listeners (unix socket /
// 127.0.0.1), never on the LAN peer listener, and the POST/DELETE routes are
// covered by the existing cross-site guard in apiHandler with no extra code:
// apiStateChanging catches every non-GET method.

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/graiz/local.vibe/internal/config"
	"github.com/graiz/local.vibe/internal/peer"
)

const peersDisabledMsg = "peers are off — turn them on in the dashboard's Peers section or with `vibe peers enable`"

type peerRouteResponse struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Running bool   `json:"running"`
	Ready   bool   `json:"ready"`
}

type peerResponse struct {
	Name        string              `json:"name"`
	Host        string              `json:"host"`
	Port        int                 `json:"port"`
	Fingerprint string              `json:"fingerprint"`
	AddedAt     time.Time           `json:"added_at"`
	Reachable   bool                `json:"reachable"`
	LastError   string              `json:"last_error,omitempty"`
	Routes      []peerRouteResponse `json:"routes"`
	// OAuthBridges lists the localhost ports this machine holds on the
	// peer's behalf (oauth_peer_bridge.go). Surfaced so a local server that
	// hits EADDRINUSE on one of them has somewhere to find out why.
	OAuthBridges []peerBridgeResponse `json:"oauth_bridges,omitempty"`
}

type peerBridgeResponse struct {
	Port  int    `json:"port"`
	Route string `json:"route"`
	Bound bool   `json:"bound"` // false: some other process already holds the port
}

type peersListResponse struct {
	Enabled bool           `json:"enabled"`
	Peers   []peerResponse `json:"peers"`
}

// handlePeersList reports paired peers with their cached routes and sync
// health. Reachable means the last successful sync is recent (within two
// poll intervals), so the CLI/doctor need no probing of their own.
func (s *Server) handlePeersList(w http.ResponseWriter, _ *http.Request) {
	resp := peersListResponse{Enabled: s.peersEnabled(), Peers: []peerResponse{}}
	// Before peerMu: peerOAuthBridges takes it itself.
	bridges := map[string][]peerBridgeResponse{}
	held := s.oauthBridgePortsHeld()
	for port, b := range s.peerOAuthBridges() {
		bridges[b.peer.Name] = append(bridges[b.peer.Name], peerBridgeResponse{Port: port, Route: b.route, Bound: held[port]})
	}
	for _, bs := range bridges {
		sort.Slice(bs, func(i, j int) bool { return bs[i].Port < bs[j].Port })
	}
	s.peerMu.Lock()
	for _, p := range s.peerList {
		pr := peerResponse{
			Name:         p.Name,
			Host:         p.Host,
			Port:         p.Port,
			Fingerprint:  p.Fingerprint,
			AddedAt:      p.AddedAt,
			Routes:       []peerRouteResponse{},
			OAuthBridges: bridges[p.Name],
		}
		if st, ok := s.peerStates[p.Name]; ok {
			pr.Reachable = st.reachable()
			pr.LastError = st.lastErr
			for _, rt := range st.routes {
				pr.Routes = append(pr.Routes, peerRouteResponse{
					Name: rt.Name, Type: rt.Type, Running: rt.Running, Ready: rt.Ready,
				})
			}
		}
		resp.Peers = append(resp.Peers, pr)
	}
	s.peerMu.Unlock()
	json.NewEncoder(w).Encode(resp)
}

type peerInviteResponse struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
	Port      int       `json:"port"`
	Key       string    `json:"key"` // this machine's short key fingerprint, to compare on the other screen
	Name      string    `json:"name"`
	Addresses []string  `json:"addresses"` // where the other machine can reach this one
}

func (s *Server) handlePeerInvite(w http.ResponseWriter, _ *http.Request) {
	if !s.peersEnabled() {
		writeJSONError(w, http.StatusConflict, peersDisabledMsg)
		return
	}
	code, expires, err := s.openPeerInvite()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	json.NewEncoder(w).Encode(peerInviteResponse{
		Code: code, ExpiresAt: expires, Port: s.cfg.Daemon.Peers.Port, Key: peer.ShortKey(s.peerFP),
		Name: localPeerName(), Addresses: localPeerAddresses(),
	})
}

// handlePeerInviteClose is POST /_api/peers/invite/close {"code"}: the
// dashboard withdraws the code its Add peer dialog opened when the dialog
// closes, so an unpaired machine stops being findable then. Only that code:
// one opened elsewhere (`vibe peer invite`) is left alone.
func (s *Server) handlePeerInviteClose(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" {
		writeJSONError(w, http.StatusBadRequest, "code is required")
		return
	}
	s.peerMu.Lock()
	if s.peerInviteCode != "" && s.peerInviteCode == req.Code {
		s.peerInviteCode = ""
	}
	s.peerMu.Unlock()
	s.maybeStopPeerListener()
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// localPeerAddresses lists this machine's private-LAN and Tailscale IPv4
// addresses — what to type on the other machine. Never nil.
func localPeerAddresses() []string {
	out := []string{}
	cgnat := netip.MustParsePrefix("100.64.0.0/10")
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(n.IP.To4())
			if ok && (ip.IsPrivate() || cgnat.Contains(ip)) {
				out = append(out, ip.String())
			}
		}
	}
	return out
}

type peerAddRequest struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	Code string `json:"code"`
	// Fingerprint, when set, is the key the user was shown (from a probe);
	// pairing refuses if the machine answering now has a different one.
	Fingerprint string `json:"fingerprint"`
}

// isOldStyleCode reports a v1 (pre-2026-11) 6-digit pairing code.
func isOldStyleCode(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != 6 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (s *Server) handlePeerAdd(w http.ResponseWriter, r *http.Request) {
	if !s.peersEnabled() {
		writeJSONError(w, http.StatusConflict, peersDisabledMsg)
		return
	}
	var req peerAddRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Host == "" || req.Code == "" {
		writeJSONError(w, http.StatusBadRequest, "host and code are required")
		return
	}
	if isOldStyleCode(req.Code) {
		writeJSONError(w, http.StatusBadRequest, "that's a 6-digit code from an older vibe — update vibe on both machines (codes now look like K7QM-3XR9)")
		return
	}
	code, ok := peer.NormalizeCode(req.Code)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "that isn't a pairing code — codes look like K7QM-3XR9 (shown at the top of Add peer, or by `vibe peer invite`, on the other machine)")
		return
	}
	if req.Port == 0 {
		req.Port = config.DefaultPeerPort
	}
	p, err := s.pairWithPeerExpecting(req.Host, req.Port, code, req.Fingerprint)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	go s.refreshAllPeers(true)
	json.NewEncoder(w).Encode(peerResponse{
		Name: p.Name, Host: p.Host, Port: p.Port,
		Fingerprint: p.Fingerprint, AddedAt: p.AddedAt, Routes: []peerRouteResponse{},
	})
}

// handlePeerUpdate is PUT /_api/peers/{name} with any of {"name","host","port"}.
// The fingerprint is never editable: changing the host only changes where
// the pinned key is looked for, so a wrong host shows unreachable — it can't
// connect to a different machine.
func (s *Server) handlePeerUpdate(w http.ResponseWriter, r *http.Request, name string) {
	if !s.peersEnabled() {
		writeJSONError(w, http.StatusConflict, peersDisabledMsg)
		return
	}
	var req struct {
		Name *string `json:"name"`
		Host *string `json:"host"`
		Port *int    `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	newName := ""
	if req.Name != nil {
		lower := strings.ToLower(strings.TrimSpace(*req.Name))
		if lower == "" || len(lower) > 63 || peer.SanitizeName(lower) != lower {
			writeJSONError(w, http.StatusBadRequest, "names use lowercase letters, digits and dashes (at most 63)")
			return
		}
		newName = lower
	}
	if req.Host != nil && strings.TrimSpace(*req.Host) == "" {
		writeJSONError(w, http.StatusBadRequest, "host can't be empty")
		return
	}
	if req.Port != nil && (*req.Port < 1 || *req.Port > 65535) {
		writeJSONError(w, http.StatusBadRequest, "port must be 1-65535")
		return
	}

	s.peerMu.Lock()
	idx := -1
	for i, p := range s.peerList {
		if p.Name == name {
			idx = i
		} else if newName != "" && p.Name == newName {
			s.peerMu.Unlock()
			writeJSONError(w, http.StatusConflict, fmt.Sprintf("another peer is already called %s", newName))
			return
		}
	}
	if idx < 0 {
		s.peerMu.Unlock()
		writeJSONError(w, http.StatusNotFound, "unknown peer")
		return
	}
	p := &s.peerList[idx]
	if newName != "" && newName != p.Name {
		if st, ok := s.peerStates[p.Name]; ok {
			delete(s.peerStates, p.Name)
			s.peerStates[newName] = st
		}
		p.Name = newName
		p.NameSetByUser = true
	}
	if req.Host != nil {
		p.Host = strings.TrimSpace(*req.Host)
	}
	if req.Port != nil {
		p.Port = *req.Port
	}
	updated := *p
	err := peer.SavePeers(s.configDir(), s.peerList)
	s.peerMu.Unlock()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	go s.refreshPeerRoutes(updated, true)
	json.NewEncoder(w).Encode(peerResponse{
		Name: updated.Name, Host: updated.Host, Port: updated.Port,
		Fingerprint: updated.Fingerprint, AddedAt: updated.AddedAt, Routes: []peerRouteResponse{},
	})
}

func (s *Server) handlePeerRemove(w http.ResponseWriter, _ *http.Request, name string) {
	if !s.peersEnabled() {
		writeJSONError(w, http.StatusConflict, peersDisabledMsg)
		return
	}
	s.peerMu.Lock()
	idx := -1
	for i, p := range s.peerList {
		if p.Name == name {
			idx = i
			break
		}
	}
	if idx == -1 {
		s.peerMu.Unlock()
		writeJSONError(w, http.StatusNotFound, "unknown peer")
		return
	}
	s.dropPeerTransportLocked(s.peerList[idx].Fingerprint)
	s.peerList = append(s.peerList[:idx], s.peerList[idx+1:]...)
	delete(s.peerStates, name)
	err := peer.SavePeers(s.configDir(), s.peerList)
	s.peerMu.Unlock()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The removed peer's OAuth bridges (if any) close with it, its route
	// names leave the TLS leaf, and an unpaired machine stops listening.
	s.reconcileOAuthBridgeListenersLogged()
	if err := s.reloadTLSCert(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: TLS reload after peer removal: %v\n", err)
	}
	s.maybeStopPeerListener()
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
