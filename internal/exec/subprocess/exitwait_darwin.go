//go:build darwin

package subprocess

import "syscall"

// waitExitedUnreaped blocks until the child pid has exited WITHOUT reaping
// it: the child stays a zombie, so its pid — and the process group id that is
// the same number — cannot be handed to anybody else until the caller reaps
// it (see exitwait.go for why the supervisor needs exactly that).
//
// On darwin that is kqueue's EVFILT_PROC/NOTE_EXIT, which reports an exit and
// leaves the process for wait to collect. A child that has already exited
// cannot be attached to and answers ESRCH: for an unreaped child of this
// process that can only mean it is already a zombie, which is the answer.
func waitExitedUnreaped(pid int) error {
	kq, err := syscall.Kqueue()
	if err != nil {
		return err
	}
	defer syscall.Close(kq)

	var change syscall.Kevent_t
	syscall.SetKevent(&change, pid, syscall.EVFILT_PROC, syscall.EV_ADD|syscall.EV_ONESHOT)
	change.Fflags = syscall.NOTE_EXIT
	events := make([]syscall.Kevent_t, 1)
	for {
		n, err := syscall.Kevent(kq, []syscall.Kevent_t{change}, events, nil)
		switch {
		case err == syscall.EINTR:
			continue
		case err == syscall.ESRCH:
			return nil
		case err != nil:
			return err
		case n == 0:
			continue
		}
		if events[0].Flags&syscall.EV_ERROR != 0 {
			if syscall.Errno(events[0].Data) == syscall.ESRCH {
				return nil
			}
			return syscall.Errno(events[0].Data)
		}
		if events[0].Fflags&syscall.NOTE_EXIT != 0 {
			return nil
		}
	}
}
