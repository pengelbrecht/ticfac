//go:build unix

package subprocess

import (
	"os"
	"syscall"
)

// Process control, the part of this executor that is genuinely a unix
// subprocess host.
//
// Every process it starts is a PROCESS GROUP LEADER, and that is what makes
// two rules implementable rather than aspirational: cancel kills a tree rather
// than a pid, and the executor dying does not take the work with it — the
// supervisor is in its own group, so a signal to the caller's group never
// reaches it.

// newProcessGroup puts a child in a process group of its own.
func newProcessGroup() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// processAlive asks the operating system, which is the only thing outside the
// job that can answer. A record the job wrote about itself is never evidence
// of its liveness (Appendix A #2).
//
// It answers about a NUMBER, and the kernel reuses numbers (tick rmc): an
// attempt with a locks directory never asks it, and see lock.go for what it
// asks instead. It remains for an attempt started before that, and nothing
// else.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	switch err {
	case nil:
		return true
	case syscall.EPERM:
		// Alive, and not ours to signal.
		return true
	default:
		return false
	}
}

// signalGroup sends a signal to a whole process group. A runner that spawned
// children of its own leaves them running when only its own pid is signalled,
// and those children are what keeps spending after a cancellation.
func signalGroup(pgid int, sig syscall.Signal) error {
	if pgid <= 0 {
		return nil
	}
	return syscall.Kill(-pgid, sig)
}

// holdLock takes f's lock exclusively. It blocks, because the only thing it
// can find in its way is an observer mid-probe, which lets go at once.
func holdLock(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			return err
		}
	}
}

// lockHeld asks whether some OTHER open file description holds f's lock, by
// trying to take a shared one without waiting. Shared, so that two observers
// asking at the same instant never read each other as a holder; and if it is
// granted it is released again before this returns, so an observer never
// holds anything a holder has to wait out for longer than this call.
func lockHeld(f *os.File) (bool, error) {
	fd := int(f.Fd())
	for {
		err := syscall.Flock(fd, syscall.LOCK_SH|syscall.LOCK_NB)
		switch err {
		case nil:
			return false, syscall.Flock(fd, syscall.LOCK_UN)
		case syscall.EWOULDBLOCK:
			return true, nil
		case syscall.EINTR:
			continue
		default:
			return false, err
		}
	}
}

// inheritedLockFD is where Start hands the supervisor its lock: the first of
// exec.Cmd's ExtraFiles, which is always fd 3 in the child.
const inheritedLockFD = 3

// inheritedLock returns the lock Start handed this process, if it was handed
// one: fd 3 counts only if it is the very file of one of the candidates, by
// device and inode. Anything else on fd 3 is not this executor's to touch —
// it is not even wrapped, so no finalizer can ever close it.
//
// The lock is made close-on-exec here. The supervisor's lock is the
// supervisor's alone: git, which the supervisor runs to push, must not carry
// it, and the runner is given a lock of its own rather than this one, so that
// "the supervisor is alive" stays a sentence about exactly one process.
func inheritedLock(candidates []string) *os.File {
	var got syscall.Stat_t
	if err := syscall.Fstat(inheritedLockFD, &got); err != nil {
		return nil
	}
	for _, path := range candidates {
		var want syscall.Stat_t
		if err := syscall.Stat(path, &want); err != nil {
			continue
		}
		if want.Dev == got.Dev && want.Ino == got.Ino {
			syscall.CloseOnExec(inheritedLockFD)
			return os.NewFile(inheritedLockFD, path)
		}
	}
	return nil
}

func sigTerm() syscall.Signal { return syscall.SIGTERM }
func sigKill() syscall.Signal { return syscall.SIGKILL }
