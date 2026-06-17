//go:build darwin

package daemon

import "golang.org/x/sys/unix"

// watchPIDExit blocks until the given pid exits, then calls fn once. It uses
// kqueue's EVFILT_PROC/NOTE_EXIT — an event delivered by the kernel when the
// process dies — so there's no polling. Used to detect death of adopted managed
// children (which have no *exec.Cmd to Wait on). Returns silently without
// calling fn if the watch can't be established (e.g. the pid is already gone).
func watchPIDExit(pid int, fn func()) {
	kq, err := unix.Kqueue()
	if err != nil {
		return
	}
	defer unix.Close(kq)

	ev := unix.Kevent_t{
		Ident:  uint64(pid),
		Filter: unix.EVFILT_PROC,
		Flags:  unix.EV_ADD | unix.EV_ONESHOT,
		Fflags: unix.NOTE_EXIT,
	}
	if _, err := unix.Kevent(kq, []unix.Kevent_t{ev}, nil, nil); err != nil {
		return
	}

	out := make([]unix.Kevent_t, 1)
	for {
		n, err := unix.Kevent(kq, nil, out, nil)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return
		}
		if n > 0 {
			fn()
			return
		}
	}
}
