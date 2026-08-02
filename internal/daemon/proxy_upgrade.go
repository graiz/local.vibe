package daemon

import (
	"net/http"
	"strings"
)

// isWebSocketUpgrade reports whether a request is a protocol-upgrade handshake
// to websocket.
//
// Connection is a comma-separated list of tokens ("keep-alive, Upgrade" is
// what several browsers and proxies send), and both header values are
// case-insensitive per RFC 7230, so neither can be compared with a plain ==.
func isWebSocketUpgrade(r *http.Request) bool {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	for _, tok := range strings.Split(r.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
			return true
		}
	}
	return false
}
