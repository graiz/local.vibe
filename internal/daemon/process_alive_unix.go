//go:build !windows

package daemon

import (
	"os"
	"syscall"
)

// processAlive reports whether a process with the given PID is still running.
// On unix-like systems it uses signal 0 — a no-op delivery that errors if the
// process has exited or we lack permission to signal it.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
