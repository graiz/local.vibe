//go:build darwin

package cert

import (
	"fmt"
	"os/exec"
	"path/filepath"
)

// TrustCA installs the CA certificate into the macOS System Keychain.
// Requires root privileges.
func TrustCA(certsDir string) error {
	certPath := filepath.Join(certsDir, "ca.pem")

	// Check if already trusted
	out, err := exec.Command("security", "find-certificate", "-c", "local.vibe CA", "/Library/Keychains/System.keychain").CombinedOutput()
	if err == nil && len(out) > 0 {
		// Already trusted — remove old one first so we can update
		_ = exec.Command("security", "delete-certificate", "-c", "local.vibe CA", "/Library/Keychains/System.keychain").Run()
	}

	cmd := exec.Command("security", "add-trusted-cert",
		"-d", "-r", "trustRoot",
		"-k", "/Library/Keychains/System.keychain",
		certPath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("trust CA: %w — %s", err, string(output))
	}
	return nil
}
