package daemon

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/graiz/local.vibe/internal/peer"
)

// probeFixture is a prober daemon and a target daemon with a listening peer
// port. Pins and invites are arranged per test.
type probeFixture struct {
	prober, target *Server
	port           int
}

func newProbeFixture(t *testing.T) *probeFixture {
	t.Helper()
	p, tg := newBareServer(t), newBareServer(t)
	for _, s := range []*Server{p, tg} {
		if err := s.ensurePeerIdentity(); err != nil {
			t.Fatal(err)
		}
	}
	if err := tg.ensurePeerListener(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePeerLn(tg) })
	_, port := splitAddr(t, tg.peerLn.Addr().String())
	return &probeFixture{prober: p, target: tg, port: port}
}

func pin(s *Server, name, fp string) {
	s.peerMu.Lock()
	s.peerList = append(s.peerList, peer.Peer{Name: name, Host: "127.0.0.1", Port: 1, Fingerprint: fp})
	s.peerMu.Unlock()
}

func (f *probeFixture) probe(t *testing.T) peerProbeResult {
	t.Helper()
	return f.prober.probePeerHost(context.Background(), "127.0.0.1", f.port)
}

func TestProbeNone(t *testing.T) {
	s := newBareServer(t)
	if err := s.ensurePeerIdentity(); err != nil {
		t.Fatal(err)
	}
	r := s.probePeerHost(context.Background(), "127.0.0.1", freeLoopbackPort(t))
	if r.State != "none" || !strings.Contains(r.Message, "Enable peers") || !strings.Contains(r.Message, "Add peer") || strings.Contains(r.Message, "Show a code") {
		t.Fatalf("closed port: %+v", r)
	}
}

func TestProbeVibeNoCode(t *testing.T) {
	f := newProbeFixture(t)
	pin(f.target, "someone-else", "ff")
	r := f.probe(t)
	if r.State != "vibe" || r.Key != peer.ShortKey(f.target.peerFP) {
		t.Fatalf("listening, no code, not pinned: %+v", r)
	}
}

func TestProbePairing(t *testing.T) {
	f := newProbeFixture(t)
	if _, _, err := f.target.openPeerInvite(); err != nil {
		t.Fatal(err)
	}
	r := f.probe(t)
	if r.State != "pairing" || r.Name != localPeerName() || r.Key != peer.ShortKey(f.target.peerFP) {
		t.Fatalf("code open: %+v", r)
	}
}

func TestProbePaired(t *testing.T) {
	f := newProbeFixture(t)
	pin(f.target, "prober", f.prober.peerFP)
	pin(f.prober, "target-box", f.target.peerFP)
	r := f.probe(t)
	if r.State != "paired" || r.PairedAs != "target-box" {
		t.Fatalf("mutually pinned: %+v", r)
	}
}

func TestProbeTrustsUs(t *testing.T) {
	f := newProbeFixture(t)
	pin(f.target, "prober", f.prober.peerFP)
	r := f.probe(t)
	if r.State != "trusts-us" {
		t.Fatalf("target pins us, we removed it: %+v", r)
	}
}

func TestProbeRemovedUs(t *testing.T) {
	f := newProbeFixture(t)
	pin(f.target, "someone-else", "ff")
	pin(f.prober, "target-box", f.target.peerFP)
	r := f.probe(t)
	if r.State != "removed-us" || r.PairedAs != "target-box" {
		t.Fatalf("we pin target, it removed us: %+v", r)
	}
}

// Review Focus 4: something on the port that accepts TCP and never speaks
// TLS must not hang the picker.
func TestProbeStalledHostTimesOut(t *testing.T) {
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
			defer c.Close() // hold it open, say nothing
		}
	}()
	s := newBareServer(t)
	if err := s.ensurePeerIdentity(); err != nil {
		t.Fatal(err)
	}
	_, port := splitAddr(t, ln.Addr().String())
	start := time.Now()
	r := s.probePeerHost(context.Background(), "127.0.0.1", port)
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("probe took %v against a stalled host", el)
	}
	if r.State != "none" {
		t.Fatalf("stalled host: %+v", r)
	}
}

// Review Focus 5: a name a machine reports about itself is untrusted text.
func TestProbeSanitizesName(t *testing.T) {
	prev := peerHostname
	t.Cleanup(func() { peerHostname = prev })
	peerHostname = func() (string, error) { return "<b>Evil Box</b>", nil }
	f := newProbeFixture(t)
	if _, _, err := f.target.openPeerInvite(); err != nil {
		t.Fatal(err)
	}
	r := f.probe(t)
	if strings.ContainsAny(r.Name, "<> ") || r.Name == "" {
		t.Fatalf("unsanitized name %q", r.Name)
	}
}

// The probe endpoint is dashboard/CLI-only: a route's own page can't use it.
func TestPeerProbeEndpointGated(t *testing.T) {
	s := testServer(t)
	s.peersOn.Store(true)
	s.table.Add(&Route{Name: "app", Type: RouteManaged, Port: 4100, Cmd: "x", RegisteredAt: time.Now()})
	req := httptest.NewRequest(http.MethodPost, "/_api/peers/probe", strings.NewReader(`{"host":"127.0.0.1","port":1}`))
	req.Host = "app.test"
	req.Header.Set("Origin", "https://app.test")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("probe from a route origin = %d; want 403", w.Code)
	}
}

// fakePeerListener is a peer-identity TLS listener that admits any client and
// then runs serve on each connection — a stand-in for a slow or older peer.
func fakePeerListener(t *testing.T, serve func(c *tls.Conn)) (s *Server, port int) {
	t.Helper()
	s = newBareServer(t)
	if err := s.ensurePeerIdentity(); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	cfg := peer.ServerTLSConfig(*s.peerIdentity, func(string) bool { return true })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				tc := tls.Server(c, cfg)
				if tc.Handshake() == nil {
					serve(tc)
				}
			}()
		}
	}()
	_, port = splitAddr(t, ln.Addr().String())
	return s, port
}

// Review: a paired peer that is merely slow to answer /peer/hello (deadline
// hit mid-discovery) is still paired — never "removed this machine", which
// would offer Unpair on a healthy pairing.
func TestProbeSlowPairedPeerIsNotRemovedUs(t *testing.T) {
	stall := make(chan struct{})
	t.Cleanup(func() { close(stall) })
	target, port := fakePeerListener(t, func(*tls.Conn) { <-stall })
	prober := newBareServer(t)
	if err := prober.ensurePeerIdentity(); err != nil {
		t.Fatal(err)
	}
	pin(prober, "slowbox", target.peerFP)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if r := prober.probePeerHost(ctx, "127.0.0.1", port); r.State != probePaired || r.PairedAs != "slowbox" {
		t.Fatalf("slow pinned peer: %+v; want paired", r)
	}

	// Unpinned and slow: we know nothing about it — not "vibe, no code".
	other := newBareServer(t)
	if err := other.ensurePeerIdentity(); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	if r := other.probePeerHost(ctx2, "127.0.0.1", port); r.State != probeNone {
		t.Fatalf("slow unpinned host: %+v; want none", r)
	}
}

// Review: a paired machine still on a build without /peer/hello (it answers
// 404 through route proxying) shows as paired, not "no vibe found".
func TestProbeOlderPairedPeerIsPaired(t *testing.T) {
	target, port := fakePeerListener(t, func(c *tls.Conn) {
		_, _ = http.ReadRequest(bufio.NewReader(c))
		_, _ = io.WriteString(c, "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
	})
	prober := newBareServer(t)
	if err := prober.ensurePeerIdentity(); err != nil {
		t.Fatal(err)
	}
	pin(prober, "oldbox", target.peerFP)
	if r := prober.probePeerHost(context.Background(), "127.0.0.1", port); r.State != probePaired || r.PairedAs != "oldbox" {
		t.Fatalf("older pinned peer: %+v; want paired", r)
	}
}
