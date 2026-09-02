//go:build unix

package subprocess

import "syscall"

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

func sigTerm() syscall.Signal { return syscall.SIGTERM }
func sigKill() syscall.Signal { return syscall.SIGKILL }
