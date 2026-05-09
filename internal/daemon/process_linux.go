//go:build linux

package daemon

import (
	"os"
	"os/exec"
	"syscall"
)

// buildShellCommand on Linux uses `$SHELL -c` (no -i). The interactive flag
// interferes with non-interactive runners (CI, systemd) without giving us
// the nvm-shim benefit we get on macOS — Linux distros tend to put runtime
// version managers on PATH via .bash_profile / .profile rather than .bashrc,
// so a non-interactive shell is fine.
func buildShellCommand(routeCmd string) *exec.Cmd {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	return exec.Command(shell, "-c", routeCmd)
}

func applySpawnAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func afterSpawn(name string, cmd *exec.Cmd) { _, _ = name, cmd }
func afterExit(name string)                 { _ = name }

func killProcessTree(name string, cmd *exec.Cmd) error {
	_ = name
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err == nil {
		return syscall.Kill(-pgid, syscall.SIGTERM)
	}
	return cmd.Process.Signal(syscall.SIGTERM)
}
