package daemon

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/graiz/local.vibe/internal/peer"
)

// A responder on an older build (or a wrong code) answers 403. The initiator
// must say what to check — including the version, since v1 and v2 proofs
// can't pair — rather than a bare rejection.
func TestPairWithOldResponderSaysUpdate(t *testing.T) {
	a := newBareServer(t)
	if err := a.ensurePeerIdentity(); err != nil {
		t.Fatal(err)
	}
	// A TLS server that accepts any client cert and 403s every pairing.
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "pairing failed", http.StatusForbidden)
	}))
	srv.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	host, port := splitAddr(t, srv.Listener.Addr().String())

	code, _ := peer.NewInviteCode()
	_, err := a.pairWithPeer(host, port, code)
	if err == nil || !strings.Contains(err.Error(), "same vibe version") {
		t.Fatalf("error = %v; want it to mention running the same vibe version", err)
	}
}

// Codes are normalized server-side; anything that isn't a v2 code is a 400
// that shows the expected shape — not a doomed network round-trip.
func TestPeerAddRejectsMalformedCode(t *testing.T) {
	s := newBareServer(t)
	for _, code := range []string{"123456", "ABCD-EFGH", "K7QM"} {
		req := httptest.NewRequest(http.MethodPost, "/_api/peers", strings.NewReader(`{"host":"127.0.0.1","port":1,"code":"`+code+`"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.apiHandler(w, req)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "K7QM-3XR9") {
			t.Errorf("code %q: %d %s; want 400 naming the K7QM-3XR9 shape", code, w.Code, w.Body.String())
		}
	}
}
