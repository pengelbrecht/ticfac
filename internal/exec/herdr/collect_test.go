package herdr

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
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
	// One commit, no report: the worker did work and never said what it did.
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local.Worktree, "hello.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, local.Worktree, "git", "add", "hello.txt")
	mustRun(t, local.Worktree, "git", "commit", "--quiet", "-m", "no report")

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
	// The unsettled and the settled-but-incomplete shapes get DIFFERENT
	// sentences (Appendix A #9): one is a worker that never reported, the
	// other is one whose agent is provably gone.
	if collected.Message == "" || collected.Message == "the attempt failed" {
		t.Errorf("the message was %q: a collapsed failure sentence sends the next repair at the wrong problem",
			collected.Message)
	}
}

func TestCollectDistinguishesSettledFromUnsettled(t *testing.T) {
	settled := collectForState(t, true)
	unsettled := collectForState(t, false)
	if settled.Verdict != unsettled.Verdict || settled.Result.Outcome != unsettled.Result.Outcome {
		t.Errorf("both shapes must collect as missing-result/failed, got %s/%s and %s/%s",
			settled.Verdict, settled.Result.Outcome, unsettled.Verdict, unsettled.Result.Outcome)
	}
	if settled.Message == unsettled.Message {
		t.Errorf("the settled and unsettled shapes share one sentence (%q): one is a gone agent, "+
			"the other is an attempt nothing can settle yet", settled.Message)
	}
}

func collectForState(t *testing.T, settled bool) *subprocess.Collection {
	t.Helper()
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	// Work committed, report never written: the shape both no-report
	// sentences are about, with and without the settlement evidence.
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
		if _, err := h.ex.Inspect(handle, ""); err != nil {
			t.Fatal(err)
		}
	}
	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatal(err)
	}
	return collected
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
