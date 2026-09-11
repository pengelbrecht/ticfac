package herdr

import (
	"os"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/herdtest"
)

// dispose: tear the workspace down — after collect, with the liveness
// question answered NEXT TO the removal, and never over work nobody kept.

func TestDisposeTearsTheWorkspaceDown(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	// The run made the work durable on the remote — what a merge means here.
	mustRun(t, h.repo.Dir, "git", "push", "--quiet", "origin", local.Branch)

	if err := h.ex.Dispose(handle, subprocess.DisposeOptions{
		Reason: "attempt 1 of t1 is merged and the tick is closed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(local.Worktree); !os.IsNotExist(err) {
		t.Errorf("the worktree still exists after disposal: %v", err)
	}
	if _, err := os.Stat(local.State + "/" + fileResult); err != nil {
		t.Error("disposal must not take the attempt's state with it: the collected result outlives the worktree")
	}
	// The archived report survived the worktree it was removed from.
	if _, err := os.Stat(local.State + "/" + fileReportArchive); err != nil {
		t.Errorf("the worker's report was not archived before the worktree went: %v", err)
	}
	removed := h.removals()
	if len(removed) != 1 || removed[0] != local.WorkspaceID {
		t.Fatalf("worktree.remove saw %v, want the one workspace %s", removed, local.WorkspaceID)
	}
}

func TestDisposeRefusesBeforeTheFactsArePersisted(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")

	err = h.ex.Dispose(handle, subprocess.DisposeOptions{})
	if err == nil {
		t.Fatal("an uncollected attempt was disposed: disposal before its facts are persisted is how a run " +
			"loses the only record of what it did")
	}
	if refusal, ok := subprocess.AsRefusal(err); !ok || refusal.Reason != subprocess.RefusedNotPersisted {
		t.Errorf("the refusal was %v, want the not-persisted one", err)
	}
	// An explicit reason is the escape hatch, and it does not need the
	// collect to have happened. The branch is kept: a reason permits an
	// uncollected disposal, and never says the commits are disposable.
	if err := h.ex.Dispose(handle, subprocess.DisposeOptions{
		Reason: "the run was released by the operator", KeepBranch: true}); err != nil {
		t.Errorf("a disposal with an explicit reason was refused: %v", err)
	}
}

func TestDisposeRefusesAWorkingAgent(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatal(err)
	}
	h.setStatus("working")

	err = h.ex.Dispose(handle, subprocess.DisposeOptions{Reason: "cleanup"})
	if err == nil {
		t.Fatal("a WORKING agent was torn down: it may be mid-turn, about to commit")
	}
	if refusal, ok := subprocess.AsRefusal(err); !ok || refusal.Reason != subprocess.RefusedLive {
		t.Errorf("the refusal was %v, want the live one", err)
	}
	if len(h.server.Removed()) != 0 {
		t.Error("the refusal must have torn down nothing")
	}
}

func TestDisposeRefusesOnAnUnansweredLivenessQuestion(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatal(err)
	}
	// herdr goes quiet between the collect and the removal: silence is not
	// evidence that killing an agent is safe.
	h.server.Route(herdtest.MethodAgentGet, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		return herdtest.RespondErr(w, req.ID, "invalid_request", "herdr has nothing to say")
	})

	err = h.ex.Dispose(handle, subprocess.DisposeOptions{Reason: "cleanup"})
	if err == nil {
		t.Fatal("an unanswered liveness question let the teardown through")
	}
	if refusal, ok := subprocess.AsRefusal(err); !ok || refusal.Reason != subprocess.RefusedUnknown {
		t.Errorf("the refusal was %v, want the liveness-unknown one", err)
	}
	if len(h.server.Removed()) != 0 {
		t.Error("the refusal must have torn down nothing")
	}
}

func TestDisposeToleratesAnAlreadyGoneWorkspace(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	// A workspace herdr no longer has is the state the step exists to
	// reach, not a failure to reach it.
	mustRun(t, h.repo.Dir, "git", "worktree", "remove", "--force", local.Worktree)
	delete(h.workspaces, local.WorkspaceID)

	if err := h.ex.Dispose(handle, subprocess.DisposeOptions{Reason: "cleanup", KeepBranch: true}); err != nil {
		t.Errorf("an already-gone workspace failed the disposal: %v", err)
	}
}

func TestDisposeRefusesABranchNobodyKept(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatal(err)
	}

	// The commits exist only on this branch. Deleting it would discard the
	// only copy — and a Reason does not lift that.
	err = h.ex.Dispose(handle, subprocess.DisposeOptions{Reason: "the run was released"})
	if err == nil {
		t.Fatal("a branch holding commits no remote has was deleted")
	}
	if refusal, ok := subprocess.AsRefusal(err); !ok || refusal.Reason != subprocess.RefusedBranchUnsafe {
		t.Errorf("the refusal was %v, want the branch-unsafe one — it is what the reconciler retries on KeepBranch", err)
	}
	// The reconciler's retry keeps the branch: the worktree and the
	// workspace go, the commits stay.
	if err := h.ex.Dispose(handle, subprocess.DisposeOptions{Reason: "the run was released", KeepBranch: true}); err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	if head := headOf(h.repo.Dir, local.Branch); head == "" {
		t.Error("KeepBranch disposed the branch too: the refusal a Reason does not lift was lifted anyway")
	}
}

func TestDisposeArchivesTheOwnReportBeforeRemoving(t *testing.T) {
	// The never-Force rule, and its one narrow exception: the attempt's own
	// untracked report is the only dirt a collected worktree should carry,
	// and it moves beside the attempt record rather than widening to Force.
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, h.repo.Dir, "git", "push", "--quiet", "origin", local.Branch)

	if err := h.ex.Dispose(handle, subprocess.DisposeOptions{Reason: "merged and closed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(local.State + "/" + fileReportArchive); err != nil {
		t.Fatalf("the report was not archived: %v", err)
	}
	// The removal went through WITHOUT force — the harness's remove refuses
	// a dirty worktree, so a passing disposal proves the archive happened.
	if len(h.removals()) != 1 {
		t.Errorf("worktree.remove saw %v, want the one workspace", h.removals())
	}
}
