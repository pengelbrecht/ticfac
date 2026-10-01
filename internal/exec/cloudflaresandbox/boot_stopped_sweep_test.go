package cloudflaresandbox

import (
	"context"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/sandboximage"
)

// A worker that stopped in its boot leaves a `<worker branch>-boot-stopped`
// branch on origin (#176). It is the reason a person or a resume reads while
// its tick is open, and nothing once the tick is closed — so the run's sweep
// (internal/reconcile/sweep.go, at a tick's close and at run start, resume
// and end) takes it down with the rest of the closed tick's leftovers, and
// leaves every marker of a tick that is still open or held.
//
// short: local throwaway git repositories; no container, no door.
func TestTheSweepDeletesTheBootMarkersOfAClosedTickOnly(t *testing.T) {
	repo := newGitRepo(t)
	worker := repo.workerDir("worker")
	marker := func(workerBranch string) string { return sandboximage.WorkerBootStoppedBranch(workerBranch) }
	body := map[string]string{"BOOT-STOPPED.md": "exit: 14\nreason: the gateway did not answer\n"}

	closedOwn := marker(sandboximage.WorkerBranch("xte/attempt-1", "a1"))
	closedRole := marker(sandboximage.WorkerBranch("xte/attempt-2-resolve-2-deadbeef", "a1"))
	closedFallback := marker(runLandingBranch(sandboximage.WorkerBranch("xte/attempt-3", "a1"), "r1"))
	held := marker(sandboximage.WorkerBranch("xte/attempt-1", "b2"))
	otherEpic := marker(sandboximage.WorkerBranch("abc/attempt-1", "a1"))
	workerBranch := sandboximage.WorkerBranch("xte/attempt-1", "a1")
	for _, branch := range []string{closedOwn, closedRole, closedFallback, held, otherEpic, workerBranch} {
		repo.commitOn(worker, branch, "a branch on origin", body)
	}

	sweeper := NewLeftoverSweeper(repo.Clone, "origin", "xte", "r1")
	report, err := sweeper.SweepLeftovers(context.Background(), subprocess.SweepScope{
		Namespace: "ticfac/run-r1/",
		Sweepable: func(tick string) bool { return tick == "a1" },
	})
	if err != nil {
		t.Fatalf("SweepLeftovers: %v", err)
	}
	if len(report.Failed) != 0 {
		t.Errorf("the sweep failed: %v", report.Failed)
	}

	onOrigin := func(branch string) bool {
		return strings.TrimSpace(mustGit(t, repo.Clone, "ls-remote", "origin", "refs/heads/"+branch)) != ""
	}
	for _, gone := range []string{closedOwn, closedRole, closedFallback} {
		if onOrigin(gone) {
			t.Errorf("the closed tick's boot marker %s is still on origin", gone)
		}
		if !strings.Contains(strings.Join(report.Removed, "\n"), gone) {
			t.Errorf("the sweep's report does not name %s: %v", gone, report.Removed)
		}
	}
	for _, kept := range []string{held, otherEpic, workerBranch} {
		if !onOrigin(kept) {
			t.Errorf("the sweep deleted %s, which is not a closed tick's boot marker of this epic", kept)
		}
	}
}

// A sweep that cannot reach origin reports it and refuses nothing: a sweep
// never stops a run, and an error would make the reconciler skip the git half
// of the sweep for every other executor's leftovers.
//
// short: one local throwaway git repository; no container, no door.
func TestABootMarkerSweepThatCannotReachOriginIsNotAnError(t *testing.T) {
	repo := newGitRepo(t)
	sweeper := NewLeftoverSweeper(repo.Clone, "no-such-remote", "xte", "r1")
	report, err := sweeper.SweepLeftovers(context.Background(), subprocess.SweepScope{
		Namespace: "ticfac/run-r1/",
		Sweepable: func(string) bool { return true },
	})
	if err != nil {
		t.Fatalf("SweepLeftovers returned an error: %v", err)
	}
	if len(report.Failed) == 0 {
		t.Errorf("an unreachable origin was not reported: %+v", report)
	}
}
