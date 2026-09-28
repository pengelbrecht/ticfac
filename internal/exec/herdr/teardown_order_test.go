package herdr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The teardown order (epic-6in, 46x attempt 2): the workspace, then the
// worktree, then the branch — and every step is the teardown's own, whoever
// held the resource last.
//
// The live failure: the wall clock closed the attempt's pane after the
// interrupt went unhonoured, herdr took the WORKSPACE with the pane, and the
// worktree stayed registered on disk. Disposal asked herdr what exists, found
// nothing, declined to remove "git state it cannot attribute", and went
// straight on to `git branch -D` — which git refused, because the worktree
// still had the branch checked out. The attempt was never disposed, and every
// retry failed the same way.

// wallStopTookTheWorkspace drives one attempt to the wall-clock pane close on
// a herdr whose close takes the workspace with the pane, and collects it. The
// worktree is left dirty, the way a stopped worker leaves it.
func wallStopTookTheWorkspace(t *testing.T) (*harness, *subprocess.JobHandle, *herdrHandle) {
	t.Helper()
	h := newHarness(t, harnessOptions{spawnAgent: true, agentMode: "ignore", paneCloseTakesWorkspace: true})
	clock := &wallClock{t: time.Now().UTC()}
	h.ex.now = clock.now
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	loc, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(loc.Worktree, "uncommitted.txt"), []byte("work at the stop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clock.advance(301 * time.Second)
	if _, err := h.ex.Inspect(handle, ""); err != nil {
		t.Fatal(err)
	}
	clock.advance(stopGrace + time.Second)
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateFailed || len(h.paneCloses()) != 1 {
		t.Fatalf("the fixture did not reach the wall-clock pane close: state %s, closes %v", status.State, h.paneCloses())
	}
	h.mu.Lock()
	held := len(h.workspaces)
	h.mu.Unlock()
	if held != 0 {
		t.Fatalf("the fixture's pane close left %d workspaces: the live shape is a close that takes the workspace", held)
	}
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatal(err)
	}
	return h, handle, loc
}

func registered(t *testing.T, repo, path string) bool {
	t.Helper()
	regs, err := subprocess.ListWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	_, ok := subprocess.FindWorktree(regs, path)
	return ok
}

func TestAWallClockStopWhoseCloseTookTheWorkspaceIsDisposedCompletely(t *testing.T) {
	shorttest.EndToEnd(t)
	h, handle, loc := wallStopTookTheWorkspace(t)
	if !registered(t, h.repo.Dir, loc.Worktree) {
		t.Fatal("the fixture's worktree is not registered after the close: the live shape keeps it")
	}

	// The attempt committed nothing, so its branch is meant to go — the
	// cleanUp a closed or rejected tick runs.
	if err := h.ex.Dispose(handle, subprocess.DisposeOptions{Reason: "the attempt was stopped at its wall clock"}); err != nil {
		t.Fatalf("the teardown after a wall-clock close was not disposed: %v", err)
	}
	if _, err := os.Stat(loc.Worktree); !os.IsNotExist(err) {
		t.Errorf("the worktree %s is still on disk after disposal: %v", loc.Worktree, err)
	}
	if registered(t, h.repo.Dir, loc.Worktree) {
		t.Errorf("git still holds a worktree registration at %s after disposal", loc.Worktree)
	}
	if branchExists(h.repo.Dir, loc.Branch) {
		t.Errorf("the branch %s outlived its disposal", loc.Branch)
	}
	h.mu.Lock()
	left := len(h.workspaces)
	h.mu.Unlock()
	if left != 0 {
		t.Errorf("herdr still holds %d workspaces after disposal", left)
	}
	// The pane went at the close; nothing re-opened one.
	if closes := h.paneCloses(); len(closes) != 1 {
		t.Errorf("pane.close calls = %v, want the one wall-clock close", closes)
	}
	// The stopped worker's uncommitted work survived: the close preserved it
	// before the pane went, and the removal destroyed nothing unpreserved.
	snap, ok := h.ex.storeAt(loc.State).wipSnapshot()
	if !ok {
		t.Fatal("no wip snapshot is recorded: the worktree's uncommitted work was destroyed unpreserved")
	}
	if got, ok := showFile(h.repo.Dir, snap.Commit, "uncommitted.txt"); !ok || got != "work at the stop" {
		t.Errorf("the preserved snapshot reads %q (present %t), want the work as it stood", got, ok)
	}
}

func TestAWorktreeRemovalThatFailsIsRetriedAndSucceeds(t *testing.T) {
	shorttest.EndToEnd(t)
	h, handle, loc := wallStopTookTheWorkspace(t)

	// A removal git refuses: a locked worktree is refused by a single
	// --force, the way any failed removal fails.
	mustRun(t, h.repo.Dir, "git", "worktree", "lock", "--reason", "held for the test", loc.Worktree)
	first := h.ex.Dispose(handle, subprocess.DisposeOptions{Reason: "the attempt was stopped at its wall clock"})
	if first == nil {
		t.Fatal("a removal git refused was reported as a disposal")
	}
	if !strings.Contains(first.Error(), "remove the attempt worktree") {
		t.Errorf("the failure reads %q, want it to name the worktree step that failed", first)
	}
	if !branchExists(h.repo.Dir, loc.Branch) {
		t.Error("the branch went although the worktree step before it failed: the order is workspace, worktree, branch")
	}
	if _, err := os.Stat(loc.State + "/" + fileAttempt); err != nil {
		t.Fatalf("the attempt record did not outlive the failed teardown: %v", err)
	}

	// The retry — the same call the tick's close or the run's sweep makes
	// again — finds the worktree from the durable record and finishes.
	mustRun(t, h.repo.Dir, "git", "worktree", "unlock", loc.Worktree)
	if err := h.ex.Dispose(handle, subprocess.DisposeOptions{Reason: "the attempt was stopped at its wall clock"}); err != nil {
		t.Fatalf("the retried teardown did not finish: %v", err)
	}
	if _, err := os.Stat(loc.Worktree); !os.IsNotExist(err) {
		t.Errorf("the worktree survived the retried teardown: %v", err)
	}
	if registered(t, h.repo.Dir, loc.Worktree) {
		t.Errorf("git still registers %s after the retried teardown", loc.Worktree)
	}
	if branchExists(h.repo.Dir, loc.Branch) {
		t.Errorf("the branch %s survived the retried teardown", loc.Branch)
	}
}

// TestADirtyWorktreeIsPreservedThenRemovedNotRefusedForever: herdr's
// worktree.remove without Force refuses a dirty worktree, and a refusal no
// retry answers differently strands the attempt for a person. The dirt is
// put on the job's wip ref first; then the removal forces past it.
func TestADirtyWorktreeIsPreservedThenRemovedNotRefusedForever(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatal(err)
	}
	loc, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, h.repo.Dir, "git", "push", "--quiet", "origin", loc.Branch)
	if err := os.WriteFile(filepath.Join(loc.Worktree, "left-behind.txt"), []byte("stray work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.ex.Dispose(handle, subprocess.DisposeOptions{Reason: "merged and closed"}); err != nil {
		t.Fatalf("a dirty worktree refused its teardown: %v", err)
	}
	if _, err := os.Stat(loc.Worktree); !os.IsNotExist(err) {
		t.Errorf("the dirty worktree survived its teardown: %v", err)
	}
	snap, ok := h.ex.storeAt(loc.State).wipSnapshot()
	if !ok {
		t.Fatal("the dirty worktree was removed with no record of where its work was preserved")
	}
	if got, ok := showFile(h.repo.Dir, snap.Commit, "left-behind.txt"); !ok || got != "stray work" {
		t.Errorf("the snapshot's left-behind.txt reads %q (present %t)", got, ok)
	}
}
