//go:build darwin

package cmd

import (
	"fmt"
	"os"
	"os/exec"
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
	c := exec.Command("sudo", self, "pf-apply")
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}
