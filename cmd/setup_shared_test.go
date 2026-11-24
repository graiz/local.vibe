package cmd

import (
	"path/filepath"
	"testing"

	"github.com/graiz/local.vibe/internal/config"
)

// Re-running setup must not reset a custom TLS port: the redirect step
// forwards 443 to the configured port, so a reset leaves 443 pointing at a
// port the daemon no longer listens on.
func TestTLSConfigForKeepsExistingPort(t *testing.T) {
	home := t.TempDir()
	got := tlsConfigFor(home, map[string]any{"enabled": false, "port": float64(8443), "certs_dir": "/custom"})
	if got["enabled"] != true {
		t.Errorf("enabled = %v; want true", got["enabled"])
	}
	if got["port"] != float64(8443) {
		t.Errorf("port = %v; want the existing 8443", got["port"])
	}
	if got["certs_dir"] != "/custom" {
		t.Errorf("certs_dir = %v; want the existing /custom", got["certs_dir"])
	}

	fresh := tlsConfigFor(home, nil)
	if fresh["port"] != config.DefaultTLSPort {
		t.Errorf("fresh port = %v; want default %d", fresh["port"], config.DefaultTLSPort)
	}
	if fresh["certs_dir"] != filepath.Join(home, ".vibe", "certs") {
		t.Errorf("fresh certs_dir = %v", fresh["certs_dir"])
	}
}
