package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/graiz/local.vibe/internal/peer"
)

// Review #1: a pairing (or invite) that completes after peers were turned
// off must not bring the listener back up — off means nothing listens.
func TestListenerStaysDownWhenPeersOff(t *testing.T) {
	b := newBareServer(t)
	a := newBareServer(t)
	code, _, err := b.openPeerInvite()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePeerLn(b); closePeerLn(a) })
	host, port := splitAddr(t, b.peerLn.Addr().String())
	if err := a.ensurePeerIdentity(); err != nil {
		t.Fatal(err)
	}
	if err := a.setPeersEnabled(false); err != nil {
		t.Fatal(err)
	}
	_, _ = a.pairWithPeer(host, port, code) // turned off mid-pairing
	if listenerUp(a) {
		t.Fatal("pairing finished after Turn off and started the listener")
	}
	if _, _, err := a.openPeerInvite(); err == nil || listenerUp(a) {
		t.Fatalf("invite while off: err=%v listener=%v; want refused and nothing listening", err, listenerUp(a))
	}
}

// Review #2: the realistic mixed-version case is an OLD machine showing a
// 6-digit code and the new one entering it — say to update, not "that isn't
// a code".
func TestOldStyleCodeSaysUpdate(t *testing.T) {
	s := newBareServer(t)
	req := httptest.NewRequest(http.MethodPost, "/_api/peers", strings.NewReader(`{"host":"127.0.0.1","port":1,"code":"123456"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "older vibe") {
		t.Fatalf("6-digit code: %d %s; want 400 naming an older vibe", w.Code, w.Body.String())
	}
}

// Review #3: editing or removing a peer while peers are off is refused, and
// a sync never runs while off (no dial, no transport, no SANs).
func TestPeerEditsRefusedWhileOff(t *testing.T) {
	b := newBareServer(t)
	a := newBareServer(t)
	pairAB(t, a, b)
	b.table.Add(&Route{Name: "bshare", Type: RouteSticky, Port: 1})
	name := a.peerList[0].Name
	p := a.peerList[0]
	if err := a.setPeersEnabled(false); err != nil {
		t.Fatal(err)
	}
	if w := putPeer(t, a, name, `{"host":"10.0.0.1"}`); w.Code != http.StatusConflict {
		t.Errorf("edit while off = %d; want 409", w.Code)
	}
	rec := httptest.NewRecorder()
	a.handlePeerRemove(rec, nil, name)
	if rec.Code != http.StatusConflict {
		t.Errorf("remove while off = %d; want 409", rec.Code)
	}
	a.refreshPeerRoutes(p, true)
	a.peerMu.Lock()
	states, transports := len(a.peerStates), len(a.peerTransports)
	a.peerMu.Unlock()
	if states != 0 || transports != 0 || containsHost(a.tlsHostnames(), "bshare.vibe") {
		t.Fatalf("sync ran while off: %d states, %d transports, SANs %v", states, transports, a.tlsHostnames())
	}
}

// Review #8: the key shown in the picker is the key that gets pinned —
// pairing refuses if the machine answering now has a different one.
func TestPairRefusesUnexpectedKey(t *testing.T) {
	b := newBareServer(t)
	a := newBareServer(t)
	code, _, err := b.openPeerInvite()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePeerLn(b); closePeerLn(a) })
	host, port := splitAddr(t, b.peerLn.Addr().String())
	if err := a.ensurePeerIdentity(); err != nil {
		t.Fatal(err)
	}
	wrong := strings.Repeat("ab", 32)
	if _, err := a.pairWithPeerExpecting(host, port, code, wrong); err == nil || !strings.Contains(err.Error(), "different key") {
		t.Fatalf("pair with unexpected key: err=%v; want a refusal naming a different key", err)
	}
	if a.peerByFingerprint(b.peerFP) != nil {
		t.Fatal("pinned a machine whose key didn't match the one shown")
	}
	if _, err := a.pairWithPeerExpecting(host, port, code, b.peerFP); err != nil {
		t.Fatalf("pair with the shown key: %v", err)
	}
}

// Review #6: unreachable-on-LAN can be an offline machine, not only macOS
// privacy — the message must not claim one cause.
func TestBlockedMessageNamesBothCauses(t *testing.T) {
	r := peerProbeResult{Host: "192.168.1.50", Port: 7444}.with(probeBlocked)
	if !strings.Contains(r.Message, "offline") || !strings.Contains(r.Message, "Local Network") {
		t.Fatalf("blocked message = %q", r.Message)
	}
	_ = peer.ShortKey
}
