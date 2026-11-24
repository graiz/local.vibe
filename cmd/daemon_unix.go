//go:build !windows

package cmd

import (
	"os"
	"syscall"
)

// detachAttrs puts the forked daemon in its own session (Setsid) so it
// survives the CLI's terminal going away.
func detachAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// cliProcessAlive checks whether a daemon PID is still running. Uses signal
// 0 — a no-op delivery that errors if the process has exited.
func cliProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// signalDaemonStop sends SIGTERM to a daemon the platform autostart hook
// doesn't manage; the daemon's signal handler runs Server.Stop.
func signalDaemonStop(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(syscall.SIGTERM)
}
