package daemon

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/graiz/local.vibe/internal/peer"
)

func putPeer(t *testing.T, s *Server, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/_api/peers/"+name, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)
	return w
}

// A name the user types sticks: rename pickup from the machine no longer
// overwrites it.
func TestPeerEditRenameSticks(t *testing.T) {
	s := newBareServer(t)
	p := peer.Peer{Name: "old-box", Host: "127.0.0.1", Port: 1, Fingerprint: "fp1"}
	s.peerList = []peer.Peer{p}
	s.peerStates["old-box"] = &peerState{}
	if w := putPeer(t, s, "old-box", `{"name":"My-Box"}`); w.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", w.Code, w.Body.String())
	}
	if s.peerList[0].Name != "my-box" || !s.peerList[0].NameSetByUser {
		t.Fatalf("after rename: %+v", s.peerList[0])
	}
	if _, old := s.peerStates["old-box"]; old {
		t.Fatal("sync state still keyed by the old name")
	}
	if _, ok := s.peerStates["my-box"]; !ok {
		t.Fatal("sync state not re-keyed to the new name")
	}
	saved, _ := os.ReadFile(filepath.Join(s.configDir(), "peers.json"))
	if !strings.Contains(string(saved), `"name_set_by_user": true`) {
		t.Fatalf("peers.json: %s", saved)
	}
	s.adoptPeerName(s.peerList[0], "machine-name")
	if s.peerList[0].Name != "my-box" {
		t.Fatalf("automatic rename overwrote the user's name: %q", s.peerList[0].Name)
	}
}

func TestPeerEditNameValidation(t *testing.T) {
	s := newBareServer(t)
	s.peerList = []peer.Peer{
		{Name: "one", Host: "127.0.0.1", Port: 1, Fingerprint: "fp1"},
		{Name: "two", Host: "127.0.0.1", Port: 1, Fingerprint: "fp2"},
	}
	if w := putPeer(t, s, "one", `{"name":"two"}`); w.Code != http.StatusConflict {
		t.Errorf("rename onto another peer's name = %d; want 409", w.Code)
	}
	for _, bad := range []string{`{"name":"!!!"}`, `{"name":"a b"}`, `{"port":70000}`, `{"host":"  "}`} {
		if w := putPeer(t, s, "one", bad); w.Code != http.StatusBadRequest {
			t.Errorf("%s = %d; want 400", bad, w.Code)
		}
	}
	if s.peerList[0].Name != "one" || s.peerList[0].Port != 1 {
		t.Fatalf("rejected edit changed the peer: %+v", s.peerList[0])
	}
}

func TestPeerEditUnknown(t *testing.T) {
	s := newBareServer(t)
	if w := putPeer(t, s, "ghost", `{"host":"10.0.0.1"}`); w.Code != http.StatusNotFound {
		t.Fatalf("edit unknown peer = %d; want 404", w.Code)
	}
}

// Changing the host never changes trust: the pin is re-verified, so pointing
// a peer at a different machine just fails — it can't connect to it.
func TestPeerEditHostKeepsPin(t *testing.T) {
	b := newBareServer(t)
	a := newBareServer(t)
	pairAB(t, a, b)
	other := newBareServer(t)
	if err := other.ensurePeerIdentity(); err != nil {
		t.Fatal(err)
	}
	if err := other.ensurePeerListener(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePeerLn(other) })
	host, port := splitAddr(t, other.peerLn.Addr().String())
	name, fp := a.peerList[0].Name, a.peerList[0].Fingerprint

	if w := putPeer(t, a, name, fmt.Sprintf(`{"host":%q,"port":%d}`, host, port)); w.Code != http.StatusOK {
		t.Fatalf("edit host: %d %s", w.Code, w.Body.String())
	}
	// The edit already kicked off a forced refresh; a second one may find it
	// in flight (single-flighted) and return at once, so wait for the result.
	var lastErr string
	waitFor(t, "a sync against the new host", func() bool {
		a.refreshPeerRoutes(a.peerList[0], true)
		a.peerMu.Lock()
		defer a.peerMu.Unlock()
		if st, ok := a.peerStates[name]; ok {
			lastErr = st.lastErr
		}
		return lastErr != ""
	})
	a.peerMu.Lock()
	got := a.peerList[0]
	a.peerMu.Unlock()
	if got.Fingerprint != fp || got.Port != port {
		t.Fatalf("edit changed the pin or didn't apply: %+v", got)
	}
	if !strings.Contains(lastErr, "fingerprint") {
		t.Fatalf("sync against a different machine: lastErr %q; want a fingerprint failure", lastErr)
	}
}
