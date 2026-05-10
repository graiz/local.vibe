//go:build windows

package cmd

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/graiz/local.vibe/internal/winutil"
)

// precheckPortCollisions returns an error if a port the daemon must own
// (UDP :53 for the embedded resolver) is already held by another process.
// Best-effort identification of the holder via netstat + tasklist; if we
// can't identify the holder, we still warn and prompt.
//
// Must run BEFORE any state-changing setup step — if the user declines to
// continue, nothing has been written to the system yet, so they're back to
// exactly where they started.
//
// :80 and :443 collisions are not checked here; netsh portproxy's own
// `add` failure surfaces the offending state with a clear error from the
// IP Helper service.
func precheckPortCollisions() error {
	if port53Free() {
		return nil
	}
	holders := findUDPPortHolders(53)
	var msg strings.Builder
	msg.WriteString("  port 53 (UDP) is already in use")
	if len(holders) > 0 {
		var parts []string
		for _, pid := range holders {
			if name := winutil.TaskImageName(pid); name != "" {
				parts = append(parts, fmt.Sprintf("%s (PID %d)", name, pid))
			} else {
				parts = append(parts, fmt.Sprintf("PID %d", pid))
			}
		}
		msg.WriteString(" — held by ")
		msg.WriteString(strings.Join(parts, ", "))
	}
	msg.WriteString("\n  vibe's resolver won't be able to bind, and *.vibe lookups will fail.")
	msg.WriteString("\n  Common holders: Acrylic DNS, Internet Connection Sharing (ICS), NextDNS, Pi-hole.")
	fmt.Fprintln(os.Stderr, msg.String())
	if !promptYN("Continue with setup anyway?") {
		return fmt.Errorf("setup aborted (port 53 collision); no system state was changed")
	}
	return nil
}

// port53Free returns true if we can bind UDP 127.0.0.1:53 right now. Closes
// the test socket immediately; the real bind happens later when the daemon
// starts. A false from this check just means SOMETHING else holds :53 — it
// doesn't tell us what.
func port53Free() bool {
	pc, err := net.ListenPacket("udp", "127.0.0.1:53")
	if err != nil {
		return false
	}
	pc.Close()
	return true
}

// findUDPPortHolders parses `netstat -ano` UDP rows and returns every PID
// bound to the given port on either 127.0.0.1 or the unspecified address.
// UDP rows in netstat have 4 columns (Proto, Local Address, Foreign Address,
// PID) — no State column. The foreign address is always "*:*" for UDP.
func findUDPPortHolders(port int) []int {
	out, err := exec.Command(winutil.Sys32("netstat"), "-ano", "-p", "UDP").Output()
	if err != nil {
		// Some Windows builds reject `-p UDP`; fall back to all-protocols
		// and let the row-prefix filter sort it out.
		out, err = exec.Command(winutil.Sys32("netstat"), "-ano").Output()
		if err != nil {
			return nil
		}
	}
	suffix := ":" + strconv.Itoa(port)
	seen := map[int]bool{}
	var pids []int
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}
		if !strings.HasPrefix(fields[0], "UDP") {
			continue
		}
		if !strings.HasSuffix(fields[1], suffix) {
			continue
		}
		pid, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil || pid <= 0 {
			continue
		}
		if seen[pid] {
			continue
		}
		seen[pid] = true
		pids = append(pids, pid)
	}
	return pids
}

