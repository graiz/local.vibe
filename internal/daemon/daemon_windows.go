//go:build windows

package daemon

import (
	"os"
	"os/exec"

	"github.com/graiz/local.vibe/internal/winutil"
)

// terminateProcess on Windows uses TerminateProcess via os.Process.Kill —
// there is no graceful equivalent of SIGTERM for arbitrary PIDs we don't
// own. Callers that need a graceful shutdown should signal their own
// children via os.Interrupt (which Go translates to a console ctrl-event).
func terminateProcess(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}

// findPortHoldersDefault returns every PID listening on the given TCP port.
// Implemented by parsing `netstat -ano` (NOT `-p TCP`, which is locale-
// translated on non-English Windows). The parser keys on the foreign-
// address-is-unspecified shape rather than the localized state word, so
// this works on any Windows locale.
func findPortHoldersDefault(port int) []int {
	out, err := exec.Command(winutil.Sys32("netstat"), "-ano").Output()
	if err != nil {
		return nil
	}
	var pids []int
	seen := map[int]bool{}
	for _, l := range parseNetstatListeners(string(out)) {
		if l.Port != port {
			continue
		}
		if seen[l.PID] {
			continue
		}
		seen[l.PID] = true
		pids = append(pids, l.PID)
	}
	return pids
}

// pidCommandDefault returns a short executable name for a PID. Thin
// adapter for the daemon's findPortHoldersFn/pidCommandFn indirection;
// the implementation lives in internal/winutil.
func pidCommandDefault(pid int) string { return winutil.TaskImageName(pid) }
