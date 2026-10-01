package cloudflaresandbox

import (
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// hn6's ltg: run_911b's worker settled succeeded with its work on
// `tick/hn6/attempt-4/ltg`, and the run died before it collected it. The run
// that took its claim over holds a credential the door answers ONLY for its
// own run, so the other run's attempt cannot be asked about there: the
// settlement comes from the factory's record, and this executor turns it into
// a handle this run's collect can rule on — without booting anything, and
// without asking the door.

// TestAnotherRunsSettledAttemptIsCollectedWithoutTheDoor: this run's attempt
// 2 rules on run_old's attempt 1, whose container pushed to the per-run
// fallback of its landing branch. Inspect reads it settled from the record,
// the collect reads its work ready to merge, and the door saw nothing.
//
// short: an httptest door and local throwaway git repositories.
func TestAnotherRunsSettledAttemptIsCollectedWithoutTheDoor(t *testing.T) {
	h := newHarness(t)
	repo := newGitRepo(t)
	ex := h.newExecutorWithRepo(t.TempDir(), repo.Clone)
	ex.opts.RunID = h.door.runID
	ex.opts.Attempt = 2
	spec := h.newSpec("keh")
	spec.JobID = "run-" + h.door.runID + "/tick-keh/attempt-2"
	spec.Source.WriteRef = "refs/heads/ticfac/" + spec.JobID
	spec.Source.BaseSHA = repo.Base

	// run_old's container pushed to its per-run fallback (another run's
	// attempt held the shared landing name), which is the branch the
	// reconciler's takeover found the work on and names here.
	head := repo.commitOn(repo.workerDir("worker"), "tick/xte/attempt-1/keh-run_old", "implement keh",
		map[string]string{"internal/keh.go": "package keh\n", resultFile("keh"): reportBody})

	handle, err := ex.AdoptSettledElsewhere(spec, "run_old", "run-run_old/tick-keh/attempt-1", 1,
		"tick/xte/attempt-1/keh-run_old", "the factory recorded its container completed with exit 0")
	if err != nil {
		t.Fatalf("AdoptSettledElsewhere: %v", err)
	}
	if handle.JobID != spec.JobID || handle.Attempt != 2 {
		t.Errorf("the handle names %s attempt %d, want this run's %s attempt 2", handle.JobID, handle.Attempt, spec.JobID)
	}
	status, err := ex.Inspect(handle, "")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !status.Terminal || status.State != subprocess.StateSucceeded {
		t.Errorf("the adopted attempt reads %s (terminal %v), want succeeded", status.State, status.Terminal)
	}
	collected, err := ex.CollectDetail(handle)
	if err != nil {
		t.Fatalf("CollectDetail: %v", err)
	}
	if collected.Verdict != subprocess.VerdictReadyToMerge {
		t.Errorf("verdict %q, want %q: %s", collected.Verdict, subprocess.VerdictReadyToMerge, collected.Message)
	}
	if collected.Result.Source.HeadSHA == nil || *collected.Result.Source.HeadSHA != head {
		t.Errorf("the collected head is %v, want run_old's landing work %s", collected.Result.Source.HeadSHA, head)
	}
	if collected.Result.Source.BaseSHA != repo.Base {
		t.Errorf("the collect was measured from %s, want the dispatched base %s", collected.Result.Source.BaseSHA, repo.Base)
	}
	if h.door.startCount() != 0 || h.door.statusCount() != 0 {
		t.Errorf("the door saw %d starts and %d status reads, want none: it answers only for its own run",
			h.door.startCount(), h.door.statusCount())
	}

	// The next resume re-addresses the same record; a different job is not
	// re-described over it.
	again, err := ex.AdoptSettledElsewhere(spec, "run_old", "run-run_old/tick-keh/attempt-1", 1,
		"tick/xte/attempt-1/keh-run_old", "the factory recorded its container completed with exit 0")
	if err != nil || again.JobID != handle.JobID {
		t.Fatalf("the second adoption answered %v, %v; want the same handle", again, err)
	}
	if _, err := ex.AdoptSettledElsewhere(spec, "run_other", "run-run_other/tick-keh/attempt-3", 3,
		"tick/xte/attempt-3/keh", "somebody else's"); err == nil {
		t.Error("an adoption of another job was re-described over an existing record")
	}
}
