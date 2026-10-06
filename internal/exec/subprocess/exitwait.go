package subprocess

import (
	"os/exec"
	"sync/atomic"
	"syscall"
)

// A RUNNER'S LIFE, AS A STOP SEES IT.
//
// A stop has to go on signalling the runner's process group for as long as
// anything of the runner is alive — not just the runner itself. One group
// signal can miss a child forked while it is delivered (killUntilGone), and
// that child can outlive the runner: it holds the runner's lock (inherited as
// fd 3) and keeps the attempt spending. So "is the runner still alive?" is
// asked of the lock, which is what the rest of this package asks too.
//
// The danger in signalling a group whose leader is gone is pid reuse (tick
// rmc): a process group id is its leader's pid, and once the leader is reaped
// that number can be handed to a stranger, who can then lead a group of the
// same id. The supervisor removes the danger rather than guessing at it: it
// does NOT REAP the runner until the stop is over. An unreaped child is a
// zombie, and a zombie's pid is still taken — so for as long as the runner is
// unreaped, no process anywhere can be given its number, no group can be
// created with that id, and a signal to it reaches the runner's own leftovers
// or nobody. The exit is observed without reaping (waitExitedUnreaped: kqueue
// NOTE_EXIT on darwin, waitid WNOWAIT on linux); the reap happens only when
// the supervisor's loop collects the exit code, which it never does in the
// middle of a stop.
//
// Where an exit cannot be observed without reaping it, the runner is reaped at
// once as before, and a stop signals its group only until then.

// runnerLife tracks one runner process from the supervisor.
type runnerLife struct {
	cmd      *exec.Cmd
	lockPath string

	exited atomic.Bool
	reaped atomic.Bool
	// deathSig is the signal that killed the runner, when it died by one:
	// the name a relaunch cites as the death it recovered (relaunch.go), which
	// the exit code cannot carry — Go reports a signalled process as -1, the
	// same number a wall-clock stop and an OOM kill would otherwise share.
	deathSig atomic.Int32
	// exitedCh is closed once the runner has exited.
	exitedCh chan struct{}
	// early carries the exit code when the runner had to be reaped as soon
	// as it exited (no way to observe an exit without reaping).
	early chan int
}

// watchRunner starts watching a runner that has just been started.
func watchRunner(cmd *exec.Cmd, lockPath string) *runnerLife {
	life := &runnerLife{cmd: cmd, lockPath: lockPath, exitedCh: make(chan struct{}), early: make(chan int, 1)}
	go func() {
		if err := waitExitedUnreaped(cmd.Process.Pid); err != nil {
			life.early <- life.reap()
		}
		life.exited.Store(true)
		close(life.exitedCh)
	}()
	return life
}

// alive is the stop's liveness question: the runner has not exited, or it
// has but its lock is still held by something it started AND its pid is still
// reserved by being unreaped — so the group signal can only reach its own.
func (l *runnerLife) alive() bool {
	if !l.exited.Load() {
		return true
	}
	if l.reaped.Load() || l.lockPath == "" {
		return false
	}
	held, err := lockPathHeld(l.lockPath)
	return err == nil && held
}

// waitStatus is the kernel's wait status as this package reads it, asserted
// as an interface so no per-platform file is needed for a read every Unix
// kernel answers and the others simply do not: the supervisor runs on this
// host's platforms, and where the assertion fails the death is still a
// signal death by its -1 exit — only its NAME is missing.
type waitStatus interface {
	Signaled() bool
	Signal() syscall.Signal
}

// code reaps the runner — once its exit has been observed — and returns its
// exit code.
func (l *runnerLife) code() int {
	select {
	case code := <-l.early:
		return code
	default:
	}
	return l.reap()
}

func (l *runnerLife) reap() int {
	err := l.cmd.Wait()
	l.reaped.Store(true)
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if asExitError(err, &exitErr) {
		if ws, ok := exitErr.Sys().(waitStatus); ok && ws.Signaled() {
			l.deathSig.Store(int32(ws.Signal()))
		}
		return exitErr.ExitCode()
	}
	return 1
}

// deathSignal names the signal that killed the runner, when it died by one.
// It is asked only after the exit was observed and the code collected, so the
// store it reads has already happened before any reader asks — the channel
// that carried the code, or the reap itself, is the ordering.
func (l *runnerLife) deathSignal() (syscall.Signal, bool) {
	if sig := l.deathSig.Load(); sig != 0 {
		return syscall.Signal(sig), true
	}
	return 0, false
}
