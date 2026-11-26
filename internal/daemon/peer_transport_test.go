package daemon

import (
	"net/http/httptest"
	"testing"
)

// Every sync and every relayed request used to build a fresh http.Transport,
// so each opened its own TLS connection that was never reused or closed —
// a live daemon accumulated 100+ connections to a peer within minutes. One
// pool per pinned peer, reused, and dropped when the peer goes.
func TestPeerTransportReusedAndDropped(t *testing.T) {
	b := newBareServer(t)
	a := newBareServer(t)
	pairAB(t, a, b)
	fp := a.peerList[0].Fingerprint
	t1, err := a.peerTransport(fp)
	if err != nil {
		t.Fatal(err)
	}
	t2, _ := a.peerTransport(fp)
	if t1 != t2 {
		t.Fatal("a second request to the same peer got a new transport (new connections every time)")
	}
	a.refreshAllPeers(true)
	a.refreshAllPeers(true)
	if t3, _ := a.peerTransport(fp); t3 != t1 {
		t.Fatal("sync replaced the peer's transport")
	}

	rec := httptest.NewRecorder()
	a.handlePeerRemove(rec, nil, a.peerList[0].Name)
	a.peerMu.Lock()
	_, kept := a.peerTransports[fp]
	a.peerMu.Unlock()
	if kept {
		t.Fatal("removed peer's transport still cached")
	}
}
