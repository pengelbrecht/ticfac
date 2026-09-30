package cloudflaresandbox

import (
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// Epic hn6's cloud run: an attempt that settled while no orchestrator was
// watching it, re-addressed by an orchestrator whose disk holds nothing of
// the one that dispatched it. Start refuses it as settled — rightly — and
// ReattachSettled is how the resume reaches its collect instead.

// TestASettledAttemptIsReattachedAndCollectedFromAFreshDisk: the fresh
// executor holds no record, the door says the identity succeeded, and the
// worker's work is on the attempt's landing branch. Reattach boots nothing,
// and the collect it enables reads the work as ready to merge.
//
// short: an httptest door and local throwaway git repositories.
func TestASettledAttemptIsReattachedAndCollectedFromAFreshDisk(t *testing.T) {
	h := newHarness(t)
	repo := newGitRepo(t)
	ex := h.newExecutorWithRepo(t.TempDir(), repo.Clone)
	ex.opts.RunID = h.door.runID
	spec := h.newSpec("keh")
	spec.Source.BaseSHA = repo.Base
	h.settled("keh", subprocess.StateSucceeded)

	// The container pushed its work and report to the landing branch the
	// door derives for the attempt's own job.
	head := repo.commitOn(repo.workerDir("worker"), "tick/xte/attempt-1/keh", "implement keh",
		map[string]string{"internal/keh.go": "package keh\n", resultFile("keh"): reportBody})

	if _, err := ex.Start(spec); err == nil {
		t.Fatal("Start accepted a settled identity")
	} else if refusal, ok := subprocess.AsRefusal(err); !ok || refusal.Reason != subprocess.RefusedSettled {
		t.Fatalf("Start's refusal is %v, want %s", err, subprocess.RefusedSettled)
	}

	handle, err := ex.ReattachSettled(spec)
	if err != nil {
		t.Fatalf("ReattachSettled: %v", err)
	}
	if h.door.startCount() != 0 {
		t.Errorf("the door saw %d starts, want 0: a settled attempt is never booted again", h.door.startCount())
	}
	status, err := ex.Inspect(handle, "")
	if err != nil {
		t.Fatalf("Inspect the reattached handle: %v", err)
	}
	if !status.Terminal || status.State != subprocess.StateSucceeded {
		t.Errorf("the reattached attempt reads %s (terminal %v), want succeeded", status.State, status.Terminal)
	}
	collected, err := ex.CollectDetail(handle)
	if err != nil {
		t.Fatalf("CollectDetail the reattached handle: %v", err)
	}
	if collected.Verdict != subprocess.VerdictReadyToMerge {
		t.Errorf("verdict %q, want %q: %s", collected.Verdict, subprocess.VerdictReadyToMerge, collected.Message)
	}
	if collected.Result.Source.HeadSHA == nil || *collected.Result.Source.HeadSHA != head {
		t.Errorf("the collected head is %v, want the landing branch's %s", collected.Result.Source.HeadSHA, head)
	}

	// A second reattach — the next resume — reads the record it wrote.
	again, err := ex.ReattachSettled(spec)
	if err != nil {
		t.Fatalf("the second ReattachSettled: %v", err)
	}
	if again.JobID != handle.JobID {
		t.Errorf("the second reattach answered for %s, want %s", again.JobID, handle.JobID)
	}
}

// TestReattachRefusesWhatItCannotRuleOn: a live attempt is Start's to adopt,
// and a role job under the attempt number lands on a branch this side does
// not derive — neither is reattached, and neither writes a record.
//
// short: an httptest door and one state directory.
func TestReattachRefusesWhatItCannotRuleOn(t *testing.T) {
	h := newHarness(t)
	h.ex.opts.RunID = h.door.runID

	h.running("keh")
	if _, err := h.ex.ReattachSettled(h.newSpec("keh")); err == nil {
		t.Error("a live attempt was reattached: it is adopted through Start")
	}

	repair := h.repairSpec("keh", "repair-1")
	h.door.setJobStatus(repair.JobID, doorStatus{state: subprocess.StateFailed, terminal: true})
	if _, err := h.ex.ReattachSettled(repair); err == nil {
		t.Error("a settled role job was reattached: its landing branch is not derived here")
	}
	if h.ex.storeAt(h.ex.stateDirFor(repair.JobID, 1)).exists(fileAttempt) {
		t.Error("a refused reattach wrote an attempt record")
	}
}
