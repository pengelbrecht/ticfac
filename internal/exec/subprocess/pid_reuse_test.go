package subprocess

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// A saved PID is a number, and the kernel hands numbers out again (tick rmc).
//
// On a busy macOS host PIDs were measured advancing ~700 a second, and macOS
// wraps them at 99999: the whole space is reused every couple of minutes. So a
// dead attempt's supervisor.pid is, soon enough, somebody else's pid — and an
// executor that asks kill(pid, 0) about it hears "alive". That is what failed
// TestALostAttemptIsReleasedByAPersonAndTheNextRunDispatchesANewOne twice in
// one day: the run found the attempt lost, and moments later Settle found it
// "running" and refused to release it.
//
// A test cannot choose which pid the kernel reuses, so these tests do the next
// best thing: they kill an attempt and then point every pid it ever saved at a
// live process the test owns — the decoy — which is exactly the state a reused
// pid leaves behind. Against liveness-by-pid every one of them fails.

// startDecoy starts a process that is alive, is in a process group of its own
// (so a group signal aimed at its pid would reach it), and belongs to nothing
// the executor started. It is this test's own unreaped child, so its pid
// cannot be handed to anything else while the test holds it — which is what
// makes it safe for the test itself to kill at cleanup.
func startDecoy(t *testing.T) (pid int, alive func() bool) {
	t.Helper()
	cmd := exec.Command("sleep", "120")
	cmd.SysProcAttr = newProcessGroup()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})
	// A signal takes a moment to be delivered and the exit a moment to be
	// reaped, so "alive" is asked with that moment's grace: a decoy that a
	// stray group kill reached is gone well inside it.
	return cmd.Process.Pid, func() bool {
		select {
		case <-exited:
			return false
		case <-time.After(300 * time.Millisecond):
			return true
		}
	}
}

// pointPIDsAt rewrites every pid an attempt has saved — both pid files and the
// attempt record's own copy — to name pid instead.
func pointPIDsAt(t *testing.T, st *store, pid int) {
	t.Helper()
	line := []byte(strconv.Itoa(pid) + "\n")
	for _, name := range []string{fileSupervisorPID, fileRunnerPID} {
		if err := atomicWrite(st.path(name), line, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	record, err := st.readAttempt()
	if err != nil {
		t.Fatal(err)
	}
	record.SupervisorPID = pid
	if err := st.writeAttempt(record, true); err != nil {
		t.Fatal(err)
	}
}

// A dead attempt whose pids now belong to somebody else is not running.
func TestADeadAttemptWhosePIDWasReusedIsNotRunning(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "hang"})
	handle := f.Start(f.spec("run-rmc/tick-r1/attempt-1", "r1"))
	st := f.store(handle)
	waitFor(t, "the runner to start", 20*time.Second, func() bool { return st.runnerPID() > 0 })

	killAttempt(t, st)
	decoy, decoyAlive := startDecoy(t)
	pointPIDsAt(t, st, decoy)

	status := f.inspect(handle)
	if status.State == StateRunning {
		t.Fatalf("an attempt whose supervisor and runner are dead reads as RUNNING because pid %d was reused "+
			"by an unrelated live process: liveness is a reusable number, not the process that was started", decoy)
	}
	if status.State != StateLost {
		t.Errorf("state = %s, want %s: killed with nothing settled, nobody can say what it did", status.State, StateLost)
	}
	if !decoyAlive() {
		t.Error("observing the attempt killed the unrelated process that inherited its pid")
	}
}

// Cancelling that dead attempt signals nothing — least of all the process
// group of whoever holds its old pid now.
func TestCancellingADeadAttemptNeverSignalsTheProcessThatInheritedItsPID(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "hang"})
	handle := f.Start(f.spec("run-rmc/tick-r2/attempt-1", "r2"))
	st := f.store(handle)
	waitFor(t, "the runner to start", 20*time.Second, func() bool { return st.runnerPID() > 0 })

	killAttempt(t, st)
	decoy, decoyAlive := startDecoy(t)
	pointPIDsAt(t, st, decoy)

	ack, err := f.Executor.Cancel(handle)
	if err != nil {
		t.Fatal(err)
	}
	if !decoyAlive() {
		t.Fatalf("cancelling a dead attempt killed pid %d, an unrelated process that inherited the attempt's pid", decoy)
	}
	if ack.StopRequested {
		t.Error("the ack says a stop was requested of an attempt that had nothing left alive to stop")
	}
}

// The harness stops what it started, and nothing else: its cleanup runs after
// every test, on a host where every pid it ever saw may already be someone
// else's.
func TestTheHarnessNeverSignalsAProcessItDidNotStart(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "hang"})
	handle := f.Start(f.spec("run-rmc/tick-r3/attempt-1", "r3"))
	st := f.store(handle)
	waitFor(t, "the runner to start", 20*time.Second, func() bool { return st.runnerPID() > 0 })

	// Every place the harness has ever read a pid from now names the decoy:
	// the pid files, the attempt record, the observation log, and a handle it
	// tracks. The attempt itself is still running, so the harness still has
	// real work to do alongside the thing it must not touch.
	decoy, decoyAlive := startDecoy(t)
	pointPIDsAt(t, st, decoy)
	if err := st.observe(Observation{At: "2026-09-19T00:00:00Z", Kind: ObsStarted,
		Detail: fmt.Sprintf("claude runner, pid %d, worktree elsewhere", decoy)}); err != nil {
		t.Fatal(err)
	}
	local, err := handle.Local()
	if err != nil {
		t.Fatal(err)
	}
	stale := *local
	stale.PID = decoy
	f.track(&JobHandle{SchemaVersion: handle.SchemaVersion, JobID: handle.JobID, Attempt: handle.Attempt,
		Executor: handle.Executor, Handle: stale.asMap(), IssuedAt: handle.IssuedAt})

	// The attempt's own locks, opened before the cleanup removes the directory
	// they live in: an open file keeps its inode, so the kernel can still be
	// asked afterwards whether the processes the harness DID start are gone.
	live, err := st.liveLocks()
	if err != nil || len(live) < 2 {
		t.Fatalf("want a live supervisor and a live runner before the cleanup, got %d lock(s) (%v)", len(live), err)
	}
	var held []*os.File
	for _, lock := range live {
		file, err := os.Open(lock.path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		held = append(held, file)
	}

	f.stopEverything()

	if !decoyAlive() {
		t.Fatalf("the harness's cleanup killed pid %d, a process it never started, because a pid it had saved "+
			"was reused", decoy)
	}
	for _, lock := range held {
		if still, err := lockHeld(lock); err != nil || still {
			t.Errorf("%s is still held after the cleanup (%v): the harness spared the decoy by stopping "+
				"nothing at all", lock.Name(), err)
		}
	}
}
