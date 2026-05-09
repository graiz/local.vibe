//go:build windows

package cert

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// TrustCA installs the CA into the Windows root store via certutil. Requires
// Administrator. Chrome and Edge read this store directly; Firefox uses NSS
// independently and is not handled here — first-time Firefox users will see
// a cert warning unless they import the CA manually.
//
// Idempotent: certutil -addstore replaces an existing entry with the same
// subject. The "duplicate" exit code is mapped to success.
func TrustCA(certsDir string) error {
	certPath := filepath.Join(certsDir, "ca.pem")

	out, err := exec.Command("certutil", "-addstore", "-f", "Root", certPath).CombinedOutput()
	if err == nil {
		return nil
	}
	combined := string(out)
	// certutil returns 0x80092009 (CRYPT_E_NO_MATCH) when the store already
	// contains an identical cert with -f; some Windows builds map that to a
	// non-zero exit even though the install succeeded. Treat the "already
	// installed" wording as success so re-running setup is idempotent.
	if strings.Contains(strings.ToLower(combined), "duplicate") {
		return nil
	}
	return fmt.Errorf("trust CA via certutil: %w — %s", err, strings.TrimSpace(combined))
}
