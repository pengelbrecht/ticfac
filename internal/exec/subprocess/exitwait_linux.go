//go:build linux

package subprocess

import "golang.org/x/sys/unix"

// waitExitedUnreaped blocks until the child pid has exited WITHOUT reaping
// it (see exitwait.go). On linux that is waitid with WNOWAIT, which reports
// the exit and leaves the zombie for a later wait to collect.
func waitExitedUnreaped(pid int) error {
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err == unix.EINTR {
			continue
		}
		return err
	}
}
