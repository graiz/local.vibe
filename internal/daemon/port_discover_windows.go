//go:build windows

package daemon

import (
	"bufio"
	"os/exec"
	"strconv"
	"strings"
)

// portFromProcessGroup on Windows enumerates the route's Job Object members
// (every PID in the route's child tree) and parses `netstat -ano` to find
// the first TCP socket in LISTENING state owned by one of those PIDs.
//
// This replaces the unix lsof+process-group strategy with the closest
// Windows analogue. If no job is tracked for the route (early-spawn race,
// or the daemon was restarted without re-attaching) we return (0, false)
// and let the caller fall back to log-scan.
func portFromProcessGroup(route *Route) (int, bool) {
	if route == nil {
		return 0, false
	}
	pids, err := jobPIDsForRoute(route.Name)
	if err != nil || len(pids) == 0 {
		return 0, false
	}
	pidSet := make(map[int]bool, len(pids))
	for _, p := range pids {
		pidSet[p] = true
	}
	return netstatListenPort(pidSet)
}

// netstatListenPort runs `netstat -ano -p TCP` and returns the first
// LISTENING port whose owning PID is in the supplied set. Parsing logic is
// split out into parseNetstatListenPort so it can be unit-tested without
// shelling out.
func netstatListenPort(pids map[int]bool) (int, bool) {
	out, err := exec.Command("netstat", "-ano", "-p", "TCP").Output()
	if err != nil {
		// Fall back without -p in case the locale-translated netstat doesn't
		// understand it; some Windows builds expect a different protocol name.
		out, err = exec.Command("netstat", "-ano").Output()
		if err != nil {
			return 0, false
		}
	}
	return parseNetstatListenPort(string(out), pids)
}

// parseNetstatListenPort scans netstat -ano output looking for the first
// TCP LISTENING row whose PID is in the supplied set. Sample output:
//
//	  Proto  Local Address          Foreign Address        State           PID
//	  TCP    0.0.0.0:135            0.0.0.0:0              LISTENING       1024
//	  TCP    127.0.0.1:3000         0.0.0.0:0              LISTENING       9876
//	  TCP    [::]:135               [::]:0                 LISTENING       1024
//
// We only care about TCP rows that say LISTENING and whose PID is ours.
// IPv6 lines are accepted too — netstat shows them as "TCPv6" or with [::]
// addresses.
func parseNetstatListenPort(out string, pids map[int]bool) (int, bool) {
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 {
			continue
		}
		if !strings.HasPrefix(fields[0], "TCP") {
			continue
		}
		if strings.ToUpper(fields[3]) != "LISTENING" {
			continue
		}
		pid, err := strconv.Atoi(fields[4])
		if err != nil {
			continue
		}
		if !pids[pid] {
			continue
		}
		// Local Address is "ip:port" or "[::]:port" — split on the LAST colon.
		addr := fields[1]
		idx := strings.LastIndex(addr, ":")
		if idx == -1 {
			continue
		}
		port, err := strconv.Atoi(addr[idx+1:])
		if err != nil || port <= 0 || port > 65535 {
			continue
		}
		return port, true
	}
	return 0, false
}
