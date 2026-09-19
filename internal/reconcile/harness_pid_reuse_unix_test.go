//go:build unix

package reconcile

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// The fixture's teardown stops what the fixture started, and nothing else
// (tick rmc).
//
// It used to kill the process GROUP of every pid it could find saved under its
// state root — supervisor.pid, runner.pid, the observation log — if kill(pid,
// 0) said that pid was alive. On a host where pids were measured advancing
// ~700 a second and wrapping at 99999, a pid a test saved a few minutes ago is
// routinely somebody else's by the time the teardown reads it, and the
// teardown then SIGKILLs that somebody's whole process group.
//
// A test cannot choose which pid the kernel reuses, so this one plants the
// state a reused pid leaves behind: an attempt directory whose pid files name
// a live process the test owns and the fixture never started.
func TestTheTeardownNeverSignalsAProcessTheFixtureDidNotStart(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})

	decoy := exec.Command("sleep", "120")
	decoy.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := decoy.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		_ = decoy.Wait()
		close(exited)
	}()
	// The decoy is this test's own unreaped child, so its pid is not reused
	// while the test holds it, and killing it here is killing what it started.
	t.Cleanup(func() {
		_ = decoy.Process.Kill()
		<-exited
	})
	pid := decoy.Process.Pid

	state := filepath.Join(f.StateRoot, "attempt-dir")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	line := []byte(strconv.Itoa(pid) + "\n")
	for _, name := range []string{"supervisor.pid", "runner.pid"} {
		if err := os.WriteFile(filepath.Join(state, name), line, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	started := fmt.Sprintf(`{"at":"2026-09-19T00:00:00Z","kind":"started","detail":"claude runner, pid %d, worktree x"}`+"\n", pid)
	if err := os.WriteFile(filepath.Join(state, "observations.jsonl"), []byte(started), 0o644); err != nil {
		t.Fatal(err)
	}

	f.stopEverything()

	select {
	case <-exited:
		t.Fatalf("the fixture's teardown killed pid %d, a process it never started, on the strength of a pid "+
			"file alone", pid)
	case <-time.After(300 * time.Millisecond):
	}
}
