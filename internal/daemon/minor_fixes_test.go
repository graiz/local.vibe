package daemon

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/graiz/local.vibe/internal/peer"
)

// Outbound requests to a peer that are in flight — a slow relay, an HMR
// WebSocket — are cut when the peer is removed (and so on Turn off), not
// left running on a connection the pool no longer owns.
func TestRemovePeerCutsInFlightOutbound(t *testing.T) {
	release := make(chan struct{})
	got := make(chan struct{}, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- struct{}{}
		<-release
	}))
	t.Cleanup(func() { close(release); backend.Close() })
	b := newBareServer(t)
	a := newBareServer(t)
	pairAB(t, a, b)
	route := &Route{Name: "slow", Type: RouteSticky, Port: portOf(t, backend.URL), RegisteredAt: time.Now()}
	route.Running.Store(true)
	route.Ready.Store(true)
	b.table.Add(route)
	p := a.peerList[0]

	done := make(chan int, 1)
	go func() {
		req := httptest.NewRequest("GET", "http://slow.vibe/", nil)
		req.Host = "slow.vibe"
		w := httptest.NewRecorder()
		a.proxyToPeer(w, req, p, "slow")
		done <- w.Code
	}()
	<-got
	rec := httptest.NewRecorder()
	a.handlePeerRemove(rec, nil, p.Name)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight relay to a removed peer still running")
	}
}

// A maybeStopPeerListener racing openPeerInvite (an old code's expiry timer,
// the post-failure timer, a last-peer removal) must not leave a fresh code
// showing with nothing listening.
func TestOpenInviteSurvivesConcurrentStop(t *testing.T) {
	s := newBareServer(t)
	t.Cleanup(func() { closePeerLn(s) })
	for i := 0; i < 200; i++ {
		stop := make(chan struct{})
		go func() {
			for {
				select {
				case <-stop:
					return
				default:
					s.maybeStopPeerListener()
				}
			}
		}()
		_, _, err := s.openPeerInvite()
		close(stop)
		if err != nil {
			t.Fatal(err)
		}
		if !listenerUp(s) {
			t.Fatalf("iteration %d: code open but nothing listening", i)
		}
		s.peerMu.Lock()
		s.peerInviteCode = ""
		s.peerMu.Unlock()
		s.maybeStopPeerListener()
	}
}

// A slow Tailscale CLI must not use up the deadline before the LAN sweep
// runs.
func TestSlowTailscaleDoesNotStarveLAN(t *testing.T) {
	f := newProbeFixture(t)
	if _, _, err := f.target.openPeerInvite(); err != nil {
		t.Fatal(err)
	}
	s := f.prober
	s.cfg.Daemon.Peers.Port = f.port
	s.discoverHostsFn = func() ([]netip.Addr, []string) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	s.tailscaleNodesFn = func(ctx context.Context) ([]tailscaleNode, string) {
		select {
		case <-time.After(2800 * time.Millisecond):
		case <-ctx.Done():
		}
		return nil, ""
	}
	start := time.Now()
	resp := s.discoverPeers(context.Background())
	if el := time.Since(start); el > 3500*time.Millisecond {
		t.Fatalf("discovery took %v", el)
	}
	if len(resp.Candidates) != 1 || resp.Candidates[0].State != probePairing {
		t.Fatalf("candidates = %+v; want the LAN machine despite a slow Tailscale CLI", resp.Candidates)
	}
}

// One machine reachable at several addresses (Tailscale + LAN interfaces)
// is one row: candidates are merged by key, Tailscale first.
func TestDiscoverMergesSameMachine(t *testing.T) {
	f := newProbeFixture(t)
	if _, _, err := f.target.openPeerInvite(); err != nil {
		t.Fatal(err)
	}
	s := f.prober
	s.cfg.Daemon.Peers.Port = f.port
	s.discoverHostsFn = func() ([]netip.Addr, []string) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	s.tailscaleNodesFn = func(context.Context) ([]tailscaleNode, string) {
		return []tailscaleNode{{Name: "box", IP: netip.MustParseAddr("::1")}}, ""
	}
	resp := s.discoverPeers(context.Background())
	if len(resp.Candidates) != 1 || resp.Candidates[0].Source != "tailscale" {
		t.Fatalf("candidates = %+v; want one merged row from Tailscale", resp.Candidates)
	}
}

// Re-pairing a machine keeps a name the user typed.
func TestRepairKeepsUserName(t *testing.T) {
	s := newBareServer(t)
	s.peerList = []peer.Peer{{Name: "my-box", Host: "h", Port: 1, Fingerprint: "fp", NameSetByUser: true}}
	if err := s.storePeer(peer.Peer{Name: "machine-name", Host: "h2", Port: 2, Fingerprint: "fp"}); err != nil {
		t.Fatal(err)
	}
	if p := s.peerList[0]; p.Name != "my-box" || !p.NameSetByUser || p.Host != "h2" {
		t.Fatalf("after re-pair: %+v", p)
	}
}

func TestPeerEditNameLengthCap(t *testing.T) {
	s := newBareServer(t)
	s.peerList = []peer.Peer{{Name: "one", Host: "h", Port: 1, Fingerprint: "fp"}}
	long := strings.Repeat("a", 64)
	if w := putPeer(t, s, "one", fmt.Sprintf(`{"name":%q}`, long)); w.Code != http.StatusBadRequest {
		t.Fatalf("64-char name = %d; want 400", w.Code)
	}
	if w := putPeer(t, s, "one", fmt.Sprintf(`{"name":%q}`, long[:63])); w.Code != http.StatusOK {
		t.Fatalf("63-char name = %d; want 200", w.Code)
	}
}

// A failed save leaves both the switch and the in-memory config unchanged.
func TestSetPeersEnabledFailedSaveChangesNothing(t *testing.T) {
	s := newDisabledServer(t)
	blocker := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(blocker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	s.ConfigDir = blocker // config.json can't be written under a file
	if err := s.setPeersEnabled(true); err == nil {
		t.Fatal("save into a file path succeeded?")
	}
	if s.peersEnabled() || s.cfg.Daemon.Peers.Enabled {
		t.Fatalf("failed save still turned peers on (flag %v, cfg %v)", s.peersEnabled(), s.cfg.Daemon.Peers.Enabled)
	}
}
