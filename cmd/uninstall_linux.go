//go:build linux

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/graiz/local.vibe/internal/cert"
)

// uninstallPlatform reverses every artifact `vibe setup` installs on Linux,
// including the binary itself (step 5). Each step is best-effort — a
// missing piece (setup partially failed, or was never run) should not
// block the rest of the cleanup.
func uninstallPlatform() error {
	if os.Getuid() != 0 {
		return fmt.Errorf("uninstall requires root — run: sudo vibe uninstall")
	}

	fmt.Println("Removing local.vibe from Linux...")
	fmt.Println()

	home, _ := realLinuxUserHome()

	// 1. User systemd unit. Stop + disable as the real user, then remove the
	//    unit file and reload.
	if home != "" {
		userUnit := filepath.Join(home, ".config", "systemd", "user", userUnitFilename)
		if _, err := os.Stat(userUnit); err == nil {
			_, _ = runAsRealUserCombined("systemctl", "--user", "stop", userUnitFilename)
			_, _ = runAsRealUserCombined("systemctl", "--user", "disable", userUnitFilename)
			_ = os.Remove(userUnit)
			_, _ = runAsRealUserCombined("systemctl", "--user", "daemon-reload")
			fmt.Println("  User systemd unit removed")
		}
	}

	// 2. nftables redirect — stop the service (which runs ExecStop=nft delete
	//    table), disable, remove the unit, remove the ruleset, reload.
	if _, err := os.Stat(nftServicePath); err == nil {
		_ = exec.Command("systemctl", "stop", "vibe-nft.service").Run()
		_ = exec.Command("systemctl", "disable", "vibe-nft.service").Run()
		_ = os.Remove(nftServicePath)
		_ = exec.Command("systemctl", "daemon-reload").Run()
		fmt.Println("  nft systemd unit removed")
	}
	// Even if the unit was gone, the table may still be loaded — flush it.
	if nft, err := exec.LookPath("nft"); err == nil {
		_ = exec.Command(nft, "delete", "table", "inet", "vibe").Run()
	}
	if _, err := os.Stat(nftRulesetPath); err == nil {
		_ = os.Remove(nftRulesetPath)
		fmt.Println("  nft ruleset removed")
	}

	// 3. DNS routing. The vibe-resolved.service unit's ExecStop deletes the
	//    vibe0 dummy interface, which removes resolved's per-link config
	//    with it. Stop before disable so ExecStop fires.
	if _, err := os.Stat(vibeResolvedServicePath); err == nil {
		_ = exec.Command("systemctl", "stop", "vibe-resolved.service").Run()
		_ = exec.Command("systemctl", "disable", "vibe-resolved.service").Run()
		_ = os.Remove(vibeResolvedServicePath)
		_ = exec.Command("systemctl", "daemon-reload").Run()
		fmt.Println("  DNS routing unit removed and vibe0 interface deleted")
	}

	// 4. CA trust — system store + NSS. Capture certsDir BEFORE wiping
	//    ~/.vibe/certs because UntrustCA needs to read ca.pem (p11-kit's
	//    `trust anchor --remove` takes a path, not a CN).
	if home != "" {
		certsDir := filepath.Join(home, ".vibe", "certs")
		if err := cert.UntrustCA(certsDir); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: untrust CA from system store: %v\n", err)
		} else {
			fmt.Println("  Trusted CA removed from system store")
		}
		if err := cert.UntrustCAInUserNSS(home); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: untrust CA from NSS: %v\n", err)
		} else {
			fmt.Println("  Trusted CA removed from user NSS db")
		}
		_ = os.RemoveAll(certsDir)
		fmt.Println("  ~/.vibe/certs removed")
	}

	// 5. The vibe binary itself. Done last so we keep working until the very
	//    end of cleanup. os.Remove on a running executable is safe on Linux —
	//    the inode stays mapped in our address space until exit, only the
	//    directory entry disappears.
	if self, err := os.Executable(); err == nil {
		self, _ = filepath.EvalSymlinks(self)
		if err := os.Remove(self); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: could not remove %s: %v\n", self, err)
		} else {
			fmt.Printf("  Binary removed (%s)\n", self)
		}
	}

	fmt.Println()
	fmt.Println("Uninstall complete.")
	return nil
}
