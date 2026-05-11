//go:build !darwin && !linux && !windows

package cmd

import (
	"fmt"
	"runtime"
)

func uninstallPlatform() error {
	fmt.Printf("Automatic uninstall is not supported on %s.\n", runtime.GOOS)
	return nil
}
