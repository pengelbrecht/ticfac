package herdr

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/herdtest"
)

// collect: the report and the branch, and NOTHING herdr says. These tests are
// the seam tick 2xu builds its guarantee on — the last one closes the server
// outright, which is only possible because collect never dials it.

func TestCollectReadsTheVerdictFromTheReportAndTheBranch(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")

	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatal(err)
	}
	if collected.Verdict != subprocess.VerdictReadyToMerge {
		t.Errorf("verdict = %s, want ready-to-merge (commits %d, report %t)",
			collected.Verdict, collected.Result.Source.Commits, collected.HasReport)
	}
	if collected.Result.Outcome != subprocess.OutcomeSucceeded {
		t.Errorf("outcome = %s, want succeeded", collected.Result.Outcome)
	}
	if collected.Result.RoleResult == nil || collected.Result.RoleResult.Status != "DONE" {
		t.Fatalf("the report's status line did not reach the role-result envelope: %+v", collected.Result.RoleResult)
	}
	if collected.Result.Source.Commits != 1 {
		t.Errorf("commits = %d, want the one the worker made beyond the recorded base", collected.Result.Source.Commits)
	}
	if collected.Result.Source.HeadSHA == nil {
		t.Error("head_sha is null on an attempt that produced a commit: the no-commits fact is stated, not inferred")
	}
	// The collected result is persisted: disposal asks for exactly that.
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(local.State + "/" + fileResult); err != nil {
		t.Errorf("the collected result was not persisted: %v", err)
	}
}

func TestCollectRefusesNoCommits(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	// A report but no commit: the worker SAID done and did nothing.
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(local.ResultPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local.ResultPath, []byte("STATUS: DONE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatal(err)
	}
	if collected.Verdict != subprocess.VerdictNoCommits {
		t.Errorf("verdict = %s, want no-commits: a worker that reports DONE over an empty branch is not ready to merge",
			collected.Verdict)
	}
}

func TestCollectRefusesAMissingResult(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	// One commit, no report, and the attempt SETTLED: herdr positively
	// answered that the agent is gone, and inspect recorded it durably.
	// The worker did work and never said what it did — durable evidence,
	// collectable.
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local.Worktree, "hello.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, local.Worktree, "git", "add", "hello.txt")
	mustRun(t, local.Worktree, "git", "commit", "--quiet", "-m", "no report")
	h.settleGone(t, handle)

	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatal(err)
	}
	if collected.Verdict != subprocess.VerdictMissingResult {
		t.Errorf("verdict = %s, want missing-result", collected.Verdict)
	}
	if collected.Result.RoleResult != nil {
		t.Error("a missing report must not carry a role result: there is no worker's answer to carry")
	}
	// The settled-no-report shape gets its own sentence (Appendix A #9): a
	// worker whose agent is provably gone is not a guess about anything.
	if collected.Message == "" || collected.Message == "the attempt failed" {
		t.Errorf("the message was %q: a collapsed failure sentence sends the next repair at the wrong problem",
			collected.Message)
	}
}

// TestCollectDistinguishesSettledFromUnsettled is x6j's line at the
// collect: a SETTLED attempt with no report is a verdict from durable
// evidence (the agent is provably gone); an UNSETTLED one — no report, no
// settlement any leg recorded, herdr silent for every poll — is nobody's to
// collect, and collect HOLDS it for a person instead of minting a verdict
// out of the substrate's silence. The two must not share an outcome.
func TestCollectDistinguishesSettledFromUnsettled(t *testing.T) {
	settled, err := collectForState(t, true)
	if err != nil {
		t.Fatalf("the settled attempt did not collect: %v", err)
	}
	if settled.Verdict != subprocess.VerdictMissingResult ||
		settled.Result.Outcome != subprocess.OutcomeFailed {
		t.Errorf("the settled shape collected %s/%s, want missing-result/failed from the durable settlement",
			settled.Verdict, settled.Result.Outcome)
	}

	_, unsettled := collectForState(t, false)
	if unsettled == nil {
		t.Fatal("an unsettled attempt was collected: nobody ever settled it, and the collect answered anyway")
	}
	if refusal, ok := subprocess.AsRefusal(unsettled); !ok || refusal.Reason != subprocess.RefusedUnknown {
		t.Errorf("the unsettled collect was %v, want the liveness-unknown hold: it is held for a person, "+
			"never collected into a verdict", unsettled)
	}
}

// collectForState drives the no-report shape with and without the
// settlement evidence. The settled shape returns the collection; the
// unsettled one returns the collect error.
func collectForState(t *testing.T, settled bool) (*subprocess.Collection, error) {
	t.Helper()
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	// Work committed, report never written: the shape both no-report
	// outcomes are about, with and without the settlement evidence.
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local.Worktree, "hello.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, local.Worktree, "git", "add", "hello.txt")
	mustRun(t, local.Worktree, "git", "commit", "--quiet", "-m", "work, no report")
	if settled {
		h.settleGone(t, handle)
	}
	return h.ex.CollectDetail(handle)
}

// settleGone settles the attempt the way a real settlement happens: herdr
// positively answers that the agent is no longer there, and inspect records
// that answer durably as this executor's own settlement fact.
func (h *harness) settleGone(t *testing.T, handle *subprocess.JobHandle) {
	t.Helper()
	h.server.Route(herdtest.MethodAgentGet, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		return herdtest.RespondErr(w, req.ID, "agent_not_found", "no such agent")
	})
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateFailed {
		t.Fatalf("the settle fixture inspecting as %s, want failed from the positive answer", status.State)
	}
}

func TestCollectReadsTheReportFromTheBranchWhenTheWorktreeIsGone(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	// The worker committed its report (against the prompt's instruction —
	// the artifact boundary tick, p6b, owns refusing that) and the worktree
	// is gone. The branch still has it, and collect still reads it.
	if err := os.MkdirAll(filepath.Dir(local.ResultPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local.ResultPath, []byte("## Report\n\nCommitted my own report.\n\nSTATUS: DONE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, local.Worktree, "git", "add", "-f", local.ResultRel)
	mustRun(t, local.Worktree, "git", "commit", "--quiet", "-m", "work and report")
	_ = os.Remove(local.ResultPath)
	mustRun(t, h.repo.Dir, "git", "worktree", "remove", "--force", local.Worktree)

	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatal(err)
	}
	if !collected.HasReport || collected.Report.Status != "DONE" {
		t.Errorf("the report on the branch was not read back: has=%t status=%q", collected.HasReport, collected.Report.Status)
	}
	if collected.Report.Path != local.Branch+":"+local.ResultRel {
		t.Errorf("report.Path = %q, want the branch-relative form — an absolute host path must not ride into a record",
			collected.Report.Path)
	}
}

func TestCollectNeedsNoHerdr(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")

	// herdr is GONE. Collect still reads the report and the branch and
	// answers ready-to-merge — the seam tick 2xu asserts from its side with
	// every call erroring; here the substrate is not merely erroring, it is
	// not there at all, and nothing dials it.
	h.server.Close()
	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatal(err)
	}
	if collected.Verdict != subprocess.VerdictReadyToMerge {
		t.Errorf("verdict = %s with herdr down, want ready-to-merge: a collect that needed the substrate "+
			"could be swayed by it", collected.Verdict)
	}
}

func TestCollectReportsABoundaryViolation(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	// The worker wrote a tracker record — the authority that is not its own.
	mustRun(t, local.Worktree, "git", "rm", "--quiet", "--cached", ".tick/issues/t1.json")
	_ = os.WriteFile(filepath.Join(local.Worktree, ".tick/issues/t1.json"), []byte("{}"), 0o644)
	if err := os.MkdirAll(filepath.Dir(local.ResultPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local.ResultPath, []byte("STATUS: DONE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, local.Worktree, "git", "add", "-A")
	mustRun(t, local.Worktree, "git", "commit", "--quiet", "-m", "boundary")

	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatal(err)
	}
	if collected.Verdict != subprocess.VerdictBoundaryViolation {
		t.Errorf("verdict = %s, want boundary-violation: the tracker's records are not a worker's to write",
			collected.Verdict)
	}
	if len(collected.BoundaryViolations) != 1 {
		t.Errorf("boundary violations = %v, want the one tracker record", collected.BoundaryViolations)
	}
}
