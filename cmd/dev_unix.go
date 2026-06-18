//go:build !windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"
)

// defaultVibeInstallPath is the fallback path when `which vibe` fails.
// macOS Homebrew is the canonical install location.
func defaultVibeInstallPath() string {
	return "/opt/homebrew/bin/vibe"
}

// replaceVibeBinary builds the source tree and atomically swaps it into
// place. Build to a temp file first so a compile failure doesn't truncate
// the running binary, and so we can SIGKILL the running process *after*
// the new binary is on disk — LaunchAgent's KeepAlive then resurrects it
// pointing at the new code.
func replaceVibeBinary(srcDir, binary string) error {
	tmpBin := binary + ".tmp"
	build := exec.Command("go", "build", "-o", tmpBin, ".")
	build.Dir = srcDir
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		os.Remove(tmpBin)
		return fmt.Errorf("build failed: %w", err)
	}
	if err := os.Rename(tmpBin, binary); err != nil {
		os.Remove(tmpBin)
		return fmt.Errorf("install failed: %w", err)
	}
	return nil
}

// restartDaemonForDev is per-OS: dev_darwin.go (launchctl kickstart) and
// dev_unix_other.go (kill + manual restart).
