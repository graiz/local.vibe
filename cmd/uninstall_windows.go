//go:build windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/graiz/local.vibe/internal/cert"
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

	// Remove the trusted CA from the system root store. Prefer matching by
	// the cert's SHA1 thumbprint over matching by Subject CN — the CN
	// "local.vibe CA" is shared by every install we've ever made, so a CN
	// match will quietly remove a cert that belongs to e.g. a different
	// user's install or another tool that happens to use the same name.
	// The thumbprint is unique to the actual bytes on disk.
	//
	// Capture certsDir up front so we can read the thumbprint BEFORE wiping
	// the cert files.
	home, _ := os.UserHomeDir()
	var certsDir string
	if home != "" {
		certsDir = filepath.Join(home, ".vibe", "certs")
	}

	if certsDir != "" {
		if thumb, err := cert.CAThumbprint(certsDir); err == nil {
			out, err := exec.Command(winutil.Sys32("certutil"), "-delstore", "Root", thumb).CombinedOutput()
			if err != nil {
				fmt.Fprintf(os.Stderr, "  warning: certutil delstore by thumbprint: %v — %s\n", err, strings.TrimSpace(string(out)))
			} else {
				fmt.Println("  Trusted CA removed from system root store")
			}
		} else {
			// Cert file gone (manual cleanup before uninstall, or never installed).
			// Fall back to CN match with a clear warning that this is imprecise.
			fmt.Fprintf(os.Stderr, "  note: ~/.vibe/certs/ca.pem unreadable (%v); attempting CN-based delete (may match other certs sharing the same Subject)\n", err)
			out, err := exec.Command(winutil.Sys32("certutil"), "-delstore", "Root", "local.vibe CA").CombinedOutput()
			if err != nil {
				fmt.Fprintf(os.Stderr, "  warning: certutil delstore by CN: %v — %s\n", err, strings.TrimSpace(string(out)))
			} else {
				fmt.Println("  Trusted CA removed from system root store (CN match)")
			}
		}
	}

	// Remove cert files.
	if certsDir != "" {
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
