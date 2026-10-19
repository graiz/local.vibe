//go:build linux

package cert

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// TrustCA installs the CA certificate into the system-wide trust store.
// Requires root. Detects the distro's trust toolchain by probing for binaries
// and well-known anchor directories rather than parsing /etc/os-release —
// the binary's presence is what actually matters, and probing keeps us
// honest on derivatives (Ubuntu→Debian, Rocky→RHEL, Manjaro→Arch).
//
// Toolchains, in probe order:
//   - update-ca-certificates → /usr/local/share/ca-certificates/local-vibe.crt   (Debian/Ubuntu)
//   - update-ca-trust        → /etc/pki/ca-trust/source/anchors/local-vibe.pem  (Fedora/RHEL)
//   - update-ca-trust        → /etc/ca-certificates/trust-source/anchors/...    (Arch/Manjaro)
//   - trust (p11-kit)        → trust anchor --store <ca.pem>                    (fallback)
//
// Chrome/Firefox use NSS, not the system store — TrustCAInUserNSS handles
// that separately. Returns nil only when the system store install succeeds.
func TrustCA(certsDir string) error {
	caPath := filepath.Join(certsDir, "ca.pem")
	if _, err := os.Stat(caPath); err != nil {
		return fmt.Errorf("CA not found at %s: %w", caPath, err)
	}

	// Debian/Ubuntu — anchor file MUST end in .crt for update-ca-certificates
	// to pick it up.
	if _, err := exec.LookPath("update-ca-certificates"); err == nil {
		dir := "/usr/local/share/ca-certificates"
		if err := os.MkdirAll(dir, 0755); err == nil {
			dst := filepath.Join(dir, "local-vibe.crt")
			if err := copyFile(caPath, dst, 0644); err != nil {
				return fmt.Errorf("install CA to %s: %w", dst, err)
			}
			out, err := exec.Command("update-ca-certificates").CombinedOutput()
			if err != nil {
				return fmt.Errorf("update-ca-certificates: %w — %s", err, strings.TrimSpace(string(out)))
			}
			return nil
		}
	}

	// Fedora/RHEL and Arch both use update-ca-trust but with different anchor
	// directories. Try the most likely path for each family.
	if _, err := exec.LookPath("update-ca-trust"); err == nil {
		for _, dir := range []string{
			"/etc/pki/ca-trust/source/anchors",
			"/etc/ca-certificates/trust-source/anchors",
		} {
			if _, err := os.Stat(dir); err != nil {
				continue
			}
			dst := filepath.Join(dir, "local-vibe.pem")
			if err := copyFile(caPath, dst, 0644); err != nil {
				return fmt.Errorf("install CA to %s: %w", dst, err)
			}
			out, err := exec.Command("update-ca-trust").CombinedOutput()
			if err != nil {
				return fmt.Errorf("update-ca-trust: %w — %s", err, strings.TrimSpace(string(out)))
			}
			return nil
		}
	}

	// Fallback: p11-kit trust tool. Works on most modern distros even when
	// the per-family helpers above aren't installed.
	if _, err := exec.LookPath("trust"); err == nil {
		out, err := exec.Command("trust", "anchor", "--store", caPath).CombinedOutput()
		if err != nil {
			return fmt.Errorf("trust anchor --store: %w — %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}

	return fmt.Errorf("no supported CA trust tool found (need update-ca-certificates, update-ca-trust, or p11-kit's `trust`)")
}

// UntrustCA reverses TrustCA. Best-effort: missing artifacts are not errors,
// since uninstall should converge to a clean state regardless of which path
// the original install took.
func UntrustCA(certsDir string) error {
	caPath := filepath.Join(certsDir, "ca.pem")

	if _, err := exec.LookPath("update-ca-certificates"); err == nil {
		dst := "/usr/local/share/ca-certificates/local-vibe.crt"
		if _, err := os.Stat(dst); err == nil {
			_ = os.Remove(dst)
			_ = exec.Command("update-ca-certificates", "--fresh").Run()
		}
	}

	if _, err := exec.LookPath("update-ca-trust"); err == nil {
		removed := false
		for _, dir := range []string{
			"/etc/pki/ca-trust/source/anchors",
			"/etc/ca-certificates/trust-source/anchors",
		} {
			dst := filepath.Join(dir, "local-vibe.pem")
			if _, err := os.Stat(dst); err == nil {
				_ = os.Remove(dst)
				removed = true
			}
		}
		if removed {
			_ = exec.Command("update-ca-trust").Run()
		}
	}

	// p11-kit — `trust anchor --remove` rejects a path that was never
	// installed, so errors are non-fatal.
	if _, err := exec.LookPath("trust"); err == nil {
		if _, err := os.Stat(caPath); err == nil {
			_ = exec.Command("trust", "anchor", "--remove", caPath).Run()
		}
	}

	return nil
}

// TrustCAInUserNSS installs the CA into the NSS database at
// <homeDir>/.pki/nssdb so Chrome and Chromium-derived browsers accept the
// cert. Firefox uses per-profile NSS dbs and is handled via
// security.enterprise_roots.enabled — documented in README, not automated.
//
// Soft no-op when certutil isn't installed: NSS automation is a UX
// enhancement, not a correctness requirement. A missing libnss3-tools must
// not fail `vibe setup`.
func TrustCAInUserNSS(certsDir, homeDir string) error {
	caPath := filepath.Join(certsDir, "ca.pem")
	if _, err := os.Stat(caPath); err != nil {
		return nil
	}
	certutil, err := exec.LookPath("certutil")
	if err != nil {
		return nil
	}

	nssDir := filepath.Join(homeDir, ".pki", "nssdb")
	if _, err := os.Stat(nssDir); err != nil {
		if err := os.MkdirAll(nssDir, 0755); err != nil {
			return fmt.Errorf("create %s: %w", nssDir, err)
		}
		if out, err := exec.Command(certutil, "-d", "sql:"+nssDir, "-N", "--empty-password").CombinedOutput(); err != nil {
			return fmt.Errorf("certutil -N: %w — %s", err, strings.TrimSpace(string(out)))
		}
	}

	_ = exec.Command(certutil, "-d", "sql:"+nssDir, "-D", "-n", "local.vibe CA").Run()
	out, err := exec.Command(certutil, "-d", "sql:"+nssDir,
		"-A", "-t", "C,,", "-n", "local.vibe CA", "-i", caPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("certutil -A: %w — %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// UntrustCAInUserNSS removes the CA from the user's NSS database. Best-effort.
func UntrustCAInUserNSS(homeDir string) error {
	certutil, err := exec.LookPath("certutil")
	if err != nil {
		return nil
	}
	nssDir := filepath.Join(homeDir, ".pki", "nssdb")
	if _, err := os.Stat(nssDir); err != nil {
		return nil
	}
	_ = exec.Command(certutil, "-d", "sql:"+nssDir, "-D", "-n", "local.vibe CA").Run()
	return nil
}

func copyFile(src, dst string, perm os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, perm)
}
