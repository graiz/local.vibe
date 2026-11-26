package daemon

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestProbeUpstreamTLS(t *testing.T) {
	tlsApp := httptest.NewTLSServer(http.NotFoundHandler())
	defer tlsApp.Close()
	plainApp := httptest.NewServer(http.NotFoundHandler())
	defer plainApp.Close()

	if !probeUpstreamTLS(portOf(t, tlsApp.URL)) {
		t.Error("self-signed TLS listener not detected")
	}
	if probeUpstreamTLS(portOf(t, plainApp.URL)) {
		t.Error("plain HTTP listener detected as TLS")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	if probeUpstreamTLS(closed) {
		t.Error("closed port detected as TLS")
	}
}

// An app serving HTTPS with its own self-signed cert (for direct Tailscale
// access) must work behind vibe for every local route type, with no flag —
// plain HTTP sent to it hangs rather than failing, so this used to read as
// "isn't running".
func TestRouteRequestProxiesToTLSUpstream(t *testing.T) {
	app := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "tls-app %s", r.URL.Path)
	}))
	defer app.Close()
	port := portOf(t, app.URL)

	for _, typ := range []RouteType{RouteSticky, RouteManaged} {
		t.Run(string(typ), func(t *testing.T) {
			s := testServer(t)
			route := &Route{Name: "cos", Type: typ, Port: port, Cmd: "true", RegisteredAt: time.Now()}
			if typ == RouteManaged {
				route.SetPID(os.Getpid()) // live child, so it proxies rather than recovers
			}
			route.Running.Store(true)
			// A prior failed round-trip left it not-ready; answering must
			// restore it, or `vibe ls` and the peer listing say "starting".
			route.Ready.Store(false)
			s.table.Add(route)

			req := httptest.NewRequest(http.MethodGet, "/hello", nil)
			req.Host = "cos.test"
			w := httptest.NewRecorder()
			s.routeRequest(w, req)

			if w.Code != http.StatusOK || w.Body.String() != "tls-app /hello" {
				t.Fatalf("got %d %q; want 200 \"tls-app /hello\"", w.Code, w.Body.String())
			}
			if !route.Ready.Load() {
				t.Error("Ready not restored after a successful proxy")
			}
		})
	}
}

// The peer hop is the one that talks to the app on the owning machine, so it
// needs the same scheme detection: A → B's peer listener → B's HTTPS app.
func TestPeerRelayToTLSUpstream(t *testing.T) {
	app := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "tls-app")
	}))
	defer app.Close()

	b := newBareServer(t)
	a := newBareServer(t)
	pairAB(t, a, b)
	route := &Route{Name: "cos", Type: RouteSticky, Port: portOf(t, app.URL), RegisteredAt: time.Now()}
	route.Running.Store(true)
	route.Ready.Store(true)
	b.table.Add(route)

	req := httptest.NewRequest(http.MethodGet, "http://cos.vibe/", nil)
	req.Host = "cos.vibe"
	rec := httptest.NewRecorder()
	a.routeRequest(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "tls-app" {
		t.Fatalf("relay: code=%d body=%q", rec.Code, rec.Body.String())
	}
}

// The cached scheme is tied to port+PID, and a failed round-trip forgets it:
// an app restarted on the same port with the other scheme is re-detected.
func TestRouteUsesTLSCache(t *testing.T) {
	tlsApp := httptest.NewTLSServer(http.NotFoundHandler())
	defer tlsApp.Close()
	plainApp := httptest.NewServer(http.NotFoundHandler())
	defer plainApp.Close()

	route := &Route{Name: "x", Type: RouteSticky, Port: portOf(t, tlsApp.URL)}
	if !routeUsesTLS(route) {
		t.Fatal("TLS app not detected")
	}
	route.Port = portOf(t, plainApp.URL)
	if routeUsesTLS(route) {
		t.Fatal("port change did not re-probe")
	}

	// Same port, scheme flips behind vibe's back (no PID to notice): the
	// cache holds until a proxy error forgets it.
	route.upstreamTLS.Store(true)
	if !routeUsesTLS(route) {
		t.Fatal("cache not consulted")
	}
	forgetUpstreamScheme(route)
	if routeUsesTLS(route) {
		t.Fatal("forgetUpstreamScheme did not force a re-probe")
	}
}

// The scheme probe costs up to 800ms and fires a ClientHello at the app, so it
// belongs in the readiness poll — off the request path — not on the first
// page load after every start.
func TestWaitForReadyWarmsUpstreamScheme(t *testing.T) {
	app := httptest.NewServer(http.NotFoundHandler())
	defer app.Close()
	s := testServer(t)
	s.ReadyTimeout = 5 * time.Second
	route := &Route{Name: "warm", Type: RouteManaged, Port: portOf(t, app.URL), Cmd: "true", RegisteredAt: time.Now()}
	route.SetPID(os.Getpid())
	route.Running.Store(true)
	s.table.Add(route)

	s.waitForReady(route, os.Getpid())

	if !route.Ready.Load() {
		t.Fatal("route never became ready")
	}
	if route.upstreamTLSKey.Load() == 0 {
		t.Error("readiness poll did not probe the upstream scheme")
	}
	if route.upstreamTLS.Load() {
		t.Error("plain HTTP app detected as TLS")
	}
}
