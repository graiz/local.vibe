//go:build !darwin && !linux

package daemon

import "time"

// watchPIDExit blocks until the given pid exits, then calls fn once. Platforms
// without a kernel PID-exit primitive (Windows, etc.) have no event to wait on,
// so this polls the pid's liveness. It is a targeted per-PID watcher — one
// goroutine for the single process a caller cares about — NOT a revival of the
// removed global route-monitoring sweep. It backstops PID-tracked route removal
// on Windows, where the documented "auto-removed when the tracked PID dies"
// behavior would otherwise never fire (darwin/linux use kqueue/pidfd instead).
// Adopted managed children never reach here — Windows kills them with the
// daemon via Job Objects, so there's nothing to adopt or watch.
func watchPIDExit(pid int, fn func()) {
	const interval = 2 * time.Second
	for {
		if !processAlive(pid) {
			fn()
			return
		}
		time.Sleep(interval)
	}
}
