// Package netprobe holds tiny, dependency-free network checks shared across the
// CLI and daemon — chiefly probing the privileged-port redirect that a VPN's
// firewall reload can silently flush.
package netprobe

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"time"
)

// PortOpen reports whether a TCP connection to 127.0.0.1:port succeeds within
// timeout. Dialing the privileged ports (:80/:443) succeeds only when the
// redirect is forwarding them to the daemon, so this detects a flushed redirect
// without needing root.
func PortOpen(port int, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// DaemonAnswers reports whether the vibe daemon answers its health endpoint on
// 127.0.0.1:port over the given scheme ("http" or "https").
//
// This is strictly stronger than PortOpen, and the difference is not academic:
// a VPN kill-switch can intercept a direct loopback connection, completing the
// TCP handshake and then never forwarding anything. A dial-based probe calls
// that healthy (and flaps between succeeding and failing run to run), while an
// actual request hangs every time — which is what the user experiences.
// Requiring a 200 from the daemon's own endpoint also means an unrelated
// server occupying the port isn't mistaken for vibe.
//
// TLS verification is skipped: the leaf cert carries .vibe SANs, not
// 127.0.0.1, and this is a liveness probe rather than a trust decision.
func DaemonAnswers(scheme string, port int, timeout time.Duration) bool {
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:             nil, // never route a loopback probe through a proxy
			DisableKeepAlives: true,
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := client.Get(fmt.Sprintf("%s://127.0.0.1:%d/_api/health", scheme, port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
