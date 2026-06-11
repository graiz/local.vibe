//go:build darwin

package daemon

import (
	"os"
	"os/exec"
	"syscall"
)

// buildShellCommand on macOS uses `$SHELL -lic`. The login + interactive
// flags pull nvm/rbenv/pyenv shims onto PATH for managed routes — without
// them, every managed JS/Ruby/Python project fails with "command not
// found". This is a load-bearing invariant; do not change without testing
// `vibe start` on a route whose command depends on a shell-managed runtime.
func buildShellCommand(routeCmd string) *exec.Cmd {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	return exec.Command(shell, "-lic", routeCmd)
}

func applySpawnAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// afterSpawn / afterExit are hooks per-OS files use to maintain extra state
// (e.g. Job Object handles on Windows). On darwin they're no-ops.
func afterSpawn(name string, cmd *exec.Cmd) { _, _ = name, cmd }
func afterExit(name string)                 { _ = name }

// killProcessTree sends SIGTERM to the negative PID so every process in the
// child's process group receives it (the typical "kill the entire dev-server
// tree, including npm + node + esbuild children" case).
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

// killAdoptedProcess terminates a managed child the daemon re-adopted after a
// restart — it has the process-group leader PID but no *exec.Cmd. It signals
// the whole process group, matching killProcessTree's semantics for children
// the daemon spawned itself.
func killAdoptedProcess(pid int) error {
	if pid <= 1 {
		return nil
	}
	if pgid, err := syscall.Getpgid(pid); err == nil {
		return syscall.Kill(-pgid, syscall.SIGTERM)
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}
