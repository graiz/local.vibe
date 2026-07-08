package daemon

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// discoverRoutePort tries to locate the real listening port for a route
// whose registered Port no longer accepts connections. Two strategies, in
// order:
//
//  1. Inspect the route's process group (managed routes whose original
//     child is still alive but whose app rebound to a different port).
//     Implementation lives in port_discover_<goos>.go because the unix
//     approach uses lsof and process groups, while Windows (Phase 2)
//     enumerates a Job Object and parses netstat.
//  2. Regex-scan the tail of the per-route log file (works even when the
//     tracked PID is gone, as long as vibe's log is still being written).
//     This step is portable and stays here.
//
// A candidate is returned only if it differs from route.Port AND a TCP
// dial to 127.0.0.1 or [::1] on that port succeeds.
func (s *Server) discoverRoutePort(route *Route) (int, bool) {
	if p, ok := portFromProcessGroup(route); ok && p != route.Port && s.isPortReady(p) {
		return p, true
	}
	// Log-scan fallback. Skip it for a managed route whose child is still
	// alive: that child's real port is authoritative via its process group
	// (checked above), so a *stale* port named in the log — one an old run
	// announced, now answered by an unrelated process that squatted the
	// recycled port — must never hijack the registration. That squatter-
	// adoption is exactly how a healthy route drifts onto a stranger and starts
	// serving someone else's 401s. Only when there is no live child to anchor
	// on (the managed process is gone, or this is a non-managed route) do we
	// trust the log.
	if managedLiveChild(route) {
		return 0, false
	}
	logPath := filepath.Join(s.configDir(), route.Name+".log")
	if p, ok := portFromLog(logPath); ok && p != route.Port && s.isPortReady(p) {
		return p, true
	}
	return 0, false
}

// managedLiveChild reports whether route is a managed route whose tracked child
// process is currently alive. When true, the child's process group — not a log
// line — is the authoritative source of the route's real listening port.
func managedLiveChild(route *Route) bool {
	if route.Type != RouteManaged {
		return false
	}
	pid, ok := route.PIDValue()
	return ok && processAlive(pid)
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

// lsofPortRE matches the "NAME" column of lsof output for listening
// sockets: "127.0.0.1:3001 (LISTEN)" or "[::1]:3001 (LISTEN)" or
// "*:3001 (LISTEN)". Used by the unix portFromProcessGroup; kept here
// because parseLsofListenPort is shared by tests on all platforms.
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
