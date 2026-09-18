package herdr

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// The recorded no-commits rule (tick 19l), enforced in this collect the same
// way the local executor enforces it: the verdict follows the ROLE's rule, not
// one rule for every job, and the same tick with the same facts must collect
// the same verdict on either executor — so the review's empty branch collects
// as ready-to-merge here too.
func TestCollectAppliesTheRolesRecordedNoCommitsRule(t *testing.T) {
	t.Parallel()

	review := collectRoleOverEmptyBranch(t, "review-epic", "nrv")
	if review.Verdict != subprocess.VerdictReadyToMerge {
		t.Errorf("the review collected %s, want ready-to-merge: for this role the empty branch is what a "+
			"correct attempt looks like", review.Verdict)
	}
	if review.Result.Outcome != subprocess.OutcomeSucceeded {
		t.Errorf("the review collected as %s: a role whose deliverable is its answer is not failed by "+
			"committing nothing", review.Result.Outcome)
	}
	if review.Result.Source.Commits != 0 {
		t.Errorf("the review collected %d commits; the rule releases the ROLE, not the fact", review.Result.Source.Commits)
	}

	closeout := collectRoleOverEmptyBranch(t, "closeout-epic", "nco")
	if closeout.Verdict != subprocess.VerdictNoCommits {
		t.Errorf("the close-out collected %s, want no-commits: its deliverable is the record it leaves in "+
			"the repository, and an empty branch did not leave one", closeout.Verdict)
	}
	if closeout.Result.Outcome != subprocess.OutcomeFailed {
		t.Errorf("the close-out collected as %s, want failed", closeout.Result.Outcome)
	}
}

// collectRoleOverEmptyBranch runs one attempt in the pwp shape — a DONE
// report over an empty branch — under the named role, and returns its
// collect. The report is durable evidence, so the liveness-unknown hold never
// fires: the verdict is the role's rule, not a guess.
func collectRoleOverEmptyBranch(t *testing.T, role, tick string) *subprocess.Collection {
	t.Helper()
	h := newHarness(t, harnessOptions{})
	handle, err := h.startRole(t, tick, role)
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
	if err := os.WriteFile(local.ResultPath, []byte("STATUS: DONE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatalf("collect the %s attempt: %v", role, err)
	}
	return collected
}

// startRole starts one attempt under a role other than the harness default,
// the way the reconciler dispatches role jobs: the role and the role's output
// schema are the only two fields that differ.
func (h *harness) startRole(t *testing.T, tick, role string) (*subprocess.JobHandle, error) {
	t.Helper()
	spec := h.spec(fmt.Sprintf("run-harness/tick-%s/attempt-%d", tick, h.ex.opts.Attempt), tick)
	spec.Role = role
	spec.OutputSchema = "ticfac.job-result." + role + ".v1"
	handle, err := h.ex.Start(spec)
	if err == nil {
		h.last = handle
	}
	return handle, err
}
