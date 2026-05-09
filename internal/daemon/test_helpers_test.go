package daemon

import (
	"os"

	"github.com/graiz/local.vibe/internal/config"
)

// testServer constructs a Server suitable for in-process tests: zero TCP
// port (no real bind), a temp config dir so we never clobber a real
// ~/.vibe/routes.json. Used across api_test.go, proxy_bookmark_test.go,
// oauth_fallback_test.go, and route_request_managed_test.go — kept in its
// own file (not api_test.go) so it remains visible on Windows builds where
// the unix-only api_test.go is excluded by build tag.
func testServer() *Server {
	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			Port: 0,
			TLD:  "test",
		},
	}
	s := NewServer(cfg)
	s.ConfigDir = os.TempDir()
	return s
}
