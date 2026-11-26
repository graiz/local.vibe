package daemon

// The probe: what is at host:port, from a pairing point of view? Shared by
// the Add-peer picker's discovery and its typed-address Check. Nothing is
// trusted from a probe — it only decides which message to show.

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"syscall"
	"time"

	"github.com/graiz/local.vibe/internal/config"
	"github.com/graiz/local.vibe/internal/peer"
)

// Probe states (spec §6).
const (
	probeNone      = "none"       // nothing answered
	probeBlocked   = "blocked"    // macOS may be blocking local network access
	probePairing   = "pairing"    // showing a code; offer the code field
	probeTrustsUs  = "trusts-us"  // it still pins us; we removed it
	probePaired    = "paired"     // pinned both ways
	probeVibe      = "vibe"       // vibe peers on, no code showing
	probeRemovedUs = "removed-us" // we pin it; it removed us
)

const (
	probeDialTimeout = 300 * time.Millisecond
	probeConnTimeout = 2 * time.Second
)

type peerProbeResult struct {
	Host  string `json:"host"`
	Port  int    `json:"port"`
	State string `json:"state"`
	Name  string `json:"name,omitempty"` // sanitized claim from /peer/hello
	Key   string `json:"key,omitempty"`  // peer.ShortKey of the server cert
	// Fingerprint is the full server key, sent back when pairing so the key
	// the user compared is the key that gets pinned.
	Fingerprint string `json:"fingerprint,omitempty"`
	PairedAs    string `json:"paired_as,omitempty"` // local name when pinned here
	Message     string `json:"message"`
}

type peerHello struct {
	Name     string `json:"name"`
	CodeOpen bool   `json:"code_open"`
}

// probePeerHost dials host:port, does a TLS handshake presenting this
// daemon's peer identity, and asks GET /peer/hello. Under TLS 1.3 a rejected
// client cert only surfaces on the first read, so the request — not the
// handshake — tells "admitted" from "rejected".
func (s *Server) probePeerHost(ctx context.Context, host string, port int) peerProbeResult {
	r := peerProbeResult{Host: host, Port: port}
	if err := s.ensurePeerIdentity(); err != nil {
		return r.with(probeNone)
	}
	s.peerMu.Lock()
	id := *s.peerIdentity
	s.peerMu.Unlock()

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := (&net.Dialer{Timeout: probeDialTimeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		if (errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH)) && isPrivateLAN(host) {
			return r.with(probeBlocked)
		}
		return r.with(probeNone)
	}
	defer conn.Close()
	deadline := time.Now().Add(probeConnTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)

	var serverFP string
	tc := tls.Client(conn, peer.ClientTLSConfig(id, func(fp string) error {
		serverFP = fp
		return nil // record, never trust: this is a probe
	}))
	if err := tc.Handshake(); err != nil {
		return r.with(probeNone) // not a TLS 1.3 peer listener
	}
	r.Key = peer.ShortKey(serverFP)
	r.Fingerprint = serverFP
	if p := s.peerByFingerprint(serverFP); p != nil {
		r.PairedAs = p.Name
	}

	hello, admitted := fetchHello(tc)
	switch {
	case !admitted && r.PairedAs != "":
		return r.with(probeRemovedUs)
	case !admitted:
		return r.with(probeVibe)
	case hello == nil:
		return r.with(probeNone) // TLS service on the port, but not vibe
	}
	r.Name = sanitizeClaimedName(hello.Name)
	switch {
	case r.PairedAs != "":
		return r.with(probePaired)
	case hello.CodeOpen:
		return r.with(probePairing)
	default:
		return r.with(probeTrustsUs)
	}
}

// fetchHello sends GET /peer/hello on an established TLS conn. admitted is
// false when the server rejected our certificate (alert/reset/EOF on read);
// hello is nil when it answered but not as a vibe peer.
func fetchHello(tc *tls.Conn) (hello *peerHello, admitted bool) {
	if _, err := io.WriteString(tc, "GET /peer/hello HTTP/1.1\r\nHost: peer\r\nConnection: close\r\n\r\n"); err != nil {
		return nil, false
	}
	resp, err := http.ReadResponse(bufio.NewReader(tc), nil)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, true
	}
	var h peerHello
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&h); err != nil || h.Name == "" {
		return nil, true
	}
	return &h, true
}

func sanitizeClaimedName(name string) string {
	if name == "" {
		return ""
	}
	return peer.SanitizeName(name)
}

// isPrivateLAN reports whether host is an RFC 1918 IPv4 address — the only
// addresses for which "no route to host" suggests macOS Local Network privacy
// rather than a machine that simply isn't there.
func isPrivateLAN(host string) bool {
	a, err := netip.ParseAddr(host)
	if err != nil || !a.Is4() {
		return false
	}
	return a.IsPrivate()
}

// with sets the state and its user-facing message (spec §6).
func (r peerProbeResult) with(state string) peerProbeResult {
	r.State = state
	who := r.Name
	if who == "" {
		who = r.Host
	}
	switch state {
	case probeNone:
		r.Message = fmt.Sprintf("No vibe peer service answered at %s:%d. On that machine: install local.vibe (clone github.com/graiz/local.vibe and run ./setup.sh, or setup.ps1 on Windows — see the README), open its dashboard, click Enable peers, then Show a code.", r.Host, r.Port)
	case probeBlocked:
		r.Message = fmt.Sprintf("Couldn't reach %s. The machine may be offline or asleep — or macOS may be blocking vibe's local network access (System Settings → Privacy & Security → Local Network).", r.Host)
	case probePairing:
		r.Message = fmt.Sprintf("%s is showing a code. Enter it to pair; check that its key matches %s.", who, r.Key)
	case probeTrustsUs:
		r.Message = fmt.Sprintf("%s still trusts this machine (you removed it here). Show a code on %s to pair again.", who, who)
	case probePaired:
		r.Message = fmt.Sprintf("Already paired as %s.", r.PairedAs)
	case probeVibe:
		r.Message = fmt.Sprintf("vibe is running at %s but isn't showing a code. Click Show a code in its dashboard (or run `vibe peer invite`).", r.Host)
	case probeRemovedUs:
		r.Message = fmt.Sprintf("Paired here as %s, but %s no longer trusts this machine. Remove it here, then pair again with a new code.", r.PairedAs, r.Host)
	}
	return r
}

// handlePeerProbe is POST /_api/peers/probe {"host", "port"}.
func (s *Server) handlePeerProbe(w http.ResponseWriter, r *http.Request) {
	if !s.peersEnabled() {
		writeJSONError(w, http.StatusConflict, peersDisabledMsg)
		return
	}
	var req struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Host == "" {
		writeJSONError(w, http.StatusBadRequest, "host is required")
		return
	}
	if req.Port == 0 {
		req.Port = config.DefaultPeerPort
	}
	if req.Port < 1 || req.Port > 65535 {
		writeJSONError(w, http.StatusBadRequest, "port must be 1-65535")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	json.NewEncoder(w).Encode(s.probePeerHost(ctx, req.Host, req.Port))
}
