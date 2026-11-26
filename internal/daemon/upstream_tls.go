package daemon

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/graiz/local.vibe/internal/netprobe"
)

// Some local apps serve HTTPS, not HTTP, on their port — typically with a
// self-signed cert so a phone reaching them directly over Tailscale gets a
// secure context (getUserMedia refuses plain http). Next's
// --experimental-https and Vite's basicSsl plugin do the same. vibe used to
// speak plain HTTP to every local route, and plain HTTP sent to a TLS
// listener doesn't fail: the server waits for a ClientHello that never comes,
// so the request hangs and the route reads as "isn't running" while the app is
// perfectly healthy. There is no error to react to, so detect the scheme
// up front with a TLS handshake and remember it.

// upstreamTLSProbeTimeout bounds the one-off handshake. A TLS server answers
// a ClientHello at once and nearly every plain-HTTP server rejects the bytes
// with an immediate 400; the timeout only bites for a plain server that sits
// waiting for a newline (Python's http.server), which then pays it once.
const upstreamTLSProbeTimeout = 500 * time.Millisecond

// probeUpstreamTLS reports whether the listener on localhost:port completes a
// TLS handshake. Certificates aren't verified: this asks which protocol the
// port speaks, not whom to trust.
func probeUpstreamTLS(port int) bool {
	conn, err := netprobe.DialLoopback(port, 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.SetDeadline(time.Now().Add(upstreamTLSProbeTimeout))
	tc := tls.Client(conn, &tls.Config{InsecureSkipVerify: true, ServerName: "localhost"}) //nolint:gosec // protocol probe only
	err = tc.Handshake()
	tc.Close()
	return err == nil
}

// upstreamTLSKey identifies what a cached probe result was measured against:
// the port and the process holding it. A new port or a restarted managed
// child gets re-probed. Never zero, so zero can mean "not probed".
func upstreamTLSKey(route *Route) int64 {
	pid, _ := route.PIDValue()
	return int64(route.Port)<<32 | int64(uint32(pid)) | 1<<62
}

// routeUsesTLS reports whether the route's local listener speaks TLS,
// probing on first use and whenever the port or process changed. Concurrent
// first requests may each probe; they agree, and it happens once per change.
func routeUsesTLS(route *Route) bool {
	key := upstreamTLSKey(route)
	if route.upstreamTLSKey.Load() == key {
		return route.upstreamTLS.Load()
	}
	isTLS := probeUpstreamTLS(route.Port)
	route.upstreamTLS.Store(isTLS)
	route.upstreamTLSKey.Store(key)
	return isTLS
}

// forgetUpstreamScheme drops the cached probe so the next request re-probes —
// called when a proxy round-trip fails, since a sticky route's app can be
// restarted on the same port with a different scheme and vibe sees no PID.
func forgetUpstreamScheme(route *Route) { route.upstreamTLSKey.Store(0) }

// localTLSTransport talks to self-signed local listeners. Verification is off
// because these certs are the app's own, minted for direct (e.g. Tailscale)
// access; the browser-facing TLS is vibe's, and this hop never leaves the
// machine — it only ever dials localhost.
var localTLSTransport = buildUpstreamTransport(true)

// localUpstream returns the base URL and transport for reaching a local
// (managed or sticky) route's app. The transport is nil for plain HTTP, which
// leaves http.DefaultTransport in effect.
func localUpstream(route *Route) (*url.URL, http.RoundTripper) {
	if routeUsesTLS(route) {
		u, _ := url.Parse(fmt.Sprintf("https://localhost:%d", route.Port))
		return u, localTLSTransport
	}
	u, _ := url.Parse(fmt.Sprintf("http://localhost:%d", route.Port))
	return u, nil
}
