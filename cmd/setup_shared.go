package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/graiz/local.vibe/internal/cert"
	"github.com/graiz/local.vibe/internal/config"
)

// The OS-agnostic halves of setup. The darwin and windows files differ in
// how they find the user's home (sudo de-escalation vs. %USERPROFILE%) and
// in ownership fixups; the certificate work and the config.json edits are
// the same on both and live here so they can't drift.

// updateConfigJSON edits ~/.vibe/config.json in place as a generic map, so
// fields vibe doesn't know about (hand additions, newer keys) survive, and
// hands the "daemon" object to mutate. A missing file starts from empty.
func updateConfigJSON(cfgPath string, mutate func(daemon map[string]any)) error {
	var cfgMap map[string]any
	data, err := os.ReadFile(cfgPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &cfgMap); err != nil {
			return fmt.Errorf("parse config: %w", err)
		}
	}
	if cfgMap == nil {
		cfgMap = make(map[string]any)
	}
	daemon, _ := cfgMap["daemon"].(map[string]any)
	if daemon == nil {
		daemon = make(map[string]any)
	}
	mutate(daemon)
	cfgMap["daemon"] = daemon
	out, err := json.MarshalIndent(cfgMap, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(cfgPath, out, 0644)
}

// tlsConfigFor is the daemon.tls object setup enables, pointing at the certs
// dir under home.
func tlsConfigFor(home string, existing any) map[string]any {
	// Merge onto what's there rather than replacing it. The pf/portproxy
	// step forwards 443 to the *configured* TLS port (configuredDaemonPortsIn);
	// resetting it to the default here on a re-run would leave 443 forwarding
	// to a port the daemon no longer listens on — every https://*.vibe dead.
	tls := map[string]any{}
	if m, ok := existing.(map[string]any); ok {
		for k, v := range m {
			tls[k] = v
		}
	}
	tls["enabled"] = true
	if p, ok := tls["port"].(float64); !ok || p <= 0 {
		tls["port"] = config.DefaultTLSPort
	}
	if d, ok := tls["certs_dir"].(string); !ok || d == "" {
		tls["certs_dir"] = filepath.Join(home, ".vibe", "certs")
	}
	return tls
}

// generateCertsIn creates the local CA and an initial leaf for local.vibe
// under home/.vibe/certs. The daemon regenerates the leaf with real route
// names on startup and on every route change.
func generateCertsIn(home string) error {
	certsDir := filepath.Join(home, ".vibe", "certs")
	caCert, caKey, err := cert.EnsureCA(certsDir)
	if err != nil {
		return err
	}
	_, _, err = cert.EnsureLeaf(certsDir, caCert, caKey, []string{"local.vibe"})
	return err
}

// trustCAIn installs home/.vibe/certs/ca.pem into the OS trust store.
func trustCAIn(home string) error {
	return cert.TrustCA(filepath.Join(home, ".vibe", "certs"))
}

// configuredDaemonPortsIn reads the daemon's HTTP and TLS ports from the
// config under home, falling back to the defaults. Setup's port-forwarding
// rules (pf on macOS, netsh portproxy on Windows) must target whatever the
// daemon actually listens on; forwarding to the defaults when the user has
// moved the daemon leaves every *.vibe URL pointing at a dead port.
func configuredDaemonPortsIn(home string) (httpPort, tlsPort int) {
	httpPort, tlsPort = config.DefaultPort, config.DefaultTLSPort
	data, err := os.ReadFile(filepath.Join(home, ".vibe", "config.json"))
	if err != nil {
		return
	}
	var cfgMap map[string]any
	if json.Unmarshal(data, &cfgMap) != nil {
		return
	}
	daemon, _ := cfgMap["daemon"].(map[string]any)
	if daemon == nil {
		return
	}
	if p, ok := daemon["port"].(float64); ok && int(p) > 0 {
		httpPort = int(p)
	}
	if tls, ok := daemon["tls"].(map[string]any); ok {
		if p, ok := tls["port"].(float64); ok && int(p) > 0 {
			tlsPort = int(p)
		}
	}
	return
}

// daemonStartFailed is the error for a daemon that never answered after a
// start attempt, carrying the tail of its log when there is one.
func daemonStartFailed(logPath string) error {
	if tail := tailFile(logPath, 20); tail != "" {
		return fmt.Errorf("daemon did not start — last lines of %s:\n%s", logPath, tail)
	}
	return fmt.Errorf("daemon did not start — check %s", logPath)
}
