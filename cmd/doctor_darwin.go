//go:build darwin

package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/graiz/local.vibe/internal/config"
)

// redirectMechanismName is the macOS privileged-port layer: pf.
func redirectMechanismName() string { return "pf" }

// platformRepairRedirect re-applies vibe's pf redirect by running `vibe pf-apply`
// as root (it merges the redirect back into the live ruleset without clobbering
// other pf users). pfctl needs root, so this shells out via sudo and will prompt
// for a password. Reuses the exact same code path as the network-change
// LaunchDaemon — single source of truth.
func platformRepairRedirect() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not locate vibe binary: %w", err)
	}
	// Pass the configured ports explicitly: pf-apply runs as root under sudo,
	// where it would otherwise read root's config and fall back to the
	// defaults — re-installing a redirect aimed at a dead port on any
	// non-default setup, which is exactly the breakage --fix is meant to cure.
	// doctor itself runs as the user, so cfg here is the right one.
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("could not read config: %w", err)
	}
	c := exec.Command("sudo", self, "pf-apply",
		"--http-port", fmt.Sprint(cfg.Daemon.Port),
		"--tls-port", fmt.Sprint(cfg.Daemon.TLS.Port))
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}
