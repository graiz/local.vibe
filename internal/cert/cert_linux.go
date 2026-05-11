//go:build linux

package cert

import "errors"

// TrustCA on Linux is not yet automated. Phase 1 stub. Phase 2 (Linux
// implementation, separate branch) will install via update-ca-certificates
// or update-ca-trust depending on the distro, plus NSS via certutil so
// Chrome and Firefox accept the cert.
func TrustCA(certsDir string) error {
	_ = certsDir
	return errors.New("CA trust installation is not yet automated on Linux — see docs for the manual steps")
}
