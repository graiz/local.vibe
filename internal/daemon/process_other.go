//go:build !darwin && !linux && !windows

package daemon

import (
	"os"
	"os/exec"
)

// Generic POSIX fallback for unknown unix-like platforms (FreeBSD, OpenBSD,
// etc.). Behaves like the Linux build — `$SHELL -c`, no Setpgid (since the
// syscall struct fields aren't guaranteed to exist), and a plain Kill on
// stop. Real support for these platforms is left as an exercise.
func buildShellCommand(routeCmd string) *exec.Cmd {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	return exec.Command(shell, "-c", routeCmd)
}

func applySpawnAttrs(cmd *exec.Cmd) { _ = cmd }

func afterSpawn(name string, cmd *exec.Cmd) { _, _ = name, cmd }
func afterExit(name string)                 { _ = name }

func killProcessTree(name string, cmd *exec.Cmd) error {
	_ = name
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
