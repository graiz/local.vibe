package daemon

// Invite-code pairing for the experimental peer subsystem, both sides:
// handlePeerPair answers on the LAN listener while an invite is open, and
// pairWithPeer dials out when this machine runs `vibe peer add`. Trust is
// mutual fingerprint pinning; the invite code authenticates the one exchange
// that establishes it. Every proof binds the code to the certificate
// fingerprints actually seen on the wire, so a MITM without the code can
// neither forge a request nor splice its own cert into either direction.

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/graiz/local.vibe/internal/peer"
)

const peerInviteTTL = 5 * time.Minute

type pairRequest struct {
	Name  string `json:"name"`
	Host  string `json:"host"`
	Port  int    `json:"port"`
	Proof string `json:"proof"`
}

type pairResponse struct {
	Name  string `json:"name"`
	Port  int    `json:"port"`
	Proof string `json:"proof"`
}

// openPeerInvite opens a one-time pairing window: generates a code, makes
// sure the identity and LAN listener exist, and returns the code + expiry.
// The listener may thus start before any peer exists — that window is the
// only time an unpinned cert can complete a handshake, and it is confined
// to /peer/pair by peerHandler.
func (s *Server) openPeerInvite() (string, time.Time, error) {
	if err := s.ensurePeerIdentity(); err != nil {
		return "", time.Time{}, err
	}
	if err := s.ensurePeerListener(); err != nil {
		return "", time.Time{}, err
	}
	code, err := peer.NewInviteCode()
	if err != nil {
		return "", time.Time{}, err
	}
	expires := time.Now().Add(peerInviteTTL)
	s.peerMu.Lock()
	s.peerInviteCode = code
	s.peerInviteExpires = expires
	s.peerMu.Unlock()
	return code, expires, nil
}

// handlePeerPair answers a pairing request on the LAN listener. Every
// failure is the same generic 403 — an attacker probing the window learns
// nothing about whether an invite exists, expired, or the code was wrong.
// The requester's fingerprint comes from the TLS client cert on the
// connection, never from the body, which binds the pin to the channel.
func (s *Server) handlePeerPair(w http.ResponseWriter, r *http.Request, clientFP string) {
	deny := func() { http.Error(w, "pairing failed", http.StatusForbidden) }

	s.peerMu.Lock()
	code := s.peerInviteCode
	open := code != "" && time.Now().Before(s.peerInviteExpires)
	localFP := s.peerFP
	s.peerMu.Unlock()
	if !open {
		deny()
		return
	}

	var req pairRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		deny()
		return
	}
	if !peer.VerifyProof(req.Proof, code, clientFP, localFP) {
		deny()
		return
	}

	host := strings.TrimSpace(req.Host)
	if host == "" {
		host, _, _ = net.SplitHostPort(r.RemoteAddr)
	}
	newPeer := peer.Peer{
		Name:        peer.SanitizeName(req.Name),
		Host:        host,
		Port:        req.Port,
		Fingerprint: clientFP,
		AddedAt:     time.Now(),
	}
	if err := s.storePeer(newPeer); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not persist peer: %v\n", err)
		deny()
		return
	}

	// The invite is one-time: consumed by the first successful pairing.
	s.peerMu.Lock()
	s.peerInviteCode = ""
	s.peerMu.Unlock()

	hostname, _ := os.Hostname()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(pairResponse{
		Name:  peer.SanitizeName(hostname),
		Port:  s.cfg.Daemon.Peers.Port,
		Proof: peer.Proof(code, localFP, clientFP),
	})
}

// pairWithPeer runs the initiator side against host:port using an invite
// code read off the other machine's screen. Three steps: learn the server's
// fingerprint from a probe handshake, send a proof bound to both
// fingerprints over a connection pinned to that server cert, and verify the
// role-swapped proof in the reply — which fails if any relay substituted a
// certificate in either direction.
func (s *Server) pairWithPeer(host string, port int, code string) (peer.Peer, error) {
	if err := s.ensurePeerIdentity(); err != nil {
		return peer.Peer{}, err
	}
	s.peerMu.Lock()
	id := *s.peerIdentity
	localFP := s.peerFP
	s.peerMu.Unlock()

	addr := net.JoinHostPort(host, fmt.Sprint(port))

	// Step 1: probe handshake to learn the responder's fingerprint.
	var serverFP string
	probeConf := peer.ClientTLSConfig(id, func(fp string) error {
		serverFP = fp
		return nil
	})
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", addr, probeConf)
	if err != nil {
		return peer.Peer{}, fmt.Errorf("pairing: cannot reach %s: %w", addr, err)
	}
	conn.Close()

	// Step 2: the pairing request, pinned to the fingerprint just observed.
	pinned := peer.ClientTLSConfig(id, func(fp string) error {
		if fp != serverFP {
			return fmt.Errorf("peer certificate changed during pairing")
		}
		return nil
	})
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: pinned},
	}
	hostname, _ := os.Hostname()
	body, err := json.Marshal(pairRequest{
		Name:  peer.SanitizeName(hostname),
		Host:  hostname,
		Port:  s.cfg.Daemon.Peers.Port,
		Proof: peer.Proof(code, localFP, serverFP),
	})
	if err != nil {
		return peer.Peer{}, err
	}
	resp, err := client.Post("https://"+addr+"/peer/pair", "application/json", bytes.NewReader(body))
	if err != nil {
		return peer.Peer{}, fmt.Errorf("pairing: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return peer.Peer{}, fmt.Errorf("pairing rejected — check the invite code and that `vibe peer invite` is still open on the other machine")
	}
	var pr pairResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return peer.Peer{}, fmt.Errorf("pairing: bad response: %w", err)
	}

	// Step 3: the responder must prove the same code over the same certs.
	if !peer.VerifyProof(pr.Proof, code, serverFP, localFP) {
		return peer.Peer{}, fmt.Errorf("pairing: responder failed mutual proof — possible interception, not pairing")
	}

	peerPort := pr.Port
	if peerPort == 0 {
		peerPort = port
	}
	newPeer := peer.Peer{
		Name:        pr.Name,
		Host:        host,
		Port:        peerPort,
		Fingerprint: serverFP,
		AddedAt:     time.Now(),
	}
	if err := s.storePeer(newPeer); err != nil {
		return peer.Peer{}, err
	}
	if err := s.ensurePeerListener(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
	return newPeer, nil
}

// storePeer appends (or, matched by fingerprint, updates in place — a
// re-pair refreshes host/port) and persists peers.json. Slice order is the
// collision tie-break order, so updates never reorder.
func (s *Server) storePeer(p peer.Peer) error {
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	replaced := false
	for i := range s.peerList {
		if s.peerList[i].Fingerprint == p.Fingerprint {
			p.AddedAt = s.peerList[i].AddedAt
			s.peerList[i] = p
			replaced = true
			break
		}
	}
	if !replaced {
		s.peerList = append(s.peerList, p)
	}
	return peer.SavePeers(s.configDir(), s.peerList)
}
