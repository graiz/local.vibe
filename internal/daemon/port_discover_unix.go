//go:build !windows

package daemon

import (
	"bufio"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// portFromProcessGroup returns the first listening TCP port owned by any
// process in the route's process group. Uses syscall.Getpgid + ps + lsof,
// all of which are unix-only.
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

// lsofListenPort invokes `lsof -a -iTCP -sTCP:LISTEN -P -n -p <csv>` for the
// given PIDs and returns the first listening TCP port owned by one of them.
//
// The `-a` is load-bearing: lsof ORs its selection filters by default, so
// `-iTCP -sTCP:LISTEN -p <pids>` without it means "(every TCP LISTEN socket on
// the machine) OR (all files of <pids>)" — i.e. it lists strangers' listeners
// too, and the parse below would return the first one. That is exactly how a
// managed route's repair adopted an unrelated app's recycled ephemeral port
// and started serving its 401s. `-a` ANDs the filters so only LISTEN sockets
// actually held by <pids> are returned.
func lsofListenPort(pids []int) (int, bool) {
	if len(pids) == 0 {
		return 0, false
	}
	ids := make([]string, len(pids))
	for i, p := range pids {
		ids[i] = strconv.Itoa(p)
	}
	out, err := exec.Command("lsof", "-a", "-iTCP", "-sTCP:LISTEN", "-P", "-n", "-p", strings.Join(ids, ",")).Output()
	if err != nil {
		// lsof exits non-zero when there are no matching sockets — that's a
		// legitimate "no listener yet" case, not an error we should propagate.
		return 0, false
	}
	return parseLsofListenPort(string(out))
}
