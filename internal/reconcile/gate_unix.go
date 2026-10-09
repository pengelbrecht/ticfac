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
//
// Aimed only at a shell that has not been REAPED: an unreaped corpse still
// holds its number, so both the group kill and this fallback can reach nothing
// but this gate's own leftovers. Once the reap has handed the number back, a
// signal to it is aimed at a stranger — which is why wait() settles the group
// before it reaps, and why the tests belt only a gate they never collected
// (tick rmc; internal/exec/subprocess's exitwait.go for the same rule on
// attempts).
func killGateGroup(pid int) error {
	if pid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		return syscall.Kill(pid, syscall.SIGKILL)
	}
	return nil
}
