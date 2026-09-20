package herdr

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// The recorded no-commits rule (tick 19l), enforced in this collect the same
// way the local executor enforces it: the verdict follows the ROLE's rule, not
// one rule for every job, and the same tick with the same facts must collect
// the same verdict on either executor — so the review's empty branch collects
// as ready-to-merge here too.
// short: collected from records in a tempdir; no herdr server and no agent
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

// The review's answer carries its OWN verdict, not the collect vocabulary's
// (tick b50), and this executor mints it through the same shared
// implementation the local one does — a review's answer must not read
// differently depending on which host collected it. The closeout keeps the
// collect verdict where it belongs: its branch IS its deliverable.
func TestTheReviewsPayloadCarriesItsOwnVerdictNotTheCollects(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})

	review, err := h.startRole(t, "nrv2", "review-epic")
	if err != nil {
		t.Fatal(err)
	}
	local, err := local(review)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(local.ResultPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local.ResultPath, []byte(
		"REVIEW-VERDICT: NOT READY — the reconciler was never wired to the run\n\nSTATUS: DONE_WITH_CONCERNS\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	collected, err := h.ex.CollectDetail(review)
	if err != nil {
		t.Fatalf("collect the review attempt: %v", err)
	}
	payload := collected.Result.RoleResult.Result
	if got := payload["review_verdict"]; got != subprocess.ReviewVerdictNotReady {
		t.Errorf("the review's payload says review_verdict %v, want %s", got, subprocess.ReviewVerdictNotReady)
	}
	if _, ok := payload["verdict"]; ok {
		t.Errorf("the review's payload still carries the collect verdict %v: a word that read as "+
			"approval was the whole defect", payload["verdict"])
	}
	if strings.Contains(collected.Result.RoleResult.Summary, subprocess.VerdictReadyToMerge) {
		t.Errorf("the review's summary %q spells the collect verdict", collected.Result.RoleResult.Summary)
	}
	if !strings.Contains(collected.Result.RoleResult.Summary, subprocess.ReviewVerdictNotReady) {
		t.Errorf("the review's summary %q does not state the review's own verdict", collected.Result.RoleResult.Summary)
	}

	closeout := collectRoleOverEmptyBranch(t, "closeout-epic", "nco2")
	closeoutPayload := closeout.Result.RoleResult.Result
	if got := closeoutPayload["verdict"]; got != subprocess.VerdictNoCommits {
		t.Errorf("the close-out's payload says verdict %v, want %s", got, subprocess.VerdictNoCommits)
	}
	if _, ok := closeoutPayload["review_verdict"]; ok {
		t.Error("a close-out's payload carries a review verdict nobody asked it for")
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
