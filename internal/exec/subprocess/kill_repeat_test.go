package subprocess

import (
	"sync"
	"syscall"
	"testing"
	"time"
)

// A GROUP SIGNAL CAN MISS A MEMBER, and the kill path has to act on what the
// locks say afterwards rather than on having sent one signal.
//
// Measured on this macOS host (2026-09-28): a shell in its own process group
// that forks in a loop, sent ONE killpg(SIGKILL), still had a live member half
// a second later in 41 of 200 trials — the child forked while the signal was
// being delivered is in the group but never receives it. A runner forks all
// the time (git, tools, test suites), so the child a cancel misses holds the
// runner's lock, keeps the attempt alive, and keeps spending. That is what
// failed TestCancellingADeadAttemptNeverSignalsTheProcessThatInheritedItsPID
// under load: its runner was forking git when killAttempt's one SIGKILL went
// out, and the missed child held the runner lock past the 10s wait.
//
// The kernel race cannot be scheduled on demand, so these tests reproduce its
// OUTCOME deterministically: the first signal to the runner's group is
// dropped, exactly as if it had missed every member. A kill path that signals
// once and then only waits leaves the runner alive; one that re-signals while
// the runner's lock is still held takes it.

// missing is one signal to drop: `count` signals of kind `sig` to group
// `pgid`, the first ones sent.
type missing struct {
	pgid  int
	sig   syscall.Signal
	count int
}

// dropSignals makes the listed signals vanish, as if they had reached no
// member of their group, and restores the real signal on cleanup. Only the
// listed groups are affected. It returns how many were dropped.
func dropSignals(t *testing.T, misses ...missing) func() int {
	t.Helper()
	var mu sync.Mutex
	left := append([]missing{}, misses...)
	dropped := 0
	real := groupSignal
	t.Cleanup(func() { groupSignal = real })
	groupSignal = func(target int, sig syscall.Signal) error {
		mu.Lock()
		for i := range left {
			if left[i].pgid == target && left[i].sig == sig && left[i].count > 0 {
				left[i].count--
				dropped++
				mu.Unlock()
				return nil
			}
		}
		mu.Unlock()
		return real(target, sig)
	}
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return dropped
	}
}

// The fixture teardown's kill (and the evacuation's): a SIGKILL that missed the
// runner is sent again while the runner's lock is held.
func TestAKillThatMissesTheRunnerIsSentAgainWhileItsLockIsHeld(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "hang"})
	handle := f.Start(f.spec("run-kil/tick-k1/attempt-1", "k1"))
	st := f.store(handle)
	waitFor(t, "the runner to start", 20*time.Second, func() bool { return st.runnerPID() > 0 })
	runner := provenPID(t, st, lockRunner)

	dropped := dropSignals(t, missing{pgid: runner, sig: syscall.SIGKILL, count: 1})
	alive, err := KillLiveProcesses(st.dir, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if dropped() != 1 {
		t.Fatalf("%d signals to the runner's group were dropped, want the one missed kill", dropped())
	}
	if alive || liveOf(st, lockRunner) {
		t.Fatalf("the runner is still alive after a kill that missed it: a single group signal is not a kill, " +
			"and the lock it still holds says so")
	}
}

// Cancel's stop: the TERMs miss both groups — so the supervisor never gets
// to stop its runner itself — and the first KILL misses the runner. The
// cancel still does not return with the runner alive.
func TestACancelWhoseSignalsMissTheRunnerStillStopsIt(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "hang"})
	handle := f.Start(f.spec("run-kil/tick-k2/attempt-1", "k2"))
	st := f.store(handle)
	waitFor(t, "the runner to start", 20*time.Second, func() bool { return st.runnerPID() > 0 })
	runner, supervisor := provenPID(t, st, lockRunner), provenPID(t, st, lockSupervisor)

	dropped := dropSignals(t,
		missing{pgid: runner, sig: syscall.SIGTERM, count: 1},
		missing{pgid: supervisor, sig: syscall.SIGTERM, count: 1},
		missing{pgid: runner, sig: syscall.SIGKILL, count: 1})
	ack, err := f.Executor.Cancel(handle)
	if err != nil {
		t.Fatal(err)
	}
	if !ack.StopRequested {
		t.Error("the ack says no stop was requested of an attempt whose runner was running")
	}
	// The runner's TERM and first KILL were both missed. (The supervisor's
	// TERM is dropped only when it is sent: once its runner is gone the
	// supervisor settles and exits on its own, and nothing signals it.)
	if dropped() < 2 {
		t.Fatalf("%d signals were dropped, want at least the runner's TERM and first KILL", dropped())
	}
	if liveOf(st, lockRunner) {
		t.Fatal("the cancel returned with the runner still alive: its TERM and its one KILL missed, " +
			"and nothing looked at the runner's lock again")
	}
}
