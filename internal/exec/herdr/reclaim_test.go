package herdr

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// Reclamation at startup (tick 5hz): a run must be able to ask herdr what it
// still holds for this repository, name the workspaces whose tick the
// caller says is closed, and REPORT them — removal is destructive, so the
// listing removes nothing, and only an explicit authorisation ever does.
//
// The herdr executor does not know which ticks are closed; that is the
// tracker's authority, and the caller holds it. What the executor owns is
// the evidence herdr itself carries — the branch a worktree holds, the path
// it sits at, the label it was created under — and the honest answer to
// "which workspace belongs to that tick".

func TestReclaimableListsClosedTickWorkspacesWithoutRemovingAnything(t *testing.T) {
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

	// Two more workspaces herdr holds for this repository: one for a tick
	// that is still OPEN, and one that is not this run's shape at all.
	h.mu.Lock()
	h.workspaces["w2"] = harnessWorkspace{
		path:   filepath.Join(h.repo.Root, "wt-t2"),
		branch: "ticfac/run-harness/tick-t2/attempt-1", label: "t2",
	}
	h.workspaces["w3"] = harnessWorkspace{
		path:   filepath.Join(h.repo.Root, "wt-main"),
		branch: "main", label: "main",
	}
	h.mu.Unlock()

	closed := func(tickID string) bool { return tickID == "t1" }
	orphans, err := h.ex.Reclaimable(context.Background(), closed)
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 {
		t.Fatalf("the report listed %d workspace(s), want exactly the closed tick's: %+v", len(orphans), orphans)
	}
	if orphans[0].WorkspaceID != local.WorkspaceID {
		t.Errorf("the reported workspace was %s, want %s", orphans[0].WorkspaceID, local.WorkspaceID)
	}
	if orphans[0].TickID != "t1" {
		t.Errorf("the tick was derived as %q, want t1 (from the branch %s)", orphans[0].TickID, local.Branch)
	}
	if orphans[0].Evidence == "" {
		t.Error("the report does not say what evidence attributed the workspace to the tick")
	}
	if orphans[0].Worktree != local.Worktree || orphans[0].Branch != local.Branch {
		t.Errorf("the report's own facts do not match the workspace: %+v", orphans[0])
	}
	if removed := h.removals(); len(removed) != 0 {
		t.Errorf("a report removed something: worktree.remove saw %v — reclamation removes nothing "+
			"unless authorised", removed)
	}
	if _, err := os.Stat(local.Worktree); err != nil {
		t.Errorf("the reported workspace did not survive the report: %v", err)
	}
}

func TestReclaimRefusesWithoutAuthorisation(t *testing.T) {
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
	orphans, err := h.ex.Reclaimable(context.Background(), func(tickID string) bool { return tickID == "t1" })
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 || orphans[0].WorkspaceID != local.WorkspaceID {
		t.Fatalf("the report listed %+v, want the one closed tick's workspace", orphans)
	}

	_, err = h.ex.Reclaim(context.Background(), orphans, "")
	if err == nil {
		t.Fatal("an unauthorised reclamation removed a workspace: removal is destructive and " +
			"reports before anything else")
	}
	if refusal, ok := subprocess.AsRefusal(err); !ok || refusal.Reason != RefusedReclaimUnauthorised {
		t.Errorf("the refusal was %v, want the reclaim-unauthorised one", err)
	}
	if removed := h.removals(); len(removed) != 0 {
		t.Errorf("the refusal must have removed nothing; worktree.remove saw %v", removed)
	}
}

func TestReclaimRemovesOnlyWhatTheOperatorAuthorised(t *testing.T) {
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
	// A second workspace the authorisation does NOT name.
	h.mu.Lock()
	h.workspaces["w2"] = harnessWorkspace{
		path:   filepath.Join(h.repo.Root, "wt-t2"),
		branch: "ticfac/run-harness/tick-t2/attempt-1", label: "t2",
	}
	h.mu.Unlock()

	orphans, err := h.ex.Reclaimable(context.Background(), func(tickID string) bool { return tickID == "t1" })
	if err != nil {
		t.Fatal(err)
	}
	outcomes, err := h.ex.Reclaim(context.Background(), orphans, "the operator, closing the epic")
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || !outcomes[0].Removed {
		t.Fatalf("the authorised reclamation reported %+v, want the one removal", outcomes)
	}
	if removed := h.removals(); len(removed) != 1 || removed[0] != local.WorkspaceID {
		t.Errorf("worktree.remove saw %v, want exactly the authorised workspace %s", removed, local.WorkspaceID)
	}
	if _, err := os.Stat(local.Worktree); !os.IsNotExist(err) {
		t.Errorf("the authorised workspace survived reclamation: %v", err)
	}
	h.mu.Lock()
	_, otherStillThere := h.workspaces["w2"]
	h.mu.Unlock()
	if !otherStillThere {
		t.Error("the reclamation took a workspace the authorisation did not name")
	}
}

func TestReclaimRefusesAWorkspaceWhoseAgentIsStillWorking(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")
	h.setStatus("working") // the workspace's agent is mid-turn

	orphans, err := h.ex.Reclaimable(context.Background(), func(tickID string) bool { return tickID == "t1" })
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 {
		t.Fatalf("the report listed %+v, want the one closed tick's workspace", orphans)
	}
	outcomes, err := h.ex.Reclaim(context.Background(), orphans, "the operator, closing the epic")
	if err != nil {
		t.Fatal(err)
	}
	if outcomes[0].Removed {
		t.Errorf("a WORKING agent's workspace was removed: %+v — it may be mid-turn about to commit", outcomes)
	}
	if removed := h.removals(); len(removed) != 0 {
		t.Errorf("the refusal must have removed nothing; worktree.remove saw %v", removed)
	}
}
