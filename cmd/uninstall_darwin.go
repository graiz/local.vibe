//go:build darwin

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/graiz/local.vibe/internal/cert"
	"github.com/graiz/local.vibe/internal/vibeskill"
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
	// Root-owned pf helper copy the LaunchDaemon executed (see stagePFHelper).
	_ = os.RemoveAll(pfHelperDir)
	fmt.Println("  pf LaunchDaemon removed")

	// pf anchor: flush its live rules, drop the anchor file, strip our two
	// lines from /etc/pf.conf, then reload so the running ruleset matches the
	// file again. Order matters — reloading before stripping would just
	// re-attach the anchor we are removing.
	_ = exec.Command("/sbin/pfctl", "-a", pfAnchorName, "-F", "all").Run()
	_ = os.Remove(pfAnchorFile)
	if conf, err := os.ReadFile(pfConfPath); err == nil {
		if stripped, changed := stripPFConf(string(conf)); changed {
			if err := os.WriteFile(pfConfPath, []byte(stripped), 0644); err == nil {
				_ = exec.Command("/sbin/pfctl", "-f", pfConfPath).Run()
			}
		}
	}
	fmt.Println("  pf anchor removed, /etc/pf.conf restored")

	// /etc/resolver/vibe
	_ = os.Remove("/etc/resolver/vibe")
	fmt.Println("  /etc/resolver/vibe removed")

	// dnsmasq vibe entry — best-effort: leave dnsmasq.conf untouched if we
	// can't safely strip just our line. Most users don't mind one stale
	// `address=/.vibe/127.0.0.1` lying around in dnsmasq.conf.
	fmt.Println("  (dnsmasq.conf left intact — remove `address=/.vibe/127.0.0.1` manually if desired)")

	// Trusted CA in Keychain. Match by SHA1 thumbprint while ca.pem is still
	// on disk: a CN match (-c) deletes just one of possibly several certs
	// named "local.vibe CA" — an uninstall/setup cycle regenerates the CA, so
	// duplicates are normal — and could remove the wrong one. Fall back to CN
	// only when the certs dir is already gone. Mirrors uninstall_windows.go,
	// which has matched by thumbprint since certutil -delstore needed it.
	if home, _ := realUserHome(); home != "" {
		certsDir := filepath.Join(home, ".vibe", "certs")
		if thumb, err := cert.CAThumbprint(certsDir); err == nil {
			_ = exec.Command("security", "delete-certificate", "-Z", thumb,
				"/Library/Keychains/System.keychain").Run()
		} else {
			_ = exec.Command("security", "delete-certificate", "-c", "local.vibe CA",
				"/Library/Keychains/System.keychain").Run()
		}
		fmt.Println("  Trusted CA removed from Keychain")

		_ = os.RemoveAll(certsDir)
		fmt.Println("  ~/.vibe/certs removed")

		// Global agent skill
		_ = vibeskill.UninstallFrom(home)
		fmt.Println("  ~/.claude/skills/local-vibe removed")
	}

	fmt.Println()
	fmt.Println("Uninstall complete.")
	fmt.Println("(The `vibe` binary is still installed — delete it manually if you want a fully clean machine.)")
	return nil
}
