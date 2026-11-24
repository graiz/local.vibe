package daemon

import (
	"testing"

	"github.com/graiz/local.vibe/internal/config"
)

// testServer constructs a Server suitable for in-process tests: zero TCP
// port (no real bind) and a per-test temp config dir, so tests never share a
// routes.json with each other or clobber a real ~/.vibe. Kept in its own file
// (not api_test.go) so it remains visible on Windows builds where the
// unix-only api_test.go is excluded by build tag.
func testServer(t testing.TB) *Server {
	t.Helper()
	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			Port: 0,
			TLD:  "test",
		},
	}
	s := NewServer(cfg)
	s.ConfigDir = t.TempDir()
	return s
}
