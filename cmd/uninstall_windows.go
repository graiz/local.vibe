//go:build windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// uninstallPlatform reverses every artifact `vibe setup` installs on
// Windows. Each step is best-effort — a missing piece (setup partially
// failed, or was never run) should not block the rest of the cleanup.
//
// Reset DNS to DHCP rather than restoring whatever was set before. Users
// with static DNS configs are rare and can manually re-set after uninstall.
func uninstallPlatform() error {
	if !isElevated() {
		return fmt.Errorf("uninstall requires Administrator — right-click PowerShell, choose \"Run as administrator\", then re-run: vibe uninstall")
	}

	fmt.Println("Removing local.vibe from Windows...")
	fmt.Println()

	// Stop and remove the Scheduled Task.
	if scheduledTaskExists(scheduledTaskName) {
		_ = exec.Command("schtasks", "/end", "/tn", scheduledTaskName).Run()
		_ = exec.Command("schtasks", "/delete", "/tn", scheduledTaskName, "/f").Run()
		fmt.Println("  Scheduled Task `vibe` removed")
	}

	// Remove portproxy rules.
	for _, listen := range []string{"80", "443"} {
		_ = exec.Command("netsh", "interface", "portproxy", "delete", "v4tov4",
			"listenport="+listen, "listenaddress=127.0.0.1").Run()
	}
	fmt.Println("  netsh portproxy rules removed")

	// Reset DNS on every connected adapter back to DHCP.
	adapters, _ := connectedIPv4Adapters()
	for _, name := range adapters {
		out, err := exec.Command("netsh", "interface", "ipv4", "set", "dnsservers",
			"name="+name, "dhcp",
		).CombinedOutput()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warning: could not reset DNS on %q: %v — %s\n", name, err, strings.TrimSpace(string(out)))
		}
	}
	fmt.Println("  Adapter DNS reset to DHCP")

	// Remove the trusted CA from the system root store.
	out, err := exec.Command("certutil", "-delstore", "Root", "local.vibe CA").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  warning: certutil delstore: %v — %s\n", err, strings.TrimSpace(string(out)))
	} else {
		fmt.Println("  Trusted CA removed from system root store")
	}

	// Remove cert files.
	home, _ := os.UserHomeDir()
	if home != "" {
		certsDir := filepath.Join(home, ".vibe", "certs")
		_ = os.RemoveAll(certsDir)
		fmt.Println("  ~/.vibe/certs removed")
	}

	// Flush DNS cache so the change takes effect immediately.
	_ = exec.Command("ipconfig", "/flushdns").Run()

	fmt.Println()
	fmt.Println("Uninstall complete.")
	fmt.Println("(The `vibe.exe` binary is still installed — delete it manually if you want a fully clean machine.)")
	return nil
}

// isElevated and scheduledTaskExists / connectedIPv4Adapters / scheduledTaskName
// live in setup_windows.go and daemon_windows.go — both files are in the same
// package and same build, so we reuse them directly.
