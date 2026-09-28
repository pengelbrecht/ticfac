package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// The leftover sweep (sweep.go): what a run made for a tick nothing will come
// back for is taken down by name — at the tick's close, at run start and
// resume, and at run end — and a step that fails is retried by the next
// sweep, never left for a person.

// sweepingExecutor stands in for the substrate half of the sweep — the
// herdr executor's workspaces — and records every scope it was asked to sweep.
type sweepingExecutor struct {
	log *sweepLog
}

type sweepLog struct {
	mu     sync.Mutex
	scopes []subprocess.SweepScope
}

func (e *sweepingExecutor) SweepLeftovers(_ context.Context, scope subprocess.SweepScope) (subprocess.SweepReport, error) {
	e.log.mu.Lock()
	e.log.scopes = append(e.log.scopes, scope)
	e.log.mu.Unlock()
	return subprocess.SweepReport{}, nil
}

// covered says whether any sweep the run made covered the branch.
func (l *sweepLog) covered(branch string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, scope := range l.scopes {
		if scope.Covers(branch) {
			return true
		}
	}
	return false
}

func closeInTracker(t *testing.T, f *fixture, tick string) {
	t.Helper()
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	record := state.Ticks[tick]
	record.Status = "closed"
	state.Ticks[tick] = record
	f.Tracker.write(t, state)
}

func worktreeRegistered(t *testing.T, repo, path string) bool {
	t.Helper()
	regs, err := subprocess.ListWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	_, ok := subprocess.FindWorktree(regs, path)
	return ok
}

func localBranch(repo, branch string) bool {
	return mustRunAllowingFailure(repo, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
}

// TestTheRunSweepsWhatItsClosedTicksLeftBehind is the epic-6in shape: a
// repair start of a tick that never ran (herdr refused it before #88) left a
// worktree and a branch; the tick closed anyway, and nothing owned them. The
// same for a tick that closes DURING the run. Unmerged commits stay, and a
// tick the tracker does not call closed is never touched.
func TestTheRunSweepsWhatItsClosedTicksLeftBehind(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	log := &sweepLog{}
	f.sweeper = &sweepingExecutor{log: log}
	repo := f.Repo.Dir

	// a1 is already closed — the killed run closed it and died before its
	// cleanup — and holds a start that never ran, and an attempt whose
	// commits nothing merged.
	closeInTracker(t, f, "a1")
	stranded := "ticfac/run-r-fixture/tick-a1/repair-3"
	strandedTree := filepath.Join(f.Root, "herdr-worktrees", "ticfac-run-r-fixture-tick-a1-repair-3")
	mustRun(t, repo, "git", "worktree", "add", "--quiet", "-b", stranded, strandedTree, "HEAD")
	write(t, filepath.Join(strandedTree, "half-done.txt"), "a stray edit\n")
	unmerged := "ticfac/run-r-fixture/tick-a1/attempt-1"
	commit := strings.TrimSpace(mustRun(t, repo, "git", "commit-tree", "-m", "work nothing merged", "HEAD^{tree}", "-p", "HEAD"))
	mustRun(t, repo, "git", "update-ref", "refs/heads/"+unmerged, commit)

	// a2 is open at the start and closes during the run: its never-run
	// start is swept at its close, not before.
	closesLater := "ticfac/run-r-fixture/tick-a2/repair-1"
	closesLaterTree := filepath.Join(f.Root, "herdr-worktrees", "ticfac-run-r-fixture-tick-a2-repair-1")
	mustRun(t, repo, "git", "worktree", "add", "--quiet", "-b", closesLater, closesLaterTree, "HEAD")

	// zz is no tick the tracker knows: never this run's to decide.
	foreign := "ticfac/run-r-fixture/tick-zz/attempt-1"
	foreignTree := filepath.Join(f.Root, "herdr-worktrees", "ticfac-run-r-fixture-tick-zz-attempt-1")
	mustRun(t, repo, "git", "worktree", "add", "--quiet", "-b", foreign, foreignTree, "HEAD")

	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Rejected) != 0 || result.Failure != nil {
		t.Fatalf("the run did not complete: %s %+v", result.State, result.Failure)
	}

	for _, gone := range []struct{ branch, tree string }{{stranded, strandedTree}, {closesLater, closesLaterTree}} {
		if _, err := os.Stat(gone.tree); !os.IsNotExist(err) {
			t.Errorf("the worktree %s of %s outlived the sweep: %v", gone.tree, gone.branch, err)
		}
		if worktreeRegistered(t, repo, gone.tree) {
			t.Errorf("git still registers %s after the sweep", gone.tree)
		}
		if localBranch(repo, gone.branch) {
			t.Errorf("the branch %s, which carries nothing, outlived the sweep", gone.branch)
		}
	}
	if !localBranch(repo, unmerged) {
		t.Errorf("the branch %s holds commits nothing merged and was swept: the only copy is gone", unmerged)
	}
	if !localBranch(repo, foreign) || !worktreeRegistered(t, repo, foreignTree) {
		t.Errorf("the leftovers of %s, a tick the tracker does not call closed, were touched", foreign)
	}
	// The stray edit was preserved before the forced removal.
	wip := subprocess.WipRefFor(subprocess.JobOfBranch(stranded))
	if !mustRunAllowingFailure(repo, "git", "rev-parse", "--verify", "--quiet", wip) {
		t.Errorf("the stranded worktree's uncommitted edit was removed without a snapshot on %s", wip)
	}

	// The substrate's half was asked about the same things.
	if !log.covered(stranded) || !log.covered(closesLater) {
		t.Error("the executor's sweep was never asked about the closed ticks' jobs")
	}
	if log.covered(foreign) {
		t.Error("the executor's sweep was asked to take a job of a tick the tracker does not call closed")
	}

	// a2's leftovers went at a2's close, under a2's name in the feed.
	sweptAtClose := false
	for _, event := range r.Journal() {
		if event.Tick == "a2" && event.Stage == StageCleanedUp && strings.Contains(event.Detail, closesLater) {
			sweptAtClose = true
		}
	}
	if !sweptAtClose {
		t.Error("a2's never-run start was not swept at a2's close")
	}
}

// TestASweepStepThatFailsIsRetriedByTheNextSweep: a worktree git refuses to
// remove is recorded, not dropped, and the next incarnation's sweep — here, a
// re-run of the finished run — takes it.
func TestASweepStepThatFailsIsRetriedByTheNextSweep(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	repo := f.Repo.Dir
	closeInTracker(t, f, "a1")
	stranded := "ticfac/run-r-fixture/tick-a1/repair-3"
	strandedTree := filepath.Join(f.Root, "herdr-worktrees", "ticfac-run-r-fixture-tick-a1-repair-3")
	mustRun(t, repo, "git", "worktree", "add", "--quiet", "-b", stranded, strandedTree, "HEAD")
	mustRun(t, repo, "git", "worktree", "lock", "--reason", "held for the test", strandedTree)

	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure != nil {
		t.Fatalf("the run did not complete: %+v", result.Failure)
	}
	recorded := false
	for _, event := range r.Journal() {
		if event.Stage == StageCleanedUp && strings.Contains(event.Detail, "not swept") &&
			strings.Contains(event.Detail, strandedTree) {
			recorded = true
		}
	}
	if !recorded {
		t.Error("the sweep's failed removal was not recorded in the feed")
	}
	if !worktreeRegistered(t, repo, strandedTree) {
		t.Fatal("the locked worktree went: the fixture is not exercising a failed step")
	}

	mustRun(t, repo, "git", "worktree", "unlock", strandedTree)
	if _, _, err := f.run(f.Repo, fixtureOptions{}); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if worktreeRegistered(t, repo, strandedTree) {
		t.Error("the next sweep did not retry the failed removal")
	}
	if localBranch(repo, stranded) {
		t.Errorf("the branch %s outlived the retried sweep", stranded)
	}
}
