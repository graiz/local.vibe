//go:build windows

package cert

import "errors"

// TrustCA on Windows is a Phase 1 stub. Phase 2 will invoke
// `certutil -addstore -f Root <ca.pem>` to install the CA into the system
// root store (read by Chrome and Edge). Firefox uses NSS independently.
func TrustCA(certsDir string) error {
	_ = certsDir
	return errors.New("CA trust installation on Windows is not yet implemented — Phase 2 will wire up certutil")
}
