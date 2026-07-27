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

// killAdoptedProcess terminates a managed child the daemon re-adopted after a
// restart — it has the process-group leader PID but no *exec.Cmd. It signals
// the whole process group, matching killProcessTree's semantics for children
// the daemon spawned itself.
// The PID comes from routes.json and may be stale: the daemon was down while
// the real child exited, and the OS may have recycled the number. Signalling a
// whole process group off a recycled PID would hit an unrelated tree, so the
// ownership of the PID is verified first. applySpawnAttrs starts every managed
// child with Setpgid, making it its own group leader (pgid == pid); a PID that
// isn't a group leader was therefore never one of ours, and is left alone
// rather than signalled individually. This mirrors the refusal guards in
// safeKillPID / killPort.
func killAdoptedProcess(pid int) error {
	if pid <= 1 || pid == os.Getpid() {
		return nil
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return nil // already gone
	}
	if pgid != pid {
		return nil // not a group leader — not a child we spawned
	}
	return syscall.Kill(-pgid, syscall.SIGTERM)
}
