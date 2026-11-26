package daemon

// Browsing-side relay for the peer subsystem: requests for a name that only
// a paired machine serves are proxied from this daemon to that machine's
// peer listener. Browser TLS terminates locally with this machine's own
// cert; the cross-machine hop is mTLS pinned to the peer's fingerprint.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/graiz/local.vibe/internal/peer"
)

// proxyToPeer relays a browser request for name.<tld> to the paired machine
// that serves it. req.Host is deliberately left as the browser sent it —
// the peer listener on the other side routes by Host.
func (s *Server) proxyToPeer(w http.ResponseWriter, r *http.Request, p peer.Peer, name string) {
	transport, err := s.peerTransport(p.Fingerprint)
	if err != nil {
		s.servePeerUnreachablePage(w, p, name, err)
		return
	}
	target, _ := url.Parse("https://" + net.JoinHostPort(p.Host, fmt.Sprint(p.Port)))
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = transport
	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		// A client-aborted request is not a peer failure — same guard as the
		// local proxy path.
		if errors.Is(err, context.Canceled) || req.Context().Err() != nil {
			return
		}
		s.setPeerErr(s.peerStateFor(p.Name), err)
		s.servePeerUnreachablePage(rw, p, name, err)
	}
	proxy.ServeHTTP(w, r)
}

// servePeerUnreachablePage is the no-bare-502 stance applied to the relay:
// name which machine has the route, what failed, and what to check.
func (s *Server) servePeerUnreachablePage(w http.ResponseWriter, p peer.Peer, name string, err error) {
	data := messagePageData{
		Heading: fmt.Sprintf("%s lives on %s", name, p.Name),
		Lines: []string{
			fmt.Sprintf("%s is not answering (%s).", p.Name, err.Error()),
			"Check that the machine is awake and its vibe daemon is running, then run `vibe peers` on both machines.",
			fmt.Sprintf("If it was reinstalled, `vibe peer remove %s` and pair again.", p.Name),
		},
	}
	renderPage(w, http.StatusBadGateway, tmplMessagePage, s.routeHost(name), &data)
}
