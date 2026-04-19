package daemon

import (
	"bufio"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// discoverRoutePort tries to locate the real listening port for a route
// whose registered Port no longer accepts connections. Two strategies, in
// order:
//
//  1. lsof on the route's process group (managed routes whose original
//     child is still alive but whose app rebound to a different port).
//  2. Regex-scan the tail of the per-route log file (works even when the
//     tracked PID is gone, as long as vibe's log is still being written).
//
// A candidate is returned only if it differs from route.Port AND a TCP
// dial to 127.0.0.1 or [::1] on that port succeeds.
func (s *Server) discoverRoutePort(route *Route) (int, bool) {
	if p, ok := portFromProcessGroup(route); ok && p != route.Port && s.isPortReady(p) {
		return p, true
	}
	logPath := filepath.Join(s.configDir(), route.Name+".log")
	if p, ok := portFromLog(logPath); ok && p != route.Port && s.isPortReady(p) {
		return p, true
	}
	return 0, false
}

// portFromProcessGroup returns the first listening TCP port owned by any
// process in the route's process group.
func portFromProcessGroup(route *Route) (int, bool) {
	pid, ok := route.PIDValue()
	if !ok || !processAlive(pid) {
		return 0, false
	}
	pids := []int{pid}
	if pgid, err := syscall.Getpgid(pid); err == nil {
		if group, err := pidsInGroup(pgid); err == nil && len(group) > 0 {
			pids = group
		}
	}
	return lsofListenPort(pids)
}

// pidsInGroup returns all process IDs whose process group matches pgid,
// via `ps -A -o pid=,pgid=`. Works on macOS and Linux.
func pidsInGroup(pgid int) ([]int, error) {
	out, err := exec.Command("ps", "-A", "-o", "pid=,pgid=").Output()
	if err != nil {
		return nil, err
	}
	var pids []int
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		gid, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		if gid == pgid {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// lsofListenPort invokes `lsof -iTCP -sTCP:LISTEN -P -n -p <csv>` for the
// given PIDs and returns the first listening TCP port.
func lsofListenPort(pids []int) (int, bool) {
	if len(pids) == 0 {
		return 0, false
	}
	ids := make([]string, len(pids))
	for i, p := range pids {
		ids[i] = strconv.Itoa(p)
	}
	out, err := exec.Command("lsof", "-iTCP", "-sTCP:LISTEN", "-P", "-n", "-p", strings.Join(ids, ",")).Output()
	if err != nil {
		// lsof exits non-zero when there are no matching sockets — that's a
		// legitimate "no listener yet" case, not an error we should propagate.
		return 0, false
	}
	return parseLsofListenPort(string(out))
}

// lsofPortRE matches the "NAME" column of lsof output for listening
// sockets: "127.0.0.1:3001 (LISTEN)" or "[::1]:3001 (LISTEN)" or
// "*:3001 (LISTEN)".
var lsofPortRE = regexp.MustCompile(`:(\d{2,5})\s*\(LISTEN\)`)

func parseLsofListenPort(out string) (int, bool) {
	for _, line := range strings.Split(out, "\n") {
		m := lsofPortRE.FindStringSubmatch(line)
		if len(m) < 2 {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil || n <= 0 || n > 65535 {
			continue
		}
		return n, true
	}
	return 0, false
}

// portFromLog reads the tail of path and returns the most plausible TCP
// port announcement found inside it.
func portFromLog(path string) (int, bool) {
	tail := tailLogFile(path, 80)
	return scanLogForPort(tail)
}

// logPortPatterns is in priority order, most-specific first. Within a
// single pattern, the later match in the tail wins (newer log entries).
var logPortPatterns = []*regexp.Regexp{
	// http://localhost:3001, http://127.0.0.1:3001, http://[::1]:3001, http://0.0.0.0:3001
	regexp.MustCompile(`(?i)https?://(?:localhost|127\.0\.0\.1|\[::1?\]|0\.0\.0\.0):(\d{2,5})`),
	// "Local:   http://foo.bar:3001" (Vite / Turbopack)
	regexp.MustCompile(`(?i)Local:\s+https?://\S+?:(\d{2,5})`),
	// "listening on :3001", "listening on port 3001", "listening at :3001"
	regexp.MustCompile(`(?i)listening (?:on|at)[^\d\n]{0,24}(\d{2,5})`),
	// "Server running at http://...:3001" / "started server on 0.0.0.0:3001"
	regexp.MustCompile(`(?i)(?:running at|started (?:server|app)\s*(?:on|at)?)[^\d\n]{0,24}(\d{2,5})`),
	// Weakest: "port 3001".
	regexp.MustCompile(`(?i)\bport\s+(\d{2,5})\b`),
}

// scanLogForPort returns the most relevant port announcement found in tail.
// Patterns are evaluated in priority order; the first pattern with any
// match wins, and within that pattern the last (newest) match is returned.
// Only TCP-plausible ports (1024..65535) are accepted.
func scanLogForPort(tail string) (int, bool) {
	if tail == "" {
		return 0, false
	}
	for _, re := range logPortPatterns {
		matches := re.FindAllStringSubmatch(tail, -1)
		if len(matches) == 0 {
			continue
		}
		for i := len(matches) - 1; i >= 0; i-- {
			n, err := strconv.Atoi(matches[i][1])
			if err != nil || n < 1024 || n > 65535 {
				continue
			}
			return n, true
		}
	}
	return 0, false
}
