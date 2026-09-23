//go:build unix

package reconcile

import "syscall"

// syscallKillGroup stops a process group a test started itself. It is NOT how
// the fixture stops an attempt: a saved pid is a number the kernel reuses, and
// the fixture's teardown reaches an attempt's processes through their locks
// instead (tick rmc).
func syscallKillGroup(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		return syscall.Kill(pid, syscall.SIGKILL)
	}
	return nil
}

// processAlive is whether a process is LIVE. Signal 0 asks whether the pid
// exists, and a ZOMBIE answers — a dead child the factory container's PID 1
// (the sandbox control server, which never reaps) leaves pending forever
// (tick 58z, zombie_linux_test.go). Existence is therefore not life: a
// process is dead the moment the kernel has read its exit status, whether or
// not anyone ever collects it. On Linux, /proc says which; on the other
// unices there is no /proc and no gap to close — the host's init reaps in
// milliseconds, which is the whole reason the container failure never showed
// on a laptop.
//
// It is what makes the teardown WAIT rather than merely signal — a kill sent
// and not waited for is not the same fact as a process being gone. EPERM is
// alive too: a process this test may not signal is still a process writing
// into the directory about to be removed.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err != nil && err != syscall.EPERM {
		return false
	}
	return !processZombie(pid)
}
