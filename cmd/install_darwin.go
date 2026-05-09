//go:build darwin

package cmd

import (
	"os"
	"path/filepath"
)

// installDestination on macOS prefers Homebrew bin (writable without sudo on
// Apple Silicon Macs), falling back to /usr/local/bin (Intel / older brew).
func installDestination() string {
	for _, p := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
		if _, err := os.Stat(p); err == nil {
			return filepath.Join(p, "vibe")
		}
	}
	return "/usr/local/bin/vibe"
}
