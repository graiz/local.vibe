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

var peerInviteTTL = 5 * time.Minute // var so tests can shorten it

// peerPairMaxAttempts is how many proof checks one invite allows. The
// code is six digits; with the window open for five minutes and no cap, a LAN
// client at ~1k requests/s had a real chance of guessing it and getting its
// certificate pinned as a peer.
const peerPairMaxAttempts = 5

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
	code, err := peer.NewInviteCode()
	if err != nil {
		return "", time.Time{}, err
	}
	// Open the code BEFORE ensuring the listener: a maybeStopPeerListener
	// racing us (an old code's expiry timer, a last-peer removal) then sees
	// an open code and leaves the listener alone.
	expires := time.Now().Add(peerInviteTTL)
	s.peerMu.Lock()
	s.peerInviteCode = code
	s.peerInviteExpires = expires
	s.peerPairAttempts = 0
	s.peerMu.Unlock()
	if err := s.ensurePeerListener(); err != nil {
		s.peerMu.Lock()
		if s.peerInviteCode == code {
			s.peerInviteCode = ""
		}
		s.peerMu.Unlock()
		return "", time.Time{}, err
	}
	// Findable only while showing a code: once it lapses, an unpaired
	// machine stops listening again (a few ms of slack past the expiry).
	time.AfterFunc(time.Until(expires)+20*time.Millisecond, s.maybeStopPeerListener)
	return code, expires, nil
}

// peerPairReserve re-reads the invite and claims one of its
// peerPairMaxAttempts proof checks, atomically. It runs AFTER the request body
// has been read: a handler that snapshotted the code at entry and verified it
// after a slow body could check a guess against an invite the cap had already
// closed — holding K bodies open turned a 5-guess invite into a K-guess one.
// Counting attempts at reservation (not failures after the fact) bounds the
// checks per invite no matter how many requests are in flight.
func (s *Server) peerPairReserve() (code string, ok bool) {
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	code = s.peerInviteCode
	if code == "" || !time.Now().Before(s.peerInviteExpires) || s.peerPairAttempts >= peerPairMaxAttempts {
		return "", false
	}
	s.peerPairAttempts++
	return code, true
}

// peerPairFailed closes the invite once its attempts are spent. The caller
// still answers the generic 403.
func (s *Server) peerPairFailed(code string) {
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	if s.peerPairAttempts >= peerPairMaxAttempts && s.peerInviteCode == code && code != "" {
		s.peerInviteCode = ""
		fmt.Fprintf(os.Stderr, "vibe: peer invite closed after %d failed pairing attempts — run `vibe peer invite` again\n", s.peerPairAttempts)
	}
}

// handlePeerPair answers a pairing request on the LAN listener. Every
// failure is the same generic 403 — an attacker probing the window learns
// nothing about whether an invite exists, expired, or the code was wrong.
// The requester's fingerprint comes from the TLS client cert on the
// connection, never from the body, which binds the pin to the channel.
func (s *Server) handlePeerPair(w http.ResponseWriter, r *http.Request, clientFP string) {
	deny := func() { http.Error(w, "pairing failed", http.StatusForbidden) }

	// Cheap early exit only — the authoritative check is peerPairReserve,
	// after the body.
	s.peerMu.Lock()
	open := s.peerInviteCode != "" && time.Now().Before(s.peerInviteExpires)
	localFP := s.peerFP
	s.peerMu.Unlock()
	if !open {
		deny()
		return
	}

	var req pairRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, apiMaxBodyBytes)).Decode(&req); err != nil {
		deny()
		return
	}
	code, ok := s.peerPairReserve()
	if !ok {
		deny()
		return
	}
	if !peer.VerifyProof(req.Proof, code, clientFP, localFP) {
		s.peerPairFailed(code)
		deny()
		// If that closed the invite on an unpaired machine, stop listening —
		// after this 403 has been written, not from under it.
		time.AfterFunc(200*time.Millisecond, s.maybeStopPeerListener)
		return
	}
	// One-time: consume the invite before pinning, so two concurrent correct
	// proofs can't both pair off one code.
	s.peerMu.Lock()
	consumed := s.peerInviteCode == code
	if consumed {
		s.peerInviteCode = ""
	}
	s.peerMu.Unlock()
	if !consumed {
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(pairResponse{
		Name:  localPeerName(),
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
	return s.pairWithPeerExpecting(host, port, code, "")
}

// pairWithPeerExpecting is pairWithPeer that also refuses, before sending
// any proof, when the machine answering has a different key than expectFP
// (the key the dashboard showed the user). "" skips the check (CLI).
func (s *Server) pairWithPeerExpecting(host string, port int, code, expectFP string) (peer.Peer, error) {
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

	if expectFP != "" && !strings.EqualFold(serverFP, expectFP) {
		return peer.Peer{}, fmt.Errorf("the machine at %s now has a different key than the one shown (%s) — not pairing; check the key on its screen", host, peer.ShortKey(expectFP))
	}

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
	hostname, _ := peerHostname()
	body, err := json.Marshal(pairRequest{
		Name:  localPeerName(),
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
		return peer.Peer{}, fmt.Errorf("pairing failed — check the code, that it's still showing on the other machine, and that both machines run the same vibe version")
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
		Name:        peer.SanitizeName(pr.Name), // the responder's claim is data, not a key
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
// peerHostname is os.Hostname, indirected so tests can simulate a rename.
var peerHostname = os.Hostname

// localPeerName is the name this daemon gives itself on the peer channel:
// its hostname, sanitized the same way a receiving peer would.
func localPeerName() string {
	h, _ := peerHostname()
	return peer.SanitizeName(h)
}

func (s *Server) storePeer(p peer.Peer) error {
	s.peerMu.Lock()
	replaced := false
	for i := range s.peerList {
		if s.peerList[i].Fingerprint == p.Fingerprint {
			p.AddedAt = s.peerList[i].AddedAt
			if s.peerList[i].NameSetByUser { // a name the user typed sticks
				p.Name, p.NameSetByUser = s.peerList[i].Name, true
			}
			s.peerList[i] = p
			replaced = true
			break
		}
	}
	if !replaced {
		s.peerList = append(s.peerList, p)
	}
	err := peer.SavePeers(s.configDir(), s.peerList)
	s.peerMu.Unlock()
	// A first pairing made at runtime must start the periodic sync; it used
	// to start only at daemon startup. Idempotent.
	s.startPeerSync(false)
	return err
}
