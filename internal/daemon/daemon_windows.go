//go:build windows

package daemon

import "os"

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

// findPortHoldersDefault on Windows is a Phase 1 stub. Phase 2 will parse
// `netstat -ano -p TCP` (or call iphlpapi.GetExtendedTcpTable) and return
// the PIDs of LISTENING entries on the given port.
//
// Returning nil means killPort is a no-op on Windows in Phase 1 — the
// recovery flow can't auto-clear EADDRINUSE yet.
func findPortHoldersDefault(port int) []int {
	_ = port
	return nil
}

// pidCommandDefault on Windows is a Phase 1 stub. Phase 2 will use
// `tasklist /FI "PID eq <pid>"` (or the toolhelp32 snapshot APIs) to fetch
// the executable name. For now returning "" means recovery messages just
// say "PID N" without the friendly process name.
func pidCommandDefault(pid int) string {
	_ = pid
	return ""
}
