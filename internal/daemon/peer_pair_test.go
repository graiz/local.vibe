package daemon

import (
	"strconv"
	"strings"
	"testing"

	"github.com/graiz/local.vibe/internal/config"
	"github.com/graiz/local.vibe/internal/peer"
)

func newBareServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Daemon.Port = 0
	cfg.Daemon.TLS.Enabled = false
	cfg.Daemon.Peers.Enabled = true
	cfg.Daemon.Peers.Port = 0
	cfg.Daemon.TLS.CertsDir = t.TempDir()
	s := NewServer(cfg)
	s.ConfigDir = t.TempDir()
	return s
}

func splitAddr(t *testing.T, addr string) (string, int) {
	t.Helper()
	i := strings.LastIndex(addr, ":")
	if i == -1 {
		t.Fatalf("bad addr %q", addr)
	}
	port, err := strconv.Atoi(addr[i+1:])
	if err != nil {
		t.Fatalf("bad addr %q: %v", addr, err)
	}
	host := strings.Trim(addr[:i], "[]")
	if host == "::" || host == "" {
		host = "127.0.0.1"
	}
	return host, port
}

func TestPairingHappyPathIsMutual(t *testing.T) {
	b := newBareServer(t)
	code, _, err := b.openPeerInvite()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.peerLn.Close() })
	host, port := splitAddr(t, b.peerLn.Addr().String())

	a := newBareServer(t)
	if err := a.ensurePeerIdentity(); err != nil {
		t.Fatal(err)
	}
	got, err := a.pairWithPeer(host, port, code)
	if err != nil {
		t.Fatalf("pairWithPeer: %v", err)
	}
	if got.Fingerprint != b.peerFP {
		t.Fatalf("A pinned %q, want B's fp %q", got.Fingerprint, b.peerFP)
	}
	// Mutual: B stored A too, pinned to A's real fingerprint.
	if p := b.peerByFingerprint(a.peerFP); p == nil {
		t.Fatal("B did not store A after pairing")
	}
	// Invite is one-time.
	if _, err := a.pairWithPeer(host, port, code); err == nil {
		t.Fatal("second pairing with a used invite succeeded")
	}
	// Both sides persisted.
	if ps, _ := peer.LoadPeers(a.configDir()); len(ps) != 1 {
		t.Fatalf("A peers.json: %+v", ps)
	}
	if ps, _ := peer.LoadPeers(b.configDir()); len(ps) != 1 {
		t.Fatalf("B peers.json: %+v", ps)
	}
	// A's listener came up as a side effect (pairing is mutual — B will dial back).
	if a.peerLn == nil {
		t.Fatal("A's peer listener not started after pairing")
	}
	a.peerLn.Close()
}

func TestPairingWrongCodeRejected(t *testing.T) {
	b := newBareServer(t)
	if _, _, err := b.openPeerInvite(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.peerLn.Close() })
	host, port := splitAddr(t, b.peerLn.Addr().String())

	a := newBareServer(t)
	if err := a.ensurePeerIdentity(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.pairWithPeer(host, port, "000000"); err == nil {
		t.Fatal("wrong invite code accepted")
	}
	if len(b.peerList) != 0 {
		t.Fatal("B stored a peer despite a bad proof")
	}
}

func TestPairingRequiresOpenInvite(t *testing.T) {
	b := newBareServer(t)
	if err := b.ensurePeerIdentity(); err != nil {
		t.Fatal(err)
	}
	if err := b.ensurePeerListener(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.peerLn.Close() })
	host, port := splitAddr(t, b.peerLn.Addr().String())
	a := newBareServer(t)
	if err := a.ensurePeerIdentity(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.pairWithPeer(host, port, "123456"); err == nil {
		t.Fatal("pairing succeeded with no invite open")
	}
}
