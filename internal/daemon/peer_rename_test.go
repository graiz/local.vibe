package daemon

import (
	"testing"

	"github.com/graiz/local.vibe/internal/peer"
)

// A machine renamed after pairing used to stay listed under its old hostname
// forever (the name was fixed at pairing). The sync now adopts the name the
// peer reports — over the connection pinned to its fingerprint — unless
// another paired peer already has it. Host and pin never change.
func TestPeerRenameAdoptedOnSync(t *testing.T) {
	prev := peerHostname
	t.Cleanup(func() { peerHostname = prev })
	peerHostname = func() (string, error) { return "old-name.local", nil }

	b := newBareServer(t)
	a := newBareServer(t)
	pairAB(t, a, b)
	if got := a.peerList[0].Name; got != "old-name" {
		t.Fatalf("paired name = %q; want old-name", got)
	}
	before := a.peerList[0]

	// The machine is renamed; the next sync picks it up.
	peerHostname = func() (string, error) { return "Deathstar.local", nil }
	a.refreshAllPeers(true)

	a.peerMu.Lock()
	got := a.peerList[0]
	_, oldState := a.peerStates["old-name"]
	_, newState := a.peerStates["deathstar"]
	a.peerMu.Unlock()
	if got.Name != "deathstar" {
		t.Fatalf("name after rename = %q; want deathstar", got.Name)
	}
	if got.Host != before.Host || got.Port != before.Port || got.Fingerprint != before.Fingerprint {
		t.Fatalf("rename changed more than the name: before %+v after %+v", before, got)
	}
	if oldState || !newState {
		t.Fatalf("sync state not re-keyed: old=%v new=%v", oldState, newState)
	}
	saved, err := peer.LoadPeers(a.configDir())
	if err != nil || len(saved) != 1 || saved[0].Name != "deathstar" {
		t.Fatalf("rename not persisted to peers.json: %+v err=%v", saved, err)
	}
}

// A peer on an older build sends no name header; its stored name must stay
// as is. (SanitizeName("") is the placeholder "peer", which the first version
// of adoptPeerName adopted — renaming every not-yet-updated peer to "peer".)
func TestPeerRenameIgnoresMissingHeader(t *testing.T) {
	s := newBareServer(t)
	p := peer.Peer{Name: "old-box", Host: "127.0.0.1", Port: 1, Fingerprint: "fp-old"}
	s.peerList = []peer.Peer{p}
	s.peerStates["old-box"] = &peerState{}
	s.adoptPeerName(p, "")
	s.adoptPeerName(p, "   ")
	if s.peerList[0].Name != "old-box" {
		t.Fatalf("missing name header renamed the peer to %q", s.peerList[0].Name)
	}
}

// A new name already used by another paired peer is not adopted: names key
// `vibe peer remove` and the collision order.
func TestPeerRenameSkippedOnConflict(t *testing.T) {
	prev := peerHostname
	t.Cleanup(func() { peerHostname = prev })
	peerHostname = func() (string, error) { return "box-one", nil }

	b := newBareServer(t)
	a := newBareServer(t)
	pairAB(t, a, b)
	a.peerMu.Lock()
	a.peerList = append(a.peerList, peer.Peer{Name: "taken", Host: "127.0.0.1", Port: 1, Fingerprint: "other-fp"})
	a.peerMu.Unlock()

	peerHostname = func() (string, error) { return "taken", nil }
	a.refreshAllPeers(true)

	a.peerMu.Lock()
	defer a.peerMu.Unlock()
	if a.peerList[0].Name != "box-one" {
		t.Fatalf("peer renamed onto another peer's name: %q", a.peerList[0].Name)
	}
}
