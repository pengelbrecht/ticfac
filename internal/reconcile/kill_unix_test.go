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
