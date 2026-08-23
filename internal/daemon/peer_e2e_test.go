package daemon

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestPeerEndToEnd walks the whole feature in one process: pair → sync →
// browse A's routeRequest through B's peer listener to B's app → verify the
// upgrade-only Origin rewrite ran on B's hop → B goes away → A serves the
// unreachable page → peer removed → the name falls back to the dashboard.
func TestPeerEndToEnd(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "origin=%s", r.Header.Get("Origin"))
	}))
	t.Cleanup(backend.Close)
	backendPort := portOf(t, backend.URL)

	b := newBareServer(t)
	a := newBareServer(t)
	pairAB(t, a, b)
	route := &Route{Name: "face", Type: RouteSticky, Port: backendPort, RegisteredAt: time.Now()}
	route.Running.Store(true)
	route.Ready.Store(true)
	b.table.Add(route)

	browse := func(mutate func(*http.Request)) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "http://face.vibe/", nil)
		req.Host = "face.vibe"
		if mutate != nil {
			mutate(req)
		}
		rec := httptest.NewRecorder()
		a.routeRequest(rec, req)
		return rec
	}

	// Happy path through both hops.
	if rec := browse(nil); rec.Code != http.StatusOK || rec.Body.String() != "origin=" {
		t.Fatalf("relay: code=%d body=%q", rec.Code, rec.Body.String())
	}

	// A plain request's Origin passes through untouched (apps check it for CSRF).
	rec := browse(func(r *http.Request) { r.Header.Set("Origin", "https://face.vibe") })
	if rec.Body.String() != "origin=https://face.vibe" {
		t.Fatalf("plain-request Origin was rewritten: %q", rec.Body.String())
	}

	// An upgrade request's Origin is rewritten to the upstream's own origin
	// on B's hop — the HMR-socket gate defusal.
	rec = browse(func(r *http.Request) {
		r.Header.Set("Origin", "https://face.vibe")
		r.Header.Set("Connection", "Upgrade")
		r.Header.Set("Upgrade", "websocket")
	})
	want := fmt.Sprintf("origin=http://localhost:%d", backendPort)
	if rec.Body.String() != want {
		t.Fatalf("upgrade Origin rewrite: got %q, want %q", rec.Body.String(), want)
	}

	// B goes dark: no bare 502, the page names the peer and the fix.
	b.peerLn.Close()
	rec = browse(nil)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "vibe peers") {
		t.Fatalf("unreachable peer: code=%d body has hint=%v", rec.Code, strings.Contains(rec.Body.String(), "vibe peers"))
	}

	// Remove the peer: the name no longer resolves anywhere and falls back
	// to the dashboard.
	var peerName string
	a.peerMu.Lock()
	peerName = a.peerList[0].Name
	a.peerMu.Unlock()
	delRec := httptest.NewRecorder()
	a.handlePeerRemove(delRec, httptest.NewRequest("DELETE", "/_api/peers/"+peerName, nil), peerName)
	if delRec.Code != http.StatusOK {
		t.Fatalf("peer remove: %d %s", delRec.Code, delRec.Body.String())
	}
	rec = browse(nil)
	if rec.Code == http.StatusBadGateway {
		t.Fatal("removed peer still resolving")
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "<html") && !strings.Contains(strings.ToLower(rec.Body.String()), "<!doctype") {
		t.Fatalf("expected dashboard fallback after removal, got %q", rec.Body.String()[:120])
	}
}
