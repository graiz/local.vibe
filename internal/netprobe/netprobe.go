// Package netprobe holds tiny, dependency-free network checks shared across the
// CLI and daemon — chiefly probing the privileged-port redirect that a VPN's
// firewall reload can silently flush.
package netprobe

import (
	"fmt"
	"net"
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
