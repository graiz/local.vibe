package daemon

// Discovery for the Add-peer picker: probe the local IPv4 subnets and the
// Tailscale nodes for vibe's peer port, on demand only. Nothing is ever
// broadcast; a machine is findable while its listener is up (paired, or
// showing a code), and the probe says which.

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	sweepMaxAddresses = 1024
	discoverDeadline  = 3 * time.Second
	discoverParallel  = 128
)

type discoverCandidate struct {
	peerProbeResult
	Source  string `json:"source"` // "lan" | "tailscale"
	DNSName string `json:"dns_name,omitempty"`
}

type discoverResponse struct {
	Candidates []discoverCandidate `json:"candidates"`
	Notes      []string            `json:"notes"`
}

type tailscaleNode struct {
	Name    string
	DNSName string
	IP      netip.Addr
}

// sweepTargets expands the private IPv4 subnets among nets into host
// addresses, excluding network/broadcast and this machine's own addresses.
// Subnets larger than max addresses are skipped with a note.
func sweepTargets(nets []*net.IPNet, self []net.IP, max int) ([]netip.Addr, []string) {
	skip := map[netip.Addr]bool{}
	for _, ip := range self {
		if a, ok := netip.AddrFromSlice(ip.To4()); ok {
			skip[a] = true
		}
	}
	var hosts []netip.Addr
	var notes []string
	seen := map[netip.Addr]bool{}
	for _, n := range nets {
		ip4 := n.IP.To4()
		if ip4 == nil {
			continue
		}
		base, _ := netip.AddrFromSlice(ip4.Mask(n.Mask))
		if !base.IsPrivate() {
			continue // public, Tailscale's 100.64/10, link-local: never swept
		}
		ones, bits := n.Mask.Size()
		if bits != 32 {
			continue
		}
		size := 1 << (32 - ones)
		if size > max {
			notes = append(notes, fmt.Sprintf("network %s is too large to scan (%d addresses) — enter an address instead", n.String(), size))
			continue
		}
		start := binary.BigEndian.Uint32(ip4.Mask(n.Mask))
		first, last := start, start+uint32(size)-1
		if size > 2 {
			first, last = first+1, last-1 // network and broadcast
		}
		for v := first; ; v++ {
			var b [4]byte
			binary.BigEndian.PutUint32(b[:], v)
			a := netip.AddrFrom4(b)
			if !skip[a] && !seen[a] {
				seen[a] = true
				hosts = append(hosts, a)
			}
			if v == last {
				break
			}
		}
	}
	return hosts, notes
}

// parseTailscaleStatus extracts online peers with an IPv4 address from
// `tailscale status --json`, excluding this machine.
func parseTailscaleStatus(data []byte) []tailscaleNode {
	var st struct {
		Peer map[string]struct {
			HostName     string
			DNSName      string
			TailscaleIPs []string
			Online       bool
		}
	}
	if json.Unmarshal(data, &st) != nil {
		return nil
	}
	var out []tailscaleNode
	for _, p := range st.Peer {
		if !p.Online {
			continue
		}
		for _, s := range p.TailscaleIPs {
			if a, err := netip.ParseAddr(s); err == nil && a.Is4() {
				out = append(out, tailscaleNode{Name: p.HostName, DNSName: strings.TrimSuffix(p.DNSName, "."), IP: a})
				break
			}
		}
	}
	return out
}

// findTailscaleCLI locates the tailscale CLI: PATH, then the macOS app
// bundle (not on PATH by default), then the Windows install dir.
func findTailscaleCLI() string {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p
	}
	for _, p := range []string{
		"/Applications/Tailscale.app/Contents/MacOS/Tailscale",
		filepath.Join(os.Getenv("ProgramFiles"), "Tailscale", "tailscale.exe"),
	} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// tailscaleStatusCmd builds `tailscale status --json`. TAILSCALE_BE_CLI=1
// matters for the macOS app's bundled binary: started from a launchd daemon
// (bare environment) it otherwise tries to launch the GUI, prints "The
// Tailscale GUI failed to start" and exits 0.
func tailscaleStatusCmd(ctx context.Context, cli string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, cli, "status", "--json")
	cmd.Env = append(os.Environ(), "TAILSCALE_BE_CLI=1")
	return cmd
}

// tailscaleNodesFromOutput turns the CLI's result into nodes, or a note when
// it failed or didn't print JSON — never a silent "no machines".
func tailscaleNodesFromOutput(out []byte, err error) ([]tailscaleNode, string) {
	if err != nil {
		return nil, fmt.Sprintf("couldn't read Tailscale status: %v", err)
	}
	if !json.Valid(out) {
		return nil, "couldn't read Tailscale status (the tailscale CLI printed something unexpected)"
	}
	return parseTailscaleStatus(out), ""
}

func defaultTailscaleNodes(ctx context.Context) ([]tailscaleNode, string) {
	cli := findTailscaleCLI()
	if cli == "" {
		return nil, "Tailscale not found on this machine"
	}
	return tailscaleNodesFromOutput(tailscaleStatusCmd(ctx, cli).Output())
}

func defaultDiscoverHosts() ([]netip.Addr, []string) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, []string{fmt.Sprintf("couldn't list network interfaces: %v", err)}
	}
	var nets []*net.IPNet
	var self []net.IP
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
				nets = append(nets, n)
				self = append(self, n.IP)
			}
		}
	}
	return sweepTargets(nets, self, sweepMaxAddresses)
}

// discoverPeers probes LAN hosts and Tailscale nodes on the peer port,
// returning whatever it has by the deadline.
func (s *Server) discoverPeers(ctx context.Context) discoverResponse {
	ctx, cancel := context.WithTimeout(ctx, discoverDeadline)
	defer cancel()
	hostsFn, tsFn := s.discoverHostsFn, s.tailscaleNodesFn
	if hostsFn == nil {
		hostsFn = defaultDiscoverHosts
	}
	if tsFn == nil {
		tsFn = defaultTailscaleNodes
	}
	port := s.cfg.Daemon.Peers.Port

	// The Tailscale CLI runs alongside the LAN sweep under the same
	// deadline, so a slow CLI can't use up the budget before any LAN probe.
	type tsResult struct {
		nodes []tailscaleNode
		note  string
	}
	tsDone := make(chan tsResult, 1)
	go func() { n, note := tsFn(ctx); tsDone <- tsResult{n, note} }()

	sem := make(chan struct{}, discoverParallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var lan, ts []discoverCandidate
	probe := func(host, source string, node tailscaleNode) {
		select {
		case <-ctx.Done():
			return
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r := s.probePeerHost(ctx, host, port)
			c := discoverCandidate{peerProbeResult: r, Source: source, DNSName: node.DNSName}
			if c.Name == "" && node.Name != "" {
				c.Name = sanitizeClaimedName(node.Name)
			}
			mu.Lock()
			if source == "lan" {
				lan = append(lan, c)
			} else {
				ts = append(ts, c)
			}
			mu.Unlock()
		}()
	}

	lanHosts, notes := hostsFn()
	for _, h := range lanHosts {
		probe(h.String(), "lan", tailscaleNode{})
	}
	select {
	case r := <-tsDone:
		if r.note != "" {
			notes = append(notes, r.note)
		}
		for _, n := range r.nodes {
			probe(n.IP.String(), "tailscale", n)
		}
	case <-ctx.Done():
		notes = append(notes, "Tailscale didn't answer in time; enter an address if a machine is missing")
	}
	wg.Wait()

	if notes == nil {
		notes = []string{} // [] not null, for the dashboard JS and CLI
	}
	resp := discoverResponse{Candidates: []discoverCandidate{}, Notes: notes}
	// One machine reachable at several addresses (Tailscale + each LAN
	// interface) is one row: merge by key, Tailscale first.
	seenHost, seenKey := map[string]bool{}, map[string]bool{}
	add := func(c discoverCandidate) {
		if seenHost[c.Host] || (c.Fingerprint != "" && seenKey[c.Fingerprint]) {
			return
		}
		seenHost[c.Host] = true
		if c.Fingerprint != "" {
			seenKey[c.Fingerprint] = true
		}
		resp.Candidates = append(resp.Candidates, c)
	}
	for _, c := range ts {
		add(c)
	}
	lanProbed, lanBlocked := len(lan), 0
	for _, c := range lan {
		if c.State == probeBlocked {
			lanBlocked++
		}
		if c.State == probeNone || c.State == probeBlocked {
			continue
		}
		add(c)
	}
	if lanProbed > 0 && lanBlocked == lanProbed {
		resp.Notes = append(resp.Notes, "local network access may be blocked — check System Settings → Privacy & Security → Local Network")
	}
	if ctx.Err() != nil && len(lanHosts) > 0 {
		resp.Notes = append(resp.Notes, "search stopped after 3 seconds; enter an address if a machine is missing")
	}
	return resp
}

// handlePeerDiscover is POST /_api/peers/discover: a POST because it has side
// effects (a network sweep, probes, creating the identity) and a GET could be
// triggered cross-site by an <img>.
func (s *Server) handlePeerDiscover(w http.ResponseWriter, r *http.Request) {
	if !s.peersEnabled() {
		writeJSONError(w, http.StatusConflict, peersDisabledMsg)
		return
	}
	json.NewEncoder(w).Encode(s.discoverPeers(r.Context()))
}
