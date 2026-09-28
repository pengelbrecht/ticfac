package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/tk"
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
	// The stray edit was preserved before the forced removal, and the feed
	// names the commit. a1 is closed, so its wip ref retires with the tick's
	// other wip refs (tick tyv); the commit itself stays in the object store
	// until git's gc expires it.
	wip := subprocess.WipRefFor(subprocess.JobOfBranch(stranded))
	preserved := ""
	for _, event := range r.Journal() {
		if _, rest, ok := strings.Cut(event.Detail, " on "+wip+" "); ok &&
			strings.Contains(event.Detail, "preserved the uncommitted work in ") {
			if _, commit, ok := strings.Cut(rest, "(commit "); ok {
				preserved, _, _ = strings.Cut(commit, ")")
			}
		}
	}
	if preserved == "" || !mustRunAllowingFailure(repo, "git", "cat-file", "-e", preserved+"^{commit}") {
		t.Errorf("the stranded worktree's uncommitted edit was removed without a snapshot the feed names (%q)", preserved)
	}
	if mustRunAllowingFailure(repo, "git", "rev-parse", "--verify", "--quiet", wip) {
		t.Errorf("a1 is closed and its wip ref %s outlived the sweep", wip)
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

// wipRefs is every ref under prefix, in a checkout or in the bare origin.
func wipRefs(t *testing.T, gitDir, prefix string) []string {
	t.Helper()
	out := mustRun(t, gitDir, "git", "for-each-ref", "--format=%(refname)", prefix)
	var refs []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			refs = append(refs, line)
		}
	}
	return refs
}

// addOpenTick puts a tick the tracker calls open beside the epic: never
// dispatched by the run, so it is still open when the run ends.
func addOpenTick(t *testing.T, f *fixture, id string) {
	t.Helper()
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	state.Ticks[id] = tk.Tick{ID: id, Title: "tick " + id, Status: "open", Type: "task", Parent: "other", Priority: 2}
	f.Tracker.write(t, state)
}

// plantWip makes a wip ref locally and pushes it to origin — what an
// evacuation or a wall-clock snapshot leaves.
func plantWip(t *testing.T, repo, ref string) {
	t.Helper()
	commit := strings.TrimSpace(mustRun(t, repo, "git", "commit-tree", "-m", "a wip snapshot", "HEAD^{tree}", "-p", "HEAD"))
	mustRun(t, repo, "git", "update-ref", ref, commit)
	mustRun(t, repo, "git", "push", "--quiet", "origin", ref+":"+ref)
}

// TestClosingATickPrunesItsWipRefsLocallyAndOnOrigin is tick tyv: an
// evacuation's and a wall-clock stop's snapshots are pushed to origin under
// refs/ticfac/wip/run-<run>/tick-<t>/, and nothing retired them there. The
// tick's close retires them — locally and on origin — as it retires the
// attempt branch; a tick still open keeps every one.
func TestClosingATickPrunesItsWipRefsLocallyAndOnOrigin(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	repo := f.Repo.Dir
	addOpenTick(t, f, "op")
	closing := []string{
		"refs/ticfac/wip/run-r-fixture/tick-a1/attempt-1",
		"refs/ticfac/wip/run-r-fixture/tick-a1/repair-2-swept",
	}
	open := "refs/ticfac/wip/run-r-fixture/tick-op/attempt-1"
	for _, ref := range append(append([]string{}, closing...), open) {
		plantWip(t, repo, ref)
	}

	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure != nil {
		t.Fatalf("the run did not complete: %+v", result.Failure)
	}

	for _, where := range []struct{ name, dir string }{{"locally", repo}, {"on origin", f.Repo.Origin}} {
		if left := wipRefs(t, where.dir, "refs/ticfac/wip/run-r-fixture/tick-a1/"); len(left) != 0 {
			t.Errorf("a1 closed and its wip refs outlived it %s: %v", where.name, left)
		}
		if kept := wipRefs(t, where.dir, open); len(kept) != 1 {
			t.Errorf("op is open and its wip ref was pruned %s", where.name)
		}
	}
	// Pruned at a1's close, under a1's name in the feed.
	atClose := false
	for _, event := range r.Journal() {
		if event.Tick == "a1" && event.Stage == StageCleanedUp && strings.Contains(event.Detail, closing[0]) &&
			strings.Contains(event.Detail, "origin") {
			atClose = true
		}
	}
	if !atClose {
		t.Error("a1's wip refs were not pruned from origin at a1's close")
	}
}

// TestAWipPruneOriginRefusesIsRecordedAndRetried: origin refusing the
// deletion is a line in the feed, never a stop, and the next sweep — here a
// re-run of the finished run — prunes what the first could not.
func TestAWipPruneOriginRefusesIsRecordedAndRetried(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	repo := f.Repo.Dir
	ref := "refs/ticfac/wip/run-r-fixture/tick-a1/attempt-1"
	plantWip(t, repo, ref)
	hook := filepath.Join(f.Repo.Origin, "hooks", "pre-receive")
	write(t, hook, "#!/bin/sh\nwhile read old new name; do\n"+
		"  case \"$name\" in refs/ticfac/wip/*) [ \"$new\" = 0000000000000000000000000000000000000000 ] && "+
		"{ echo \"wip deletions refused for the test\" >&2; exit 1; } ;; esac\ndone\nexit 0\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}

	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure != nil {
		t.Fatalf("a refused wip prune stopped the run: %+v", result.Failure)
	}
	recorded := false
	for _, event := range r.Journal() {
		if event.Stage == StageCleanedUp && strings.Contains(event.Detail, "not swept") &&
			strings.Contains(event.Detail, ref) && strings.Contains(event.Detail, "next sweep retries") {
			recorded = true
		}
	}
	if !recorded {
		t.Error("origin's refusal to prune a wip ref was not recorded in the feed")
	}
	if len(wipRefs(t, f.Repo.Origin, ref)) != 1 {
		t.Fatal("origin lost the ref: the fixture is not exercising a refused prune")
	}

	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.run(f.Repo, fixtureOptions{}); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if left := wipRefs(t, f.Repo.Origin, ref); len(left) != 0 {
		t.Errorf("the next sweep did not retry the refused prune: origin still has %v", left)
	}
}

// TestTheSweepLeavesAWorktreeWhoseAttemptProcessIsAlive: the git half of the
// sweep never removes a worktree out from under a live local-executor
// process. Its attempt state's supervisor and runner pid files are read and
// asked about BY PID; a live one is a recorded skip, and the next sweep —
// once the process is gone — takes the worktree.
func TestTheSweepLeavesAWorktreeWhoseAttemptProcessIsAlive(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	repo := f.Repo.Dir
	closeInTracker(t, f, "a1")
	state := filepath.Join(f.Root, "local-state", "attempt-7")
	tree := filepath.Join(state, "worktree")
	branch := "ticfac/run-r-fixture/tick-a1/attempt-7"
	mustRun(t, repo, "git", "worktree", "add", "--quiet", "-b", branch, tree, "HEAD")
	write(t, filepath.Join(state, "attempt.json"), `{"worktree": "`+tree+`", "branch": "`+branch+`"}`+"\n")
	// This test's own process stands in for the attempt's live supervisor.
	write(t, filepath.Join(state, "supervisor.pid"), strconv.Itoa(os.Getpid())+"\n")

	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure != nil {
		t.Fatalf("the run did not complete: %+v", result.Failure)
	}
	if !worktreeRegistered(t, repo, tree) {
		t.Fatal("the sweep removed a worktree whose attempt's supervisor is alive")
	}
	if !localBranch(repo, branch) {
		t.Fatal("the sweep deleted the branch a live attempt has checked out")
	}
	recorded := false
	for _, event := range r.Journal() {
		if event.Stage == StageCleanedUp && strings.Contains(event.Detail, tree) &&
			strings.Contains(event.Detail, "pid "+strconv.Itoa(os.Getpid())) {
			recorded = true
		}
	}
	if !recorded {
		t.Error("the skip over a live attempt's worktree was not recorded in the feed")
	}

	// The process is gone: the pid of one that has exited and been reaped.
	done := harnessCommand("true").command(f.Root)
	if err := done.Run(); err != nil {
		t.Fatal(err)
	}
	gone := strconv.Itoa(done.Process.Pid) + "\n"
	write(t, filepath.Join(state, "supervisor.pid"), gone)
	write(t, filepath.Join(state, "runner.pid"), gone)
	if _, _, err := f.run(f.Repo, fixtureOptions{}); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if worktreeRegistered(t, repo, tree) {
		t.Error("the next sweep did not take the worktree once its attempt's processes were gone")
	}
}
