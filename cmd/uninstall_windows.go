//go:build windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/graiz/local.vibe/internal/winutil"
)

// uninstallPlatform reverses every artifact `vibe setup` installs on
// Windows. Each step is best-effort — a missing piece (setup partially
// failed, or was never run) should not block the rest of the cleanup.
//
// DNS is restored from the snapshot taken at setup time
// (~/.vibe/dns-backup.json) so users with static DNS configs end up back
// where they started. Adapters with no snapshot entry, or installs where
// the backup was lost, fall through to a DHCP reset.
func uninstallPlatform() error {
	if !isElevated() {
		return fmt.Errorf("uninstall requires Administrator — right-click PowerShell, choose \"Run as administrator\", then re-run: vibe uninstall")
	}

	fmt.Println("Removing local.vibe from Windows...")
	fmt.Println()

	// Stop and remove the Scheduled Task.
	if scheduledTaskExists(scheduledTaskName) {
		_ = exec.Command(winutil.Sys32("schtasks"), "/end", "/tn", scheduledTaskName).Run()
		_ = exec.Command(winutil.Sys32("schtasks"), "/delete", "/tn", scheduledTaskName, "/f").Run()
		fmt.Println("  Scheduled Task `vibe` removed")
	}

	// Remove portproxy rules.
	for _, listen := range []string{"80", "443"} {
		_ = exec.Command(winutil.Sys32("netsh"), "interface", "portproxy", "delete", "v4tov4",
			"listenport="+listen, "listenaddress=127.0.0.1").Run()
	}
	fmt.Println("  netsh portproxy rules removed")

	// Restore each connected adapter's DNS from the snapshot saved at setup
	// time. Missing/unreadable backup → fall back to DHCP-reset for safety.
	snap, err := loadDNSBackup()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not read %s: %v — falling back to DHCP\n", dnsBackupFile(), err)
		snap = nil
	}
	restoreAdapterDNS(snap)
	if snap != nil {
		fmt.Println("  Adapter DNS restored from backup")
	} else {
		fmt.Println("  Adapter DNS reset to DHCP (no backup found)")
	}

	// Final safety check: re-read the live DNS state and force DHCP on any
	// adapter still pointing at 127.0.0.1. Belt-and-suspenders against any
	// path through the restore that left a loopback pointer behind — without
	// this, a user could end up with a dead DNS config after uninstall.
	if fixed := verifyAndFixLoopbackDNS(); len(fixed) > 0 {
		fmt.Fprintf(os.Stderr, "  forced DHCP on adapter(s) still pointing at 127.0.0.1: %v\n", fixed)
	}

	// Only remove the backup file once we've confirmed the restore + verify
	// step both ran. If the user re-runs uninstall later (or runs setup
	// again), they want the original snapshot back, not a freshly-captured
	// one that might already include our listener.
	if snap != nil {
		_ = os.Remove(dnsBackupFile())
	}

	// Remove the trusted CA from the system root store.
	out, err := exec.Command(winutil.Sys32("certutil"), "-delstore", "Root", "local.vibe CA").CombinedOutput()
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
	_ = exec.Command(winutil.Sys32("ipconfig"), "/flushdns").Run()

	fmt.Println()
	fmt.Println("Uninstall complete.")
	fmt.Println("(The `vibe.exe` binary is still installed — delete it manually if you want a fully clean machine.)")
	return nil
}

// isElevated and scheduledTaskExists / connectedIPv4Adapters / scheduledTaskName
// live in setup_windows.go and daemon_windows.go — both files are in the same
// package and same build, so we reuse them directly.
