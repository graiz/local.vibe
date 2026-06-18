//go:build linux

package daemon

import "golang.org/x/sys/unix"

// watchPIDExit blocks until the given pid exits, then calls fn once. It uses a
// pidfd (pidfd_open + poll) — the kernel makes the fd readable when the process
// dies — so there's no polling loop. Used to detect death of adopted managed
// children. Returns silently without calling fn if the watch can't be
// established (e.g. the pid is already gone, or the kernel predates pidfd).
func watchPIDExit(pid int, fn func()) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
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
