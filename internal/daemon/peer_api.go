package daemon

// Loopback /_api/peers endpoints — the CLI's window into the peer
// subsystem. These run on the ordinary daemon listeners (unix socket /
// 127.0.0.1), never on the LAN peer listener, and the POST/DELETE routes are
// covered by the existing cross-site guard in apiHandler with no extra code:
// apiStateChanging catches every non-GET method.

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/graiz/local.vibe/internal/peer"
)

const peersDisabledMsg = "peers are disabled — set daemon.peers.enabled=true in ~/.vibe/config.json and restart the daemon"

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
	s.peerMu.Lock()
	for _, p := range s.peerList {
		pr := peerResponse{
			Name:        p.Name,
			Host:        p.Host,
			Port:        p.Port,
			Fingerprint: p.Fingerprint,
			AddedAt:     p.AddedAt,
			Routes:      []peerRouteResponse{},
		}
		if st, ok := s.peerStates[p.Name]; ok {
			pr.Reachable = time.Since(st.lastOK) < 2*peerSyncInterval
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
	json.NewEncoder(w).Encode(peerInviteResponse{Code: code, ExpiresAt: expires, Port: s.cfg.Daemon.Peers.Port})
}

type peerAddRequest struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	Code string `json:"code"`
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
	if req.Port == 0 {
		req.Port = 7444
	}
	p, err := s.pairWithPeer(req.Host, req.Port, req.Code)
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

func (s *Server) handlePeerRemove(w http.ResponseWriter, _ *http.Request, name string) {
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
	s.peerList = append(s.peerList[:idx], s.peerList[idx+1:]...)
	delete(s.peerStates, name)
	err := peer.SavePeers(s.configDir(), s.peerList)
	s.peerMu.Unlock()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
