//go:build windows

package daemon

import (
	"bufio"
	"os/exec"
	"strconv"
	"strings"

	"github.com/graiz/local.vibe/internal/winutil"
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
	out, err := exec.Command(winutil.Sys32("netstat"), "-ano", "-p", "TCP").Output()
	if err != nil {
		// Fall back without -p in case the locale-translated netstat doesn't
		// understand it; some Windows builds expect a different protocol name.
		out, err = exec.Command(winutil.Sys32("netstat"), "-ano").Output()
		if err != nil {
			return 0, false
		}
	}
	return parseNetstatListenPort(string(out), pids)
}

// netstatListener is a single (port, pid) pair extracted from a TCP
// LISTENING row of netstat output.
type netstatListener struct {
	Port int
	PID  int
}

// parseNetstatListeners scans netstat -ano output and returns every TCP
// listener row's (port, pid) pair. Sample output:
//
//	  Proto  Local Address          Foreign Address        State           PID
//	  TCP    0.0.0.0:135            0.0.0.0:0              LISTENING       1024
//	  TCP    127.0.0.1:3000         0.0.0.0:0              LISTENING       9876
//	  TCP    [::]:135               [::]:0                 LISTENING       1024
//
// We accept both TCP and TCPv6 rows. The state column is locale-translated
// on non-English Windows installs ("ABHÖREN" on German, "ÉCOUTE" on French,
// etc.), so we don't match the literal word "LISTENING". Instead we infer
// "listening" from the foreign address being the unspecified-address form
// (0.0.0.0:0 or [::]:0). That shape is what netstat renders for any row
// where the connection is open but unconnected — which on TCP rows means
// LISTEN. ESTABLISHED / TIME_WAIT / etc. rows have a real foreign address,
// so they're filtered out.
func parseNetstatListeners(out string) []netstatListener {
	var listeners []netstatListener
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 {
			continue
		}
		if !strings.HasPrefix(fields[0], "TCP") {
			continue
		}
		if !isUnspecifiedNetstatAddr(fields[2]) {
			continue
		}
		pid, err := strconv.Atoi(fields[4])
		if err != nil || pid <= 0 {
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
		listeners = append(listeners, netstatListener{Port: port, PID: pid})
	}
	return listeners
}

// isUnspecifiedNetstatAddr reports whether a netstat foreign-address column
// is the "no peer" form that signals a listening socket: "0.0.0.0:0" for
// IPv4 or "[::]:0" for IPv6.
func isUnspecifiedNetstatAddr(addr string) bool {
	return addr == "0.0.0.0:0" || addr == "[::]:0"
}

// parseNetstatListenPort returns the first listener row whose PID is in
// the supplied set. Layered on top of parseNetstatListeners so the two
// callers (route-port discovery, port-conflict recovery) share parsing.
func parseNetstatListenPort(out string, pids map[int]bool) (int, bool) {
	for _, l := range parseNetstatListeners(out) {
		if pids[l.PID] {
			return l.Port, true
		}
	}
	return 0, false
}
