//go:build !darwin && !linux && !windows

package cmd

import (
	"fmt"
	"os"
	"runtime"
)

func setupPlatform() error {
	fmt.Printf("Automatic setup is not supported on %s.\n", runtime.GOOS)
	return nil
}

func openTTYPlatform() (*os.File, error) {
	return os.Open("/dev/tty")
}
