package subprocess

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// THE STOP PATHS AND THE DETACHED TOOLS (tick ug0).
//
// pi-durable's NodeExecutionEnv spawns every bash tool `detached`, so each
// tool leads a process group of its own under the runner — and every stop
// here signalled only the RUNNER's process group. The runner died, the tools
// stayed: orphaned to init, running forever, and holding the runner's
// inherited lock fd, which is the attempt's liveness. The stuck steer already
// interrupts those groups (interrupt.go, tick l6n); these tests hold every
// other stop — the supervisor's and a cancel's — to the same reach.

// detachedToolRunnerArgv is a runner with pi-durable's tool shape: it spawns
// one shell `detached`, leading a group of its own exactly the way
// NodeExecutionEnv spawns every tool, and hands it the runner's inherited
// lock fd (fd 3) the way an inherited descriptor reaches every descendant —
// then hangs the way a worker in a hung tool does: the tool runs, the runner
// waits. The tool's pid is named in the runner's log, which is the durable
// place a test can read it from.
func detachedToolRunnerArgv(t *testing.T) []string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("the detached tool is spawned by node, as pi-durable spawns it: %v", err)
	}
	script := `
const { spawn } = require("node:child_process");
const tool = spawn("sh", ["-c", "sleep 3600"], { detached: true, stdio: ["ignore", "ignore", "ignore", 3] });
tool.unref();
console.log("TICFAC_DETACHED_TOOL=" + tool.pid);
setInterval(() => {}, 60000);
`
	return []string{node, "-e", script}
}

// waitDetachedTool waits for the runner's log to name its detached tool, and
// returns the tool's pid — which is also its process group, because a
// detached child leads its own. The cleanup kills that group whatever the
// test concludes: a stop that missed it would otherwise leave `sleep 3600`
// alive on the host after the test is gone.
func waitDetachedTool(t *testing.T, st *store) int {
	t.Helper()
	var tool int
	t.Cleanup(func() {
		if tool > 0 {
			_ = syscall.Kill(-tool, syscall.SIGKILL)
		}
	})
	waitFor(t, "the runner to name its detached tool", 20*time.Second, func() bool {
		raw, _ := os.ReadFile(st.path(fileRunnerLog))
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "TICFAC_DETACHED_TOOL=") {
				continue
			}
			if pid, err := strconv.Atoi(strings.TrimPrefix(line, "TICFAC_DETACHED_TOOL=")); err == nil && pid > 0 {
				tool = pid
				return true
			}
		}
		return false
	})
	return tool
}

// A supervisor that is stopped — by a TERM, the way a person or a teardown
// stops one — interrupts the runner's detached tool groups before it stops
// the runner's own group. Before the fix the tool outlived the stop: a
// `sleep 3600` orphaned to init, holding the attempt's liveness lock, with
// the attempt settled and nobody left to stop it.
//
// short: one real supervisor, one node runner and two sleeps, settling in
// seconds
func TestAStoppedSupervisorInterruptsTheRunnersDetachedTools(t *testing.T) {
	f := newFixture(t, fixtureOptions{runnerArgv: detachedToolRunnerArgv(t)})
	handle := f.Start(f.spec("run-ug0/tick-ug0/attempt-1", "ug0"))
	st := f.store(handle)
	tool := waitDetachedTool(t, st)

	supervisor := provenPID(t, st, lockSupervisor)
	if err := syscall.Kill(supervisor, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the supervisor to settle and exit", 40*time.Second, func() bool {
		return !liveOf(st, lockSupervisor)
	})

	if groupAlive(tool) {
		t.Fatalf("the detached tool's group %d survived the supervisor's stop: orphaned to init, "+
			"it runs forever and holds the attempt's liveness lock after the attempt settled", tool)
	}
	waitFor(t, "nothing of the attempt to hold a lock", 30*time.Second, func() bool {
		return !liveOf(st, lockRunner) && !liveOf(st, lockSupervisor)
	})
}

// A cancel stops the attempt from outside, through the processes its locks
// prove are alive: the runner's group and the supervisor's. A tool that leads
// a group of its own is in neither, so the cancel interrupts those groups
// too — BEFORE the runner is stopped, because they are named as a live
// runner's descendants and a stopped runner has none.
//
// short: one real supervisor, one node runner and two sleeps, cancelling in
// seconds
func TestACancelInterruptsTheRunnersDetachedTools(t *testing.T) {
	f := newFixture(t, fixtureOptions{runnerArgv: detachedToolRunnerArgv(t)})
	handle := f.Start(f.spec("run-ug0/tick-ug0/attempt-2", "ug0"))
	st := f.store(handle)
	tool := waitDetachedTool(t, st)

	if _, err := f.Executor.Cancel(handle); err != nil {
		t.Fatal(err)
	}

	if groupAlive(tool) {
		t.Fatalf("the detached tool's group %d survived the cancel: orphaned to init, it runs forever "+
			"and holds the attempt's liveness lock after the credential was revoked", tool)
	}
	waitFor(t, "nothing of the attempt to hold a lock", 30*time.Second, func() bool {
		return !liveOf(st, lockRunner) && !liveOf(st, lockSupervisor)
	})
}
