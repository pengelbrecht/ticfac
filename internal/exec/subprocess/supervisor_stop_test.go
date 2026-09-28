package subprocess

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// THE SUPERVISOR'S OWN STOP, when a group signal misses a child (tick after
// #107). Cancel already signals until the runner's LOCK is released; the
// supervisor's stops — the wall clock, the stuck stop, and a TERM to the
// supervisor — asked only "has my runner been reaped?", so a child the kill
// missed outlived its runner, kept the runner's lock and kept spending while
// the supervisor settled the attempt.
//
// The kernel race is reproduced by its outcome, deterministically: the
// supervisor runs as a helper mode of this test binary with its group signal
// replaced — the TERM reaches nobody, and the first KILL reaches only the
// runner itself, never the child it forked. That is exactly what the kernel
// does to a child forked while the signal is delivered.

const superviseMissingChildrenArg = "__supervise_missing_children__"

// superviseMissingChildren is the helper mode: Supervise, with group signals
// that miss the runner's children as described above.
func superviseMissingChildren(args []string) int {
	fs := flag.NewFlagSet("supervise", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	state := fs.String("state", "", "")
	if err := fs.Parse(args); err != nil || *state == "" {
		fmt.Fprintf(os.Stderr, "supervise helper: --state is required\n")
		return 2
	}
	var mu sync.Mutex
	killed := 0
	groupSignal = func(pgid int, sig syscall.Signal) error {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case sig == syscall.SIGTERM:
			return nil
		case sig == syscall.SIGKILL && killed == 0:
			killed++
			return syscall.Kill(pgid, sig) // the leader alone: its child is missed
		}
		return signalGroup(pgid, sig)
	}
	if err := Supervise(*state); err != nil {
		fmt.Fprintf(os.Stderr, "supervise helper: %v\n", err)
		return 1
	}
	return 0
}

// A stop whose kill missed the runner's child goes on until the runner's lock
// is released: the child dies too, and nothing of the attempt is left alive.
func TestASupervisorStopWhoseKillMissesTheRunnersChildStillStopsIt(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "hang",
		supervisorArgv: []string{os.Args[0], superviseMissingChildrenArg}})
	handle := f.Start(f.spec("run-svs/tick-s1/attempt-1", "s1"))
	st := f.store(handle)
	waitFor(t, "the runner to start", 20*time.Second, func() bool { return st.runnerPID() > 0 })
	runner := provenPID(t, st, lockRunner)
	// The runner (a shell) forks its sleeper after committing; the stop must
	// find that child in the group, or there is nothing for a kill to miss.
	waitFor(t, "the runner's forked child", 20*time.Second, func() bool {
		out, _ := exec.Command("pgrep", "-g", strconv.Itoa(runner), "sleep").Output()
		return strings.TrimSpace(string(out)) != ""
	})
	supervisor := provenPID(t, st, lockSupervisor)

	if err := syscall.Kill(supervisor, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the supervisor to settle and exit", 40*time.Second, func() bool { return !liveOf(st, lockSupervisor) })

	if liveOf(st, lockRunner) {
		out, _ := exec.Command("pgrep", "-lg", strconv.Itoa(runner)).Output()
		t.Fatalf("the supervisor settled the attempt with the runner's child still alive and holding its lock "+
			"(group %d: %s): its kill missed the child and it stopped signalling when the runner itself was reaped",
			runner, strings.TrimSpace(string(out)))
	}
}

// The pid-reuse guard: the supervisor observes its runner's exit WITHOUT
// reaping it, so the runner's pid — its process group id — stays taken (a
// zombie) until the supervisor collects the exit code, and a stop signalling
// that group in between cannot reach a stranger who was handed the number.
func TestARunnersExitIsObservedWithoutFreeingItsPID(t *testing.T) {
	for _, already := range []bool{false, true} {
		cmd := exec.Command("sh", "-c", "sleep 0.2; exit 3")
		cmd.SysProcAttr = newProcessGroup()
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		pid := cmd.Process.Pid
		if already {
			// Exited before anyone watched: still observed, still unreaped.
			time.Sleep(600 * time.Millisecond)
		}
		life := watchRunner(cmd, "")
		select {
		case <-life.exitedCh:
		case <-time.After(10 * time.Second):
			t.Fatalf("already=%v: the runner's exit was never observed", already)
		}
		if life.reaped.Load() {
			t.Fatalf("already=%v: observing the exit reaped the runner, freeing its pid for reuse", already)
		}
		// Still a zombie: the number is taken, so the group id is still ours.
		if err := syscall.Kill(pid, 0); err != nil {
			t.Errorf("already=%v: the exited runner's pid is not held (%v): it could be handed to a stranger", already, err)
		}
		if code := life.code(); code != 3 {
			t.Errorf("already=%v: the collected exit code is %d, want 3", already, code)
		}
		if !life.reaped.Load() {
			t.Errorf("already=%v: collecting the exit code did not reap the runner", already)
		}
	}
}
