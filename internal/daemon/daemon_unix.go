//go:build !windows

package daemon

import (
	"fmt"
	"os"
	"os/exec"
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

// findPortHoldersDefault runs `lsof -ti tcp:PORT` and returns the listening
// PIDs. Returns nil on lsof error or when no process is bound to the port.
func findPortHoldersDefault(port int) []int {
	out, err := exec.Command("lsof", "-ti", fmt.Sprintf("tcp:%d", port)).Output()
	if err != nil {
		return nil
	}
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var pid int
		if _, err := fmt.Sscan(line, &pid); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}

// pidCommandDefault returns a short command name for a pid via
// `ps -p PID -o comm=`. Best-effort: returns "" on error or empty output.
func pidCommandDefault(pid int) string {
	out, err := exec.Command("ps", "-p", fmt.Sprintf("%d", pid), "-o", "comm=").Output()
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
