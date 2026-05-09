//go:build !darwin && !linux && !windows

package cert

import (
	"fmt"
	"runtime"
)

// TrustCA on unknown platforms returns a clear error so callers can surface
// the limitation to the user rather than silently succeeding.
func TrustCA(certsDir string) error {
	_ = certsDir
	return fmt.Errorf("CA trust installation is not supported on %s", runtime.GOOS)
}
