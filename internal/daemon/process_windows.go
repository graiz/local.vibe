//go:build windows

package daemon

import (
	"os"
	"os/exec"
)

// buildShellCommand on Windows uses %COMSPEC% (cmd.exe) /C. Phase 1 leaves
// it at this — Phase 2 will likely keep cmd.exe but document that PowerShell
// users can wrap their command (`powershell -Command "..."`) in route.Cmd.
func buildShellCommand(routeCmd string) *exec.Cmd {
	shell := os.Getenv("COMSPEC")
	if shell == "" {
		shell = "cmd.exe"
	}
	return exec.Command(shell, "/C", routeCmd)
}

// applySpawnAttrs on Windows is a no-op in Phase 1. Phase 2 will assign the
// child to a Job Object (CreateJobObject + AssignProcessToJobObject) so its
// descendants can be terminated atomically — equivalent to the unix
// process-group + Kill(-pgid) pattern.
func applySpawnAttrs(cmd *exec.Cmd) { _ = cmd }

// afterSpawn / afterExit are hooks Phase 2 will use to track Job Object
// handles per route. Phase 1 has no extra state.
func afterSpawn(name string, cmd *exec.Cmd) { _, _ = name, cmd }
func afterExit(name string)                 { _ = name }

// killProcessTree on Windows in Phase 1 only kills the root process —
// descendants are NOT cleaned up. Long-running dev servers that fork
// (nodemon → node, etc.) will leak children. Phase 2 fixes this with
// TerminateJobObject.
func killProcessTree(name string, cmd *exec.Cmd) error {
	_ = name
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
