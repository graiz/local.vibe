package daemon

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"
)

func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// The sweep covers private IPv4 subnets only, skips networks over the cap
// with a note, and never probes this machine or Tailscale/link-local ranges.
func TestSweepTargetsPrivateOnly(t *testing.T) {
	self := []net.IP{net.ParseIP("192.168.1.5")}
	cases := []struct {
		cidr      string
		wantHosts int
		wantNote  string
	}{
		{"192.168.1.0/24", 253, ""},     // 254 usable minus self
		{"172.16.4.0/22", 1022, ""},     // 1024 addresses: at the cap, swept
		{"10.0.0.0/16", 0, "too large"}, // over the cap
		{"100.101.102.0/24", 0, ""},     // Tailscale CGNAT range: never swept
		{"169.254.0.0/16", 0, ""},       // link-local
		{"203.0.113.0/24", 0, ""},       // public
	}
	for _, c := range cases {
		hosts, notes := sweepTargets([]*net.IPNet{mustCIDR(t, c.cidr)}, self, 1024)
		if len(hosts) != c.wantHosts {
			t.Errorf("%s: %d hosts; want %d", c.cidr, len(hosts), c.wantHosts)
		}
		joined := strings.Join(notes, " ")
		if (c.wantNote == "") != (joined == "") || !strings.Contains(joined, c.wantNote) {
			t.Errorf("%s: notes %q; want containing %q", c.cidr, joined, c.wantNote)
		}
		for _, h := range hosts {
			if h == netip.MustParseAddr("192.168.1.5") {
				t.Errorf("%s: swept this machine's own address", c.cidr)
			}
		}
	}
}

func TestParseTailscaleStatus(t *testing.T) {
	data, err := os.ReadFile("testdata/tailscale-status.json")
	if err != nil {
		t.Fatal(err)
	}
	nodes := parseTailscaleStatus(data)
	if len(nodes) != 1 || nodes[0].Name != "box-c" || nodes[0].IP != netip.MustParseAddr("100.91.0.2") ||
		nodes[0].DNSName != "box-c.example-tailnet.ts.net" {
		t.Fatalf("nodes = %+v; want only the online IPv4 peer box-c (self, offline, v6-only excluded)", nodes)
	}
	if got := parseTailscaleStatus([]byte("not json")); len(got) != 0 {
		t.Fatalf("garbage parsed to %+v", got)
	}
}

// A machine showing a code on the LAN shows up as a pairing candidate; LAN
// addresses with nothing there are left out, Tailscale nodes are kept (the
// picker says "no vibe found" for them).
func TestDiscoverReportsCandidates(t *testing.T) {
	f := newProbeFixture(t)
	if _, _, err := f.target.openPeerInvite(); err != nil {
		t.Fatal(err)
	}
	s := f.prober
	s.cfg.Daemon.Peers.Port = f.port
	// The "nothing there" hosts are RFC 5737 TEST-NET addresses, not other
	// 127/8 ones: on Linux and Windows all of 127/8 reaches loopback, where the
	// fixture's wildcard-bound peer listener answers (macOS has only
	// 127.0.0.1 on lo0, which hid this).
	s.discoverHostsFn = func() ([]netip.Addr, []string) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("192.0.2.2")}, nil
	}
	s.tailscaleNodesFn = func(context.Context) ([]tailscaleNode, string) {
		return []tailscaleNode{{Name: "nas", IP: netip.MustParseAddr("192.0.2.3")}}, ""
	}
	resp := s.discoverPeers(context.Background())
	var lan, ts []discoverCandidate
	for _, c := range resp.Candidates {
		if c.Source == "lan" {
			lan = append(lan, c)
		} else {
			ts = append(ts, c)
		}
	}
	if len(lan) != 1 || lan[0].Host != "127.0.0.1" || lan[0].State != probePairing {
		t.Fatalf("lan candidates = %+v; want only the code-showing 127.0.0.1", lan)
	}
	if len(ts) != 1 || ts[0].State != probeNone || ts[0].Name != "nas" {
		t.Fatalf("tailscale candidates = %+v; want nas as none", ts)
	}
}

// Discovery returns within its deadline even when every host stalls.
func TestDiscoverDeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	s := newBareServer(t)
	if err := s.ensurePeerIdentity(); err != nil {
		t.Fatal(err)
	}
	_, port := splitAddr(t, ln.Addr().String())
	s.cfg.Daemon.Peers.Port = port
	hosts := make([]netip.Addr, 300)
	for i := range hosts {
		hosts[i] = netip.MustParseAddr("127.0.0.1")
	}
	s.discoverHostsFn = func() ([]netip.Addr, []string) { return hosts, nil }
	s.tailscaleNodesFn = func(context.Context) ([]tailscaleNode, string) { return nil, "" }
	start := time.Now()
	s.discoverPeers(context.Background())
	if el := time.Since(start); el > 3500*time.Millisecond {
		t.Fatalf("discovery took %v; deadline is ~3 s", el)
	}
}

// The Tailscale app's bundled binary only acts as a CLI when told to: from a
// launchd-started daemon (bare environment) it tries to launch the GUI,
// prints an error and exits 0 — discovery silently found no Tailscale nodes.
func TestTailscaleStatusCmdForcesCLIMode(t *testing.T) {
	cmd := tailscaleStatusCmd(context.Background(), "/Applications/Tailscale.app/Contents/MacOS/Tailscale")
	found := false
	for _, e := range cmd.Env {
		if e == "TAILSCALE_BE_CLI=1" {
			found = true
		}
	}
	if !found || strings.Join(cmd.Args[1:], " ") != "status --json" {
		t.Fatalf("cmd = %v env-has-BE_CLI=%v", cmd.Args, found)
	}
}

// Output that isn't JSON is reported as a note, not silently treated as
// "no Tailscale machines".
func TestTailscaleNonJSONIsANote(t *testing.T) {
	nodes, note := tailscaleNodesFromOutput([]byte("The Tailscale GUI failed to start"), nil)
	if len(nodes) != 0 || note == "" {
		t.Fatalf("nodes=%v note=%q; want none and a note", nodes, note)
	}
}

// The response's notes are always a list ([] not null) for the dashboard JS
// and the CLI.
func TestDiscoverNotesNeverNull(t *testing.T) {
	s := newBareServer(t)
	s.discoverHostsFn = func() ([]netip.Addr, []string) { return nil, nil }
	s.tailscaleNodesFn = func(context.Context) ([]tailscaleNode, string) { return nil, "" }
	b, _ := json.Marshal(s.discoverPeers(context.Background()))
	if !strings.Contains(string(b), `"notes":[]`) {
		t.Fatalf("response = %s; want notes as []", b)
	}
}

// Review: a big LAN sweep must not starve Tailscale probes. They used to queue
// behind every LAN host on one semaphore, so on a /22 a paired Tailscale
// machine could miss the 3s window entirely.
func TestDiscoverTailscaleNotStarvedByLAN(t *testing.T) {
	prev := discoverParallel
	discoverParallel = 2
	t.Cleanup(func() { discoverParallel = prev })
	f := newProbeFixture(t)
	if _, _, err := f.target.openPeerInvite(); err != nil {
		t.Fatal(err)
	}
	s := f.prober
	s.cfg.Daemon.Peers.Port = f.port
	s.discoverHostsFn = func() ([]netip.Addr, []string) {
		var hosts []netip.Addr
		for i := 0; i < 64; i++ { // TEST-NET-1: never answers
			hosts = append(hosts, netip.AddrFrom4([4]byte{192, 0, 2, byte(i + 1)}))
		}
		return hosts, nil
	}
	s.tailscaleNodesFn = func(context.Context) ([]tailscaleNode, string) {
		return []tailscaleNode{{Name: "tsbox", IP: netip.MustParseAddr("127.0.0.1")}}, ""
	}
	resp := s.discoverPeers(context.Background())
	for _, c := range resp.Candidates {
		if c.Source == "tailscale" && c.State == probePairing {
			return
		}
	}
	t.Fatalf("tailscale node missing or not probed: %+v", resp.Candidates)
}
