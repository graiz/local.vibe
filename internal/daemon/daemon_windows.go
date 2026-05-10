//go:build windows

package daemon

import (
	"encoding/csv"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

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

// pidCommandDefault returns a short executable name for a PID via
// `tasklist /FI "PID eq N" /FO CSV /NH`. Best-effort: empty string on any
// failure. We avoid WMIC (deprecated on Win11 24H2+) and PowerShell
// (slow startup, not always available).
func pidCommandDefault(pid int) string {
	if pid <= 0 {
		return ""
	}
	out, err := exec.Command(
		winutil.Sys32("tasklist"),
		"/FI", fmt.Sprintf("PID eq %d", pid),
		"/FO", "CSV",
		"/NH",
	).Output()
	if err != nil {
		return ""
	}
	return parseTasklistCSV(string(out))
}

// parseTasklistCSV extracts the image name from the first row of a
// `tasklist /FO CSV /NH` output. Sample row:
//
//	"chrome.exe","12345","Console","1","123,456 K"
//
// When tasklist finds no matching PID it prints either an empty result or
// a localized "INFO: No tasks are running…" line; both produce "" here.
func parseTasklistCSV(out string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	// tasklist's "no match" line starts with "INFO:" and is not valid CSV.
	if strings.HasPrefix(out, "INFO:") {
		return ""
	}
	r := csv.NewReader(strings.NewReader(out))
	// tasklist quotes commas inside the memory column ("123,456 K"), so the
	// default reader settings (FieldsPerRecord = -1, comma separator) are
	// fine — we just need the first field of the first row.
	r.FieldsPerRecord = -1
	rec, err := r.Read()
	if err != nil || len(rec) == 0 {
		return ""
	}
	name := strings.TrimSpace(rec[0])
	// Reject "PID" header just in case someone passed output without /NH.
	if _, err := strconv.Atoi(name); err == nil {
		return ""
	}
	return name
}
