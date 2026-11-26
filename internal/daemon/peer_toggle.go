package daemon

// Runtime peers switch: the dashboard's Enable / Turn off and `vibe peers
// enable|disable`. On brings the subsystem up without a restart (still lazy —
// no listener on an unpaired machine); off tears everything down at once and
// keeps peers.json and the identity for next time.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// setPeersEnabled persists daemon.peers.enabled and applies it immediately.
func (s *Server) setPeersEnabled(on bool) error {
	s.peerToggleMu.Lock()
	defer s.peerToggleMu.Unlock()
	prev := s.cfg.Daemon.Peers.Enabled
	s.cfg.Daemon.Peers.Enabled = on
	if err := s.saveConfig(); err != nil {
		s.cfg.Daemon.Peers.Enabled = prev // a failed save changes nothing
		return fmt.Errorf("saving config: %w", err)
	}
	if on {
		s.peersOn.Store(true)
		s.loadPeerSubsystem()
		return nil
	}
	// Flag first, so routeRequest stops consulting peers before anything is
	// torn down under it.
	s.peersOn.Store(false)
	s.peerMu.Lock()
	s.peerInviteCode = ""
	s.peerInviteExpires = time.Time{}
	s.stopPeerSyncLocked()
	s.closePeerListenerLocked()
	s.peerStates = make(map[string]*peerState)
	for fp := range s.peerTransports {
		s.dropPeerTransportLocked(fp)
	}
	s.peerMu.Unlock()
	s.reconcileOAuthBridgeListenersLogged() // closes the peer bridges
	if err := s.reloadTLSCert(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: TLS reload after disabling peers: %v\n", err)
	}
	return nil
}

// closePeerListenerLocked stops the LAN listener and cuts every live relay
// connection (Close, not a graceful Shutdown: off means off). Caller holds
// peerMu.
func (s *Server) closePeerListenerLocked() {
	if s.peerSrv != nil {
		_ = s.peerSrv.Close()
		s.peerSrv = nil
	}
	if s.peerLn != nil {
		_ = s.peerLn.Close()
		s.peerLn = nil
	}
}

// startPeerSync starts the 30 s sync loop if peers are on, at least one is
// paired, and no loop is running. Idempotent. immediate runs a first refresh
// right away — wanted at startup (the cache is empty); not after a pairing,
// whose paths already force a refresh, and where an extra unforced one would
// spend the 3 s refresh throttle before the caller's own lookup.
func (s *Server) startPeerSync(immediate bool) {
	if !s.peersEnabled() {
		return
	}
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	if s.peerSyncStop != nil || len(s.peerList) == 0 {
		return
	}
	stop := make(chan struct{})
	s.peerSyncStop = stop
	go s.peerSyncLoop(stop, immediate)
}

// stopPeerSync stops the sync loop if one runs. Idempotent.
func (s *Server) stopPeerSync() {
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	s.stopPeerSyncLocked()
}

func (s *Server) stopPeerSyncLocked() {
	if s.peerSyncStop != nil {
		close(s.peerSyncStop)
		s.peerSyncStop = nil
	}
}

// maybeStopPeerListener stops the LAN listener when nothing needs it: no
// paired peers and no open (unexpired) invite. A machine is findable only
// while it shows a code.
func (s *Server) maybeStopPeerListener() {
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	inviteOpen := s.peerInviteCode != "" && time.Now().Before(s.peerInviteExpires)
	if len(s.peerList) > 0 || inviteOpen {
		return
	}
	s.peerInviteCode = ""
	s.closePeerListenerLocked()
}

// handlePeersSetEnabled is POST /_api/peers/enabled {"enabled": bool}.
func (s *Server) handlePeersSetEnabled(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Enabled == nil {
		writeJSONError(w, http.StatusBadRequest, `body must be {"enabled": true|false}`)
		return
	}
	if err := s.setPeersEnabled(*req.Enabled); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	json.NewEncoder(w).Encode(map[string]bool{"enabled": s.peersEnabled()})
}
