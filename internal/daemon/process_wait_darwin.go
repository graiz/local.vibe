//go:build darwin

package daemon

import "golang.org/x/sys/unix"

// watchPIDExit blocks until the given pid exits, then calls fn once. It uses
// kqueue's EVFILT_PROC/NOTE_EXIT — an event delivered by the kernel when the
// process dies — so there's no polling. Used to detect death of adopted managed
// children and PID-tracked routes (which have no *exec.Cmd to Wait on).
//
// If the watch can't be established it falls back to a definitive liveness
// check: the common failure is EV_ADD returning ESRCH because the pid already
// exited in the gap between the caller's aliveness check and here — in which
// case fn must still fire, since there's no other death signal now that the
// polling sweep is removed. A watch that fails while the process is genuinely
// alive (e.g. fd exhaustion) can't be recovered, so we return without firing.
func watchPIDExit(pid int, fn func()) {
	kq, err := unix.Kqueue()
	if err != nil {
		if !processAlive(pid) {
			fn()
		}
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
		if !processAlive(pid) {
			fn()
		}
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
