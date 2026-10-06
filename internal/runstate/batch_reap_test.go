package runstate

import (
	"errors"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The incident these tests reproduce (2026-10-06): a `ticfac watch` left up
// for four hours and forty minutes accumulated 4641 <defunct> children and
// exhausted the operator's per-user process limit; the live run beside it
// could no longer fork `git fetch` and halted. Every refresh opened a run-state
// store, read a record through the held-open `git cat-file --batch`, and
// dropped the store without stopping the batch. When the garbage collector
// finally reclaimed the store, the batch's stdin pipe was closed by its
// finaliser, git saw EOF and exited — and nothing ever Wait()ed for it. An
// unwaited exited child is a zombie, held in the process table until its
// parent dies.

// batchPID reads one record through s's batch and answers the batch
// process's pid.
func batchPID(t *testing.T, s *Store) int {
	t.Helper()
	if _, err := s.Fetch(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Read(CheckpointPath(testRun)); err != nil || !ok {
		t.Fatalf("read the checkpoint: ok=%v err=%v", ok, err)
	}
	s.git.reader.mu.Lock()
	defer s.git.reader.mu.Unlock()
	if s.git.reader.cmd == nil {
		t.Fatal("the read did not go through the batch: there is no process to reap")
	}
	return s.git.reader.cmd.Process.Pid
}

// inProcessTable answers whether pid is still in the process table — alive
// OR a zombie: kill(pid, 0) succeeds on both, and fails with ESRCH only once
// the parent has reaped the child.
func inProcessTable(pid int) bool {
	return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func reapedWithin(pid int, d time.Duration, nudge func()) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if nudge != nil {
			nudge()
		}
		if !inProcessTable(pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return !inProcessTable(pid)
}

func seededOrigin(t *testing.T) *origin {
	t.Helper()
	o := newOrigin(t)
	w := o.actor("writer", testRun)
	defer w.Close()
	if _, err := w.PutCheckpoint(testCheckpoint(StateAdmitted, "admitted")); err != nil {
		t.Fatal(err)
	}
	return o
}

// A store that is closed reaps its batch process before Close returns.
func TestClosingAStoreReapsItsBatchProcess(t *testing.T) {
	shorttest.EndToEnd(t)
	o := seededOrigin(t)
	for i := 0; i < 5; i++ {
		s := o.actor("reader", testRun)
		pid := batchPID(t, s)
		s.Close()
		if inProcessTable(pid) {
			t.Fatalf("store %d's cat-file --batch (pid %d) is still in the process table after Close: "+
				"a store opened per refresh leaks one process per refresh", i, pid)
		}
		s.Close() // idempotent
	}
}

// A store its caller forgot to close is reaped once it is unreachable — the
// incident's exact path: dropped by a long-lived process, collected, its pipe
// closed by a finaliser, git exited, and (before the fix) never waited for.
func TestAForgottenStoresBatchProcessIsReapedNotLeftAZombie(t *testing.T) {
	shorttest.EndToEnd(t)
	o := seededOrigin(t)
	var pids []int
	for i := 0; i < 5; i++ {
		s := o.actor("forgetful", testRun)
		pids = append(pids, batchPID(t, s))
		// s is dropped here, unclosed, exactly as watch's refresh did.
	}
	for _, pid := range pids {
		if !reapedWithin(pid, 10*time.Second, runtime.GC) {
			t.Errorf("an unclosed, unreachable store's cat-file --batch (pid %d) is still in the process table: "+
				"a zombie nobody will ever wait for", pid)
		}
	}
}

// A reader that breaks — its process killed under it, a protocol surprise —
// stops being used, and must not be abandoned unwaited either: the fallback
// is a fresh process per read, and the dead batch is reaped on the spot.
func TestABrokenBatchIsReapedWhenItBreaks(t *testing.T) {
	shorttest.EndToEnd(t)
	o := seededOrigin(t)
	s := o.actor("broken", testRun)
	defer s.Close()
	pid := batchPID(t, s)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	// The next read finds the batch dead, marks it broken and falls back.
	if _, ok, err := s.Read(CheckpointPath(testRun)); err != nil || !ok {
		t.Fatalf("the fallback read: ok=%v err=%v", ok, err)
	}
	if !reapedWithin(pid, 5*time.Second, nil) {
		t.Fatalf("the broken batch (pid %d) was abandoned unwaited: a zombie per broken reader", pid)
	}
}
