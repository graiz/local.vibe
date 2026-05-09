//go:build darwin

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// uninstallPlatform reverses setup on macOS. Each step is best-effort: a
// missing artifact (e.g. `vibe setup` was never run) shouldn't fail the
// whole uninstall — we want the user to end up in a clean state regardless
// of how partial their previous install was.
func uninstallPlatform() error {
	if os.Getuid() != 0 {
		return fmt.Errorf("uninstall requires root — run: sudo vibe uninstall")
	}

	fmt.Println("Removing local.vibe from macOS...")
	fmt.Println()

	// LaunchAgent (user)
	if path := launchAgentPlist(); path != "" {
		_ = exec.Command("launchctl", "unload", path).Run()
		_ = os.Remove(path)
		fmt.Println("  LaunchAgent removed")
	}

	// LaunchDaemon (root, pf rules)
	_ = exec.Command("launchctl", "unload", launchDaemonPlist).Run()
	_ = os.Remove(launchDaemonPlist)
	fmt.Println("  pf LaunchDaemon removed")

	// /etc/resolver/vibe
	_ = os.Remove("/etc/resolver/vibe")
	fmt.Println("  /etc/resolver/vibe removed")

	// dnsmasq vibe entry — best-effort: leave dnsmasq.conf untouched if we
	// can't safely strip just our line. Most users don't mind one stale
	// `address=/.vibe/127.0.0.1` lying around in dnsmasq.conf.
	fmt.Println("  (dnsmasq.conf left intact — remove `address=/.vibe/127.0.0.1` manually if desired)")

	// Trusted CA in Keychain
	_ = exec.Command("security", "delete-certificate", "-c", "local.vibe CA",
		"/Library/Keychains/System.keychain").Run()
	fmt.Println("  Trusted CA removed from Keychain")

	// Cert files in ~/.vibe/certs
	if home, _ := realUserHome(); home != "" {
		certsDir := filepath.Join(home, ".vibe", "certs")
		_ = os.RemoveAll(certsDir)
		fmt.Println("  ~/.vibe/certs removed")
	}

	fmt.Println()
	fmt.Println("Uninstall complete.")
	fmt.Println("(The `vibe` binary is still installed — delete it manually if you want a fully clean machine.)")
	return nil
}
