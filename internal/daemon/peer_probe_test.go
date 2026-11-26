package daemon

import (
	"context"
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
	if r.State != "none" || !strings.Contains(r.Message, "Enable peers") || !strings.Contains(r.Message, "Show a code") {
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
