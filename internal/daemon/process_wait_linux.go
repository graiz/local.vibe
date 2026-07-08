//go:build linux

package daemon

import "golang.org/x/sys/unix"

// watchPIDExit blocks until the given pid exits, then calls fn once. It uses a
// pidfd (pidfd_open + poll) — the kernel makes the fd readable when the process
// dies — so there's no polling loop. Used to detect death of adopted managed
// children and PID-tracked routes.
//
// If the pidfd can't be opened it falls back to a liveness check: the common
// failure is ESRCH because the pid already exited in the gap between the
// caller's aliveness check and here, in which case fn must still fire (there's
// no other death signal now that the polling sweep is removed). A failure while
// the process is alive (e.g. a kernel predating pidfd) can't be recovered, so
// we return without firing.
func watchPIDExit(pid int, fn func()) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		if !processAlive(pid) {
			fn()
		}
		return
	}
	defer unix.Close(fd)

	pfd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		_, err := unix.Poll(pfd, -1)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return
		}
		fn()
		return
	}
}
