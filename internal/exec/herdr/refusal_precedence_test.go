package herdr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// The precedence a refusal is said in (ticks rxe and lj4), which are one
// defect: when several true things can be said about a stopped attempt, the
// run said the one it computed LAST rather than the one that explains the
// others.
//
// Two live refusals from epic ncv are what these tests hold the code to.
//
// ef7 attempt 5 was stopped at its wall clock and then herdr lost its
// workspace, and the refusal read "no-commits: the attempt branch carries no
// commit beyond the base it was cut from". True, and the least informative
// sentence available: the empty branch is a CONSEQUENCE of the stop. The two
// sentences send an operator in opposite directions — raise the bound or
// split the tick, against redispatch it unchanged, which walks the tick into
// the same wall a second time.
//
// 9fc attempt 4 was refused the same way while this executor's own snapshot
// held four files and 433 lines at
// refs/ticfac/wip/run-epic-ncv/tick-9fc/attempt-4. The refusal named neither
// the ref nor the commit, so the hour was rescued by hand — with the
// mechanism's own output on screen. A safety net nobody is told about is
// barely a safety net.
//
// The refusals are synthesised here; the shapes are those two attempts'.

// A stop at the wall clock is reported as a stop at the wall clock, and the
// empty branch behind it is never the headline. This is ef7 attempt 5's
// exact shape: the bound fired, this executor recorded the stop and the
// departure, the worker left no report, and the branch is where it started.
func TestAWallClockStopOverAnEmptyBranchIsNeverRefusedAsNoCommits(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}

	// The durable markers the stop leaves, and nothing else: no report, no
	// commit. Written through the store rather than as bare files, so the
	// test settles the attempt the way the enforcement does.
	st := h.ex.storeAt(local.State)
	if err := st.markWallExceeded(h.ex.stamp()); err != nil {
		t.Fatal(err)
	}
	if err := st.markAgentGone(h.ex.stamp()); err != nil {
		t.Fatal(err)
	}

	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatal(err)
	}
	if collected.Verdict == subprocess.VerdictNoCommits {
		t.Fatalf("verdict = %s over a wall-clock stop: the empty branch is what the bound LEFT BEHIND, and "+
			"reporting it as the failure is what sent ef7 attempt 5 back to the same wall", collected.Verdict)
	}
	if collected.Verdict != subprocess.VerdictMissingResult {
		t.Errorf("verdict = %s, want %s", collected.Verdict, subprocess.VerdictMissingResult)
	}
	if collected.Result.FailureClass != subprocess.FailureWallClockExceeded {
		t.Errorf("failure class = %q, want %q: the executor's own recorded class outranks a structural "+
			"observation about the branch", collected.Result.FailureClass, subprocess.FailureWallClockExceeded)
	}
	if collected.Result.Outcome != subprocess.OutcomeFailed {
		t.Errorf("outcome = %s, want failed", collected.Result.Outcome)
	}
	// The refusal a person reads NAMES the bound — the number they would
	// change — and the teardown marker beside it.
	if !strings.Contains(collected.Message, "300 seconds") {
		t.Errorf("collect message = %q, want it to name the bound of 300 seconds: the operator's repair is to "+
			"raise that number or split the tick, and a refusal that withholds it names no repair at all",
			collected.Message)
	}
	if strings.Contains(collected.Message, "no commit beyond the base") {
		t.Errorf("collect message = %q still leads with the empty branch", collected.Message)
	}
	if !strings.Contains(collected.Message, "already gone") {
		t.Errorf("collect message = %q says nothing about the agent being gone: the stop and the departure are "+
			"both recorded, and a refusal that carries only one of them describes a cleaner stop than happened",
			collected.Message)
	}
}

// A refusal for an attempt whose work was preserved NAMES the snapshot: the
// ref, the commit, what it is, and how to look at it. 9fc attempt 4's shape.
func TestARefusalNamesTheWIPSnapshotThatHoldsTheWork(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}

	// An hour of work in the worktree, uncommitted — the state a stopped
	// worker holds — and the snapshot the stop takes of it, on a ref of its
	// own. Both are real here: the commit the note names is one git can
	// resolve, not a string the test invented.
	if err := os.WriteFile(filepath.Join(local.Worktree, "uncommitted.txt"),
		[]byte("four files and 433 lines\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	record, err := h.ex.storeAt(local.State).readAttempt()
	if err != nil {
		t.Fatal(err)
	}
	ref := subprocess.WipRefFor(record.JobID)
	commit, err := subprocess.SnapshotWorktree(local.Worktree, ref, record.Spec.ArtifactPrefix)
	if err != nil {
		t.Fatal(err)
	}
	st := h.ex.storeAt(local.State)
	if err := st.markWIPSnapshot(subprocess.WIPSnapshot{Ref: ref, Commit: commit, TakenAt: h.ex.stamp()}); err != nil {
		t.Fatal(err)
	}
	if err := st.markWallExceeded(h.ex.stamp()); err != nil {
		t.Fatal(err)
	}

	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(collected.Message, ref) {
		t.Errorf("collect message = %q names no snapshot ref, and the work is on %s: an operator told only that "+
			"the branch is empty redoes by hand what the run already preserved", collected.Message, ref)
	}
	if !strings.Contains(collected.Message, commit[:12]) {
		t.Errorf("collect message = %q does not name the snapshot commit %s", collected.Message, commit[:12])
	}
	if !strings.Contains(collected.Message, "never merged") {
		t.Errorf("collect message = %q does not say what the snapshot IS: a snapshot presented without that "+
			"framing is evidence of completion, which it is not", collected.Message)
	}
	if !strings.Contains(collected.Message, "git diff") {
		t.Errorf("collect message = %q does not say how to look at the snapshot", collected.Message)
	}
	// And the one question an operator reading a refusal actually has,
	// answered rather than left to be inferred: a re-dispatch does not start
	// from the snapshot.
	if !strings.Contains(collected.Message, "cut from the integration branch, not from this snapshot") {
		t.Errorf("collect message = %q leaves it to be guessed whether a re-dispatch starts from the snapshot",
			collected.Message)
	}
}

// And the case `no-commits` is now RESERVED for: the runner claimed an
// outcome and the branch is still empty. Nothing above explains that, which
// is exactly why it deserves its own name and its own alarm.
func TestARunnerThatClaimsSuccessOverAnEmptyBranchIsStillNoCommits(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(local.ResultPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local.ResultPath, []byte("## Report\n\nShipped it.\n\nSTATUS: DONE\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatal(err)
	}
	if collected.Verdict != subprocess.VerdictNoCommits {
		t.Fatalf("verdict = %s, want %s: a worker that reports DONE over a branch it never touched is the "+
			"surprising case the verdict is kept for", collected.Verdict, subprocess.VerdictNoCommits)
	}
	if collected.Result.Outcome != subprocess.OutcomeFailed {
		t.Errorf("outcome = %s, want failed", collected.Result.Outcome)
	}
	if !strings.Contains(collected.Message, "no commit beyond the base") {
		t.Errorf("collect message = %q, want the empty-branch sentence: here the branch IS the whole story",
			collected.Message)
	}
}
