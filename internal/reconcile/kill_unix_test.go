//go:build unix

package reconcile

import "syscall"

// syscallKillGroup stops a process group the fixture left behind. The executor
// puts each attempt in a group of its own, so a negative pid is what reaches
// the runner as well as its supervisor.
func syscallKillGroup(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		return syscall.Kill(pid, syscall.SIGKILL)
	}
	return nil
}

// processAlive is signal 0: does this pid still exist? It is what makes the
// teardown WAIT rather than merely signal — a kill sent and not waited for is
// not the same fact as a process being gone. EPERM is alive too: a process this
// test may not signal is still a process writing into the directory about to be
// removed.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
