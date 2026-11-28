package daemon

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/graiz/local.vibe/internal/config"
	"github.com/graiz/local.vibe/internal/peer"
)

// newDisabledServer is newBareServer with peers off in config.
func newDisabledServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Daemon.Port = 0
	cfg.Daemon.TLS.Enabled = false
	cfg.Daemon.Peers.Enabled = false
	cfg.Daemon.Peers.Port = 0
	cfg.Daemon.TLS.CertsDir = t.TempDir()
	s := NewServer(cfg)
	s.ConfigDir = t.TempDir()
	t.Cleanup(func() { _ = s.setPeersEnabled(false) })
	return s
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func configSaysEnabled(t *testing.T, s *Server) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(s.configDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func listenerUp(s *Server) bool {
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	return s.peerLn != nil
}

// Turning peers on persists the choice and brings the subsystem up without a
// restart — but an unpaired machine still exposes nothing (findable only
// while showing a code).
func TestEnablePeersAtRuntime(t *testing.T) {
	s := newDisabledServer(t)
	if s.peersEnabled() {
		t.Fatal("peers should start disabled")
	}
	if err := s.setPeersEnabled(true); err != nil {
		t.Fatal(err)
	}
	if !s.peersEnabled() {
		t.Fatal("setPeersEnabled(true) did not enable")
	}
	if cfg := configSaysEnabled(t, s); !strings.Contains(cfg, `"enabled": true`) {
		t.Fatalf("config.json not updated: %s", cfg)
	}
	if listenerUp(s) {
		t.Fatal("enabling with no peers started the listener")
	}

	// With a pairing on disk, enabling brings up the listener and one sync loop.
	s2 := newDisabledServer(t)
	if err := peer.SavePeers(s2.configDir(), []peer.Peer{{Name: "box", Host: "127.0.0.1", Port: 1, Fingerprint: "ff"}}); err != nil {
		t.Fatal(err)
	}
	if err := s2.setPeersEnabled(true); err != nil {
		t.Fatal(err)
	}
	if !listenerUp(s2) {
		t.Fatal("enabling with a pairing did not start the listener")
	}
	waitFor(t, "sync loop", func() bool { return s2.peerSyncLoops.Load() == 1 })
}

// Off means off: the listener closes, an in-flight relay is cut rather than
// allowed to finish, an open code is withdrawn, peer caches and SANs go, and
// the pairings stay on disk for next time.
func TestDisablePeersTearsDown(t *testing.T) {
	release := make(chan struct{})
	gotRequest := make(chan struct{}, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRequest <- struct{}{}
		<-release
	}))
	t.Cleanup(func() { close(release); backend.Close() })

	// pairAB stores b's real listener address on a (a's own advertised
	// address isn't dialable in tests), so a is the machine turned off here.
	b := newBareServer(t)
	a := newBareServer(t)
	pairAB(t, a, b)
	route := &Route{Name: "slow", Type: RouteSticky, Port: portOf(t, backend.URL), RegisteredAt: time.Now()}
	route.Running.Store(true)
	route.Ready.Store(true)
	a.table.Add(route)
	b.table.Add(&Route{Name: "bshare", Type: RouteSticky, Port: 1, RegisteredAt: time.Now()})
	a.refreshAllPeers(true) // a caches b's routes → bshare in a's SANs
	if !containsHost(a.tlsHostnames(), "bshare.vibe") {
		t.Fatalf("precondition: bshare.vibe not in a's SANs: %v", a.tlsHostnames())
	}
	if _, _, err := a.openPeerInvite(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(a.configDir(), "peers.json"))

	// An in-flight relay request from b (pinned on a) to a's slow route.
	c := peerHTTPClient(mustClientIdentity(t, b), a.peerFP)
	reqDone := make(chan error, 1)
	go func() {
		req, _ := http.NewRequest("GET", "https://"+a.peerLn.Addr().String()+"/", nil)
		req.Host = "slow.vibe"
		resp, err := c.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		reqDone <- err
	}()
	<-gotRequest

	start := time.Now()
	if err := a.setPeersEnabled(false); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("disable waited %v for in-flight relays", el)
	}
	select {
	case err := <-reqDone:
		if err == nil {
			t.Fatal("in-flight relay completed instead of being cut")
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight relay still open after disable")
	}
	if listenerUp(a) {
		t.Fatal("listener still up after disable")
	}
	a.peerMu.Lock()
	code, states := a.peerInviteCode, len(a.peerStates)
	a.peerMu.Unlock()
	if code != "" || states != 0 {
		t.Fatalf("invite %q / %d peer states left after disable", code, states)
	}
	if containsHost(a.tlsHostnames(), "bshare.vibe") {
		t.Fatal("peer route still in SANs after disable")
	}
	after, _ := os.ReadFile(filepath.Join(a.configDir(), "peers.json"))
	if string(before) != string(after) || len(after) == 0 {
		t.Fatal("disable changed or removed peers.json")
	}
	if cfg := configSaysEnabled(t, a); !strings.Contains(cfg, `"enabled": false`) {
		t.Fatalf("config.json not updated: %s", cfg)
	}
}

func containsHost(hosts []string, h string) bool {
	for _, x := range hosts {
		if x == h {
			return true
		}
	}
	return false
}

// Toggling must never leave two sync loops polling the same peers.
func TestEnableTwiceOneSyncLoop(t *testing.T) {
	s := newDisabledServer(t)
	if err := peer.SavePeers(s.configDir(), []peer.Peer{{Name: "box", Host: "127.0.0.1", Port: 1, Fingerprint: "ff"}}); err != nil {
		t.Fatal(err)
	}
	for _, on := range []bool{true, false, true, true} {
		if err := s.setPeersEnabled(on); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if n := s.peerSyncLoops.Load(); n != 1 {
		t.Fatalf("%d sync loops running; want 1", n)
	}
}

// A machine is findable only while it shows a code: when an unpaired
// machine's code expires, the listener goes away. With a pairing it stays.
func TestListenerStopsWhenInviteExpiresUnpaired(t *testing.T) {
	prev := peerInviteTTL
	peerInviteTTL = 50 * time.Millisecond
	t.Cleanup(func() { peerInviteTTL = prev })

	s := newBareServer(t)
	if _, _, err := s.openPeerInvite(); err != nil {
		t.Fatal(err)
	}
	if !listenerUp(s) {
		t.Fatal("invite did not start the listener")
	}
	waitFor(t, "listener to stop after expiry", func() bool { return !listenerUp(s) })

	paired := newBareServer(t)
	paired.peerList = []peer.Peer{{Name: "box", Host: "127.0.0.1", Port: 1, Fingerprint: "ff"}}
	if _, _, err := paired.openPeerInvite(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if !listenerUp(paired) {
		t.Fatal("listener stopped on expiry although a peer is paired")
	}
}

// Pairing for the first time at runtime must start the periodic sync — it
// used to start only at daemon startup, so a first pairing made from the UI
// never polled until the next restart.
func TestFirstPairingStartsSync(t *testing.T) {
	b := newBareServer(t)
	a := newBareServer(t)
	pairAB(t, a, b)
	waitFor(t, "a's sync loop", func() bool { return a.peerSyncLoops.Load() == 1 })
	waitFor(t, "b's sync loop", func() bool { return b.peerSyncLoops.Load() == 1 })
	t.Cleanup(func() { a.stopPeerSync(); b.stopPeerSync() })
}

// Removing a peer drops its route names from the TLS leaf's SANs.
func TestRemovePeerDropsSANs(t *testing.T) {
	b := newBareServer(t)
	a := newBareServer(t)
	pairAB(t, a, b)
	b.table.Add(&Route{Name: "bshare", Type: RouteSticky, Port: 1, RegisteredAt: time.Now()})
	a.refreshAllPeers(true)
	if !containsHost(a.tlsHostnames(), "bshare.vibe") {
		t.Fatalf("precondition: bshare.vibe not in SANs: %v", a.tlsHostnames())
	}
	rec := httptest.NewRecorder()
	a.handlePeerRemove(rec, nil, a.peerList[0].Name)
	if rec.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}
	if containsHost(a.tlsHostnames(), "bshare.vibe") {
		t.Fatal("removed peer's route still in SANs")
	}
}

// closePeerLn stops a test server's peer listener under its lock. The
// listener can legitimately be gone already (it stops when an unpaired
// machine's code closes), so this is nil-safe.
func closePeerLn(s *Server) {
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	s.closePeerListenerLocked()
}

func postInviteClose(t *testing.T, s *Server, code string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/_api/peers/invite/close", strings.NewReader(`{"code":"`+code+`"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)
	return w.Code
}

// Review: closing Add peer withdraws the code it opened, so an unpaired
// machine stops being findable then — not five minutes later. A different
// (e.g. CLI-opened) code is left alone.
func TestCloseInviteWithdrawsCodeAndStopsListener(t *testing.T) {
	s := newBareServer(t)
	code, _, err := s.openPeerInvite()
	if err != nil {
		t.Fatal(err)
	}
	if c := postInviteClose(t, s, "ZZZZ-9999"); c != http.StatusOK {
		t.Fatalf("close with another code = %d", c)
	}
	if !listenerUp(s) {
		t.Fatal("closing someone else's code stopped the listener")
	}
	if c := postInviteClose(t, s, code); c != http.StatusOK {
		t.Fatalf("close = %d", c)
	}
	s.peerMu.Lock()
	open := s.peerInviteCode
	s.peerMu.Unlock()
	if open != "" || listenerUp(s) {
		t.Fatalf("after close: code %q, listener up %v", open, listenerUp(s))
	}
}

// Review: between consuming the code and saving the pin, a pairing in flight
// keeps the listener up — an expiry or failed-proof timer firing then must
// not cut the connection and leave the pairing one-sided.
func TestPairInFlightKeepsListener(t *testing.T) {
	s := newBareServer(t)
	if _, _, err := s.openPeerInvite(); err != nil {
		t.Fatal(err)
	}
	s.peerMu.Lock()
	s.peerInviteCode = "" // consumed by the pairing
	s.peerPairsInFlight++
	s.peerMu.Unlock()
	s.maybeStopPeerListener()
	if !listenerUp(s) {
		t.Fatal("listener stopped under an in-flight pairing")
	}
	s.endPeerPairing()
	if listenerUp(s) {
		t.Fatal("listener still up after the pairing ended with nothing paired")
	}
}

// Review: a refresh or relay that passed its peersEnabled check just before
// Turn off must not put peer state or a pooled transport back afterwards.
func TestNoPeerStateRecreatedAfterDisable(t *testing.T) {
	b := newBareServer(t)
	a := newBareServer(t)
	pairAB(t, a, b)
	p := a.peerList[0]
	if err := a.setPeersEnabled(false); err != nil {
		t.Fatal(err)
	}
	// What the in-flight refresh does next, after its check passed.
	_ = a.peerStateFor(p.Name)
	if _, err := a.peerTransport(p.Fingerprint); err == nil {
		t.Error("peerTransport built a transport while peers are off")
	}
	a.peerMu.Lock()
	states, transports := len(a.peerStates), len(a.peerTransports)
	a.peerMu.Unlock()
	if states != 0 || transports != 0 {
		t.Fatalf("after disable: %d peer states, %d transports; want 0, 0", states, transports)
	}
}

// Review: the peers switch and the list/grid preference both mutate and save
// the shared config; concurrently they must not race (run with -race) and
// the file must end up with both changes.
func TestConfigWritersDoNotRace(t *testing.T) {
	s := newDisabledServer(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			_ = s.setPeersEnabled(i%2 == 0)
		}
	}()
	for i := 0; i < 20; i++ {
		req := httptest.NewRequest(http.MethodPut, "/_api/preferences", strings.NewReader(`{"view":"grid"}`))
		req.Header.Set("Content-Type", "application/json")
		s.apiHandler(httptest.NewRecorder(), req)
	}
	<-done
	_ = s.setPeersEnabled(false)
	if cfg := configSaysEnabled(t, s); !strings.Contains(cfg, `"view": "grid"`) {
		t.Fatalf("config.json lost the view preference: %s", cfg)
	}
}
