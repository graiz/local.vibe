//go:build !darwin && !linux

package daemon

// watchPIDExit is a no-op on platforms without a PID-exit primitive wired up.
// Windows never adopts managed children (Job Objects kill them when the daemon
// exits), so there's nothing to watch here.
func watchPIDExit(pid int, fn func()) {}
