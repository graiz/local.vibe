//go:build windows

package daemon

import "golang.org/x/sys/windows"

// processAlive on Windows opens a query handle on the target process and
// asks for its exit code. STILL_ACTIVE (259) means the process hasn't
// exited yet. Failure to open the handle (access denied, invalid PID,
// etc.) is treated as "not alive" — same conservative bias as the unix
// signal-0 trick.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)

	var exitCode uint32
	if err := windows.GetExitCodeProcess(h, &exitCode); err != nil {
		return false
	}
	const stillActive = 259 // STILL_ACTIVE
	return exitCode == stillActive
}
