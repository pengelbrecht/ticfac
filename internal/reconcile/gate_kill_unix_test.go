//go:build unix

package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The gate's timeout kills the gate's whole PROCESS GROUP, and returns.
//
// A declared gate is a command line, and a command line starts what it likes:
// a test runner that boots a server, a script that backgrounds a watcher. The
// timeout used to kill the shell alone, which left two problems in one: the
// children went on running, and — because they inherit the pipe the
// reconciler reads the gate's output through — cmd.Wait went on waiting for
// that pipe to close. A gate that "timed out" blocked the run for as long as
// its longest orphan felt like living.
//
// serial: this test asserts a WALL-CLOCK upper bound on the gate's process-group
// kill, and a host saturated by its own parallel siblings would turn that
// bound into scheduler noise rather than a fact about the kill path. It is
// the only test in this package that measures the host's own responsiveness.
func TestAGateThatTimesOutTakesItsChildrenWithIt(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")

	// A backgrounded child that outlives its shell and holds the shell's
	// stdout, and a parent that then waits forever.
	command := "sh -c 'echo $$ > " + pidFile + "; sleep 120' & sleep 120"

	started := time.Now()
	_, _, code, err := runShell(context.Background(), dir, command, 300*time.Millisecond)
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("a gate that never finishes returned no error")
	}
	if code >= 0 {
		t.Errorf("exit code %d: a killed gate never reported one, and `error` is not `fail`", code)
	}
	// Generously under the WaitDelay fallback: the group kill is what makes
	// this immediate, and the pipe is not what the run waits on.
	if elapsed > 3*time.Second {
		t.Errorf("the gate's timeout took %s to return: the wait is not bounded by it", elapsed)
	}

	pid := readPID(t, pidFile)
	if pid == 0 {
		t.Skip("the child never recorded its pid; there is nothing to prove about it")
	}
	deadline := time.Now().Add(3 * time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if processAlive(pid) {
		_ = syscallKillGroup(pid)
		t.Errorf("the gate's child (pid %d) outlived the timeout that killed its shell", pid)
	}
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); convErr == nil {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return 0
}
