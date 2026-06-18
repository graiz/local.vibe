//go:build !darwin && !windows

package cmd

import "os"

// restartDaemonForDev kills the running daemon. There's no autostart on these
// platforms yet, so the user brings it back up manually — `vibe dev` reporting
// "daemon not running" is correct here.
func restartDaemonForDev() error {
	pid, err := readDaemonPID()
	if err != nil || pid <= 0 {
		return nil
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Signal(os.Kill)
	}
	return nil
}
