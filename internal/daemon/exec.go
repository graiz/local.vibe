package daemon

import (
	"context"
	"os/exec"
	"time"
)

// toolTimeout bounds every helper process the daemon shells out to (lsof, ps,
// git). None of them should take more than a moment; one that hangs — lsof
// resolving names through a stalled VPN resolver, git on a CloudStorage mount
// that is still syncing — used to block the request goroutine that called it
// indefinitely, and with managedStrangerOnPort single-flighted that was every
// request to the route.
const toolTimeout = 5 * time.Second

// runTool runs name with args and returns its stdout, failing after
// toolTimeout. A non-zero exit is an error exactly as with exec.Command.Output.
func runTool(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), toolTimeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}
