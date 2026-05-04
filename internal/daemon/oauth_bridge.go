package daemon

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

func (s *Server) validateOAuthBridgeConfig(routeName string, appPort, callbackPort int) error {
	if callbackPort < 1 || callbackPort > 65535 {
		return fmt.Errorf("oauth_callback_port must be between 1 and 65535")
	}
	if appPort > 0 && callbackPort == appPort {
		return fmt.Errorf("oauth_callback_port (%d) must differ from route port (%d)", callbackPort, appPort)
	}
	if callbackPort == s.cfg.Daemon.Port || (s.cfg.Daemon.TLS.Enabled && callbackPort == s.cfg.Daemon.TLS.Port) {
		return fmt.Errorf("oauth_callback_port %d conflicts with vibe daemon listener", callbackPort)
	}
	for _, r := range s.table.List() {
		if r.Name != routeName && r.OAuthCallbackPort == callbackPort {
			return fmt.Errorf("oauth_callback_port %d is already used by route %q", callbackPort, r.Name)
		}
	}

	// Bindability is enforced by reconcileOAuthBridgeListeners (which holds
	// the actual listener); a probe-and-close here would race against any
	// concurrent listener acquiring the port between the probe and reconcile.
	return nil
}

func (s *Server) reconcileOAuthBridgeListeners() error {
	wanted := make(map[int]struct{})
	for _, r := range s.table.List() {
		if r.OAuthCallbackPort > 0 {
			if err := s.validateOAuthBridgeConfig(r.Name, r.Port, r.OAuthCallbackPort); err != nil {
				return err
			}
			wanted[r.OAuthCallbackPort] = struct{}{}
		}
	}

	s.oauthBridgeMu.Lock()
	defer s.oauthBridgeMu.Unlock()

	for port, srv := range s.oauthBridgeServers {
		if _, ok := wanted[port]; ok {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = srv.Shutdown(ctx)
		cancel()
		if ln, ok := s.oauthBridgeListeners[port]; ok {
			_ = ln.Close()
		}
		delete(s.oauthBridgeServers, port)
		delete(s.oauthBridgeListeners, port)
	}

	for port := range wanted {
		if _, ok := s.oauthBridgeServers[port]; ok {
			continue
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return fmt.Errorf("oauth_callback_port %d is not available: %w", port, err)
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.handleOAuthCallbackBridge(w, r, port)
		})}
		s.oauthBridgeServers[port] = srv
		s.oauthBridgeListeners[port] = ln
		go func(p int, server *http.Server, listener net.Listener) {
			if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
				fmt.Fprintf(os.Stderr, "warning: oauth callback bridge listener %d stopped: %v\n", p, err)
			}
		}(port, srv, ln)
	}

	return nil
}

func (s *Server) stopOAuthBridgeListeners() {
	s.oauthBridgeMu.Lock()
	defer s.oauthBridgeMu.Unlock()
	for port, srv := range s.oauthBridgeServers {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = srv.Shutdown(ctx)
		cancel()
		if ln, ok := s.oauthBridgeListeners[port]; ok {
			_ = ln.Close()
		}
		delete(s.oauthBridgeServers, port)
		delete(s.oauthBridgeListeners, port)
	}
}

func (s *Server) handleOAuthCallbackBridge(w http.ResponseWriter, r *http.Request, callbackPort int) {
	var route *Route
	for _, rt := range s.table.List() {
		if rt.OAuthCallbackPort == callbackPort {
			route = rt
			break
		}
	}
	if route == nil {
		http.NotFound(w, r)
		return
	}

	target := fmt.Sprintf("%s://%s.%s%s", s.vibeScheme(), route.Name, s.cfg.Daemon.TLD, r.URL.RequestURI())
	http.Redirect(w, r, target, http.StatusTemporaryRedirect)
}
