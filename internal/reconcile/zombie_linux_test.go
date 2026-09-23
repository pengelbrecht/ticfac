//go:build linux

package reconcile

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// What "alive" has to mean on the host this repo's cloud gate runs on.
//
// Inside the factory container, PID 1 is the sandbox control server, and it
// never reaps: every process whose parent dies before collecting it — exactly
// what a gate's process-group kill leaves behind — reparents to PID 1 and
// stays a zombie FOREVER. A zombie answers signal 0, so a liveness check built
// on kill(pid, 0) alone reads every corpse the container never reaps as a live
// child, and a gate that did its job perfectly fails its own proof: tick 58z
// found TestAGateThatTimesOutTakesItsChildrenWithIt red inside the container
// while the group kill had demonstrably reached the child — the pid the test
// read was State: Z, PPid: 1. On a laptop PID 1 is launchd, which reaps in
// milliseconds, which is why the same test passed on the Mac.
//
// Linux is where /proc can say so directly, so the zombie answer lives here.

// processZombie answers whether pid exists and has DIED, its exit status read
// by the kernel and collected by no one — a corpse awaiting a reap that may
// never come. It is what lets processAlive tell a dead child from a live one
// without trusting anyone to have reaped the dead one (kill_unix_test.go).
func processZombie(pid int) bool {
	return procState(pid) == "Z"
}

// procState is the state letter out of /proc/<pid>/stat, or "" when the pid is
// gone or unreadable. The comm field can contain spaces and parentheses, so
// the state is parsed after the LAST ')' rather than counted from the front.
func procState(pid int) string {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	rest := raw[strings.LastIndexByte(string(raw), ')')+1:]
	fields := strings.Fields(string(rest))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// TestAReapPendingZombieIsNotAlive reproduces the factory container's
// condition in-process, which is the only way to make it fail
// deterministically on a laptop too: a child of THIS test, killed and
// deliberately never waited for, is a zombie no init is coming to reap —
// the exact state the container's PID 1 leaves every killed orphan in.
// processAlive must answer DEAD for it: only nobody's reap is pending, and
// a reap is not life.
// short: one child process, killed in its cradle; the kill is the whole cost
func TestAReapPendingZombieIsNotAlive(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("sleep", "300")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a child to kill: %v", err)
	}
	// Collected after the assertions, never before: waiting for the child is
	// the reap this test must not perform while it is proving anything.
	defer func() { _ = cmd.Wait() }()

	pid := cmd.Process.Pid
	if !processAlive(pid) {
		t.Fatalf("the child (pid %d) did not read as alive before it died", pid)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill the child: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for procState(pid) != "Z" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if procState(pid) != "Z" {
		t.Fatalf("the child (pid %d) never became a zombie, so there is nothing here to prove", pid)
	}
	if processAlive(pid) {
		t.Errorf("processAlive(pid %d) answered true for a zombie: the child is dead, and only nobody's reap is pending", pid)
	}
}
