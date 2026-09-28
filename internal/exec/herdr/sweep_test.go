package herdr

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/client"
)

// The sweep's herdr half (epic-6in, 4i8's repair-3): a workspace worktree.
// create opened for a job that never ran — no agent, no handle anybody will
// ever dispose — is found by the name its branch carries and removed once
// its tick is closed. Nothing of an open tick, of another run, or of a
// working agent is touched.

// strand opens a workspace the way a start that never ran leaves one:
// worktree.create landed, and nothing after it did.
func strand(t *testing.T, h *harness, branch string) string {
	t.Helper()
	created, err := h.ex.client.WorktreeCreate(context.Background(), client.WorktreeCreateParams{
		Cwd: client.Ptr(h.repo.Dir), Branch: client.Ptr(branch), Base: client.Ptr("HEAD"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return created.Workspace.WorkspaceID
}

func (h *harness) holds(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.workspaces[id]
	return ok
}

func (h *harness) pathOf(id string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.workspaces[id].path
}

func sixInScope(closed ...string) subprocess.SweepScope {
	return subprocess.SweepScope{Namespace: "ticfac/run-epic-6in/", Sweepable: func(tick string) bool {
		for _, c := range closed {
			if c == tick {
				return true
			}
		}
		return false
	}}
}

func TestTheSweepRemovesAStrandedStartOfAClosedTickAndNothingElse(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	stranded := strand(t, h, "ticfac/run-epic-6in/tick-4i8/repair-3")
	strandedPath := h.pathOf(stranded)
	if err := os.WriteFile(filepath.Join(strandedPath, "stray.txt"), []byte("an edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	open := strand(t, h, "ticfac/run-epic-6in/tick-46x/attempt-4")
	otherRun := strand(t, h, "ticfac/run-epic-9pd/tick-4i8/attempt-1")

	report, err := h.ex.SweepLeftovers(context.Background(), sixInScope("4i8"))
	if err != nil {
		t.Fatal(err)
	}
	if h.holds(stranded) {
		t.Error("the stranded start of a closed tick still has its workspace")
	}
	if _, err := os.Stat(strandedPath); !os.IsNotExist(err) {
		t.Errorf("the stranded start's worktree survived the sweep: %v", err)
	}
	if !h.holds(open) {
		t.Error("the sweep removed the workspace of a tick that is still open")
	}
	if !h.holds(otherRun) {
		t.Error("the sweep removed another run's workspace")
	}
	if len(report.Removed) == 0 || len(report.Failed) != 0 {
		t.Errorf("the report is %+v, want the removal said and nothing failed", report)
	}
	// The stray edit was put on the job's wip ref before the forced removal.
	wip := subprocess.WipRefFor("run-epic-6in/tick-4i8/repair-3")
	if _, ok := showFile(h.repo.Dir, wip, "stray.txt"); !ok {
		t.Errorf("the stranded worktree's edit is not preserved on %s", wip)
	}
}

func TestTheSweepHoldsAWorkspaceWhoseAgentIsWorking(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	busy := strand(t, h, "ticfac/run-epic-6in/tick-4i8/attempt-3")
	h.setStatus("working")

	report, err := h.ex.SweepLeftovers(context.Background(), sixInScope("4i8"))
	if err != nil {
		t.Fatal(err)
	}
	if !h.holds(busy) {
		t.Fatal("the sweep tore down a workspace whose agent is working")
	}
	if len(report.Held) != 1 || !samePath(report.Held[0], h.pathOf(busy)) {
		t.Errorf("held = %v, want the working workspace's worktree so the git half leaves it alone", report.Held)
	}
	if len(report.Failed) != 1 {
		t.Errorf("the hold was not said: %+v", report)
	}

	// The next sweep, once the agent has settled, takes it.
	h.setStatus("idle")
	if _, err := h.ex.SweepLeftovers(context.Background(), sixInScope("4i8")); err != nil {
		t.Fatal(err)
	}
	if h.holds(busy) {
		t.Error("the next sweep did not take the workspace once its agent settled")
	}
}
