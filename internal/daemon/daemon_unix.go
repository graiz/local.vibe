//go:build !windows

package daemon

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

// terminateProcess sends SIGTERM to the given PID. The signal lets the
// process clean up and exit on its own; callers that need to ensure
// termination should follow up with Kill on a deadline.
func terminateProcess(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(syscall.SIGTERM)
}

// findPortHoldersDefault returns the PIDs LISTENING on a TCP port. It is the
// kill target for preflightPort/killPort, so the LISTEN filter is
// load-bearing: a bare `lsof -i tcp:PORT` also matches sockets whose remote
// port is PORT — a browser tab keeping a connection to a stale server, or
// the daemon's own health dial — and those were being SIGTERMed as "holders".
func findPortHoldersDefault(port int) []int {
	return pidsListeningOnPort(port)
}

// pidCommandDefault returns a short command name for a pid via
// `ps -p PID -o comm=`. Best-effort: returns "" on error or empty output.
func pidCommandDefault(pid int) string {
	out, err := runTool("ps", "-p", fmt.Sprintf("%d", pid), "-o", "comm=")
	if err != nil {
		return ""
	}
	cmd := strings.TrimSpace(string(out))
	// `ps -o comm=` returns the full path on macOS; trim to the basename for
	// readability in the recovery message.
	if i := strings.LastIndex(cmd, "/"); i >= 0 {
		cmd = cmd[i+1:]
	}
	return cmd
}
