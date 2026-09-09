//go:build unix

package reconcile

import "syscall"

// The gate's process control.
//
// The declared gate is a command LINE run through `sh -c`, and a command line
// spawns whatever it likes: a test runner that starts a server, a script that
// backgrounds a watcher. Killing the shell alone leaves those children running
// AND holding the pipe the reconciler is reading the gate's output from, so a
// gate that "timed out" goes on blocking the run it was supposed to bound.
//
// So the gate is a process GROUP leader and the timeout kills the group — the
// same rule the executor holds for an attempt (internal/exec/subprocess's
// process_unix.go), for the same reason.

// gateProcessGroup puts the gate's shell in a process group of its own.
func gateProcessGroup() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// killGateGroup stops the gate's whole group. The group id is the leader's pid,
// which is the shell's, and the negative pid is what reaches its children. If
// the group is already gone — the leader exited and its children with it — the
// single-pid kill is the honest fallback rather than a reported failure.
func killGateGroup(pid int) error {
	if pid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		return syscall.Kill(pid, syscall.SIGKILL)
	}
	return nil
}
