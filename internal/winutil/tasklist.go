//go:build windows

package winutil

import (
	"encoding/csv"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// TaskImageName returns the short executable name (e.g. "chrome.exe") for a
// given PID via `tasklist /FI "PID eq N" /FO CSV /NH`. Returns "" on any
// failure (PID gone, tasklist unavailable, malformed output).
//
// This is the canonical Windows analog of `ps -p N -o comm=` on unix, used
// across both the cmd package (port-collision diagnostics) and
// internal/daemon (port-conflict recovery hints). We avoid WMIC (deprecated
// on Win11 24H2+) and PowerShell (slow startup on a hot path).
func TaskImageName(pid int) string {
	if pid <= 0 {
		return ""
	}
	out, err := exec.Command(
		Sys32("tasklist"),
		"/FI", fmt.Sprintf("PID eq %d", pid),
		"/FO", "CSV",
		"/NH",
	).Output()
	if err != nil {
		return ""
	}
	return ParseTasklistCSV(string(out))
}

// ParseTasklistCSV extracts the image name from the first row of a
// `tasklist /FO CSV /NH` output. Sample row:
//
//	"chrome.exe","12345","Console","1","123,456 K"
//
// When tasklist finds no matching PID it prints either an empty result or
// a localized "INFO: No tasks are running…" line; both produce "" here.
//
// Exported so tests can verify parser behavior without shelling out.
func ParseTasklistCSV(out string) string {
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
