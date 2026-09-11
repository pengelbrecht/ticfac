package herdr

// THE RUN: one real tick, dispatched, worked, collected and disposed through
// this executor — the acceptance criterion's mechanical check.
//
// What is real here: a real repository with a bare origin, a real unix
// socket speaking herdr's real wire protocol through the production client,
// a real git worktree worktree.create made (the fake does herdr's side of
// it), a real agent PROCESS that reads the prompt it was delivered, reads
// the tick record in its worktree, does the work, commits it on the attempt
// branch and writes its report at the absolute path the prompt owns, a real
// collect over the branch and the report, and a real disposal that archives
// the report and tears the workspace down. The only replaced component is
// the agent's intelligence — which is exactly the component these tests are
// not about.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

func TestARealTickRunsEndToEndThroughThisExecutor(t *testing.T) {
	// The tick: a real unit of work, recorded the way the tracker records
	// one, pointed at by the prompt the executor renders.
	h := newHarness(t, harnessOptions{spawnAgent: true, agentMode: "implement", kind: "pi"})
	tick := "t1"

	// ---- dispatched -----------------------------------------------------
	handle, err := h.start(tick)
	if err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("dispatched: agent %s launched in workspace %s on worktree %s, branch %s from %s",
		local.AgentName, local.WorkspaceID, local.Worktree, local.Branch, local.BaseSHA)

	// ---- worked ----------------------------------------------------------
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateRunning {
		t.Fatalf("the attempt inspecting as %s before the work: expected running", status.State)
	}
	if !waitForOr(t, "the agent to finish its turn", 30*time.Second, func() bool {
		s, err := h.ex.Inspect(handle, "")
		return err == nil && s.Terminal
	}) {
		h.dumpAgent(t)
	}
	t.Logf("worked: the agent settled (%s), the report is at %s",
		h.currentStatus(), local.ResultPath)

	// ---- collected --------------------------------------------------------
	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("collected: verdict %s, outcome %s, %d commit(s) beyond %s, report status %q",
		collected.Verdict, collected.Result.Outcome, collected.Result.Source.Commits,
		local.BaseSHA[:12], collected.Report.Status)
	if collected.Verdict != subprocess.VerdictReadyToMerge {
		t.Fatalf("the run did not collect as ready-to-merge: %s (%s)", collected.Verdict, collected.Message)
	}
	if collected.Report.Status != "DONE" {
		t.Fatalf("the report ended in %q, want DONE", collected.Report.Status)
	}
	if collected.Result.Source.Commits != 1 {
		t.Fatalf("the branch carries %d commit(s) beyond the base, want the agent's one", collected.Result.Source.Commits)
	}
	// The work the tick asked for is really on the branch.
	committed, ok := showFile(h.repo.Dir, "refs/heads/"+local.Branch, "hello.txt")
	if !ok || committed != "hello" {
		t.Fatalf("the committed hello.txt reads %q, want the word the tick asked for", committed)
	}

	// ---- merged -----------------------------------------------------------
	// What the reconciler does between collect and cleanup: make the work
	// durable on the remote and merge the attempt into the run's branch.
	mustRun(t, h.repo.Dir, "git", "push", "--quiet", "origin", local.Branch)
	mustRun(t, h.repo.Dir, "git", "fetch", "--quiet", "origin")
	mustRun(t, h.repo.Dir, "git", "merge", "--quiet", "--no-edit", local.Branch)
	t.Logf("merged: %s is merged into the run's branch and pushed to origin", local.Branch)

	// ---- disposed ----------------------------------------------------------
	if err := h.ex.Dispose(handle, subprocess.DisposeOptions{
		Reason: "attempt 1 of " + tick + " is merged and the tick is closed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(local.Worktree); !os.IsNotExist(err) {
		t.Fatalf("the worktree still exists after disposal: %v", err)
	}
	if head := headOf(h.repo.Dir, local.Branch); head != "" {
		t.Fatalf("branch %s survived disposal: %s", local.Branch, head)
	}
	if _, err := os.Stat(local.State + "/" + fileReportArchive); err != nil {
		t.Fatalf("the worker's report did not survive the worktree: %v", err)
	}
	t.Logf("disposed: workspace %s and worktree are gone, the branch is deleted, the report is archived in %s",
		local.WorkspaceID, local.State)

	// ---- the run, as a person reads it ---------------------------------------
	printRun(t, h, handle)
}

// printRun shows the observation journal — the run's own record of what
// happened, the same stream inspect hands the reconciler.
func printRun(t *testing.T, h *harness, handle *subprocess.JobHandle) {
	t.Helper()
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	st := h.ex.storeAt(local.State)
	observations, _ := st.observationsFrom("")
	t.Logf("the run's observation journal (%d entries):", len(observations))
	for _, obs := range observations {
		t.Logf("  %s  %s: %s", obs.At, obs.Kind, obs.Detail)
	}
	raw, err := os.ReadFile(filepath.Join(local.State, fileResult))
	if err != nil {
		t.Fatal(err)
	}
	var result subprocess.JobResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	t.Logf("the collected record: job %s, outcome %s, head %s",
		result.JobID, result.Outcome, fmt.Sprint(result.Source.HeadSHA))
}

// TestTheRunSurvivesARestartOnTheMinimalHandle drives the same run's
// adoption shape: a controller holding nothing but the state directory —
// what findAttemptState reconstructs for a reconciler that restarted on a
// fresh clone — collects and disposes the attempt the first one dispatched.
func TestTheRunSurvivesARestartOnTheMinimalHandle(t *testing.T) {
	h := newHarness(t, harnessOptions{spawnAgent: true, agentMode: "implement"})
	tick := "t1"

	handle, err := h.start(tick)
	if err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	if !waitForOr(t, "the agent to finish its turn", 30*time.Second, func() bool {
		s, err := h.ex.Inspect(handle, "")
		return err == nil && s.Terminal
	}) {
		h.dumpAgent(t)
	}

	// The restarted controller's shape: the state directory, and nothing.
	restarted := &subprocess.JobHandle{
		SchemaVersion: subprocess.SchemaVersion,
		JobID:         handle.JobID,
		Attempt:       handle.Attempt,
		Executor:      ExecutorName,
		Handle:        map[string]any{"state": local.State},
	}
	// The same substrate it had, and the same operations.
	collected, err := h.ex.CollectDetail(restarted)
	if err != nil {
		t.Fatal(err)
	}
	if collected.Verdict != subprocess.VerdictReadyToMerge {
		t.Fatalf("the restarted controller collected %s, want ready-to-merge", collected.Verdict)
	}
	if err := h.ex.Dispose(restarted, subprocess.DisposeOptions{Reason: "the run restarted", KeepBranch: true}); err != nil {
		t.Fatal(err)
	}
}
